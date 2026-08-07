// Package ok is a component whose GetDependencies covers every Init lookup.
package ok

import (
	"context"

	cf "github.com/caerus-framework/caerus-framework"
)

// ComponentName is the default component name of DB.
const ComponentName = "db"

// DB is a minimal peer component.
type DB struct{}

func (d *DB) Name() string                                    { return ComponentName }
func (d *DB) GetInitOrderStage() cf.Stage                     { return cf.Stage("data") }
func (d *DB) Init(context.Context, *cf.CaerusFramework) error { return nil }
func (d *DB) Shutdown(context.Context) error                  { return nil }

// Queue is a named peer component instance.
type Queue struct{}

func (q *Queue) Name() string                                    { return "queue" }
func (q *Queue) GetInitOrderStage() cf.Stage                     { return cf.Stage("data") }
func (q *Queue) Init(context.Context, *cf.CaerusFramework) error { return nil }
func (q *Queue) Shutdown(context.Context) error                  { return nil }

// App depends on DB (via its ComponentName const) and the "queue" instance.
type App struct{}

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

// GetDependencies implements cf.Dependencies, covering both peers.
func (a *App) GetDependencies() []string {
	return []string{ComponentName, "queue"}
}

// Init resolves both declared peers.
func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	if _, ok := cf.Get[*DB](fw); !ok {
		return nil
	}
	if _, ok := cf.GetByName[*Queue](fw, "queue"); !ok {
		return nil
	}
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
