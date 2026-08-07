package caerusframework

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a minimal CaerusComponent for tests.
type fake struct {
	name  string
	stage Stage
	deps  []string
	init  func(ctx context.Context) error
	shut  func(ctx context.Context) error
}

// Application-defined test stages, registered after the bootstrap prefix.
const (
	testDataStage     = Stage("data")
	testBusinessStage = Stage("business")
	testServeStage    = Stage("serve")
)

// newTestFW returns a bare framework. Custom stages are auto-registered by
// AddComponent in first-seen order, so tests that mix stages add their
// components in stage order.
func newTestFW() *CaerusFramework {
	return New()
}

func newFake(name string, stage Stage, deps ...string) *fake {
	return &fake{name: name, stage: stage, deps: deps}
}

func (f *fake) Name() string              { return f.name }
func (f *fake) GetInitOrderStage() Stage  { return f.stage }
func (f *fake) GetDependencies() []string { return f.deps }
func (f *fake) Init(ctx context.Context, fw *CaerusFramework) error {
	if f.init != nil {
		return f.init(ctx)
	}
	return nil
}
func (f *fake) Shutdown(ctx context.Context) error {
	if f.shut != nil {
		return f.shut(ctx)
	}
	return nil
}

// runner embeds *fake (so it satisfies CaerusComponent) and additionally
// implements Runnable.
type runner struct {
	*fake
	run func(ctx context.Context) error
}

func (r *runner) Run(ctx context.Context) error {
	if r.run != nil {
		return r.run(ctx)
	}
	return nil
}

// healthy embeds *fake (so it satisfies CaerusComponent) and additionally
// implements HealthProvider.
type healthy struct {
	*fake
	health func(ctx context.Context) error
}

func (h *healthy) Health(ctx context.Context) error {
	if h.health != nil {
		return h.health(ctx)
	}
	return nil
}

func mustAdd(t *testing.T, fw *CaerusFramework, c CaerusComponent) {
	t.Helper()
	if err := fw.AddComponent(c); err != nil {
		t.Fatalf("AddComponent(%q): %v", c.Name(), err)
	}
}

func names(components []CaerusComponent) []string {
	out := make([]string, 0, len(components))
	for _, c := range components {
		out = append(out, c.Name())
	}
	return out
}

func TestDependencyOrder(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("b", testDataStage, "c"))
	mustAdd(t, fw, newFake("c", testDataStage))
	mustAdd(t, fw, newFake("a", testBusinessStage, "b", "c"))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "c,b,a" {
		t.Fatalf("expected c,b,a, got %q", got)
	}
}

func TestBucketTieBreak(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("db", testDataStage))
	mustAdd(t, fw, newFake("cache", testDataStage))
	mustAdd(t, fw, newFake("web", testServeStage))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "db,cache,web" {
		t.Fatalf("expected stage order db,cache,web, got %q", got)
	}
}

func TestRegistrationOrderTieBreak(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("x", testDataStage))
	mustAdd(t, fw, newFake("y", testDataStage))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "x,y" {
		t.Fatalf("expected x,y, got %q", got)
	}
}

func TestUnknownDependency(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("a", LogsStage, "missing"))

	_, err := fw.Validate()
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected unknown dependency error mentioning missing, got %v", err)
	}
}

func TestCycleDetection(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("a", LogsStage, "b"))
	mustAdd(t, fw, newFake("b", LogsStage, "a"))

	_, err := fw.Validate()
	if err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	if !strings.Contains(err.Error(), "a -> b -> a") {
		t.Fatalf("expected cycle path a -> b -> a in error, got %v", err)
	}
}

func TestThreeNodeCycle(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("a", LogsStage, "b"))
	mustAdd(t, fw, newFake("b", LogsStage, "c"))
	mustAdd(t, fw, newFake("c", LogsStage, "a"))

	_, err := fw.Validate()
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	if !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), n) {
			t.Fatalf("expected cycle path to mention %q, got %v", n, err)
		}
	}
}

func TestSelfDependencyCycle(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("a", LogsStage, "a"))

	_, err := fw.Validate()
	if err == nil || !strings.Contains(err.Error(), "a -> a") {
		t.Fatalf("expected self-cycle error, got %v", err)
	}
}

func TestValidateDeterministicAndCached(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("b", testDataStage))
	mustAdd(t, fw, newFake("a", testBusinessStage, "b"))

	first, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	second, err := fw.Validate()
	if err != nil {
		t.Fatalf("second Validate: %v", err)
	}
	if got := names(first); strings.Join(got, ",") != "b,a" {
		t.Fatalf("unexpected order %v", got)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("Validate not cached: %v vs %v", names(first), names(second))
		}
	}
}

func TestCycleDetectedBeforeInit(t *testing.T) {
	fw := newTestFW()
	initCalled := false
	cyc := newFake("a", LogsStage, "b")
	cyc.init = func(ctx context.Context) error { initCalled = true; return nil }
	mustAdd(t, fw, cyc)
	mustAdd(t, fw, newFake("b", LogsStage, "a"))

	err := fw.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	if initCalled {
		t.Fatal("a component must not initialize when the dependency graph is cyclic")
	}
}

func TestInitFailureShutsDownInitialized(t *testing.T) {
	fw := newTestFW()
	var mu sync.Mutex
	var initOrder, shutOrder []string

	a := newFake("a", LogsStage)
	a.init = func(ctx context.Context) error {
		mu.Lock()
		initOrder = append(initOrder, "a")
		mu.Unlock()
		return nil
	}
	a.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "a")
		mu.Unlock()
		return nil
	}

	b := newFake("b", LogsStage, "a")
	b.init = func(ctx context.Context) error {
		mu.Lock()
		initOrder = append(initOrder, "b")
		mu.Unlock()
		return errors.New("boom")
	}

	mustAdd(t, fw, a)
	mustAdd(t, fw, b)

	err := fw.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), `component "b" failed to initialize`) {
		t.Fatalf("expected init failure for b, got %v", err)
	}
	if got := strings.Join(initOrder, ","); got != "a,b" {
		t.Fatalf("expected init order a,b, got %q", got)
	}
	if got := strings.Join(shutOrder, ","); got != "a" {
		t.Fatalf("expected only a to be shut down, got %q", got)
	}
}

func TestShutdownReverseOrder(t *testing.T) {
	fw := newTestFW()
	var mu sync.Mutex
	var initOrder, shutOrder []string

	a := newFake("a", testBusinessStage, "b")
	a.init = func(ctx context.Context) error {
		mu.Lock()
		initOrder = append(initOrder, "a")
		mu.Unlock()
		return nil
	}
	a.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "a")
		mu.Unlock()
		return nil
	}

	b := newFake("b", testDataStage)
	b.init = func(ctx context.Context) error {
		mu.Lock()
		initOrder = append(initOrder, "b")
		mu.Unlock()
		return nil
	}
	b.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "b")
		mu.Unlock()
		return nil
	}

	mustAdd(t, fw, b)
	mustAdd(t, fw, a)

	if err := fw.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Join(initOrder, ","); got != "b,a" {
		t.Fatalf("expected init order b,a, got %q", got)
	}
	if got := strings.Join(shutOrder, ","); got != "a,b" {
		t.Fatalf("expected shutdown order a,b, got %q", got)
	}
}

func TestRunCleanCancel(t *testing.T) {
	fw := newTestFW()
	initDone := make(chan struct{})
	var shutCalled atomicBool

	worker := &runner{
		fake: newFake("worker", testServeStage),
		run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker.fake.init = func(ctx context.Context) error {
		close(initDone)
		return nil
	}
	worker.fake.shut = func(ctx context.Context) error {
		shutCalled.set(true)
		return nil
	}
	mustAdd(t, fw, worker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fw.Run(ctx) }()

	<-initDone
	time.Sleep(20 * time.Millisecond) // let the runner start
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on clean cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
	if !shutCalled.get() {
		t.Fatal("expected worker Shutdown to be called")
	}
}

func TestRunRunnerError(t *testing.T) {
	fw := newTestFW()
	var mu sync.Mutex
	var shutOrder []string

	other := newFake("other", LogsStage)
	other.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "other")
		mu.Unlock()
		return nil
	}
	mustAdd(t, fw, other)

	worker := &runner{
		fake: newFake("worker", testServeStage),
		run:  func(ctx context.Context) error { return errors.New("boom") },
	}
	worker.fake.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "worker")
		mu.Unlock()
		return nil
	}
	mustAdd(t, fw, worker)

	err := fw.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected runner error, got %v", err)
	}
	if got := strings.Join(shutOrder, ","); got != "worker,other" {
		t.Fatalf("expected all components shut down in reverse order, got %q", got)
	}
}

func TestInitializeIdempotent(t *testing.T) {
	fw := newTestFW()
	initCount := 0
	c := newFake("c", LogsStage)
	c.init = func(ctx context.Context) error { initCount++; return nil }
	mustAdd(t, fw, c)

	ctx := context.Background()
	if err := fw.Initialize(ctx); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	if err := fw.Initialize(ctx); err != nil {
		t.Fatalf("second Initialize: %v", err)
	}
	if initCount != 1 {
		t.Fatalf("expected component initialized once, got %d", initCount)
	}
}

func TestShutdownIdempotent(t *testing.T) {
	fw := newTestFW()
	shutCount := 0
	c := newFake("c", LogsStage)
	c.shut = func(ctx context.Context) error { shutCount++; return nil }
	mustAdd(t, fw, c)

	ctx := context.Background()
	if err := fw.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := fw.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if shutCount != 1 {
		t.Fatalf("expected Shutdown called once, got %d", shutCount)
	}
}

func TestAddComponentValidation(t *testing.T) {
	fw := newTestFW()
	if err := fw.AddComponent(nil); err == nil {
		t.Fatal("expected error for nil component")
	}
	empty := newFake("", LogsStage)
	if err := fw.AddComponent(empty); err == nil {
		t.Fatal("expected error for empty name")
	}
	c := newFake("dup", LogsStage)
	if err := fw.AddComponent(c); err != nil {
		t.Fatalf("first AddComponent: %v", err)
	}
	if err := fw.AddComponent(c); err == nil {
		t.Fatal("expected error for duplicate name")
	}
	badStage := newFake("badstage", "")
	if err := fw.AddComponent(badStage); err == nil {
		t.Fatal("expected error for empty stage")
	}
}

// composite embeds *fake and returns children from Subcomponents.
type composite struct {
	*fake
	children []CaerusComponent
}

func (c *composite) Subcomponents() []CaerusComponent { return c.children }

func TestAddComponentExpandsSubcomponentsBFS(t *testing.T) {
	fw := newTestFW()
	grandchild := newFake("grandchild", testDataStage)
	childA := &composite{fake: newFake("child-a", testDataStage), children: []CaerusComponent{grandchild}}
	childB := newFake("child-b", testDataStage)
	parent := &composite{
		fake:     newFake("parent", testBusinessStage),
		children: []CaerusComponent{childA, childB},
	}
	if err := fw.AddComponent(parent); err != nil {
		t.Fatalf("AddComponent: %v", err)
	}
	// BFS registration order: parent, child-a, child-b, grandchild
	want := []string{"parent", "child-a", "child-b", "grandchild"}
	var got []string
	for _, c := range fw.components {
		got = append(got, c.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("registered = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
	if _, ok := fw.Component("grandchild"); !ok {
		t.Fatal("grandchild missing from registry")
	}
}

func TestAddComponentSubcomponentsNilChild(t *testing.T) {
	fw := newTestFW()
	parent := &composite{
		fake:     newFake("parent", testDataStage),
		children: []CaerusComponent{newFake("ok", testDataStage), nil},
	}
	if err := fw.AddComponent(parent); err == nil {
		t.Fatal("expected error for nil Subcomponents entry")
	}
}

func TestAddComponentSubcomponentsDuplicateName(t *testing.T) {
	fw := newTestFW()
	dup := newFake("dup", testDataStage)
	parent := &composite{
		fake:     newFake("parent", testDataStage),
		children: []CaerusComponent{dup, newFake("dup", testDataStage)},
	}
	if err := fw.AddComponent(parent); err == nil {
		t.Fatal("expected error for duplicate child name")
	}
}

func TestGetAndMustGet(t *testing.T) {
	fw := newTestFW()
	mongo := &concreteComp{fake: newFake("mongo", testDataStage)}
	mustAdd(t, fw, mongo)

	got, ok := Get[*concreteComp](fw)
	if !ok || got != mongo {
		t.Fatalf("Get returned wrong component: %v, %v", got, ok)
	}
	if got := MustGet[*concreteComp](fw); got != mongo {
		t.Fatalf("MustGet returned wrong component: %v", got)
	}

	if _, ok := Get[*fake](fw); ok {
		t.Fatal("Get[*fake] should not match a *concreteComp registration")
	}
}

func TestMustGetPanicsOnMissing(t *testing.T) {
	fw := newTestFW()
	defer func() {
		if recover() == nil {
			t.Fatal("expected MustGet to panic for missing component")
		}
	}()
	MustGet[*fake](fw)
}

func TestGetByNameAndMustGetByName(t *testing.T) {
	fw := newTestFW()
	cache := &concreteComp{fake: newFake("cache", testDataStage)}
	sessions := &concreteComp{fake: newFake("sessions", testDataStage)}
	mustAdd(t, fw, cache)
	mustAdd(t, fw, sessions)

	// GetByName retrieves by name with type safety
	got, ok := GetByName[*concreteComp](fw, "cache")
	if !ok || got != cache {
		t.Fatalf("GetByName(cache) returned wrong component: %v, %v", got, ok)
	}
	got, ok = GetByName[*concreteComp](fw, "sessions")
	if !ok || got != sessions {
		t.Fatalf("GetByName(sessions) returned wrong component: %v, %v", got, ok)
	}

	// MustGetByName works for existing components
	if got := MustGetByName[*concreteComp](fw, "cache"); got != cache {
		t.Fatalf("MustGetByName(cache) returned wrong component: %v", got)
	}

	// GetByName returns false for missing name
	if _, ok := GetByName[*concreteComp](fw, "missing"); ok {
		t.Fatal("GetByName should return false for missing name")
	}

	// GetByName returns false for wrong type
	if _, ok := GetByName[*fake](fw, "cache"); ok {
		t.Fatal("GetByName should return false for wrong type")
	}
}

func TestMustGetByNamePanicsOnMissing(t *testing.T) {
	fw := newTestFW()
	defer func() {
		if recover() == nil {
			t.Fatal("expected MustGetByName to panic for missing component")
		}
	}()
	MustGetByName[*fake](fw, "missing")
}

func TestGetFailsOnMultipleMatches(t *testing.T) {
	fw := newTestFW()
	cache := &concreteComp{fake: newFake("cache", testDataStage)}
	sessions := &concreteComp{fake: newFake("sessions", testDataStage)}
	mustAdd(t, fw, cache)
	mustAdd(t, fw, sessions)

	// Get returns false when multiple components of the same type exist
	if _, ok := Get[*concreteComp](fw); ok {
		t.Fatal("Get should return false when multiple components of the same type exist")
	}

	// MustGet panics when multiple components of the same type exist
	defer func() {
		if recover() == nil {
			t.Fatal("expected MustGet to panic when multiple components of the same type exist")
		}
	}()
	MustGet[*concreteComp](fw)
}

func TestComponentByName(t *testing.T) {
	fw := newTestFW()
	mongo := newFake("mongo", testDataStage)
	mustAdd(t, fw, mongo)

	got, ok := fw.Component("mongo")
	if !ok || got != mongo {
		t.Fatalf("Component(\"mongo\") = %v, %v; want the registered component", got, ok)
	}
	if _, ok := fw.Component("missing"); ok {
		t.Fatal("expected Component(\"missing\") to report not found")
	}

	var nilFW *CaerusFramework
	if _, ok := nilFW.Component("mongo"); ok {
		t.Fatal("expected Component on a nil framework to report not found")
	}
}

func TestComponentsSnapshotAndHealthProviderDiscovery(t *testing.T) {
	fw := newTestFW()
	db := newFake("db", testDataStage)
	bad := &healthy{fake: newFake("bad", testDataStage), health: func(ctx context.Context) error { return errors.New("down") }}
	mustAdd(t, fw, db)
	mustAdd(t, fw, bad)

	// Components returns a snapshot in registration order.
	comps := fw.Components()
	if got := names(comps); len(got) != 2 || got[0] != "db" || got[1] != "bad" {
		t.Fatalf("Components() = %v, want [db bad]", got)
	}

	// HealthProvider discovery: only components implementing it are found.
	providers := make(map[string]HealthProvider)
	for _, c := range fw.Components() {
		if h, ok := c.(HealthProvider); ok {
			providers[c.Name()] = h
		}
	}
	if _, ok := providers["db"]; ok {
		t.Fatal("db does not implement HealthProvider and must not be discovered")
	}
	h, ok := providers["bad"]
	if !ok {
		t.Fatal("bad implements HealthProvider and must be discovered")
	}
	if err := h.Health(context.Background()); err == nil {
		t.Fatal("bad.Health should report its error")
	}

	// The snapshot is a copy: mutating it must not affect the framework.
	comps[0] = newFake("mutated", testDataStage)
	if len(fw.Components()) != 2 {
		t.Fatal("Components() must return a defensive copy")
	}

	var nilFW *CaerusFramework
	if nilFW.Components() != nil {
		t.Fatal("expected Components on a nil framework to return nil")
	}
}

func TestForwardStageDependencyRejected(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("core", LogsStage, "db"))
	mustAdd(t, fw, newFake("db", testDataStage))

	_, err := fw.Validate()
	if err == nil || !strings.Contains(err.Error(), "initializes later") {
		t.Fatalf("expected forward-stage dependency error, got %v", err)
	}
	for _, s := range []string{`"core"`, `"db"`, "logs", "data"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("expected error to mention %q, got %v", s, err)
		}
	}
}

func TestCrossStageCycleRejectedAsForwardDependency(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("a", LogsStage, "b"))
	mustAdd(t, fw, newFake("b", testDataStage, "a"))

	_, err := fw.Validate()
	if err == nil || !strings.Contains(err.Error(), "initializes later") {
		t.Fatalf("expected forward-stage dependency error, got %v", err)
	}
}

func TestBootstrapIntraStageDependency(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("secrets", LogsStage, "configuration"))
	mustAdd(t, fw, newFake("configuration", LogsStage, "logs"))
	mustAdd(t, fw, newFake("logs", LogsStage))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "logs,configuration,secrets" {
		t.Fatalf("expected logs,configuration,secrets, got %q", got)
	}
}

func TestSameStageDependencyWithinStage(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("db", testDataStage))
	mustAdd(t, fw, newFake("app2", testBusinessStage, "db"))
	mustAdd(t, fw, newFake("app", testBusinessStage, "app2"))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "db,app2,app" {
		t.Fatalf("expected db,app2,app, got %q", got)
	}
}

func TestRunNormalCompletion(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("db", testDataStage))
	mustAdd(t, fw, newFake("app", testBusinessStage, "db"))
	if err := fw.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestBootstrapStageOrderFixed(t *testing.T) {
	fw := New()
	mustAdd(t, fw, newFake("secrets", SecretsStage))
	mustAdd(t, fw, newFake("obs", ObservabilityStage))
	mustAdd(t, fw, newFake("config", ConfigurationStage))
	mustAdd(t, fw, newFake("logs", LogsStage))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "logs,config,obs,secrets" {
		t.Fatalf("expected bootstrap prefix logs,config,obs,secrets, got %q", got)
	}
}

func TestCustomStagesOrderAfterBootstrap(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("db", testDataStage))
	mustAdd(t, fw, newFake("app", testBusinessStage))
	mustAdd(t, fw, newFake("web", testServeStage))
	mustAdd(t, fw, newFake("logs", LogsStage))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "logs,db,app,web" {
		t.Fatalf("expected logs,db,app,web, got %q", got)
	}
}

func TestAddComponentAutoRegistersStage(t *testing.T) {
	fw := New()
	mustAdd(t, fw, newFake("rogue", Stage("not-registered")))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := names(order); len(got) != 1 || got[0] != "rogue" {
		t.Fatalf("order = %v, want [rogue]", got)
	}
}

// atomicBool is a tiny helper for tests.
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.v = v
}

func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

type concreteComp struct {
	*fake
}
