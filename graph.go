package caerusframework

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// graphNode is the per-component graph representation used during resolution.
type graphNode struct {
	name      string
	component CaerusComponent
	stage     Stage
	index     int
	deps      []string
}

// resolveOrder orders components by registered stage and, within each stage, by
// the component dependency graph. It is the flattened form of resolveWaves:
// components that can initialize concurrently appear adjacent in registration
// order within each wave.
//
// Stages are an ordered list. The framework-owned bootstrap stages (logs,
// configuration, observability, secrets) come first in a fixed order, followed
// by application stages in first-seen order (auto-registered on AddComponent).
// Every component in an earlier stage initializes before any component in a
// later stage. A dependency may therefore only reference a component in the
// same or an earlier stage; a dependency that points to a later stage is a
// wiring error and is rejected. This guarantees a dependency can never pull a
// later-stage component earlier.
//
// Within a stage, dependencies between same-stage components give fine-grained
// ordering and are resolved by topological sort (Kahn's algorithm).
// Components that are simultaneously ready form one init wave (Initialize runs
// those in parallel). Ties within a wave are broken by registration
// (AddComponent) order, which keeps resolution deterministic. Because forward
// stage references are rejected, a cycle can only exist within a single stage.
// Unknown dependency names, unregistered stages, and cycles are errors.
func (f *CaerusFramework) resolveOrder() ([]CaerusComponent, error) {
	waves, err := f.resolveWaves()
	if err != nil {
		return nil, err
	}
	var order []CaerusComponent
	for _, wave := range waves {
		order = append(order, wave...)
	}
	return order, nil
}

// resolveWaves returns the initialization waves: each inner slice is a set of
// components that have no unmet same-stage dependencies on each other and may
// Init concurrently. Waves are ordered so every dependency (cross-stage or
// same-stage) appears in an earlier wave.
func (f *CaerusFramework) resolveWaves() ([][]CaerusComponent, error) {
	stageIndex := make(map[Stage]int, len(f.stages))
	for i, s := range f.stages {
		stageIndex[s] = i
	}

	nodes := make([]*graphNode, 0, len(f.components))
	byName := make(map[string]*graphNode, len(f.components))

	for i, c := range f.components {
		n := &graphNode{
			name:      c.Name(),
			component: c,
			stage:     c.GetInitOrderStage(),
			index:     i,
		}
		if _, ok := stageIndex[n.stage]; !ok {
			return nil, fmt.Errorf(
				"caerus: internal invariant violated: component %q is in unregistered stage %q (AddComponent should have registered it)",
				n.name, n.stage,
			)
		}
		if d, ok := c.(Dependencies); ok {
			n.deps = d.GetDependencies()
		}
		nodes = append(nodes, n)
		byName[n.name] = n
	}

	// Reject dependencies on unregistered components and on components in a
	// later stage before building any edges.
	for _, n := range nodes {
		for _, dep := range n.deps {
			d, ok := byName[dep]
			if !ok {
				return nil, fmt.Errorf("caerus: component %q depends on unknown component %q", n.name, dep)
			}
			if stageIndex[d.stage] > stageIndex[n.stage] {
				return nil, fmt.Errorf(
					"caerus: component %q (%s stage) depends on %q (%s stage), which initializes later",
					n.name, n.stage, dep, d.stage,
				)
			}
		}
	}

	var waves [][]CaerusComponent
	for _, stage := range f.stages {
		stageNodes := make([]*graphNode, 0, len(nodes))
		for _, n := range nodes {
			if n.stage == stage {
				stageNodes = append(stageNodes, n)
			}
		}
		if len(stageNodes) == 0 {
			continue
		}
		stageWaves, err := orderStageWaves(stageNodes, byName)
		if err != nil {
			return nil, err
		}
		waves = append(waves, stageWaves...)
	}
	return waves, nil
}

// orderStageWaves topologically sorts one stage into Kahn waves. Dependencies
// on earlier stages are already satisfied by stage ordering and are ignored.
// Each wave is sorted by registration index for determinism.
func orderStageWaves(stage []*graphNode, byName map[string]*graphNode) ([][]CaerusComponent, error) {
	// adj[x] = same-stage components that depend on x.
	adj := make(map[string][]string, len(stage))
	indegree := make(map[string]int, len(stage))
	for _, n := range stage {
		for _, dep := range n.deps {
			if byName[dep].stage != n.stage {
				continue // earlier-stage dependency, already satisfied
			}
			adj[dep] = append(adj[dep], n.name)
			indegree[n.name]++
		}
	}

	ready := make([]*graphNode, 0, len(stage))
	for _, n := range stage {
		if indegree[n.name] == 0 {
			ready = append(ready, n)
		}
	}

	var waves [][]CaerusComponent
	seen := 0
	for len(ready) > 0 {
		sort.SliceStable(ready, func(i, j int) bool { return ready[i].index < ready[j].index })
		wave := make([]CaerusComponent, len(ready))
		for i, n := range ready {
			wave[i] = n.component
		}
		waves = append(waves, wave)
		seen += len(ready)

		next := make([]*graphNode, 0, len(ready))
		for _, n := range ready {
			for _, dependent := range adj[n.name] {
				indegree[dependent]--
				if indegree[dependent] == 0 {
					next = append(next, byName[dependent])
				}
			}
		}
		ready = next
	}

	if seen != len(stage) {
		roots := make([]string, 0, len(stage))
		for _, n := range stage {
			roots = append(roots, n.name)
		}
		if cycle := findCycle(adj, roots); cycle != nil {
			return nil, fmt.Errorf("caerus: cyclic component dependency detected: %s", strings.Join(cycle, " -> "))
		}
		return nil, errors.New("caerus: cyclic component dependency detected")
	}
	return waves, nil
}

// findCycle returns one concrete dependency cycle as a path of component
// names, or nil if the subgraph reachable from roots is acyclic. The path
// includes the repeated node that closes the cycle, e.g. [a b a].
func findCycle(adj map[string][]string, roots []string) []string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(adj))
	var stack []string

	var visit func(name string) []string
	visit = func(name string) []string {
		state[name] = visiting
		stack = append(stack, name)
		for _, next := range adj[name] {
			switch state[next] {
			case visiting:
				start := 0
				for i, s := range stack {
					if s == next {
						start = i
						break
					}
				}
				cycle := make([]string, 0, len(stack)-start+1)
				cycle = append(cycle, stack[start:]...)
				cycle = append(cycle, next)
				return cycle
			case unvisited:
				if cycle := visit(next); cycle != nil {
					return cycle
				}
			}
		}
		state[name] = done
		stack = stack[:len(stack)-1]
		return nil
	}

	for _, name := range roots {
		if state[name] == unvisited {
			if cycle := visit(name); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}
