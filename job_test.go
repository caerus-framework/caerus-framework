package caerusframework

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeConf implements ConfigArgv + JobSource to stand in for the configuration
// component in argv absorption and job tests.
type fakeConf struct {
	*fake
	parse func(args []string) ([]string, error)
	jobs  func() ([]JobRequest, error)
}

func (f *fakeConf) ParseFlags(args []string) ([]string, error) {
	if f.parse != nil {
		return f.parse(args)
	}
	return nil, nil
}

func (f *fakeConf) JobRequests() ([]JobRequest, error) {
	if f.jobs != nil {
		return f.jobs()
	}
	return nil, nil
}

// sourceRegistrar implements ConfigSourceRegistrar.
type sourceRegistrar struct {
	*fake
	called atomic.Int32
	conf   any
	regErr error
}

func (s *sourceRegistrar) RegisterConfigSources(conf any) error {
	s.called.Add(1)
	s.conf = conf
	return s.regErr
}

// coreSource implements CoreConfigSource.
type coreSource struct {
	*fake
	decls []ConfigSourceValue
	csErr error
}

func (s *coreSource) CoreConfigSource() ([]ConfigSourceValue, error) {
	return s.decls, s.csErr
}

// addingConf implements ConfigArgv + ConfigSourceAdder for the core-source pass.
type addingConf struct {
	*fake
	parse func(args []string) ([]string, error)
	added []ConfigSourceValue
}

func (f *addingConf) ParseFlags(args []string) ([]string, error) {
	if f.parse != nil {
		return f.parse(args)
	}
	return nil, nil
}

func (f *addingConf) AddSourceValue(src ConfigSourceValue) error {
	f.added = append(f.added, src)
	return nil
}

// migrator implements Migrator.
type migrator struct {
	*fake
	migrateCalls atomic.Int32
	migrate      func(ctx context.Context) error
}

func (m *migrator) Migrate(ctx context.Context) error {
	m.migrateCalls.Add(1)
	if m.migrate != nil {
		return m.migrate(ctx)
	}
	return nil
}

// jobRunner implements JobRunner.
type jobRunner struct {
	*fake
	runJobCalls atomic.Int32
	runJob      func(ctx context.Context, task string) error
}

func (r *jobRunner) RunJob(ctx context.Context, task string) error {
	r.runJobCalls.Add(1)
	if r.runJob != nil {
		return r.runJob(ctx, task)
	}
	return nil
}
func TestAbsorbArgsRunsRegistrarsBeforeParseFlags(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	reg := &sourceRegistrar{fake: newFake("db", testDataStage)}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, reg)
	fw.optsArgs = []string{"price", "get", "abc"}

	conf.parse = func(args []string) ([]string, error) {
		if reg.called.Load() != 1 {
			t.Errorf("ParseFlags ran before Registrars (called=%d)", reg.called.Load())
		}
		return args, nil
	}

	if err := fw.AbsorbArgs(); err != nil {
		t.Fatalf("AbsorbArgs: %v", err)
	}
	if got := reg.called.Load(); got != 1 {
		t.Fatalf("registrar called %d times, want 1", got)
	}
	if _, ok := reg.conf.(*fakeConf); !ok {
		t.Fatalf("registrar received %T, want *fakeConf", reg.conf)
	}
	if got := fw.LeftoverArgs(); len(got) != 3 || got[0] != "price" || got[2] != "abc" {
		t.Fatalf("LeftoverArgs = %v, want [price get abc]", got)
	}

	// Idempotent: a second absorb must not re-run registrars or re-parse.
	if err := fw.AbsorbArgs(); err != nil {
		t.Fatalf("second AbsorbArgs: %v", err)
	}
	if got := reg.called.Load(); got != 1 {
		t.Fatalf("registrar ran %d times after second absorb, want 1", got)
	}
}

func TestAbsorbArgsPropagatesRegistrarError(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	reg := &sourceRegistrar{fake: newFake("db", testDataStage), regErr: errors.New("boom")}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, reg)

	err := fw.AbsorbArgs()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected registrar error, got %v", err)
	}
}

func TestAbsorbArgsCollectsCoreSourcesBeforeParseFlags(t *testing.T) {
	fw := newTestFW()
	conf := &addingConf{fake: newFake("configuration", ConfigurationStage)}
	cs := &coreSource{
		fake: newFake("logs", LogsStage),
		decls: []ConfigSourceValue{{
			Name: "logs", Path: "config/logs.json", Format: "json",
			EnvPrefix: "LOGS_", Owner: "logs", Sample: "LogConfig",
		}},
	}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, cs)
	fw.optsArgs = []string{"serve", "--vpq-debug"}

	conf.parse = func(args []string) ([]string, error) {
		if len(conf.added) != 1 || conf.added[0].Name != "logs" {
			t.Errorf("ParseFlags ran before core sources were registered (added=%v)", conf.added)
		}
		return []string{"serve"}, nil
	}

	if err := fw.AbsorbArgs(); err != nil {
		t.Fatalf("AbsorbArgs: %v", err)
	}
	if len(conf.added) != 1 || conf.added[0].Name != "logs" || conf.added[0].Owner != "logs" {
		t.Fatalf("added = %+v, want the logs declaration", conf.added)
	}
	if got := fw.LeftoverArgs(); len(got) != 1 || got[0] != "serve" {
		t.Fatalf("LeftoverArgs = %v, want [serve]", got)
	}
}

func TestAbsorbArgsPropagatesCoreSourceError(t *testing.T) {
	fw := newTestFW()
	conf := &addingConf{fake: newFake("configuration", ConfigurationStage)}
	cs := &coreSource{fake: newFake("logs", LogsStage), csErr: errors.New("core boom")}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, cs)

	err := fw.AbsorbArgs()
	if err == nil || !strings.Contains(err.Error(), "core boom") {
		t.Fatalf("expected core source error, got %v", err)
	}
}

func TestMigrateInitializesOnlyCoreAndTarget(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	db := &migrator{fake: newFake("db", testDataStage)}
	sibling := newFake("sibling", testDataStage)

	var confInit, siblingInit atomic.Int32
	var dbInit, dbShut atomic.Int32
	conf.init = func(context.Context) error { confInit.Add(1); return nil }
	db.init = func(context.Context) error { dbInit.Add(1); return nil }
	sibling.init = func(context.Context) error { siblingInit.Add(1); return nil }
	db.shut = func(context.Context) error { dbShut.Add(1); return nil }

	mustAdd(t, fw, conf)
	mustAdd(t, fw, db)
	mustAdd(t, fw, sibling)

	if err := fw.Migrate(context.Background(), "db"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := db.migrateCalls.Load(); got != 1 {
		t.Fatalf("Migrate called %d times, want 1", got)
	}
	if confInit.Load() != 1 {
		t.Fatal("core configuration component was not initialized")
	}
	if dbInit.Load() != 1 {
		t.Fatal("target component was not initialized")
	}
	if siblingInit.Load() != 0 {
		t.Fatal("sibling component initialized during a job-only Migrate")
	}
	if dbShut.Load() != 1 {
		t.Fatal("target was not shut down after the job")
	}
}

func TestMigrateUnknownTarget(t *testing.T) {
	fw := newTestFW()
	if err := fw.Migrate(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown target")
	} else if !strings.Contains(err.Error(), "not a registered component") {
		t.Fatalf("error should name the unknown target: %v", err)
	}
}

func TestMigrateRequiresJobInterface(t *testing.T) {
	fw := newTestFW()
	plain := newFake("orders", testDataStage)
	mustAdd(t, fw, plain)

	err := fw.Migrate(context.Background(), "orders")
	if err == nil || !strings.Contains(err.Error(), "JobRunner") {
		t.Fatalf("expected JobRunner interface error, got %v", err)
	}
}

func TestRunWithSignalsJobRequestRunsJobInsteadOfServing(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{{Component: "postgresql", Flag: "postgresql.job", Task: "migrate"}}, nil
	}
	db := &migrator{fake: newFake("postgresql", testDataStage)}
	var workerRun atomic.Int32
	worker := &runner{
		fake: newFake("worker", testServeStage),
		run:  func(context.Context) error { workerRun.Add(1); return nil },
	}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, db)
	mustAdd(t, fw, worker)

	if err := fw.RunWithSignals(context.Background()); err != nil {
		t.Fatalf("RunWithSignals: %v", err)
	}
	if got := db.migrateCalls.Load(); got != 1 {
		t.Fatalf("postgresql Migrate called %d times, want 1", got)
	}
	if workerRun.Load() != 0 {
		t.Fatal("runner started despite job request; serve path should not run")
	}
}

func TestRunJobDoesNotStartBootstrapRunnable(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	db := &migrator{fake: newFake("db", testDataStage)}
	var obsInit, obsRun atomic.Int32
	obs := &runner{
		fake: newFake("observability", ObservabilityStage),
		run: func(context.Context) error {
			obsRun.Add(1)
			return nil
		},
	}
	obs.init = func(context.Context) error { obsInit.Add(1); return nil }
	mustAdd(t, fw, conf)
	mustAdd(t, fw, obs)
	mustAdd(t, fw, db)

	if err := fw.Migrate(context.Background(), "db"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if obsInit.Load() != 1 {
		t.Fatal("bootstrap-stage component was not initialized on the job path")
	}
	if obsRun.Load() != 0 {
		t.Fatal("bootstrap-stage Runnable started on the job path; jobs must not call Run")
	}
	if db.migrateCalls.Load() != 1 {
		t.Fatal("migrate task did not run")
	}
}

func TestRunWithSignalsJobRequestRunsTaskOnJobRunner(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{{Component: "postgresql", Flag: "postgresql.job", Task: "migrate"}}, nil
	}
	var gotTask string
	comp := &jobRunner{
		fake: newFake("postgresql", testDataStage),
		runJob: func(_ context.Context, task string) error {
			gotTask = task
			return nil
		},
	}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, comp)

	if err := fw.RunWithSignals(context.Background()); err != nil {
		t.Fatalf("RunWithSignals: %v", err)
	}
	if comp.runJobCalls.Load() != 1 || gotTask != "migrate" {
		t.Fatalf("RunJob called %d times with %q, want 1 with migrate", comp.runJobCalls.Load(), gotTask)
	}
}

func TestRunJobUnknownTaskOnMigratorOnlyComponent(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{{Component: "postgresql", Flag: "postgresql.job", Task: "reindex"}}, nil
	}
	db := &migrator{fake: newFake("postgresql", testDataStage)}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, db)

	err := fw.RunWithSignals(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot run job task") {
		t.Fatalf("expected cannot-run-task error, got %v", err)
	}
	if db.migrateCalls.Load() != 0 {
		t.Fatal("Migrate ran for a non-migrate task")
	}
}

func TestRunWithSignalsJobRequestUnknownTarget(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{{Component: "nope", Flag: "postgresql.job", Task: "migrate"}}, nil
	}
	mustAdd(t, fw, conf)

	err := fw.RunWithSignals(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected unknown-target error, got %v", err)
	}
}

func TestRunWithSignalsJobRequestRejectsNonJobComponent(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{{Component: "orders", Flag: "postgresql.orders.job", Task: "migrate"}}, nil
	}
	plain := newFake("orders", testDataStage)
	mustAdd(t, fw, conf)
	mustAdd(t, fw, plain)

	err := fw.RunWithSignals(context.Background())
	if err == nil || !strings.Contains(err.Error(), "JobRunner") {
		t.Fatalf("expected JobRunner interface error, got %v", err)
	}
}

func TestRunWithSignalsJobRequestInitializesTargetClosure(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{{Component: "app", Flag: "app.job", Task: "seed"}}, nil
	}
	var dbInit, cacheInit, appInit, siblingInit atomic.Int32
	// Plane below the target: "cache" depends on "db", both transitively pulled
	// in by targeting "app". The sibling sits in the same plane as the target
	// but is not a dependency — it must NOT initialize.
	db := newFake("db", testDataStage)
	db.init = func(context.Context) error { dbInit.Add(1); return nil }
	cache := newFake("cache", testDataStage, "db")
	cache.init = func(context.Context) error { cacheInit.Add(1); return nil }
	app := &jobRunner{fake: newFake("app", testServeStage, "cache")}
	app.init = func(context.Context) error { appInit.Add(1); return nil }
	sibling := newFake("sibling", testServeStage)
	sibling.init = func(context.Context) error { siblingInit.Add(1); return nil }
	mustAdd(t, fw, conf)
	mustAdd(t, fw, db)
	mustAdd(t, fw, cache)
	mustAdd(t, fw, app)
	mustAdd(t, fw, sibling)

	if err := fw.RunWithSignals(context.Background()); err != nil {
		t.Fatalf("RunWithSignals: %v", err)
	}
	if got := app.runJobCalls.Load(); got != 1 {
		t.Fatalf("app RunJob called %d times, want 1", got)
	}
	if dbInit.Load() != 1 {
		t.Fatal("transitive dependency below the target's plane was not initialized")
	}
	if cacheInit.Load() != 1 {
		t.Fatal("direct dependency below the target's plane was not initialized")
	}
	if siblingInit.Load() != 0 {
		t.Fatal("sibling outside the dependency closure initialized during a job")
	}
}

func TestRunWithSignalsJobRequestMultipleTargets(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	conf.parse = func(args []string) ([]string, error) { return args, nil }
	conf.jobs = func() ([]JobRequest, error) {
		return []JobRequest{
			{Component: "a", Flag: "postgresql.job", Task: "migrate"},
			{Component: "b", Flag: "postgresql.b.job", Task: "migrate"},
		}, nil
	}
	a := &migrator{fake: newFake("a", testDataStage)}
	b := &migrator{fake: newFake("b", testDataStage)}
	var siblingInit atomic.Int32
	sibling := newFake("sibling", testDataStage)
	sibling.init = func(context.Context) error { siblingInit.Add(1); return nil }
	mustAdd(t, fw, conf)
	mustAdd(t, fw, a)
	mustAdd(t, fw, b)
	mustAdd(t, fw, sibling)

	if err := fw.RunWithSignals(context.Background()); err != nil {
		t.Fatalf("RunWithSignals: %v", err)
	}
	if got := a.migrateCalls.Load(); got != 1 {
		t.Fatalf("target a Migrate called %d times, want 1", got)
	}
	if got := b.migrateCalls.Load(); got != 1 {
		t.Fatalf("target b Migrate called %d times, want 1", got)
	}
	if got := siblingInit.Load(); got != 0 {
		t.Fatalf("sibling initialized %d times during job, want 0", got)
	}
}

func TestRunJobRefusesAfterInitialize(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	db := &migrator{fake: newFake("db", testDataStage)}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, db)

	if err := fw.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := fw.Migrate(context.Background(), "db")
	if err == nil {
		t.Fatal("expected Migrate to refuse after Initialize")
	}
	if !strings.Contains(err.Error(), "cannot run jobs after Initialize") {
		t.Fatalf("error = %v, want cannot run jobs after Initialize", err)
	}
	if got := db.migrateCalls.Load(); got != 0 {
		t.Fatalf("Migrate ran %d times after refuse, want 0", got)
	}
}

func TestRunJobSurfacesShutdownError(t *testing.T) {
	fw := newTestFW()
	conf := &fakeConf{fake: newFake("configuration", ConfigurationStage)}
	db := &migrator{fake: newFake("db", testDataStage)}
	db.shut = func(context.Context) error {
		return errors.New("shutdown boom")
	}
	mustAdd(t, fw, conf)
	mustAdd(t, fw, db)

	err := fw.Migrate(context.Background(), "db")
	if err == nil {
		t.Fatal("expected Shutdown error from Migrate")
	}
	if !strings.Contains(err.Error(), "shutdown boom") {
		t.Fatalf("error = %v, want shutdown boom", err)
	}
	if got := db.migrateCalls.Load(); got != 1 {
		t.Fatalf("Migrate called %d times, want 1", got)
	}
}
