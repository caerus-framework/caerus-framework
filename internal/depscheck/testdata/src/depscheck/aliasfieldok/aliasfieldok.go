// Package aliasfieldok is the valkey-state pattern: a local peer variable is
// assigned the default const or the WithName field, and Init looks up both.
package aliasfieldok

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

// App depends on DB and on the queue instance named by valkeyName or "queue".
type App struct{ valkeyName string }

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

func (a *App) GetDependencies() []string {
	deps := []string{ComponentName}
	peer := "queue"
	if a.valkeyName != "" {
		peer = a.valkeyName
	}
	deps = append([]string{peer}, deps...)
	return deps
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	if _, ok := cf.Get[*DB](fw); !ok {
		return nil
	}
	if a.valkeyName == "" {
		if _, ok := cf.GetByName[*Queue](fw, "queue"); !ok {
			return nil
		}
	} else {
		if _, ok := cf.GetByName[*Queue](fw, a.valkeyName); !ok {
			return nil
		}
	}
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
