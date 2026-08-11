package caerusframework

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// RunOption configures RunWithSignals.
type RunOption func(*runConfig)

type runConfig struct {
	shutdownTimeout time.Duration
	initTimeout     time.Duration
	signals         []os.Signal
}

// WithShutdownTimeout sets a deadline for Shutdown after runners stop (signal
// or error). The shutdown context is derived from context.Background so a
// canceled run context cannot starve teardown. A non-positive duration means
// no deadline.
func WithShutdownTimeout(d time.Duration) RunOption {
	return func(c *runConfig) { c.shutdownTimeout = d }
}

// WithInitTimeout bounds Initialize. A non-positive duration means the parent
// context alone controls Init cancellation.
func WithInitTimeout(d time.Duration) RunOption {
	return func(c *runConfig) { c.initTimeout = d }
}

// WithSignals replaces the default interrupt signals (os.Interrupt,
// syscall.SIGTERM) watched by RunWithSignals. Passing no signals restores the
// defaults.
func WithSignals(sigs ...os.Signal) RunOption {
	return func(c *runConfig) { c.signals = append([]os.Signal(nil), sigs...) }
}

// RunWithSignals is the production entrypoint around Run: it initializes with
// an optional timeout, runs until SIGINT/SIGTERM (or a custom WithSignals set)
// or a runner error, then shuts down with an optional timeout.
//
//	err := fw.RunWithSignals(ctx,
//	    caerusframework.WithShutdownTimeout(15*time.Second),
//	    caerusframework.WithInitTimeout(30*time.Second),
//	)
//
// Run remains available for tests and embedded use that supply their own
// cancellation.
func (f *CaerusFramework) RunWithSignals(ctx context.Context, opts ...RunOption) error {
	cfg := runConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if len(cfg.signals) == 0 {
		cfg.signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}

	initCtx := ctx
	if cfg.initTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, cfg.initTimeout)
		defer cancel()
	}

	// Absorb argv (source registrars + flag overlay), then ask the configuration
	// component whether any module's job flag was set (e.g.
	// --postgresql.job=migrate). When a job is requested, only the core plus the
	// named target(s) initialize — the serving components never start.
	if err := f.absorbArgs(); err != nil {
		return err
	}
	reqs, err := f.jobRequests()
	if err != nil {
		return err
	}
	if len(reqs) > 0 {
		return f.runJobs(initCtx, reqs, cfg)
	}

	if err := f.Initialize(initCtx); err != nil {
		return err
	}

	runCtx, stop := signal.NotifyContext(ctx, cfg.signals...)
	defer stop()

	runErr := f.runRunners(runCtx)

	shutdownCtx := context.Background()
	if cfg.shutdownTimeout > 0 {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(context.Background(), cfg.shutdownTimeout)
		defer cancel()
	}
	shutdownErr := f.Shutdown(shutdownCtx)

	if runErr == nil {
		return shutdownErr
	}
	return runErr
}
