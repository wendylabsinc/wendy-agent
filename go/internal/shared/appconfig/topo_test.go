package appconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestTopologicalSort_LinearChain(t *testing.T) {
	// db → api → frontend
	services := map[string]*ServiceConfig{
		"db":       {Context: "db"},
		"api":      {Context: "api", DependsOn: []string{"db"}},
		"frontend": {Context: "frontend", DependsOn: []string{"api"}},
	}

	levels, err := TopologicalSort(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := [][]string{{"db"}, {"api"}, {"frontend"}}
	if !reflect.DeepEqual(levels, want) {
		t.Errorf("got %v, want %v", levels, want)
	}
}

func TestTopologicalSort_Diamond(t *testing.T) {
	// db → (api, worker) → frontend
	services := map[string]*ServiceConfig{
		"db":       {Context: "db"},
		"api":      {Context: "api", DependsOn: []string{"db"}},
		"worker":   {Context: "worker", DependsOn: []string{"db"}},
		"frontend": {Context: "frontend", DependsOn: []string{"api", "worker"}},
	}

	levels, err := TopologicalSort(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d: %v", len(levels), levels)
	}

	// Level 0: just db
	if !reflect.DeepEqual(levels[0], []string{"db"}) {
		t.Errorf("level 0: got %v, want [db]", levels[0])
	}

	// Level 1: api and worker (alphabetical order)
	if !reflect.DeepEqual(levels[1], []string{"api", "worker"}) {
		t.Errorf("level 1: got %v, want [api worker]", levels[1])
	}

	// Level 2: frontend
	if !reflect.DeepEqual(levels[2], []string{"frontend"}) {
		t.Errorf("level 2: got %v, want [frontend]", levels[2])
	}
}

func TestTopologicalSort_FullyIndependent(t *testing.T) {
	// All services independent — should all land in one level.
	services := map[string]*ServiceConfig{
		"a": {Context: "a"},
		"b": {Context: "b"},
		"c": {Context: "c"},
	}

	levels, err := TopologicalSort(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 1 {
		t.Fatalf("expected 1 level, got %d: %v", len(levels), levels)
	}

	if !reflect.DeepEqual(levels[0], []string{"a", "b", "c"}) {
		t.Errorf("level 0: got %v, want [a b c]", levels[0])
	}
}

func TestTopologicalSort_SingleService(t *testing.T) {
	services := map[string]*ServiceConfig{
		"app": {Context: "app"},
	}

	levels, err := TopologicalSort(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := [][]string{{"app"}}
	if !reflect.DeepEqual(levels, want) {
		t.Errorf("got %v, want %v", levels, want)
	}
}

func TestTopologicalSort_CycleDetection(t *testing.T) {
	// a → b → c → a forms a cycle
	services := map[string]*ServiceConfig{
		"a": {Context: "a", DependsOn: []string{"c"}},
		"b": {Context: "b", DependsOn: []string{"a"}},
		"c": {Context: "c", DependsOn: []string{"b"}},
	}

	_, err := TopologicalSort(services)
	if err == nil {
		t.Fatal("expected an error for cyclic graph, got nil")
	}
}

func TestTopologicalSort_EmptyMap(t *testing.T) {
	levels, err := TopologicalSort(map[string]*ServiceConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if levels != nil {
		t.Errorf("expected nil for empty map, got %v", levels)
	}
}

func TestTopologicalSort_NilServiceConfig(t *testing.T) {
	services := map[string]*ServiceConfig{
		"app": {Context: "app"},
		"bad": nil,
	}

	_, err := TopologicalSort(services)
	if err == nil {
		t.Fatal("expected an error for nil service config, got nil")
	}
	if !strings.Contains(err.Error(), "nil service config") {
		t.Errorf("expected error to contain \"nil service config\", got: %v", err)
	}
}

func TestTopologicalSort_UnknownDependency(t *testing.T) {
	services := map[string]*ServiceConfig{
		"app": {Context: "app", DependsOn: []string{"nonexistent"}},
	}

	_, err := TopologicalSort(services)
	if err == nil {
		t.Fatal("expected an error for unknown dependency, got nil")
	}
	if !strings.Contains(err.Error(), "unknown service") {
		t.Errorf("expected error to contain \"unknown service\", got: %v", err)
	}
	if strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected no mention of \"cycle\" for unknown dependency, got: %v", err)
	}
}
