// Package aliasfieldmiss looks up a WithName field in Init but never lists
// that field (or a method that returns it) in GetDependencies.
package aliasfieldmiss

import (
	"context"

	cf "github.com/caerus-framework/caerus-framework"
)

// Queue is a named peer that may be registered under WithName.
type Queue struct{}

func (q *Queue) Name() string                                    { return "queue" }
func (q *Queue) GetInitOrderStage() cf.Stage                     { return cf.Stage("data") }
func (q *Queue) Init(context.Context, *cf.CaerusFramework) error { return nil }
func (q *Queue) Shutdown(context.Context) error                  { return nil }

// App looks up c.valkeyName without declaring it.
type App struct{ valkeyName string }

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

func (a *App) GetDependencies() []string {
	return []string{"queue"}
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	_, ok := cf.GetByName[*Queue](fw, a.valkeyName) // want "Init looks up field valkeyName"
	_ = ok
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
