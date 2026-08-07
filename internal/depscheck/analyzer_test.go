package depscheck_test

import (
	"flag"
	"testing"

	"github.com/caerus-framework/caerus-framework/internal/depscheck"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
)

// staleAnalyzer is depscheck with the -stale-dep hygiene warning enabled.
var staleAnalyzer = &analysis.Analyzer{
	Name:  "depscheck",
	Doc:   depscheck.Analyzer.Doc,
	Flags: *flag.NewFlagSet("depscheck", flag.ExitOnError),
	Run:   depscheck.Analyzer.Run,
}

func init() {
	staleAnalyzer.Flags.Bool("stale-dep", true, "enable the stale-dep warning")
}

func TestDepscheck(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), depscheck.Analyzer,
		"depscheck/ok",
		"depscheck/missingdep",
		"depscheck/missingintf",
		"depscheck/helper",
		"depscheck/helpermiss",
		"depscheck/dynamic",
	)
}

func TestDepscheckStaleDep(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), staleAnalyzer, "depscheck/stale")
}
