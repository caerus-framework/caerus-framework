// Command caerusvet is a go vet-style static checker for Caerus framework
// components: it verifies that every peer a component resolves during Init
// (cf.Get / cf.MustGet / cf.GetByName / cf.MustGetByName) is declared in its
// GetDependencies.
//
// Usage:
//
//	go tool caerusvet ./...
//
// or as a vet tool:
//
//	go vet -vettool=$(go tool -n caerusvet) ./...
//
// See PLAN-STATIC-DEPS.md for the design and success criteria.
package main

import (
	"github.com/caerus-framework/caerus-framework/internal/depscheck"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(depscheck.Analyzer)
}
