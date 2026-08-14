package caerusframework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

// CaerusFramework is the core of the framework. It owns the component registry
// and the stage registry, and drives the component lifecycle: dependency
// resolution, initialization, running, and shutdown.
type CaerusFramework struct {
	mu           sync.Mutex
	startMu      sync.Mutex // serializes Initialize (prevents concurrent double-init)
	absorbMu     sync.Mutex // serializes source registration + argv absorption
	components   []CaerusComponent
	byName       map[string]CaerusComponent
	stages       []Stage
	order        []CaerusComponent
	initialized  []CaerusComponent
	started      bool
	running      bool
	jobRan       bool     // set when a job path begins Init; Shutdown does not clear it
	optsArgs     []string // process argv to absorb (FrameworkOptions.Args; nil = os.Args[1:])
	leftover     []string // args ParseFlags did not consume (subcommands, app flags)
	argsAbsorbed bool     // ConfigSourceRegistrars ran and argv was parsed
}

// New creates a framework with the bootstrap stages registered. The bootstrap
// prefix is fixed: logs, configuration, observability, secrets, in that order.
// Components register their own init stage via AddComponent (auto-registered
// in first-seen order). Start the application with Run or RunWithSignals.
//
// With no arguments the core components are not registered: the caller must
// AddComponent them (or run a bare framework). Pass a FrameworkOptions to get
// the app-as-component declaration: New auto-registers the always-on logs,
// configuration and observability components (seeding them from the options)
// plus the declared Components slice.
func New(opts ...*FrameworkOptions) *CaerusFramework {
	f := &CaerusFramework{
		byName: make(map[string]CaerusComponent),
		stages: []Stage{LogsStage, ConfigurationStage, ObservabilityStage, SecretsStage},
	}
	if len(opts) > 0 && opts[0] != nil {
		f.optsArgs = opts[0].Args
		f.registerCore(opts[0])
	}
	return f
}

// AddComponent registers a component and, if it implements Subcomponents,
// breadth-first expands and registers its children (and their children) before
// returning. Component names must be unique across the framework; a nil
// component or nil Subcomponents entry is rejected. The component's
// initialization stage (GetInitOrderStage) is registered automatically the
// first time it is seen, after the bootstrap stages, in first-seen order —
// components declare what they belong to, so there is no separate stage API.
//
// AddComponent is refused after Initialize, after a job has begun, or while
// Run is in progress. Tests that need a different graph construct a new
// framework.
func (f *CaerusFramework) AddComponent(c CaerusComponent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started || f.running || f.jobRan {
		return errors.New("caerus: cannot AddComponent after the framework has started; construct a new framework")
	}
	return f.addComponentTreeLocked(c)
}

// addComponentTreeLocked registers root and BFS-expands Subcomponents.
// Callers must hold f.mu.
func (f *CaerusFramework) addComponentTreeLocked(root CaerusComponent) error {
	if root == nil {
		return errors.New("caerus: AddComponent called with a nil component")
	}
	queue := []CaerusComponent{root}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if err := f.addComponentLocked(c); err != nil {
			return err
		}
		sc, ok := c.(Subcomponents)
		if !ok {
			continue
		}
		children := sc.Subcomponents()
		for i, child := range children {
			if child == nil {
				return fmt.Errorf("caerus: component %q Subcomponents()[%d] is nil", c.Name(), i)
			}
			queue = append(queue, child)
		}
	}
	return nil
}

// addComponentLocked registers a single component. Callers must hold f.mu.
func (f *CaerusFramework) addComponentLocked(c CaerusComponent) error {
	name := c.Name()
	if name == "" {
		return errors.New("caerus: component Name() must not be empty")
	}
	if _, exists := f.byName[name]; exists {
		return fmt.Errorf("caerus: component %q is already registered", name)
	}
	stage := c.GetInitOrderStage()
	if stage == "" {
		return fmt.Errorf("caerus: component %q declares an empty init stage", name)
	}
	if !f.stageRegisteredLocked(stage) {
		f.stages = append(f.stages, stage)
	}
	f.components = append(f.components, c)
	f.byName[name] = c
	f.order = nil // invalidate any cached init order
	return nil
}

// stageRegisteredLocked reports whether name is already a registered stage.
// Callers must hold f.mu.
func (f *CaerusFramework) stageRegisteredLocked(name Stage) bool {
	for _, s := range f.stages {
		if s == name {
			return true
		}
	}
	return false
}

// Validate resolves the component dependency graph and returns the resolved
// initialization order. It reports unknown dependency names and dependency
// cycles. Validate is safe to call before Run, e.g. from a test, so that
// wiring problems are caught at build/CI time rather than in production.
func (f *CaerusFramework) Validate() ([]CaerusComponent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.order != nil {
		return f.order, nil
	}
	order, err := f.resolveOrder()
	if err != nil {
		return nil, err
	}
	f.order = order
	return order, nil
}

// Initialize initializes every component in resolved dependency order. Within
// each topological wave (components with no unmet same-stage dependencies on
// each other), Init runs concurrently. Stages still run strictly in order, and
// a later wave does not start until the previous wave has finished.
//
// On the first failure it cancels in-flight inits in the current wave, shuts
// down every successfully initialized component in reverse order, and returns
// the error. Initialize is idempotent: calling it again after a successful run
// is a no-op. Concurrent Initialize calls are serialized; a second caller
// waits and then observes the started state. Panics in Init are recovered and
// returned as errors.
func (f *CaerusFramework) Initialize(ctx context.Context) error {
	f.startMu.Lock()
	defer f.startMu.Unlock()

	if err := f.absorbArgs(); err != nil {
		return err
	}

	f.mu.Lock()
	if f.jobRan {
		f.mu.Unlock()
		return errors.New("caerus: cannot Initialize after a job; one entrypoint per process (construct a new framework)")
	}
	if f.started {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	if _, err := f.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	waves, err := f.resolveWaves()
	f.mu.Unlock()
	if err != nil {
		return err
	}

	for _, wave := range waves {
		if err := f.initWave(ctx, wave); err != nil {
			f.shutdownAll(ctx)
			return err
		}
	}

	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return nil
}

// initWave runs Init for every component in the wave. A single-component wave
// stays synchronous; larger waves run Init concurrently. On any failure the
// wave context is canceled and the function waits for peers to finish. Only
// components that succeeded are appended to initialized (so Shutdown can tear
// them down).
func (f *CaerusFramework) initWave(ctx context.Context, wave []CaerusComponent) error {
	if len(wave) == 0 {
		return nil
	}
	if len(wave) == 1 {
		c := wave[0]
		if err := safeInit(c, ctx, f); err != nil {
			return fmt.Errorf("caerus: component %q failed to initialize: %w", c.Name(), err)
		}
		f.mu.Lock()
		f.initialized = append(f.initialized, c)
		f.mu.Unlock()
		return nil
	}

	waveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		c   CaerusComponent
		err error
	}
	results := make([]result, len(wave))
	var wg sync.WaitGroup
	for i, c := range wave {
		wg.Add(1)
		go func(i int, c CaerusComponent) {
			defer wg.Done()
			err := safeInit(c, waveCtx, f)
			if err != nil {
				cancel()
				results[i] = result{c: c, err: fmt.Errorf("caerus: component %q failed to initialize: %w", c.Name(), err)}
				return
			}
			results[i] = result{c: c}
		}(i, c)
	}
	wg.Wait()

	var firstErr error
	succeeded := make([]CaerusComponent, 0, len(wave))
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.c != nil {
			succeeded = append(succeeded, r.c)
		}
	}
	if len(succeeded) > 0 {
		f.mu.Lock()
		f.initialized = append(f.initialized, succeeded...)
		f.mu.Unlock()
	}
	return firstErr
}

// Run initializes all components, starts every Runnable component in a
// goroutine, and blocks until ctx is canceled or a runner returns an error. On
// return, all initialized components are shut down in reverse init order. A
// clean cancellation returns nil.
//
// For production services that should stop on SIGINT/SIGTERM with bounded
// shutdown, prefer RunWithSignals.
//
// Concurrent Run calls are rejected. Panics in runners are recovered and
// returned as errors (and cancel peer runners).
func (f *CaerusFramework) Run(ctx context.Context) error {
	f.mu.Lock()
	jobRan := f.jobRan
	f.mu.Unlock()
	if jobRan {
		return errors.New("caerus: cannot Run after a job; one entrypoint per process (construct a new framework)")
	}

	if err := f.ensureInitialized(ctx); err != nil {
		return err
	}

	runErr := f.runRunners(ctx)

	shutdownCtx := ctx
	if ctx.Err() != nil {
		// Parent is already canceled; do not starve Shutdown.
		shutdownCtx = context.Background()
	}
	shutdownErr := f.Shutdown(shutdownCtx)
	if runErr == nil {
		return shutdownErr
	}
	return runErr
}

// runRunners starts every Runnable and waits until ctx is done or a runner
// fails. It does not initialize or shut down components.
func (f *CaerusFramework) runRunners(ctx context.Context) error {
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return errors.New("caerus: Run already in progress")
	}
	f.running = true
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running = false
		f.mu.Unlock()
	}()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	order, err := f.Validate()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(order))
	for _, c := range order {
		r, ok := c.(Runnable)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(c CaerusComponent, r Runnable) {
			defer wg.Done()
			if err := safeRun(c, r, runCtx); err != nil {
				errCh <- err
				cancel()
			}
		}(c, r)
	}
	wg.Wait()
	close(errCh)

	var firstErr error
	for err := range errCh {
		if errors.Is(err, context.Canceled) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Shutdown gracefully stops every initialized component in reverse init order.
// It is idempotent: components shut down by an earlier call are skipped, and a
// framework that has not been initialized has nothing to do. Panics in
// Shutdown are recovered and returned as errors.
//
// Shutdown refuses while Run is in progress: cancel the Run context (or send
// SIGINT/SIGTERM to RunWithSignals) so runners drain, then Shutdown runs as
// part of that return path. Calling Shutdown from another goroutine while
// runners are live would tear peers out from under them.
func (f *CaerusFramework) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return errors.New("caerus: cannot Shutdown while Run is in progress; cancel the Run context (SIGINT/SIGTERM for RunWithSignals)")
	}
	defer f.mu.Unlock()
	return f.shutdownAllLocked(ctx)
}

func (f *CaerusFramework) ensureInitialized(ctx context.Context) error {
	f.mu.Lock()
	started := f.started
	f.mu.Unlock()
	if started {
		return nil
	}
	return f.Initialize(ctx)
}

func (f *CaerusFramework) shutdownAll(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdownAllLocked(ctx)
}

func (f *CaerusFramework) shutdownAllLocked(ctx context.Context) error {
	var firstErr error
	for i := len(f.initialized) - 1; i >= 0; i-- {
		c := f.initialized[i]
		if err := safeShutdown(c, ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("caerus: component %q failed to shut down: %w", c.Name(), err)
		}
	}
	f.initialized = nil
	f.started = false
	return firstErr
}

func safeInit(c CaerusComponent, ctx context.Context, fw *CaerusFramework) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.Init(ctx, fw)
}

func safeRun(c CaerusComponent, r Runnable, ctx context.Context) (err error) {
	defer func() {
		if rcv := recover(); rcv != nil {
			err = fmt.Errorf("caerus: component %q runner panicked: %v", c.Name(), rcv)
		}
	}()
	if err := r.Run(ctx); err != nil {
		return fmt.Errorf("caerus: component %q runner failed: %w", c.Name(), err)
	}
	return nil
}

func safeShutdown(c CaerusComponent, ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.Shutdown(ctx)
}

// Get returns the registered component of type T. Components are stored as
// their concrete pointer type, so T must match that pointer type, e.g.
//
//	var mongo *cf_mongodb.CFMongoDB = caerusframework.MustGet[*cf_mongodb.CFMongoDB](fw)
//
// If multiple components of type T are registered, Get returns false. Use
// GetByName to disambiguate when multiple instances exist.
func Get[T CaerusComponent](f *CaerusFramework) (T, bool) {
	var zero T
	if f == nil {
		return zero, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var found T
	count := 0
	for _, c := range f.components {
		if typed, ok := c.(T); ok {
			found = typed
			count++
		}
	}
	if count == 1 {
		return found, true
	}
	// Zero or multiple matches
	return zero, false
}

// MustGet returns the registered component of type T or panics if it is not
// present. Prefer Get and handle the missing case explicitly; MustGet is a
// convenience for Init and tests where the peer is guaranteed to be present.
// The framework recovers panics in Init, Run, and Shutdown only — do not call
// MustGet from an arbitrary goroutine.
func MustGet[T CaerusComponent](f *CaerusFramework) T {
	typed, ok := Get[T](f)
	if !ok {
		var zero T
		panic(fmt.Sprintf("caerus: component of type %T is not registered", zero))
	}
	return typed
}

// GetByName returns the registered component with the given name, type-asserted
// to T. It combines name-based lookup with type safety, useful when multiple
// instances of the same component type exist (e.g., multiple valkey clients).
// Returns false if no component with that name exists or if the type assertion
// fails.
func GetByName[T CaerusComponent](f *CaerusFramework, name string) (T, bool) {
	var zero T
	if f == nil {
		return zero, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byName[name]
	if !ok {
		return zero, false
	}
	typed, ok := c.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}

// MustGetByName returns the registered component with the given name,
// type-asserted to T, or panics if it is not present or the type assertion
// fails. Prefer GetByName and handle the missing case explicitly.
func MustGetByName[T CaerusComponent](f *CaerusFramework, name string) T {
	typed, ok := GetByName[T](f, name)
	if !ok {
		var zero T
		panic(fmt.Sprintf("caerus: component %q of type %T is not registered", name, zero))
	}
	return typed
}

// Component returns the registered component with the given Name (as declared
// by CaerusComponent.Name), or false if no such component is registered. It is
// the name-based counterpart of Get[T], useful when a component must be
// reached by its declared name rather than its Go type.
func (f *CaerusFramework) Component(name string) (CaerusComponent, bool) {
	if f == nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byName[name]
	return c, ok
}

// Components returns every registered component in registration order. It is a
// snapshot: the returned slice is a copy and safe to retain after returning.
// Components use it to discover optional interfaces implemented by their peers
// (e.g. the observability component finds HealthProvider components).
func (f *CaerusFramework) Components() []CaerusComponent {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CaerusComponent, len(f.components))
	copy(out, f.components)
	return out
}

// LeftoverArgs returns the argv the configuration component did not consume
// (unknown flags, subcommand tokens and positional args). It is empty until the
// framework has absorbed argv, which happens at the start of Initialize, Run,
// RunWithSignals and Migrate. main uses it for subcommand/positional handling
// (e.g. the demoapp `price get <uuid>` positional args).
func (f *CaerusFramework) LeftoverArgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leftover == nil {
		return nil
	}
	out := make([]string, len(f.leftover))
	copy(out, f.leftover)
	return out
}

// AbsorbArgs runs the ConfigSourceRegistrar pass (components register their own
// configuration sources) and the configuration component's flag overlay, then
// populates LeftoverArgs — without initializing the chassis. It is idempotent;
// Initialize, Run, RunWithSignals and Migrate absorb automatically. main uses
// it to read subcommand positionals (e.g. demoapp price get <uuid>) before
// choosing which process shape to run.
func (f *CaerusFramework) AbsorbArgs() error {
	return f.absorbArgs()
}

// absorbArgs runs the ConfigSourceRegistrar pass (components register their own
// configuration sources, including the core logs/observability sources via
// CoreConfigSource) and then hands the process argv to the configuration
// component (ParseFlags), so flag overlays and --<source> file-path overrides
// apply before any component initializes. Unknown flags and positional args are
// kept as the leftover args. Idempotent; concurrent callers are serialized.
//
// A bare framework (no configuration component registered) absorbs nothing.
func (f *CaerusFramework) absorbArgs() error {
	f.absorbMu.Lock()
	defer f.absorbMu.Unlock()

	f.mu.Lock()
	if f.argsAbsorbed {
		f.mu.Unlock()
		return nil
	}
	comps := make([]CaerusComponent, len(f.components))
	copy(comps, f.components)
	var conf ConfigArgv
	for _, c := range f.components {
		if ca, ok := c.(ConfigArgv); ok {
			conf = ca
			break
		}
	}
	f.mu.Unlock()
	if conf == nil {
		f.mu.Lock()
		f.argsAbsorbed = true
		f.mu.Unlock()
		return nil
	}

	// Self-sufficient components register their own configuration sources.
	for _, c := range comps {
		if reg, ok := c.(ConfigSourceRegistrar); ok {
			if err := reg.RegisterConfigSources(conf); err != nil {
				return fmt.Errorf("caerus: component %q register config sources: %w", c.Name(), err)
			}
		}
	}

	// Core modules (logs, observability) cannot implement ConfigSourceRegistrar
	// (the configuration module imports them); they declare their sources via
	// CoreConfigSource instead. Collected here, before ParseFlags, so the
	// --<name> file-path flag definitions exist.
	if adder, ok := conf.(ConfigSourceAdder); ok {
		for _, c := range comps {
			cs, ok := c.(CoreConfigSource)
			if !ok {
				continue
			}
			decls, err := cs.CoreConfigSource()
			if err != nil {
				return fmt.Errorf("caerus: component %q config source: %w", c.Name(), err)
			}
			for _, d := range decls {
				if err := adder.AddSourceValue(d); err != nil {
					return fmt.Errorf("caerus: component %q register config source %q: %w", c.Name(), d.Name, err)
				}
			}
		}
	}

	// Hand argv to configuration. Registrars ran first so flag definitions exist.
	args := f.optsArgs
	if args == nil {
		args = os.Args[1:]
	}
	rest, err := conf.ParseFlags(args)
	if err != nil {
		return fmt.Errorf("caerus: absorb argv: %w", err)
	}

	f.mu.Lock()
	f.leftover = rest
	f.argsAbsorbed = true
	f.mu.Unlock()
	return nil
}
