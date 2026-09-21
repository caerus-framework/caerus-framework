// Package aliasmiss looks up a WithName peer through peerName() but only
// lists the default constant in GetDependencies.
package aliasmiss

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

// App looks up the queue via peerName but declares only the default name.
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
	return []string{"queue"}
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	_, ok := cf.GetByName[*Queue](fw, a.peerName()) // want "Init looks up peerName()"
	_ = ok
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }
