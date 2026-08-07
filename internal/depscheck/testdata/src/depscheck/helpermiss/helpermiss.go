// Package helpermiss resolves a peer inside a helper called from Init, but the
// dependency is not declared in GetDependencies.
package helpermiss

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

// App resolves DB through connect but declares no dependencies.
type App struct{}

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

func (a *App) GetDependencies() []string {
	return nil
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	return a.connect(fw)
}

func (a *App) connect(fw *cf.CaerusFramework) error {
	_, ok := cf.Get[*DB](fw) // want "Init looks up"
	_ = ok
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
