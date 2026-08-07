// Package stale declares a literal dependency that no code in the package
// references; only the -stale-dep hygiene check reports it.
package stale

import (
	"context"

	cf "github.com/caerus-framework/caerus-framework"
)

// App declares "orphan" in GetDependencies but never references it.
type App struct{}

func (a *App) Name() string                { return "app" }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

// GetDependencies declares an orphan literal.
func (a *App) GetDependencies() []string { // want "declares \"orphan\""
	return []string{"orphan"}
}

func (a *App) Init(context.Context, *cf.CaerusFramework) error { return nil }

func (a *App) Shutdown(context.Context) error { return nil }
