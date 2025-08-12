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
// the component dependency graph.
//
// Stages are an ordered list. The framework-owned bootstrap stages (logs,
// configuration, observability, secrets) come first in a fixed order, followed
// by application stages in the order they were registered with RegisterStage.
// Every component in an earlier stage initializes before any component in a
// later stage. A dependency may therefore only reference a component in the
// same or an earlier stage; a dependency that points to a later stage is a
// wiring error and is rejected. This guarantees a dependency can never pull a
// later-stage component earlier.
//
// Within a stage, dependencies between same-stage components give fine-grained
// ordering and are resolved by topological sort (Kahn's algorithm).
// Components that are simultaneously ready are emitted in registration
// (AddComponent) order, which keeps resolution deterministic. Because forward
// stage references are rejected, a cycle can only exist within a single stage.
// Unknown dependency names, unregistered stages, and cycles are errors.
func (f *CaerusFramework) resolveOrder() ([]CaerusComponent, error) {
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
				"caerus: component %q declares unregistered stage %q; register it with RegisterStage before Validate/Run",
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

	var order []CaerusComponent
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
		stageInitOrder, err := orderStage(stageNodes, byName)
		if err != nil {
			return nil, err
		}
		order = append(order, stageInitOrder...)
	}
	return order, nil
}

// orderStage topologically sorts the components of a single stage using only
// the edges between components of the same stage. Dependencies on earlier
// stages are already satisfied by the stage ordering, so they are ignored
// here. Ties between simultaneously-ready components are broken by
// registration order, which keeps the result deterministic.
func orderStage(stage []*graphNode, byName map[string]*graphNode) ([]CaerusComponent, error) {
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

	order := make([]CaerusComponent, 0, len(stage))
	for len(ready) > 0 {
		sort.SliceStable(ready, func(i, j int) bool { return ready[i].index < ready[j].index })
		n := ready[0]
		ready = ready[1:]
		order = append(order, n.component)
		for _, dependent := range adj[n.name] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, byName[dependent])
			}
		}
	}

	if len(order) != len(stage) {
		roots := make([]string, 0, len(stage))
		for _, n := range stage {
			roots = append(roots, n.name)
		}
		if cycle := findCycle(adj, roots); cycle != nil {
			return nil, fmt.Errorf("caerus: cyclic component dependency detected: %s", strings.Join(cycle, " -> "))
		}
		return nil, errors.New("caerus: cyclic component dependency detected")
	}
	return order, nil
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
