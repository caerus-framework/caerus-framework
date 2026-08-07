package caerusframework

import (
	"context"
	"fmt"
	"strings"
)

// Migrate runs the job-only init path for a named target: it absorbs argv,
// initializes the core plus the target's dependency closure, runs its
// "migrate" job (via [JobRunner], or [Migrator] as fallback), shuts down and
// returns. It is the programmatic migrate sugar for multi-tool binaries:
//
//	if err := fw.Migrate(ctx, cf_postgres.ComponentName); err != nil { ... }
//
// The production path is the same machine without a subcommand: set the
// module-declared job flag (e.g. `myapp --postgresql.job=migrate` as a K8s Job)
// and RunWithSignals routes to it before serving.
func (f *CaerusFramework) Migrate(ctx context.Context, target string) error {
	return f.RunJob(ctx, target, "migrate")
}

// RunJob is the programmatic job-only path for a named target and task: it
// absorbs argv, initializes the core plus the target's dependency closure, runs
// the named task on it and shuts down. The target must implement [JobRunner]
// (or [Migrator] when task is "migrate"). Use Migrate for the common migrate
// case.
func (f *CaerusFramework) RunJob(ctx context.Context, target, task string) error {
	if err := f.absorbArgs(); err != nil {
		return err
	}
	return f.runJobs(ctx, []JobRequest{{Component: target, Task: task}})
}

// jobRequests asks the configuration component (which implements cf.JobSource)
// whether any registered source's job flag was set. A bare framework (no
// configuration component) has no jobs.
func (f *CaerusFramework) jobRequests() ([]JobRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.components {
		if js, ok := c.(JobSource); ok {
			return js.JobRequests()
		}
	}
	return nil, nil
}

// runJobs runs the job-only init path for the requested jobs: it resolves each
// request (the component must be registered and implement [JobRunner], or
// [Migrator] for the "migrate" task), initializes the core plus the transitive
// dependency closure of the targets — the target's plane and everything below
// it — runs each job in order, shuts down and returns. Nothing outside the
// closure initializes. Fail-closed contract: an unknown target or a component
// that cannot run the requested task is a hard error before any data Init.
func (f *CaerusFramework) runJobs(ctx context.Context, reqs []JobRequest) error {
	type jobCall struct {
		comp   CaerusComponent
		task   string
		runner JobRunner
		mig    Migrator
	}
	var calls []jobCall
	seen := make(map[string]bool)
	for _, r := range reqs {
		name := strings.TrimSpace(r.Component)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		f.mu.Lock()
		comp, ok := f.byName[name]
		f.mu.Unlock()
		if !ok {
			return fmt.Errorf("caerus: job target %q is not a registered component (declare it in FrameworkOptions; the module's job flag must name a registered instance)", name)
		}
		var c jobCall
		c.comp = comp
		c.task = strings.TrimSpace(r.Task)
		if jr, ok := comp.(JobRunner); ok {
			c.runner = jr
		} else if c.task == "migrate" {
			if mg, ok := comp.(Migrator); ok {
				c.mig = mg
			}
		}
		if c.runner == nil && c.mig == nil {
			return fmt.Errorf("caerus: component %q cannot run job task %q (implement JobRunner, or Migrator for the migrate task)", name, c.task)
		}
		calls = append(calls, c)
	}
	if len(calls) == 0 {
		return nil
	}
	targets := make([]CaerusComponent, 0, len(calls))
	for _, c := range calls {
		targets = append(targets, c.comp)
	}
	keep := f.jobClosure(targets)
	if err := f.initializeSubset(ctx, func(c CaerusComponent) bool { return keep[c] }); err != nil {
		return err
	}
	defer func() { _ = f.Shutdown(context.Background()) }()
	for _, c := range calls {
		var err error
		if c.runner != nil {
			err = c.runner.RunJob(ctx, c.task)
		} else {
			err = c.mig.Migrate(ctx)
		}
		if err != nil {
			return fmt.Errorf("caerus: job %q on %q: %w", c.task, c.comp.Name(), err)
		}
	}
	return nil
}

// jobClosure expands the job targets to their transitive dependency closure: a
// job initializes the target plus everything below its plane (every component
// it depends on, directly or transitively). An app-level target pulls in the
// whole data plane beneath it; a data-level target like postgres pulls in only
// the core components it needs — so a migrate job still runs when valkey is
// down. Core (bootstrap-stage) components are always initialized regardless
// (see isCoreStage).
func (f *CaerusFramework) jobClosure(targets []CaerusComponent) map[CaerusComponent]bool {
	keep := make(map[CaerusComponent]bool)
	for _, t := range targets {
		keep[t] = true
	}
	for {
		added := false
		f.mu.Lock()
		for c := range keep {
			d, ok := c.(Dependencies)
			if !ok {
				continue
			}
			for _, name := range d.GetDependencies() {
				if dep, ok := f.byName[name]; ok && !keep[dep] {
					keep[dep] = true
					added = true
				}
			}
		}
		f.mu.Unlock()
		if !added {
			return keep
		}
	}
}

// initializeSubset initializes only the core (bootstrap-stage) components plus
// the components keep returns true for. It is the job-only init path: siblings
// are not initialized "just in case" — runJobs hands it the targets' dependency
// closure, so exactly the target's plane and everything below it initialize.
func (f *CaerusFramework) initializeSubset(ctx context.Context, keep func(CaerusComponent) bool) error {
	if err := f.absorbArgs(); err != nil {
		return err
	}
	waves, err := f.resolveWaves()
	if err != nil {
		return err
	}
	for _, wave := range waves {
		subset := make([]CaerusComponent, 0, len(wave))
		for _, c := range wave {
			if isCoreStage(c.GetInitOrderStage()) || keep(c) {
				subset = append(subset, c)
			}
		}
		if len(subset) == 0 {
			continue
		}
		if err := f.initWave(ctx, subset); err != nil {
			f.shutdownAll(ctx)
			return err
		}
	}
	return nil
}

// isCoreStage reports whether a component belongs to a framework-owned
// bootstrap stage (logs, configuration, observability, secrets). Core
// components are always initialized, including on the job-only init path.
func isCoreStage(s Stage) bool {
	switch s {
	case LogsStage, ConfigurationStage, ObservabilityStage, SecretsStage:
		return true
	}
	return false
}
