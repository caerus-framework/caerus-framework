// Package missingdep has a component that looks up a peer in Init but forgets
// to declare it.
package missingdep

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

// App looks up DB in Init without declaring it.
type App struct{}

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

// GetDependencies omits "db".
func (a *App) GetDependencies() []string {
	return nil
}

// Init resolves DB, which GetDependencies forgot.
func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	_, ok := cf.Get[*DB](fw) // want "Init looks up"
	_ = ok
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
