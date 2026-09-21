// Package aliasmix lists a peerName() alias but forgets the typed Get peer.
// Mixed GetDependencies must still report the missing constant.
package aliasmix

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

// Queue is a named peer that may be registered under WithName.
type Queue struct{}

func (q *Queue) Name() string                                    { return "queue" }
func (q *Queue) GetInitOrderStage() cf.Stage                     { return cf.Stage("data") }
func (q *Queue) Init(context.Context, *cf.CaerusFramework) error { return nil }
func (q *Queue) Shutdown(context.Context) error                  { return nil }

// App declares the queue alias but omits DB.
type App struct{ stateName string }

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

func (a *App) peerName() string {
	if a.stateName != "" {
		return a.stateName
	}
	return "queue"
}

func (a *App) GetDependencies() []string {
	return []string{a.peerName()}
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	_, ok := cf.Get[*DB](fw) // want "Init looks up \"db\""
	_ = ok
	if _, ok := cf.GetByName[*Queue](fw, a.peerName()); !ok {
		return nil
	}
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
