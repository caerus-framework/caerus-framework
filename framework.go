package caerusframework

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// CaerusFramework is the core of the framework. It owns the component registry
// and the stage registry, and drives the component lifecycle: dependency
// resolution, initialization, running, and shutdown.
type CaerusFramework struct {
	mu          sync.Mutex
	components  []CaerusComponent
	byName      map[string]CaerusComponent
	stages      []Stage
	order       []CaerusComponent
	initialized []CaerusComponent
	started     bool
}

// New creates a framework with the bootstrap stages registered. The bootstrap
// prefix is fixed: logs, configuration, observability, secrets, in that order.
// Register application stages with RegisterStage and components with
// AddComponent, then start the application with Run.
func New() *CaerusFramework {
	return &CaerusFramework{
		byName: make(map[string]CaerusComponent),
		stages: []Stage{LogsStage, ConfigurationStage, ObservabilityStage, SecretsStage},
	}
}

// RegisterStage declares an application-defined initialization stage. Stages
// initialize in the order they are registered. The framework-owned bootstrap
// stages (logs, configuration, observability, secrets) are always registered
// first, in a fixed order, and cannot be redefined. Components declare the
// stage they belong to via GetInitOrderStage; a component whose stage is not
// registered fails Validate. RegisterStage may be called at any time before
// Validate/Initialize/Run.
func (f *CaerusFramework) RegisterStage(name Stage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		return errors.New("caerus: RegisterStage called with an empty stage name")
	}
	for _, s := range f.stages {
		if s == name {
			return fmt.Errorf("caerus: stage %q is already registered", name)
		}
	}
	f.stages = append(f.stages, name)
	f.order = nil // invalidate any cached init order
	return nil
}

// AddComponent registers a component. Component names must be unique across
// the framework; a nil component is rejected.
func (f *CaerusFramework) AddComponent(c CaerusComponent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c == nil {
		return errors.New("caerus: AddComponent called with a nil component")
	}
	name := c.Name()
	if name == "" {
		return errors.New("caerus: component Name() must not be empty")
	}
	if _, exists := f.byName[name]; exists {
		return fmt.Errorf("caerus: component %q is already registered", name)
	}
	f.components = append(f.components, c)
	f.byName[name] = c
	f.order = nil // invalidate any cached init order
	return nil
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

// Initialize initializes every component in resolved dependency order. On the
// first failure it shuts down the already-initialized components in reverse
// order and returns the error. Initialize is idempotent: calling it again
// after a successful run is a no-op.
func (f *CaerusFramework) Initialize(ctx context.Context) error {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	order, err := f.Validate()
	if err != nil {
		return err
	}

	for _, c := range order {
		if err := c.Init(ctx, f); err != nil {
			f.shutdownAll(ctx)
			return fmt.Errorf("caerus: component %q failed to initialize: %w", c.Name(), err)
		}
		f.mu.Lock()
		f.initialized = append(f.initialized, c)
		f.mu.Unlock()
	}

	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return nil
}

// Run initializes all components, starts every Runnable component in a
// goroutine, and blocks until ctx is canceled or a runner returns an error. On
// return, all initialized components are shut down in reverse init order. A
// clean cancellation returns nil.
func (f *CaerusFramework) Run(ctx context.Context) error {
	if err := f.ensureInitialized(ctx); err != nil {
		return err
	}

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
			if err := r.Run(runCtx); err != nil {
				errCh <- fmt.Errorf("caerus: component %q runner failed: %w", c.Name(), err)
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

	shutdownErr := f.Shutdown(ctx)
	if firstErr == nil {
		firstErr = shutdownErr
	}
	return firstErr
}

// Shutdown gracefully stops every initialized component in reverse init order.
// It is idempotent: components shut down by an earlier call are skipped, and a
// framework that has not been initialized has nothing to do.
func (f *CaerusFramework) Shutdown(ctx context.Context) error {
	f.mu.Lock()
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
		if err := c.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("caerus: component %q failed to shut down: %w", c.Name(), err)
		}
	}
	f.initialized = nil
	f.started = false
	return firstErr
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
// convenience for wiring that is guaranteed to be present.
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
