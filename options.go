package caerusframework

import (
	"fmt"
	"sync"
)

// LogsSettings are the initial log defaults seeded by cf.New before the logs
// configuration source loads. Empty fields keep the logs component's own
// construction defaults (text format, Info level). Seeding does not replace the
// configuration component as the options plane: the source, once available,
// notifies the logs component and its values win.
type LogsSettings struct {
	// Format is "text" or "json" ("" keeps the logs default).
	Format string
	// Level is "debug", "info", "warn" or "error" ("" keeps the logs default).
	Level string
	// ConfigSource is the name of the Source[LogConfig] registered for this
	// component; "" disables config-driven reloads. Must match the source's
	// Name and the Owner must be cf_logs.ComponentName.
	ConfigSource string
}

// ObservabilitySettings are the initial observability defaults seeded by
// cf.New before the observability configuration source loads. Nil pointer
// fields keep the observability component's own construction defaults (health
// checks on, metrics on, tracing off, bind ":9090", service name "caerus").
type ObservabilitySettings struct {
	// Bind is a single host:port seed for the operator HTTP server
	// (default ":9090"). Multi-bind lives in the observability config file
	// (`bind` string or array). Bound in Run, not Init.
	Bind string
	// HealthChecks overrides the health-endpoints default (enabled).
	HealthChecks *bool
	// Metrics overrides the /metrics endpoint default (enabled).
	Metrics *bool
	// Tracing overrides the tracing default (disabled).
	Tracing *bool
	// TraceEndpoint seeds the OTLP/gRPC collector address.
	TraceEndpoint string
	// TraceInsecure, when true, admits cleartext OTLP. Default false (TLS).
	TraceInsecure bool
	// TraceSampleRatio seeds head sampling (0–1). Nil keeps 1.0.
	TraceSampleRatio *float64
	// ServiceName overrides the OpenTelemetry service.name default ("caerus").
	ServiceName string
	// ConfigSource is the name of the Source[ObservabilityConfig] registered
	// for this component; "" disables config-driven reloads. Must match the
	// source's Name and the Owner must be cf_observability.ComponentName.
	ConfigSource string
}

// FrameworkOptions declares what the application uses and the initial values
// for the always-on core components (logs, configuration, observability). It
// is the app-as-component declaration: main builds one, cf.New registers the
// core plus the application's components, and the configuration component owns
// the live values for everything else.
//
// The core components are always registered, in bootstrap order, whether or
// not they appear in Options; the Logs / Observability fields only seed the
// initial defaults before their configuration sources load (defaults until
// notify). They do not replace the configuration component as the options
// plane.
type FrameworkOptions struct {
	// Logs seeds the logs component. Nil keeps its construction defaults.
	Logs *LogsSettings
	// Observability seeds the observability component. Nil keeps its
	// construction defaults.
	Observability *ObservabilitySettings
	// Components are the application's components (chassis + app classes),
	// registered after the core in the order given.
	Components []CaerusComponent
	// Args is the process argv the configuration component absorbs (its
	// ParseFlags) after components register their sources. main never sets
	// this for ordinary apps: nil means os.Args[1:], and the framework owns
	// argv entirely (subcommand positionals come back as LeftoverArgs). Set it
	// only for multi-tool binaries that must strip a binary-level prefix (the
	// wrapper tool name) before absorption.
	Args []string
}

// Core factories. The core modules (caerus-framework-logs, -configuration,
// -observability) register their component factories from package init() so
// caerus-framework stays free of imports from them; New(opts) uses the
// factories to build the always-on components.
var (
	coreMu               sync.Mutex
	logsFactory          func(*LogsSettings) (CaerusComponent, error)
	configurationFactory func() (CaerusComponent, error)
	observabilityFactory func(*ObservabilitySettings) (CaerusComponent, error)
)

// RegisterLogsFactory registers the factory the logs module uses to build its
// component. Called from that module's init(); panics on double registration.
// The settings argument is the FrameworkOptions.Logs value, nil when the
// application did not seed one.
func RegisterLogsFactory(f func(*LogsSettings) (CaerusComponent, error)) {
	coreMu.Lock()
	defer coreMu.Unlock()
	if logsFactory != nil {
		panic("caerus: logs core factory already registered")
	}
	logsFactory = f
}

// RegisterConfigurationFactory registers the factory the configuration module
// uses to build its component. Called from that module's init(); panics on
// double registration.
func RegisterConfigurationFactory(f func() (CaerusComponent, error)) {
	coreMu.Lock()
	defer coreMu.Unlock()
	if configurationFactory != nil {
		panic("caerus: configuration core factory already registered")
	}
	configurationFactory = f
}

// RegisterObservabilityFactory registers the factory the observability module
// uses to build its component. Called from that module's init(); panics on
// double registration. The settings argument is the FrameworkOptions.
// Observability value, nil when the application did not seed one.
func RegisterObservabilityFactory(f func(*ObservabilitySettings) (CaerusComponent, error)) {
	coreMu.Lock()
	defer coreMu.Unlock()
	if observabilityFactory != nil {
		panic("caerus: observability core factory already registered")
	}
	observabilityFactory = f
}

// registerCore builds and registers the always-on core components (logs,
// configuration, observability) and the application's declared components. It
// panics on wiring errors — a missing core module or a failed component build
// is a link/declaration mistake that must not start the process.
func (f *CaerusFramework) registerCore(opts *FrameworkOptions) {
	coreMu.Lock()
	defer coreMu.Unlock()

	var comps []CaerusComponent
	comps = append(comps, buildOrPanic("logs", func() (CaerusComponent, error) {
		if logsFactory == nil {
			return nil, fmt.Errorf("logs module not linked (import caerus-framework-logs)")
		}
		return logsFactory(opts.Logs)
	}))
	comps = append(comps, buildOrPanic("configuration", func() (CaerusComponent, error) {
		if configurationFactory == nil {
			return nil, fmt.Errorf("configuration module not linked (import caerus-framework-configuration)")
		}
		return configurationFactory()
	}))
	comps = append(comps, buildOrPanic("observability", func() (CaerusComponent, error) {
		if observabilityFactory == nil {
			return nil, fmt.Errorf("observability module not linked (import caerus-framework-observability)")
		}
		return observabilityFactory(opts.Observability)
	}))
	comps = append(comps, opts.Components...)

	for _, c := range comps {
		if err := f.AddComponent(c); err != nil {
			panic("caerus: register component: " + err.Error())
		}
	}
}

func buildOrPanic(name string, build func() (CaerusComponent, error)) CaerusComponent {
	c, err := build()
	if err != nil {
		panic(fmt.Sprintf("caerus: build %s component: %v", name, err))
	}
	return c
}
