// Package helper resolves peers inside a helper called from Init, all declared
// in GetDependencies.
package helper

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

// App resolves DB through connect, declared in GetDependencies.
type App struct{}

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

func (a *App) GetDependencies() []string {
	return []string{ComponentName}
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	return a.connect(fw)
}

func (a *App) connect(fw *cf.CaerusFramework) error {
	if _, ok := cf.Get[*DB](fw); !ok {
		return nil
	}
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
