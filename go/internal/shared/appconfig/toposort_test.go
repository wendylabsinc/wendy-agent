package appconfig

import (
	"testing"
)

func TestServiceTopoOrder_Linear(t *testing.T) {
	services := map[string]*ServiceConfig{
		"db":       {},
		"api":      {DependsOn: []string{"db"}},
		"frontend": {DependsOn: []string{"api"}},
	}
	order, err := ServiceTopoOrder(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order[0] != "db" || order[1] != "api" || order[2] != "frontend" {
		t.Errorf("unexpected order: %v", order)
	}
}

func TestServiceTopoOrder_DiamondDependency(t *testing.T) {
	services := map[string]*ServiceConfig{
		"db":     {},
		"cache":  {},
		"api":    {DependsOn: []string{"db", "cache"}},
		"worker": {DependsOn: []string{"db"}},
	}
	order, err := ServiceTopoOrder(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// db and cache must come before api; db before worker.
	dbIdx := indexOf(order, "db")
	cacheIdx := indexOf(order, "cache")
	apiIdx := indexOf(order, "api")
	workerIdx := indexOf(order, "worker")
	if dbIdx >= apiIdx || cacheIdx >= apiIdx || dbIdx >= workerIdx {
		t.Errorf("order constraint violated: %v", order)
	}
}

func TestServiceTopoOrder_Cycle(t *testing.T) {
	services := map[string]*ServiceConfig{
		"a": {DependsOn: []string{"b"}},
		"b": {DependsOn: []string{"a"}},
	}
	_, err := ServiceTopoOrder(services)
	if err == nil {
		t.Fatal("expected error for cycle, got nil")
	}
}

func TestServiceTopoOrder_MissingDep(t *testing.T) {
	services := map[string]*ServiceConfig{
		"api": {DependsOn: []string{"db"}},
	}
	_, err := ServiceTopoOrder(services)
	if err == nil {
		t.Fatal("expected error for missing dep, got nil")
	}
}

func TestServiceTopoOrder_Single(t *testing.T) {
	services := map[string]*ServiceConfig{"app": {}}
	order, err := ServiceTopoOrder(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "app" {
		t.Errorf("unexpected order: %v", order)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
