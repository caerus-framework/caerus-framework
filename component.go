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
// cannot be reordered. AddComponent registers a component's stage automatically
// in first-seen order, so applications define stages just by adding components
// to them.
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
	// GetInitOrderStage returns the stage the component belongs to. AddComponent
	// registers the stage automatically the first time a component declares it.
	GetInitOrderStage() Stage
	// Init performs startup work. It is called once, in dependency order, and
	// must return when the component is ready. Honor ctx cancellation/deadlines
	// and do not store ctx beyond this call. The framework is provided for
	// accessing other components or shared options.
	//
	// Do not bind listen sockets here. Jobs initialize a subset of the graph
	// and never start Runnables, so a listen in Init would open a port during
	// migrate/seed. Implement [Runnable] and bind in Run.
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

// Subcomponents is an optional interface for composite components that own
// other CaerusComponents (e.g. an app that constructs its interest VPQ).
// AddComponent expands the tree breadth-first before returning: the parent is
// registered, then each child in Subcomponents() order, recursively. Children
// are first-class in the registry (Validate, ConfigSourceRegistrar, Job,
// Runnable). The parent must not call child Init/Run/Shutdown itself.
//
// Construct children in the parent's New (inert); the framework owns lifecycle.
// Typical shape: an app component builds product children (queues, refreshers)
// in New and returns them from Subcomponents(); chassis peers (postgres,
// valkey) stay declared in main.
type Subcomponents interface {
	Subcomponents() []CaerusComponent
}

// Runnable is an optional interface for background workers and listeners.
// Run is launched in a goroutine after every component has initialized, and
// must return promptly when ctx is canceled. A Runnable returning an error
// cancels the framework run and triggers shutdown of all initialized
// components.
//
// Claiming a TCP or Unix listen address belongs here, not in Init. The
// job-only path (Migrate / RunJob / a job flag) never starts Runnables, so
// a listener that binds in Run stays closed during migrate/seed. HTTP
// servers (cf_http, observability probes and /metrics) follow that split.
type Runnable interface {
	Run(ctx context.Context) error
}

// ConfigSourceRegistrar is an optional interface for self-sufficient
// components: it lets a component register its own configuration source with
// the configuration component. The framework invokes it during argv absorption
// (before any component initializes), passing the configuration component as
// conf.
//
// conf is typed as any because the configuration component lives in
// caerus-framework-configuration, which imports caerus-framework; the framework
// cannot reference its type. Implementations type-assert conf to
// *cf_configuration.Configuration and call cf_configuration.AddSource with
// their strongly-typed Source[T].
//
// This is the module-owned-source mechanism: the module knows its config
// struct, default EnvPrefix, AfterLoad (DSN/URL) overlay and Owner, so main no
// longer calls AddSource for stock or app modules. main only points the
// instance at where the config lives via WithConfigSource(name, path, ...).
// An argv redeclaration of the source (the --<name> file-path flag) always
// wins over the component's own default path.
type ConfigSourceRegistrar interface {
	RegisterConfigSources(conf any) error
}

// ConfigSourceValue is the generic-free declaration form of a configuration
// source (see cf_configuration.Source[T]). Sample is a value of the source's
// concrete config type; its dynamic type selects decoding and the registered
// value's type, exactly as the Source[T] generic would. Format is "json"
// (default) or "yaml".
type ConfigSourceValue struct {
	// Name is the logical source name (the Lookup/Get key and the --<Name>
	// file-path flag).
	Name string
	// Path is the default configuration file path. An argv --<Name> override
	// replaces it.
	Path string
	// Format selects the file encoding ("json" or "yaml"; "" defaults to json).
	Format string
	// EnvPrefix overlays matching environment variables after the file loads.
	EnvPrefix string
	// Owner is the Name of the consuming component; its OnConfigReload (if it
	// implements cf.ConfigReloader) is notified on validated reloads.
	Owner string
	// Job, when declared, registers a CLI-only job flag for this source's Owner.
	// The flag names the instance, the value names the task to run on it (e.g.
	// --postgresql.job=migrate). The framework reads the request via cf.JobSource
	// after argv absorption and routes it before serving. CLI-only: the value
	// lives in the parsed flag, never in the config file or environment.
	Job JobSpec
	// Sample is a value of the concrete config type (e.g. cf_logs.LogConfig{}).
	Sample any
}

// ConfigSourceAdder is implemented by the configuration component. The
// framework uses it to register the configuration sources declared by core
// modules (logs, observability) that cannot implement ConfigSourceRegistrar —
// they would need to import the configuration package, an import cycle.
type ConfigSourceAdder interface {
	AddSourceValue(src ConfigSourceValue) error
}

// CoreConfigSource is an optional interface for core modules (logs,
// observability) that the configuration module imports and therefore cannot
// implement ConfigSourceRegistrar (import cycle). They declare their own
// configuration source here instead; the framework discovers them among
// registered components during argv absorption and registers the declarations
// with the configuration component (as ConfigSourceAdder), before ParseFlags.
//
// The component owns the declaration: its own default path, EnvPrefix and
// Owner, overridable by argv (the --<Name> file-path flag always wins).
type CoreConfigSource interface {
	CoreConfigSource() ([]ConfigSourceValue, error)
}

// ConfigArgv is implemented by the configuration component
// (caerus-framework-configuration). The framework uses it to hand the process
// argv to configuration after components registered their sources, so flag
// overlays (--<flag> fields and --<source-name> file paths) apply without main
// ever calling ParseFlags. Unknown flags and positional args are returned
// untouched as the leftover args.
type ConfigArgv interface {
	ParseFlags(args []string) ([]string, error)
}

// JobSpec declares a CLI-only job flag on a configuration source. Flag is the
// flag name — "<module>.job" for the module's default instance, or
// "<module>.<instance>.job" for a named instance — and the value names the
// task to run on that instance (e.g. "migrate"). Tasks lists the supported
// task values; the configuration component rejects a value outside the set. An
// empty Tasks allows any task (the target component still validates at
// dispatch).
type JobSpec struct {
	// Flag is the job flag name after "--", e.g. "postgresql.job".
	Flag string
	// Tasks are the supported task values for this flag (e.g. ["migrate"]).
	// Empty means any task is accepted.
	Tasks []string
}

// JobRequest is a resolved job-only init request produced by the configuration
// component from a module's declared job flag (e.g. postgresql migration via
// --postgresql.job=migrate). Component is the Name() of the component whose
// job callback runs — the specific instance named by the flag. Task is the
// requested task (the flag's value). Flag is the source's job flag name (for
// diagnostics); it is empty for programmatic requests (CaerusFramework.Migrate
// / RunJob).
type JobRequest struct {
	Component string
	Flag      string
	Task      string
}

// JobSource is implemented by the configuration component. The framework asks
// it, after absorbing argv, whether any registered source's job flag was set;
// when one was, it reports the resolved job requests — the components whose job
// callbacks run once the targets' dependency closure (their plane and
// everything below it) has initialized.
type JobSource interface {
	JobRequests() ([]JobRequest, error)
}

// JobRunner is the generic job contract for the framework's job-only init path.
// A component implements RunJob to execute a named task (e.g. "migrate"); the
// postgresql component maps "migrate" to its Migrate method. Components may
// implement either this interface or [Migrator] (accepted for the "migrate"
// task only).
type JobRunner interface {
	RunJob(ctx context.Context, task string) error
}

// Migrator is the migrate-specific job contract: a component that can run a
// one-shot migration. The framework's job-only init path (RunWithSignals when a
// module's job flag is set, or CaerusFramework.Migrate) initializes only the
// core plus the target component(s), runs Migrate, shuts down and returns. The
// postgresql component implements it for schema migrations; each named
// postgresql instance migrates its own database. Generic components should
// prefer [JobRunner].
type Migrator interface {
	Migrate(ctx context.Context) error
}

// ConfigReloader is an optional interface for components that want to react to
// configuration reloads. The configuration component (caerus-framework-
// configuration) invokes OnConfigReload on every affected component after a
// validated config swap, passing the source name and the freshly loaded value
// (the same *T pointer Get/Lookup return).
//
// The value is passed explicitly so components that cannot import
// caerus-framework-configuration still receive their new config: the logs
// component, for example, cannot import it (configuration imports logs).
// Components that can import it may ignore the value and re-Lookup instead.
type ConfigReloader interface {
	OnConfigReload(source string, cfg any)
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
