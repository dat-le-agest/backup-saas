// Package graph is a small directed graph with topological ordering.
//
//	g := graph.New[string]()
//	g.AddEdge("db", "repo") // db before repo
//	g.AddEdge("repo", "service")
//	order, err := g.TopoSort() // [db repo service]
//
// Every result is deterministic: nodes come in the order they were first added,
// whatever the map iteration order.
package graph

import (
	"fmt"
	"slices"
	"strings"
)

// Graph is a directed graph of comparable keys. An edge from → to means
// "from comes before to" (to depends on from). Not safe for concurrent writes.
type Graph[K comparable] struct {
	nodes []K // insertion order
	index map[K]int
	succ  map[K][]K
	pred  map[K][]K
}

func New[K comparable]() *Graph[K] {
	return &Graph[K]{index: map[K]int{}, succ: map[K][]K{}, pred: map[K][]K{}}
}

// AddNode adds k if it isn't in the graph yet.
func (g *Graph[K]) AddNode(k K) {
	if _, ok := g.index[k]; ok {
		return
	}
	g.index[k] = len(g.nodes)
	g.nodes = append(g.nodes, k)
}

// AddEdge adds from → to, and both nodes if needed. Adding an edge twice
// keeps one. A self edge (k → k) is a cycle.
func (g *Graph[K]) AddEdge(from, to K) {
	g.AddNode(from)
	g.AddNode(to)
	if slices.Contains(g.succ[from], to) {
		return
	}
	g.succ[from] = append(g.succ[from], to)
	g.pred[to] = append(g.pred[to], from)
}

func (g *Graph[K]) HasNode(k K) bool {
	_, ok := g.index[k]
	return ok
}

func (g *Graph[K]) HasEdge(from, to K) bool {
	return slices.Contains(g.succ[from], to)
}

func (g *Graph[K]) Len() int { return len(g.nodes) }

// Nodes returns every node in insertion order.
func (g *Graph[K]) Nodes() []K { return slices.Clone(g.nodes) }

// Successors returns the nodes k has an edge to, in the order the edges were added.
func (g *Graph[K]) Successors(k K) []K { return slices.Clone(g.succ[k]) }

// Predecessors returns the nodes with an edge to k, in the order the edges were added.
func (g *Graph[K]) Predecessors(k K) []K { return slices.Clone(g.pred[k]) }

// Reverse returns a new graph with every edge flipped, nodes in the same order.
func (g *Graph[K]) Reverse() *Graph[K] {
	r := New[K]()
	for _, k := range g.nodes {
		r.AddNode(k)
	}
	for _, from := range g.nodes {
		for _, to := range g.succ[from] {
			r.AddEdge(to, from)
		}
	}
	return r
}

// Reachable returns the nodes reachable from start through at least one edge,
// breadth first. start itself is included only if it lies on a cycle.
// Unknown start: nil.
func (g *Graph[K]) Reachable(start K) []K {
	seen := map[K]bool{}
	var out []K
	queue := slices.Clone(g.succ[start])
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
		queue = append(queue, g.succ[k]...)
	}
	return out
}

// TopoSort orders the nodes so every edge goes forward (Kahn's algorithm).
// Among nodes free to go next, earlier-added ones come first.
// Returns a *CycleError if the graph has a cycle.
func (g *Graph[K]) TopoSort() ([]K, error) {
	layers, err := g.TopoLayers()
	if err != nil {
		return nil, err
	}
	out := make([]K, 0, len(g.nodes))
	for _, layer := range layers {
		out = append(out, layer...)
	}
	return out, nil
}

// TopoLayers groups the nodes by depth: layer 0 has no predecessors, and every
// node of layer n has all its predecessors in earlier layers. Nodes of one layer
// don't depend on each other (they could be handled in parallel), and keep
// insertion order. Returns a *CycleError if the graph has a cycle.
func (g *Graph[K]) TopoLayers() ([][]K, error) {
	inDegree := make(map[K]int, len(g.nodes))
	var layer []K
	for _, k := range g.nodes {
		inDegree[k] = len(g.pred[k])
		if inDegree[k] == 0 {
			layer = append(layer, k)
		}
	}

	var layers [][]K
	done := 0
	for len(layer) > 0 {
		layers = append(layers, layer)
		done += len(layer)

		var next []K
		for _, k := range layer {
			for _, s := range g.succ[k] {
				inDegree[s]--
				if inDegree[s] == 0 {
					next = append(next, s)
				}
			}
		}
		slices.SortFunc(next, func(a, b K) int { return g.index[a] - g.index[b] })
		layer = next
	}

	if done != len(g.nodes) {
		return nil, &CycleError[K]{Cycle: g.findCycle(inDegree)}
	}
	return layers, nil
}

// findCycle returns one cycle among the nodes TopoLayers couldn't place
// (inDegree > 0), first node repeated at the end: [a b c a].
//
// Each such node has a predecessor that is also unplaced, so walking
// predecessors from any of them must revisit a node.
func (g *Graph[K]) findCycle(inDegree map[K]int) []K {
	var start K
	for _, k := range g.nodes {
		if inDegree[k] > 0 {
			start = k
			break
		}
	}

	pos := map[K]int{}
	var path []K
	for k := start; ; {
		if i, ok := pos[k]; ok {
			cycle := slices.Clone(path[i:])
			slices.Reverse(cycle) // walked backwards
			return append(cycle, cycle[0])
		}
		pos[k] = len(path)
		path = append(path, k)
		for _, p := range g.pred[k] {
			if inDegree[p] > 0 {
				k = p
				break
			}
		}
	}
}

// CycleError reports a cycle that prevents a topological order.
type CycleError[K comparable] struct {
	// Cycle lists the nodes along the edges, the first repeated at the end: [a b a].
	Cycle []K
}

func (e *CycleError[K]) Error() string {
	parts := make([]string, len(e.Cycle))
	for i, k := range e.Cycle {
		parts[i] = fmt.Sprint(k)
	}
	return "cycle: " + strings.Join(parts, " → ")
}
