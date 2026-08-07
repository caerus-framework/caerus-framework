package caerusframework

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitializeParallelWithinWave(t *testing.T) {
	fw := newTestFW()
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)

	makeInit := func() func(context.Context) error {
		return func(ctx context.Context) error {
			n := concurrent.Add(1)
			for {
				cur := maxConcurrent.Load()
				if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
					break
				}
			}
			wg.Done()
			wg.Wait() // hold both in Init until both have entered
			concurrent.Add(-1)
			return nil
		}
	}

	a := newFake("a", testDataStage)
	a.init = makeInit()
	b := newFake("b", testDataStage)
	b.init = makeInit()
	mustAdd(t, fw, a)
	mustAdd(t, fw, b)

	if err := fw.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := maxConcurrent.Load(); got < 2 {
		t.Fatalf("expected concurrent Init within wave, max concurrent = %d", got)
	}
}

func TestInitializeRespectsSameStageDependency(t *testing.T) {
	fw := newTestFW()
	var aDone atomic.Bool
	var bSawA atomic.Bool

	a := newFake("a", testDataStage)
	a.init = func(ctx context.Context) error {
		time.Sleep(40 * time.Millisecond)
		aDone.Store(true)
		return nil
	}
	b := newFake("b", testDataStage, "a")
	b.init = func(ctx context.Context) error {
		bSawA.Store(aDone.Load())
		return nil
	}
	mustAdd(t, fw, b) // register b first; topo must still init a before b
	mustAdd(t, fw, a)

	if err := fw.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !bSawA.Load() {
		t.Fatal("b.Init started before a finished; same-stage dependency violated")
	}
}

func TestInitializeWaveFailureShutsDownSucceededPeers(t *testing.T) {
	fw := newTestFW()
	var mu sync.Mutex
	var shutOrder []string

	okComp := newFake("ok", testDataStage)
	okComp.init = func(ctx context.Context) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}
	okComp.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "ok")
		mu.Unlock()
		return nil
	}

	bad := newFake("bad", testDataStage)
	bad.init = func(ctx context.Context) error {
		return errors.New("nope")
	}
	bad.shut = func(ctx context.Context) error {
		mu.Lock()
		shutOrder = append(shutOrder, "bad")
		mu.Unlock()
		return nil
	}

	mustAdd(t, fw, okComp)
	mustAdd(t, fw, bad)

	err := fw.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected init failure, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(shutOrder) != 1 || shutOrder[0] != "ok" {
		t.Fatalf("expected only succeeded peer shut down, got %v", shutOrder)
	}
}

func TestValidateOrderMatchesWaveFlattening(t *testing.T) {
	fw := newTestFW()
	mustAdd(t, fw, newFake("x", testDataStage))
	mustAdd(t, fw, newFake("y", testDataStage))
	mustAdd(t, fw, newFake("z", testDataStage, "x"))

	order, err := fw.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(names(order), ","); got != "x,y,z" && got != "y,x,z" {
		// x and y are the first wave (registration order x,y); z after.
		t.Fatalf("unexpected order %q", got)
	}
	if names(order)[2] != "z" {
		t.Fatalf("z must be last, got %v", names(order))
	}
}
