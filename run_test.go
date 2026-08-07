package caerusframework

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestInitializeSerializesConcurrentCalls(t *testing.T) {
	fw := newTestFW()
	var inits atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})

	c := newFake("slow", LogsStage)
	c.init = func(ctx context.Context) error {
		inits.Add(1)
		close(entered)
		<-release
		return nil
	}
	mustAdd(t, fw, c)

	errCh := make(chan error, 2)
	go func() { errCh <- fw.Initialize(context.Background()) }()
	<-entered
	go func() { errCh <- fw.Initialize(context.Background()) }()

	// Second caller should be blocked on startMu until the first finishes.
	select {
	case <-errCh:
		t.Fatal("second Initialize returned while first was still in progress")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	if got := inits.Load(); got != 1 {
		t.Fatalf("expected one Init call, got %d", got)
	}
}

func TestRunRejectsConcurrentRun(t *testing.T) {
	fw := newTestFW()
	started := make(chan struct{})
	release := make(chan struct{})

	worker := &runner{
		fake: newFake("worker", testServeStage),
		run: func(ctx context.Context) error {
			close(started)
			<-release
			return nil
		},
	}
	mustAdd(t, fw, worker)

	errCh := make(chan error, 2)
	go func() { errCh <- fw.Run(context.Background()) }()
	<-started

	err := fw.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("expected already in progress, got %v", err)
	}

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("first Run: %v", err)
	}
}

func TestInitPanicIsRecovered(t *testing.T) {
	fw := newTestFW()
	c := newFake("boom", LogsStage)
	c.init = func(ctx context.Context) error {
		panic("init explode")
	}
	mustAdd(t, fw, c)

	err := fw.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "init explode") {
		t.Fatalf("expected recovered panic error, got %v", err)
	}
}

func TestRunnerPanicIsRecovered(t *testing.T) {
	fw := newTestFW()
	var shutCalled atomic.Bool
	worker := &runner{
		fake: newFake("worker", testServeStage),
		run:  func(ctx context.Context) error { panic("run explode") },
	}
	worker.fake.shut = func(ctx context.Context) error {
		shutCalled.Store(true)
		return nil
	}
	mustAdd(t, fw, worker)

	err := fw.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run explode") {
		t.Fatalf("expected recovered runner panic, got %v", err)
	}
	if !shutCalled.Load() {
		t.Fatal("expected Shutdown after runner panic")
	}
}

func TestShutdownPanicIsRecovered(t *testing.T) {
	fw := newTestFW()
	c := newFake("boom", LogsStage)
	c.shut = func(ctx context.Context) error {
		panic("shutdown explode")
	}
	mustAdd(t, fw, c)

	if err := fw.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := fw.Shutdown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "shutdown explode") {
		t.Fatalf("expected recovered shutdown panic, got %v", err)
	}
}

func TestRunWithSignalsCancelParent(t *testing.T) {
	fw := newTestFW()
	var shutCalled atomic.Bool
	worker := &runner{
		fake: newFake("worker", testServeStage),
		run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker.fake.shut = func(ctx context.Context) error {
		shutCalled.Store(true)
		return nil
	}
	mustAdd(t, fw, worker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- fw.RunWithSignals(ctx, WithShutdownTimeout(2*time.Second))
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithSignals did not return after cancel")
	}
	if !shutCalled.Load() {
		t.Fatal("expected Shutdown")
	}
}

func TestRunWithSignalsSIGTERM(t *testing.T) {
	fw := newTestFW()
	ready := make(chan struct{})
	var shutCalled atomic.Bool
	worker := &runner{
		fake: newFake("worker", testServeStage),
		run: func(ctx context.Context) error {
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker.fake.shut = func(ctx context.Context) error {
		shutCalled.Store(true)
		return nil
	}
	mustAdd(t, fw, worker)

	done := make(chan error, 1)
	go func() {
		done <- fw.RunWithSignals(context.Background(),
			WithShutdownTimeout(2*time.Second),
			WithSignals(syscall.SIGTERM),
		)
	}()

	<-ready
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil after SIGTERM, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithSignals did not return after SIGTERM")
	}
	if !shutCalled.Load() {
		t.Fatal("expected Shutdown after SIGTERM")
	}
}

func TestRunWithSignalsInitTimeout(t *testing.T) {
	fw := newTestFW()
	c := newFake("slow", LogsStage)
	c.init = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	}
	mustAdd(t, fw, c)

	err := fw.RunWithSignals(context.Background(), WithInitTimeout(20*time.Millisecond))
	if err == nil {
		t.Fatal("expected init timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestRunWithSignalsShutdownTimeout(t *testing.T) {
	fw := newTestFW()
	started := make(chan struct{})
	c := newFake("sticky", LogsStage)
	c.shut = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	}
	mustAdd(t, fw, c)

	worker := &runner{
		fake: newFake("worker", testServeStage),
		run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	mustAdd(t, fw, worker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- fw.RunWithSignals(ctx, WithShutdownTimeout(30*time.Millisecond))
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected shutdown timeout error")
		}
		if !strings.Contains(err.Error(), "deadline exceeded") && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithSignals did not return")
	}
}

func TestRunShutdownUsesBackgroundAfterCancel(t *testing.T) {
	// After ctx cancel, Shutdown must not inherit the canceled context or
	// sticky Shutdown handlers would fail immediately.
	fw := newTestFW()
	var shutCtxErr error
	var mu sync.Mutex

	worker := &runner{
		fake: newFake("worker", testServeStage),
		run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker.fake.shut = func(ctx context.Context) error {
		mu.Lock()
		shutCtxErr = ctx.Err()
		mu.Unlock()
		return nil
	}
	mustAdd(t, fw, worker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fw.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if shutCtxErr != nil {
		t.Fatalf("Shutdown ctx should be usable, got Err=%v", shutCtxErr)
	}
}
