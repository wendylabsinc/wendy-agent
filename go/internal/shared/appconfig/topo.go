package appconfig

import "fmt"

// TopologicalSort returns services grouped into "levels" where all services
// in the same level can start concurrently. Each level's services only start
// after all services in prior levels have passed their readiness checks.
// Returns an error if the dependsOn graph contains a cycle.
// Uses Kahn's algorithm.
func TopologicalSort(services map[string]*ServiceConfig) ([][]string, error) {
	if len(services) == 0 {
		return nil, nil
	}

	// Build in-degree count and adjacency list (dependency → dependents).
	inDegree := make(map[string]int, len(services))
	// dependents maps a service name to the list of services that depend on it.
	dependents := make(map[string][]string, len(services))

	for name := range services {
		if _, ok := inDegree[name]; !ok {
			inDegree[name] = 0
		}
	}

	for name, svc := range services {
		for _, dep := range svc.DependsOn {
			inDegree[name]++
			dependents[dep] = append(dependents[dep], name)
		}
	}

	// Collect the initial frontier: services with no dependencies.
	var current []string
	for name, deg := range inDegree {
		if deg == 0 {
			current = append(current, name)
		}
	}

	// Stable ordering within each level for deterministic output.
	sortStrings(current)

	var result [][]string
	visited := 0

	for len(current) > 0 {
		result = append(result, current)
		visited += len(current)

		var next []string
		for _, name := range current {
			for _, dependent := range dependents[name] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		sortStrings(next)
		current = next
	}

	if visited != len(services) {
		return nil, fmt.Errorf("dependsOn graph contains a cycle")
	}

	return result, nil
}

// sortStrings sorts a string slice in place (insertion sort — small N).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
