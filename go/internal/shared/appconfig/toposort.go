package appconfig

import (
	"fmt"
	"sort"
)

// ServiceTopoOrder returns service names in topological order so that every
// service appears after all its DependsOn entries. Returns an error for cyclic
// or missing dependencies.
func ServiceTopoOrder(services map[string]*ServiceConfig) ([]string, error) {
	visited := make(map[string]bool, len(services))
	inStack := make(map[string]bool, len(services))
	ordered := make([]string, 0, len(services))

	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if inStack[name] {
			return fmt.Errorf("cycle detected in dependsOn graph involving service %q", name)
		}
		inStack[name] = true
		svc, ok := services[name]
		if ok {
			for _, dep := range svc.DependsOn {
				if _, present := services[dep]; !present {
					return fmt.Errorf("service %q depends on %q which is not in the services map", name, dep)
				}
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		delete(inStack, name)
		visited[name] = true
		ordered = append(ordered, name)
		return nil
	}

	names := make([]string, 0, len(services))
	for n := range services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}
