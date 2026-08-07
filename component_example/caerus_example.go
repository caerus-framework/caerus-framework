package caerus_framework_component_example

import (
	"context"

	caerusframework "github.com/caerus-framework/caerus-framework"
)

// ApplicationStage is the application-owned stage this component initializes
// in. Stages are developer-defined: AddComponent registers the stage
// automatically the first time a component returns it from GetInitOrderStage,
// after the framework-owned bootstrap prefix.
const ApplicationStage = caerusframework.Stage("application")

// CaerusExample demonstrates a minimal component. Extend the struct with
// whatever state your component needs.
type CaerusExample struct {
	fw *caerusframework.CaerusFramework
}

// Name returns a stable, unique component name used for dependency wiring.
func (c *CaerusExample) Name() string {
	return "example"
}

func (c *CaerusExample) GetInitOrderStage() caerusframework.Stage {
	return ApplicationStage
}

func (c *CaerusExample) Init(ctx context.Context, fw *caerusframework.CaerusFramework) error {
	c.fw = fw
	return nil
}

func (c *CaerusExample) Shutdown(ctx context.Context) error {
	return nil
}

// GetDependencies declares which components must be initialized first.
// Dependency cycles are startup errors.
func (c *CaerusExample) GetDependencies() []string {
	return nil
}

// Run is optional: implement caerusframework.Runnable to launch a background
// worker that honors ctx cancellation.
// func (c *CaerusExample) Run(ctx context.Context) error {
// 	<-ctx.Done()
// 	return nil
// }
