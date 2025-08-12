package caerusframework

import "context"

// Stage names the initialization bucket a component belongs to.
//
// Stages form an ordered list. Components initialize in stage order, and
// within a stage by dependency, then by registration (AddComponent) order.
// A dependency may only reference a component in the same or an earlier stage;
// referencing a later stage is a wiring error.
//
// The bootstrap stages below are always registered first, in this order, and
// cannot be reordered. Applications define additional stages with
// RegisterStage, in the order they should initialize; components declare the
// stage they belong to via GetInitOrderStage.
type Stage string

// Built-in bootstrap stages, in initialization order. Everything a bootstrap
// component needs lives inside the bootstrap prefix and is ordered by the
// framework (dependencies still apply within and across these stages).
const (
	LogsStage          Stage = "logs"
	ConfigurationStage Stage = "configuration"
	ObservabilityStage Stage = "observability"
	SecretsStage       Stage = "secrets"
)

// CaerusComponent is the lifecycle contract every component must implement.
type CaerusComponent interface {
	// Name returns a stable, unique component name. Names are the identifiers
	// used by GetDependencies and by Get/MustGet-free cross-component wiring.
	Name() string
	// GetInitOrderStage returns the stage the component belongs to. The stage
	// must be registered with the framework (bootstrap stages are built in;
	// application stages come from RegisterStage).
	GetInitOrderStage() Stage
	// Init performs startup work. It is called once, in dependency order, and
	// must return when the component is ready. Honor ctx cancellation/deadlines
	// and do not store ctx beyond this call. The framework is provided for
	// accessing other components or shared options.
	Init(ctx context.Context, fw *CaerusFramework) error
	// Shutdown gracefully stops the component. It is called in reverse init
	// order. Keep it idempotent and return promptly when ctx is canceled or its
	// deadline elapses.
	Shutdown(ctx context.Context) error
}

// Dependencies is an optional interface. Components that require other
// components to be initialized first must implement it.
//
// Dependencies are declared by component Name. The framework builds a
// dependency graph and topologically sorts it: a component is initialized only
// after every component in GetDependencies. Dependency cycles are startup
// errors and are detected before any Init runs (see Validate). Referring to an
// unregistered name is also a startup error.
type Dependencies interface {
	GetDependencies() []string
}

// Runnable is an optional interface for background workers. Run is launched in
// a goroutine after every component has initialized, and must return promptly
// when ctx is canceled. A Runnable returning an error cancels the framework
// run and triggers shutdown of all initialized components.
type Runnable interface {
	Run(ctx context.Context) error
}

// ConfigReloader is an optional interface for components that want to react to
// configuration reloads. The configuration component (caerus-framework-
// configuration) invokes OnConfigReload on every affected component after a
// validated config swap.
type ConfigReloader interface {
	OnConfigReload()
}

// HealthProvider is an optional interface for components that can report their
// health. The observability component (caerus-framework-observability)
// discovers the components implementing it and folds their health into its
// Kubernetes health-check endpoints (readiness via /readyz). A component that
// does not implement HealthProvider is simply not included, so supporting
// health checks is entirely optional.
type HealthProvider interface {
	// Health reports the component's current health. It returns nil when the
	// component is healthy, or a non-nil error describing why it is not. The
	// context carries the probe deadline; implementations must honor it and
	// return promptly.
	Health(ctx context.Context) error
}

// Metric is one runtime-state sample a component exposes for the observability
// component's metrics endpoint. The observability component serves the sample
// prefixed with "caerus_" (Name "logs_info" becomes "caerus_logs_info").
type Metric struct {
	// Name is the metric name without the "caerus_" prefix. It must not be
	// empty for the sample to be emitted.
	Name string
	// Help describes the metric for /metrics consumers.
	Help string
	// Value is the sample value. State/info samples typically use 1 and carry
	// their meaning in Labels.
	Value float64
	// Labels annotate the sample, e.g. {"format": "json", "level": "info"}.
	Labels map[string]string
}

// MetricsProvider is an optional interface for components that expose runtime
// state as metrics. The observability component (caerus-framework-observability)
// discovers the components implementing it and calls Metrics on every /metrics
// scrape, so the values are always live: a component that is not initialized
// yet returns nil and is skipped until it does — a lazy pickup that needs no
// subscription. A component that does not implement MetricsProvider contributes
// nothing to /metrics.
type MetricsProvider interface {
	// Metrics returns the component's current runtime state. Return nil while
	// the component is not initialized or has nothing to report.
	Metrics() []Metric
}
