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
// caerusvet prefers false negatives over false positives for names that
// cannot be resolved statically (os.Getenv, maps, types without
// ComponentName). WithName aliases (c.peerName(), c.valkeyName) are
// resolved and must appear in GetDependencies. Runtime Validate remains
// authoritative for the assembled graph (unknown names, missing
// registration, cycles).
package main

import (
	"github.com/caerus-framework/caerus-framework/internal/depscheck"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(depscheck.Analyzer)
}
