package caerusframework

import (
	"strings"
	"testing"
)

func TestRegisterCoreWiresFactories(t *testing.T) {
	logs := newFake("logs", LogsStage)
	conf := newFake("configuration", ConfigurationStage)
	obs := newFake("observability", ObservabilityStage)
	app := newFake("app", testBusinessStage)
	restore := swapCoreFactoriesForTest(
		func(*LogsSettings) (CaerusComponent, error) { return logs, nil },
		func() (CaerusComponent, error) { return conf, nil },
		func(*ObservabilitySettings) (CaerusComponent, error) { return obs, nil },
	)
	t.Cleanup(restore)

	fw := New(&FrameworkOptions{Components: []CaerusComponent{app}})
	if _, ok := GetByName[*fake](fw, "logs"); !ok {
		t.Fatal("logs not registered")
	}
	if _, ok := GetByName[*fake](fw, "configuration"); !ok {
		t.Fatal("configuration not registered")
	}
	if _, ok := GetByName[*fake](fw, "observability"); !ok {
		t.Fatal("observability not registered")
	}
	if _, ok := GetByName[*fake](fw, "app"); !ok {
		t.Fatal("app not registered")
	}
}

func TestRegisterCorePanicsWhenUnlinked(t *testing.T) {
	restore := swapCoreFactoriesForTest(nil, nil, nil)
	t.Cleanup(restore)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when core factories are missing")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "logs module not linked") {
			t.Fatalf("panic = %v, want logs module not linked", r)
		}
	}()
	_ = New(&FrameworkOptions{})
}

func TestRegisterLogsFactoryPanicsOnDoubleRegister(t *testing.T) {
	restore := swapCoreFactoriesForTest(nil, nil, nil)
	t.Cleanup(restore)
	RegisterLogsFactory(func(*LogsSettings) (CaerusComponent, error) {
		return newFake("logs", LogsStage), nil
	})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on double RegisterLogsFactory")
		}
	}()
	RegisterLogsFactory(func(*LogsSettings) (CaerusComponent, error) {
		return newFake("logs", LogsStage), nil
	})
}
