// Package planner validates a rename manifest and builds a collision-free
// dry-run rename plan for a case-insensitive filesystem.
package planner

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const tempPrefix = "__planner_tmp_"

// Mapping is one source-to-target rename from the manifest.
type Mapping struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// Manifest describes the files currently visible to the planner. The planner
// never uses this list to touch files; it is only its simulated filesystem.
type Manifest struct {
	Files    []string  `json:"files"`
	Mappings []Mapping `json:"mappings"`
}

// Step is one planned move.
type Step struct {
	// Component is the smallest source name in the dependency component.
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
	// Temporary is true when one endpoint of the move is the scratch name.
	Temporary bool `json:"temporary,omitempty"`
}

// Plan contains forward steps in execution order and the reverse rollback.
type Plan struct {
	TemporaryName string `json:"temporary_name,omitempty"`
	Steps         []Step `json:"steps"`
	RollbackSteps []Step `json:"rollback_steps"`
}

type activeMapping struct {
	source string
	target string
}

type component struct {
	min   string
	nodes []string
	cycle bool
}

// ParseManifest decodes and validates the manifest, then returns its plan.
func ParseManifest(data []byte) (*Plan, error) {
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse manifest: unexpected trailing JSON value")
		}
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return PlanManifest(manifest)
}

// PlanManifest validates an already-decoded manifest and builds its plan.
func PlanManifest(manifest Manifest) (*Plan, error) {
	if n := len(manifest.Files); n < 1 || n > 200 {
		return nil, fmt.Errorf("files must contain between 1 and 200 entries, got %d", n)
	}

	filesByLower := make(map[string]string, len(manifest.Files))
	reserved := make(map[string]string, len(manifest.Files)*2)
	for _, name := range manifest.Files {
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("invalid current file name %q: %w", name, err)
		}
		key := strings.ToLower(name)
		if other, ok := filesByLower[key]; ok {
			return nil, fmt.Errorf("current file names are not unique after ASCII lower-casing: %q and %q", other, name)
		}
		filesByLower[key] = name
		reserved[key] = name
	}

	// Sources must identify one existing file by its exact current spelling.
	seenSources := make(map[string]bool, len(manifest.Mappings))
	for i, mapping := range manifest.Mappings {
		if err := validateName(mapping.Source); err != nil {
			return nil, fmt.Errorf("mapping %d has invalid source %q: %w", i, mapping.Source, err)
		}
		if err := validateName(mapping.Target); err != nil {
			return nil, fmt.Errorf("mapping %d has invalid target %q: %w", i, mapping.Target, err)
		}

		owner, ok := filesByLower[strings.ToLower(mapping.Source)]
		if !ok {
			return nil, fmt.Errorf("mapping %d source %q is not a current file", i, mapping.Source)
		}
		if owner != mapping.Source {
			return nil, fmt.Errorf("mapping %d source %q does not exactly match current file name %q", i, mapping.Source, owner)
		}
		if seenSources[mapping.Source] {
			return nil, fmt.Errorf("mapping %d repeats source %q", i, mapping.Source)
		}
		seenSources[mapping.Source] = true
	}

	activeBySource := make(map[string]activeMapping)
	allTargets := make(map[string]string)

	for i, mapping := range manifest.Mappings {
		targetKey := strings.ToLower(mapping.Target)
		if other, ok := allTargets[targetKey]; ok {
			return nil, fmt.Errorf("mapping %d target %q is not unique after lower-casing; also used by target %q", i, mapping.Target, other)
		}
		allTargets[targetKey] = mapping.Target

		if targetKey == strings.ToLower(mapping.Source) {
			if mapping.Target == mapping.Source {
				// A -> A needs no filesystem operation.
				continue
			}
			// A -> a is a one-file cycle on a case-insensitive filesystem.
		} else if owner, ok := filesByLower[targetKey]; ok && !seenSources[owner] {
			return nil, fmt.Errorf("mapping %d target %q is already used by uninvolved file %q", i, mapping.Target, owner)
		}

		reserved[targetKey] = mapping.Target
		activeBySource[mapping.Source] = activeMapping{source: mapping.Source, target: mapping.Target}
	}

	components := buildComponents(activeBySource, filesByLower)
	needsTemp := false
	for _, c := range components {
		if c.cycle {
			needsTemp = true
			break
		}
	}

	plan := &Plan{Steps: []Step{}, RollbackSteps: []Step{}}
	if needsTemp {
		plan.TemporaryName = smallestTempName(reserved)
	}

	var groups [][]Step
	for _, c := range components {
		groups = append(groups, componentSteps(c, activeBySource, plan.TemporaryName))
	}
	for _, steps := range groups {
		plan.Steps = append(plan.Steps, steps...)
	}

	// The rollback is the exact inverse sequence: reverse all forward moves
	// and exchange their endpoints.
	for i := len(plan.Steps) - 1; i >= 0; i-- {
		step := plan.Steps[i]
		plan.RollbackSteps = append(plan.RollbackSteps, Step{
			Component: step.Component,
			From:      step.To,
			To:        step.From,
			Temporary: step.Temporary,
		})
	}

	return plan, nil
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("name must not be a directory reference")
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b >= 0x80 {
			return fmt.Errorf("name is not ASCII")
		}
		if b == '/' || b == 0 || b < 0x20 || b == 0x7f {
			return fmt.Errorf("name contains an unsupported control or path character")
		}
	}
	return nil
}

func buildComponents(active map[string]activeMapping, filesByLower map[string]string) []component {
	next := make(map[string]string)
	indegree := make(map[string]int)
	for source, mapping := range active {
		indegree[source] += 0 // ensure every active source is represented
		if owner, ok := filesByLower[strings.ToLower(mapping.target)]; ok {
			if _, activeTarget := active[owner]; activeTarget {
				next[source] = owner
				indegree[owner]++
			}
		}
	}

	visited := make(map[string]bool)
	var components []component

	// Every path component has a source with no incoming edge.
	var starts []string
	for node, degree := range indegree {
		if degree == 0 {
			starts = append(starts, node)
		}
	}
	sort.Strings(starts)

	for _, start := range starts {
		var nodes []string
		node := start
		for {
			nodes = append(nodes, node)
			visited[node] = true
			successor, ok := next[node]
			if !ok {
				break
			}
			node = successor
		}
		components = append(components, component{min: minName(nodes), nodes: nodes})
	}

	// All nodes left over belong to directed cycles. Pick each cycle's
	// smallest source as its representative and rotate traversal there.
	var remaining []string
	for node := range active {
		if !visited[node] {
			remaining = append(remaining, node)
		}
	}
	sort.Strings(remaining)
	for _, start := range remaining {
		if visited[start] {
			continue
		}
		var nodes []string
		node := start
		for {
			nodes = append(nodes, node)
			visited[node] = true
			node = next[node]
			if node == start {
				break
			}
		}

		minIndex := 0
		for i := 1; i < len(nodes); i++ {
			if nodes[i] < nodes[minIndex] {
				minIndex = i
			}
		}
		rotated := append([]string{}, nodes[minIndex:]...)
		rotated = append(rotated, nodes[:minIndex]...)
		components = append(components, component{min: rotated[0], nodes: rotated, cycle: true})
	}

	sort.Slice(components, func(i, j int) bool {
		return components[i].min < components[j].min
	})
	return components
}

func componentSteps(c component, active map[string]activeMapping, tempName string) []Step {
	if !c.cycle {
		steps := make([]Step, 0, len(c.nodes))
		for i := len(c.nodes) - 1; i >= 0; i-- {
			node := c.nodes[i]
			steps = append(steps, Step{
				Component: c.min,
				From:      node,
				To:        active[node].target,
			})
		}
		return steps
	}

	n := len(c.nodes)
	first := c.nodes[0]
	steps := make([]Step, 0, n+1)
	steps = append(steps, Step{
		Component: c.min,
		From:      first,
		To:        tempName,
		Temporary: true,
	})
	for i := n - 1; i >= 1; i-- {
		node := c.nodes[i]
		steps = append(steps, Step{
			Component: c.min,
			From:      node,
			To:        active[node].target,
		})
	}
	steps = append(steps, Step{
		Component: c.min,
		From:      tempName,
		To:        active[first].target,
		Temporary: true,
	})
	return steps
}

func smallestTempName(reserved map[string]string) string {
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s%d", tempPrefix, i)
		if _, exists := reserved[strings.ToLower(name)]; !exists {
			return name
		}
	}
}

func minName(names []string) string {
	result := names[0]
	for _, name := range names[1:] {
		if name < result {
			result = name
		}
	}
	return result
}
