// Package caerusframework is a minimal synthetic stand-in for the real
// caerus-framework core, used only by analysistest fixtures under
// internal/depscheck. The analyzer keys off this module's import path, so the
// fixtures type-check against the same identifiers (cf.Get, GetByName,
// CaerusComponent, Dependencies, ...) the real framework exposes.
package caerusframework

import "context"

// CaerusFramework is the registry the components are analyzed against.
type CaerusFramework struct{}

// Stage names the initialization bucket a component belongs to.
type Stage string

// CaerusComponent is the lifecycle contract every component must implement.
type CaerusComponent interface {
	Name() string
	GetInitOrderStage() Stage
	Init(ctx context.Context, fw *CaerusFramework) error
	Shutdown(ctx context.Context) error
}

// Dependencies is an optional interface for components that require other
// components to be initialized first, declared by component name.
type Dependencies interface {
	GetDependencies() []string
}

// Get returns the registered component of type T.
func Get[T CaerusComponent](f *CaerusFramework) (T, bool) {
	var zero T
	return zero, false
}

// MustGet returns the registered component of type T or panics.
func MustGet[T CaerusComponent](f *CaerusFramework) T {
	panic("unimplemented in test shim")
}

// GetByName returns the registered component with the given name, typed T.
func GetByName[T CaerusComponent](f *CaerusFramework, name string) (T, bool) {
	var zero T
	return zero, false
}

// MustGetByName returns the registered component with the given name or
// panics.
func MustGetByName[T CaerusComponent](f *CaerusFramework, name string) T {
	panic("unimplemented in test shim")
}
