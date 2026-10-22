package graph

import (
	"errors"
	"slices"
	"testing"
)

func TestTopoSortIsDeterministic(t *testing.T) {
	g := New[string]()
	g.AddNode("logger") // isolated, added first: goes first
	g.AddEdge("db", "repo")
	g.AddEdge("cache", "repo")
	g.AddEdge("repo", "service")
	g.AddEdge("db", "service")
	g.AddEdge("db", "repo") // duplicate: ignored

	for i := 0; i < 20; i++ {
		got, err := g.TopoSort()
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"logger", "db", "cache", "repo", "service"}; !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if got := g.Predecessors("repo"); !slices.Equal(got, []string{"db", "cache"}) {
		t.Errorf("predecessors = %v", got)
	}
}

func TestTopoLayers(t *testing.T) {
	g := New[int]()
	g.AddEdge(1, 3)
	g.AddEdge(2, 3)
	g.AddEdge(3, 4)
	g.AddEdge(1, 4)
	g.AddNode(5)

	got, err := g.TopoLayers()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2, 5}, {3}, {4}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEmptyGraph(t *testing.T) {
	g := New[string]()
	order, err := g.TopoSort()
	if err != nil || len(order) != 0 {
		t.Fatalf("got %v, %v", order, err)
	}
}

func TestCycleError(t *testing.T) {
	tests := []struct {
		name  string
		edges [][2]string
		want  []string
	}{
		{"self edge", [][2]string{{"a", "a"}}, []string{"a", "a"}},
		{"two nodes", [][2]string{{"a", "b"}, {"b", "a"}}, []string{"a", "b", "a"}},
		{
			"cycle behind an acyclic part",
			[][2]string{{"root", "x"}, {"x", "y"}, {"y", "z"}, {"z", "x"}, {"z", "leaf"}},
			[]string{"x", "y", "z", "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := New[string]()
			for _, e := range tt.edges {
				g.AddEdge(e[0], e[1])
			}
			_, err := g.TopoSort()
			var cerr *CycleError[string]
			if !errors.As(err, &cerr) {
				t.Fatalf("want *CycleError, got %v", err)
			}
			// a rotation of the same cycle is equally right; normalize to the expected start
			if !isRotation(cerr.Cycle, tt.want) {
				t.Fatalf("cycle = %v, want %v", cerr.Cycle, tt.want)
			}
			for i := 0; i+1 < len(cerr.Cycle); i++ {
				if !g.HasEdge(cerr.Cycle[i], cerr.Cycle[i+1]) {
					t.Fatalf("%v → %v is not an edge", cerr.Cycle[i], cerr.Cycle[i+1])
				}
			}
		})
	}
}

// isRotation: closed cycles [a b c a] and [b c a b] are the same cycle.
func isRotation(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	open := got[:len(got)-1]
	for i := range open {
		rotated := append(slices.Clone(open[i:]), open[:i]...)
		if slices.Equal(append(rotated, rotated[0]), want) {
			return true
		}
	}
	return false
}

func TestCycleErrorMessage(t *testing.T) {
	err := &CycleError[string]{Cycle: []string{"a", "b", "a"}}
	if got, want := err.Error(), "cycle: a → b → a"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReverse(t *testing.T) {
	g := New[string]()
	g.AddEdge("a", "b")
	g.AddEdge("b", "c")
	r := g.Reverse()

	order, err := r.TopoSort()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"c", "b", "a"}; !slices.Equal(order, want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	if !g.HasEdge("a", "b") || g.HasEdge("b", "a") {
		t.Error("Reverse changed the original graph")
	}
}

func TestReachable(t *testing.T) {
	g := New[string]()
	g.AddEdge("a", "b")
	g.AddEdge("a", "c")
	g.AddEdge("b", "d")
	g.AddEdge("c", "d")
	g.AddNode("e")

	if got, want := g.Reachable("a"), []string{"b", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("Reachable(a) = %v, want %v", got, want)
	}
	if got := g.Reachable("d"); len(got) != 0 {
		t.Errorf("Reachable(d) = %v, want none", got)
	}
	if got := g.Reachable("missing"); got != nil {
		t.Errorf("Reachable(missing) = %v, want nil", got)
	}

	g.AddEdge("d", "a") // now a is on a cycle
	if got := g.Reachable("a"); !slices.Contains(got, "a") {
		t.Errorf("Reachable(a) on a cycle = %v, want a included", got)
	}
}
