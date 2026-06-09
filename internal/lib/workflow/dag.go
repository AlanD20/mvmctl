package workflow

import (
	"fmt"
	"strings"
)

// BuildDAG computes the topological levels of a set of steps using Kahn's
// algorithm. Returns levels where level 0 contains steps with no dependencies,
// level 1 contains steps that only depend on level 0, and so on.
//
// Returns an error if a cycle is detected. The error includes the cycle path.
func BuildDAG(steps []Step) ([][]Step, error) {
	if len(steps) == 0 {
		return nil, nil
	}

	// Build a name-to-step map for validation.
	stepByName := make(map[string]Step, len(steps))
	for _, s := range steps {
		name := s.Name()
		if _, dup := stepByName[name]; dup {
			return nil, fmt.Errorf("duplicate step name: %s", name)
		}
		stepByName[name] = s
	}

	// Validate that all dependencies reference existing steps.
	for _, s := range steps {
		for _, d := range s.Dependencies() {
			if _, ok := stepByName[d]; !ok {
				return nil, fmt.Errorf(
					"step %q depends on %q, but no step with that name exists",
					s.Name(), d,
				)
			}
		}
	}

	// Kahn's algorithm.
	//
	// inDegree[n] = number of unprocessed dependencies for step n.
	inDegree := make(map[string]int, len(steps))
	for _, s := range steps {
		// Initialize to zero (map returns zero value for missing keys, but
		// we want an explicit entry for every step).
		if _, ok := inDegree[s.Name()]; !ok {
			inDegree[s.Name()] = 0
		}
		for range s.Dependencies() {
			inDegree[s.Name()]++
		}
	}

	// adjacency[n] = list of steps that depend on n.
	adjacency := make(map[string][]Step, len(steps))
	for _, s := range steps {
		for _, dep := range s.Dependencies() {
			adjacency[dep] = append(adjacency[dep], s)
		}
	}

	// Collect steps with zero in-degree (no remaining dependencies).
	type levelEntry struct {
		step  Step
		depth int
	}

	var queue []levelEntry
	for _, s := range steps {
		if inDegree[s.Name()] == 0 {
			queue = append(queue, levelEntry{step: s, depth: 0})
		}
	}

	// Process the queue, assigning each step to a level.
	visited := make(map[string]bool, len(steps))
	var levels [][]Step

	for len(queue) > 0 {
		entry := queue[0]
		queue = queue[1:]

		if visited[entry.step.Name()] {
			continue
		}
		visited[entry.step.Name()] = true

		// Ensure levels slice is large enough.
		for len(levels) <= entry.depth {
			levels = append(levels, nil)
		}
		levels[entry.depth] = append(levels[entry.depth], entry.step)

		// Decrease in-degree for all dependents.
		for _, dependent := range adjacency[entry.step.Name()] {
			inDegree[dependent.Name()]--
			if inDegree[dependent.Name()] == 0 {
				queue = append(queue, levelEntry{step: dependent, depth: entry.depth + 1})
			}
		}
	}

	// Check if all steps were visited (cycle detection).
	if len(visited) != len(steps) {
		var unvisited []string
		for _, s := range steps {
			if !visited[s.Name()] {
				unvisited = append(unvisited, s.Name())
			}
		}

		// Try to find a cycle starting from each unvisited node —
		// detectCycle marks nodes as visited during its DFS, so we
		// iterate until all unvisited nodes are exhausted or a cycle
		// is found.
		var cyclePath string
		for _, name := range unvisited {
			if !visited[name] {
				if cp := detectCycle(name, steps, adjacency, visited); cp != "" {
					cyclePath = cp
					break
				}
			}
		}
		if cyclePath == "" {
			cyclePath = strings.Join(unvisited, ", ")
		}
		return nil, fmt.Errorf("cycle detected in step dependencies: %s", cyclePath)
	}

	return levels, nil
}

// detectCycle attempts to find a cycle starting from any unvisited step.
// Uses DFS on the visited set to find a back edge.
func detectCycle(start string, steps []Step, adjacency map[string][]Step, visited map[string]bool) string {
	// If start is already visited, it's not part of a cycle.
	if visited[start] {
		return ""
	}

	// Map step names to steps for quick lookup.
	stepByName := make(map[string]Step, len(steps))
	for _, s := range steps {
		stepByName[s.Name()] = s
	}

	// DFS with path tracking.
	onPath := make(map[string]bool)
	var path []string

	var dfs func(node string) bool
	dfs = func(node string) bool {
		if onPath[node] {
			// Found a cycle: extract from the path.
			path = append(path, node)
			return true
		}
		if visited[node] {
			return false
		}
		onPath[node] = true
		path = append(path, node)
		for _, dep := range adjacency[node] {
			if dfs(dep.Name()) {
				return true
			}
		}
		onPath[node] = false
		path = path[:len(path)-1]
		visited[node] = true
		return false
	}

	if dfs(start) {
		// Trim path to only the cycle part.
		cycleStart := path[len(path)-1]
		cycleIdx := -1
		for i, n := range path {
			if n == cycleStart {
				cycleIdx = i
				break
			}
		}
		if cycleIdx >= 0 {
			cyclePath := path[cycleIdx:]
			return strings.Join(cyclePath, " -> ")
		}
		return strings.Join(path, " -> ")
	}
	return ""
}
