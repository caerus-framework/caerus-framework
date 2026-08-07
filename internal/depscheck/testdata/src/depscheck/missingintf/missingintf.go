// Package missingintf has a component that looks up peers in Init but
// implements no Dependencies interface at all.
package missingintf

import (
	"context"

	cf "github.com/caerus-framework/caerus-framework"
)

// Broker is a named peer component instance.
type Broker struct{}

func (b *Broker) Name() string                                    { return "broker" }
func (b *Broker) GetInitOrderStage() cf.Stage                     { return cf.Stage("data") }
func (b *Broker) Init(context.Context, *cf.CaerusFramework) error { return nil }
func (b *Broker) Shutdown(context.Context) error                  { return nil }

// Worker looks up a peer in Init but implements no Dependencies.
type Worker struct{}

func (w *Worker) Name() string                { return "worker" }
func (w *Worker) GetInitOrderStage() cf.Stage { return cf.Stage("app") }

// Init resolves a peer without declaring dependencies.
func (w *Worker) Init(ctx context.Context, fw *cf.CaerusFramework) error { // want "does not implement Dependencies"
	_, ok := cf.GetByName[*Broker](fw, "broker")
	_ = ok
	return nil
}

func (w *Worker) Shutdown(context.Context) error { return nil }
