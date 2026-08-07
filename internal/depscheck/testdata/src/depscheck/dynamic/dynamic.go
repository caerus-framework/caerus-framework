// Package dynamic exercises the false-negative-first path: names that cannot
// be resolved statically must produce no diagnostics.
package dynamic

import (
	"context"

	cf "github.com/caerus-framework/caerus-framework"
)

// Unknown is a component type whose package has no ComponentName const, so
// cf.Get[*Unknown] cannot be mapped to a declared dependency name.
type Unknown struct{}

func (u *Unknown) Name() string                                    { return "unknown" }
func (u *Unknown) GetInitOrderStage() cf.Stage                     { return cf.Stage("data") }
func (u *Unknown) Init(context.Context, *cf.CaerusFramework) error { return nil }
func (u *Unknown) Shutdown(context.Context) error                  { return nil }

// App builds its dependency list at runtime and resolves peers dynamically:
// nothing here is statically knowable, so nothing is reported.
type App struct{ name string }

func (a *App) Name() string                { return a.name }
func (a *App) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

func (a *App) GetDependencies() []string {
	return []string{a.peer()} // runtime value — declared list is incomplete
}

func (a *App) Init(ctx context.Context, fw *cf.CaerusFramework) error {
	_, _ = cf.Get[*Unknown](fw)                 // no ComponentName const in package — skipped
	_, _ = cf.GetByName[*Unknown](fw, a.peer()) // dynamic name — skipped
	return nil
}

func (a *App) Shutdown(context.Context) error { return nil }

func (a *App) peer() string { return "unknown" }
