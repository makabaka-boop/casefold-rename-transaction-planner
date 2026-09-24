package planner

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// caseInsensitiveTable is the test double for the shared filesystem. Keys are
// lower-cased ASCII names; names preserves the exact spelling users requested.
type caseInsensitiveTable struct {
	names map[string]string
	files map[string]int
}

func newTable(files []string) *caseInsensitiveTable {
	table := &caseInsensitiveTable{
		names: make(map[string]string, len(files)),
		files: make(map[string]int, len(files)),
	}
	for id, name := range files {
		key := strings.ToLower(name)
		table.names[key] = name
		table.files[key] = id + 1
	}
	return table
}

func (t *caseInsensitiveTable) move(from, to string) error {
	fromKey := strings.ToLower(from)
	id, ok := t.files[fromKey]
	if !ok {
		return fmt.Errorf("source %q does not exist", from)
	}
	if t.names[fromKey] != from {
		return fmt.Errorf("source %q does not exactly match current spelling %q", from, t.names[fromKey])
	}

	toKey := strings.ToLower(to)
	if _, exists := t.files[toKey]; exists {
		return fmt.Errorf("refusing to overwrite occupied target %q (current spelling %q)", to, t.names[toKey])
	}

	delete(t.names, fromKey)
	delete(t.files, fromKey)
	t.names[toKey] = to
	t.files[toKey] = id
	return nil
}

func (t *caseInsensitiveTable) snapshot() *caseInsensitiveTable {
	copyTable := &caseInsensitiveTable{
		names: make(map[string]string, len(t.names)),
		files: make(map[string]int, len(t.files)),
	}
	for key, name := range t.names {
		copyTable.names[key] = name
	}
	for key, id := range t.files {
		copyTable.files[key] = id
	}
	return copyTable
}

func replay(t *testing.T, initial []string, plan *Plan) *caseInsensitiveTable {
	t.Helper()
	table := newTable(initial)
	for _, step := range plan.Steps {
		if err := table.move(step.From, step.To); err != nil {
			t.Fatalf("forward move %q -> %q failed: %v", step.From, step.To, err)
		}
	}
	return table
}

func assertFinal(t *testing.T, table *caseInsensitiveTable, initial []string, expected map[string]string) {
	t.Helper()
	if len(table.files) != len(initial) {
		t.Fatalf("file count changed: got %d want %d", len(table.files), len(initial))
	}

	seenIDs := map[int]bool{}
	for source, target := range expected {
		key := strings.ToLower(target)
		actualName, ok := table.names[key]
		if !ok {
			t.Fatalf("missing final target %q for source %q", target, source)
		}
		if actualName != target {
			t.Fatalf("final spelling for source %q is %q, want %q", source, actualName, target)
		}
		id := table.files[key]
		if seenIDs[id] {
			t.Fatalf("file id %d appears at two final names", id)
		}
		seenIDs[id] = true
	}

	for id := 1; id <= len(initial); id++ {
		if !seenIDs[id] {
			t.Fatalf("initial file id %d was lost", id)
		}
	}
}

func assertRollback(t *testing.T, final *caseInsensitiveTable, initial []string, plan *Plan) {
	t.Helper()
	table := final.snapshot()
	for _, step := range plan.RollbackSteps {
		if err := table.move(step.From, step.To); err != nil {
			t.Fatalf("rollback move %q -> %q failed: %v", step.From, step.To, err)
		}
	}

	initialTable := newTable(initial)
	if len(table.files) != len(initialTable.files) {
		t.Fatalf("rollback produced %d files, want %d", len(table.files), len(initialTable.files))
	}
	for key, wantName := range initialTable.names {
		gotName, ok := table.names[key]
		if !ok {
			t.Fatalf("rollback missing restored name %q", wantName)
		}
		if gotName != wantName || table.files[key] != initialTable.files[key] {
			t.Fatalf("rollback at %q has (%q,id=%d), want (%q,id=%d)",
				key, gotName, table.files[key], wantName, initialTable.files[key])
		}
	}
}

func assertTemporaryIsSmallest(t *testing.T, manifest Manifest, plan *Plan) {
	t.Helper()
	if plan.TemporaryName == "" {
		for _, step := range plan.Steps {
			if step.Temporary {
				t.Fatal("temporary step without a temporary name")
			}
		}
		return
	}

	reserved := map[string]bool{}
	for _, name := range manifest.Files {
		reserved[strings.ToLower(name)] = true
	}
	for _, mapping := range manifest.Mappings {
		reserved[strings.ToLower(mapping.Target)] = true
	}
	for i := 1; i < 10_000; i++ {
		candidate := fmt.Sprintf("%s%d", tempPrefix, i)
		if candidate == plan.TemporaryName {
			return
		}
		if !reserved[strings.ToLower(candidate)] {
			t.Fatalf("chose temporary %q, but smaller free candidate %q exists", plan.TemporaryName, candidate)
		}
	}
	t.Fatalf("could not bound temporary-name search")
}

func TestExhaustiveSmallMappings(t *testing.T) {
	const n = 3

	for size := 1; size <= n; size++ {
		current := make([]string, size)
		for i := range current {
			current[i] = fmt.Sprintf("a%d", i)
		}
		fresh := make([]string, size)
		for i := range fresh {
			fresh[i] = fmt.Sprintf("t%d", i)
		}
		slots := append(append([]string{}, current...), fresh...)

		for includedMask := 0; includedMask < 1<<size; includedMask++ {
			var included []int
			for i := 0; i < size; i++ {
				if includedMask&(1<<i) != 0 {
					included = append(included, i)
				}
			}

			enumerateInjections(t, current, slots, included, 0, map[int]bool{}, nil)
		}
	}
}

func enumerateInjections(
	t *testing.T,
	current []string,
	slots []string,
	included []int,
	position int,
	usedSlots map[int]bool,
	chosenSlots []int,
) {
	if position == len(included) {
		for casingMask := 0; casingMask < 1<<len(included); casingMask++ {
			manifest := Manifest{Files: current, Mappings: make([]Mapping, len(included))}
			expected := make(map[string]string, len(current))
			for _, name := range current {
				expected[name] = name
			}

			for i, sourceIndex := range included {
				target := slots[chosenSlots[i]]
				if casingMask&(1<<i) != 0 {
					target = strings.ToUpper(target[:1]) + target[1:]
				}
				manifest.Mappings[i] = Mapping{Source: current[sourceIndex], Target: target}
				expected[current[sourceIndex]] = target
			}

			plan, err := PlanManifest(manifest)
			if err != nil {
				t.Fatalf("valid manifest was rejected: %v\nmanifest: %#v", err, manifest)
			}

			activeEdges, cycles := expectedGraphStats(manifest)
			if got := len(plan.Steps); got != activeEdges+cycles {
				t.Fatalf("move count = %d, want minimum %d (edges=%d cycles=%d)\nmanifest: %#v",
					got, activeEdges+cycles, activeEdges, cycles, manifest)
			}
			assertExactlyOneExtraTemporaryMovePerCycle(t, plan, cycles)
			assertComponentsOrdered(t, plan.Steps)
			assertRollbackIsExactInverse(t, plan)
			assertTemporaryIsSmallest(t, manifest, plan)
			final := replay(t, current, plan)
			assertFinal(t, final, current, expected)
			assertRollback(t, final, current, plan)
		}
		return
	}

	for slotIndex := range slots {
		if usedSlots[slotIndex] {
			continue
		}
		// A target occupying an uninvolved current file is forbidden.
		if slotIndex < len(current) {
			ownerIndex := slotIndex
			ownerIncluded := false
			for _, index := range included {
				if index == ownerIndex {
					ownerIncluded = true
					break
				}
			}
			if !ownerIncluded {
				continue
			}
		}

		usedSlots[slotIndex] = true
		enumerateInjections(t, current, slots, included, position+1, usedSlots, append(chosenSlots, slotIndex))
		delete(usedSlots, slotIndex)
	}
}

func expectedGraphStats(manifest Manifest) (edges, cycles int) {
	next := map[string]string{}
	for _, mapping := range manifest.Mappings {
		sourceKey := strings.ToLower(mapping.Source)
		targetKey := strings.ToLower(mapping.Target)
		if targetKey == sourceKey && mapping.Target == mapping.Source {
			continue
		}
		edges++
		for _, file := range manifest.Files {
			if strings.ToLower(file) == targetKey {
				next[sourceKey] = targetKey
				break
			}
		}
	}

	state := map[string]int{}
	for start := range next {
		if state[start] != 0 {
			continue
		}
		path := []string{}
		node := start
		for {
			if state[node] == 2 {
				break
			}
			if state[node] == 1 {
				cycles++
				break
			}
			state[node] = 1
			path = append(path, node)
			successor, ok := next[node]
			if !ok {
				break
			}
			node = successor
		}
		for _, visitedNode := range path {
			state[visitedNode] = 2
		}
	}
	return edges, cycles
}

func assertRollbackIsExactInverse(t *testing.T, plan *Plan) {
	t.Helper()
	if len(plan.RollbackSteps) != len(plan.Steps) {
		t.Fatal("rollback must contain one inverse move per forward move")
	}
	for i, forward := range plan.Steps {
		backward := plan.RollbackSteps[len(plan.RollbackSteps)-1-i]
		if forward.Component != backward.Component ||
			forward.From != backward.To ||
			forward.To != backward.From ||
			forward.Temporary != backward.Temporary {
			t.Fatalf("rollback step %d is not the inverse of forward step %d", i, i)
		}
	}
}

func assertExactlyOneExtraTemporaryMovePerCycle(t *testing.T, plan *Plan, cycles int) {
	t.Helper()
	temporarySteps := 0
	byComponent := map[string]int{}
	for _, step := range plan.Steps {
		if step.Temporary {
			temporarySteps++
			byComponent[step.Component]++
		}
	}
	if temporarySteps != 2*cycles {
		t.Fatalf("got %d temporary endpoint moves, want 2 per cycle (%d total)", temporarySteps, 2*cycles)
	}
	for component, count := range byComponent {
		if count != 2 {
			t.Fatalf("component %q used %d temporary endpoint moves, want exactly 2", component, count)
		}
	}
}

func assertComponentsOrdered(t *testing.T, steps []Step) {
	t.Helper()
	previous := ""
	started := map[string]bool{}
	finished := map[string]bool{}
	for _, step := range steps {
		if started[step.Component] && finished[step.Component] {
			t.Fatalf("component %q steps are not contiguous", step.Component)
		}
		started[step.Component] = true
		if previous != "" {
			if previous == step.Component {
				continue
			}
			if finished[previous] && previous >= step.Component {
				t.Fatalf("components not sorted by minimum source: %q followed by %q", previous, step.Component)
			}
		}
		if previous != "" && previous != step.Component {
			finished[previous] = true
		}
		previous = step.Component
	}
}

func TestMixedCaseCyclesAndChain(t *testing.T) {
	files := []string{"Alpha", "BETA", "gamma", "delta"}
	manifest := Manifest{
		Files: files,
		Mappings: []Mapping{
			{Source: "Alpha", Target: "ALPHA"}, // case-only one-file cycle
			{Source: "BETA", Target: "beta"},   // case-only one-file cycle
			{Source: "gamma", Target: "GAMMA"},
			{Source: "delta", Target: "DELTA"},
		},
	}

	plan, err := PlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 8 { // four requested edges + one temporary move per cycle
		t.Fatalf("got %d moves, want 8", len(plan.Steps))
	}
	assertExactlyOneExtraTemporaryMovePerCycle(t, plan, 4)
	if plan.TemporaryName != tempPrefix+"1" {
		t.Fatalf("unexpected temporary name %q", plan.TemporaryName)
	}
	if got := plan.Steps[0].Component; got != "Alpha" {
		t.Fatalf("components ordered by ASCII source; first = %q", got)
	}
	assertComponentsOrdered(t, plan.Steps)
	assertRollbackIsExactInverse(t, plan)

	expected := map[string]string{
		"Alpha": "ALPHA",
		"BETA":  "beta",
		"gamma": "GAMMA",
		"delta": "DELTA",
	}
	final := replay(t, files, plan)
	assertFinal(t, final, files, expected)
	assertRollback(t, final, files, plan)
}

func TestUninvolvedTargetIsRejected(t *testing.T) {
	manifest := Manifest{
		Files:    []string{"a", "b", "c"},
		Mappings: []Mapping{{Source: "a", Target: "B"}},
	}
	if _, err := PlanManifest(manifest); err == nil || !strings.Contains(err.Error(), "uninvolved") {
		t.Fatalf("expected uninvolved target error, got %v", err)
	}
}

func TestDuplicateCurrentNamesAndTargets(t *testing.T) {
	_, err := PlanManifest(Manifest{Files: []string{"A", "a"}})
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("expected duplicate current name error, got %v", err)
	}

	manifest := Manifest{
		Files:    []string{"a", "b", "c"},
		Mappings: []Mapping{{Source: "a", Target: "z"}, {Source: "b", Target: "Z"}},
	}
	_, err = PlanManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("expected duplicate target error, got %v", err)
	}
}

func TestInvalidSourcesAndRepeats(t *testing.T) {
	_, err := PlanManifest(Manifest{
		Files:    []string{"a"},
		Mappings: []Mapping{{Source: "A", Target: "z"}},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("expected exact source error, got %v", err)
	}

	_, err = PlanManifest(Manifest{
		Files:    []string{"missing"},
		Mappings: []Mapping{{Source: "a", Target: "z"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a current file") {
		t.Fatalf("expected missing source error, got %v", err)
	}

	_, err = PlanManifest(Manifest{
		Files: []string{"a"},
		Mappings: []Mapping{
			{Source: "a", Target: "x"},
			{Source: "a", Target: "y"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "repeats source") {
		t.Fatalf("expected repeated source error, got %v", err)
	}
}

func TestTemporaryNameAvoidsInputsAndTargets(t *testing.T) {
	files := []string{"a", "b", tempPrefix + "1", tempPrefix + "2"}
	manifest := Manifest{
		Files: files,
		Mappings: []Mapping{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "a"},
			{Source: tempPrefix + "1", Target: tempPrefix + "3"},
		},
	}
	plan, err := PlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TemporaryName != tempPrefix+"4" {
		t.Fatalf("got temporary %q, want %s4", plan.TemporaryName, tempPrefix)
	}
	final := replay(t, files, plan)
	assertFinal(t, final, files, map[string]string{
		"a":              "b",
		"b":              "a",
		tempPrefix + "1": tempPrefix + "3",
		tempPrefix + "2": tempPrefix + "2",
	})
	assertRollback(t, final, files, plan)
}

func TestExactSameMappingIsNoop(t *testing.T) {
	files := []string{"A", "b"}
	manifest := Manifest{
		Files:    files,
		Mappings: []Mapping{{Source: "A", Target: "A"}, {Source: "b", Target: "new"}},
	}
	plan, err := PlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("exact same mapping should be no-op, got %d steps", len(plan.Steps))
	}
	final := replay(t, files, plan)
	assertFinal(t, final, files, map[string]string{"A": "A", "b": "new"})
	assertRollback(t, final, files, plan)
}

func TestJSONValidation(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"files":["a"],"mappings":[],"extra":true}`)); err == nil {
		t.Fatal("unknown JSON field was accepted")
	}
	if _, err := ParseManifest([]byte(`{"files":["a"],"mappings":[]} {}`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	if _, err := ParseManifest([]byte(`{"files":[],"mappings":[]}`)); err == nil {
		t.Fatal("empty file list was accepted")
	}

	files := make([]string, 201)
	for i := range files {
		files[i] = fmt.Sprintf("f%d", i)
	}
	data, err := json.Marshal(Manifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(data); err == nil {
		t.Fatal("201-file manifest was accepted")
	}

	files = files[:200]
	data, err = json.Marshal(Manifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("200-file manifest should be accepted: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("empty mappings should produce no steps, got %d", len(plan.Steps))
	}
}

func TestExampleManifest(t *testing.T) {
	data, err := os.ReadFile("../../examples/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	final := replay(t, manifest.Files, plan)
	expected := map[string]string{}
	for _, file := range manifest.Files {
		expected[file] = file
	}
	for _, mapping := range manifest.Mappings {
		expected[mapping.Source] = mapping.Target
	}
	assertFinal(t, final, manifest.Files, expected)
	assertRollback(t, final, manifest.Files, plan)
}
