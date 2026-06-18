package infra_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// --- Dedup ---
// Rationale: Dedup is used throughout the codebase for cleaning up identifiers,
// SSH key lists, and volume IDs. A bug here would cause duplicate entries to
// appear in listings or be passed to subprocess calls.

func TestDedup_strings(t *testing.T) {
	tests := map[string]struct {
		input []string
		want  []string
	}{
		"nil_slice":              {input: nil, want: nil},
		"empty_slice":            {input: []string{}, want: []string{}},
		"single_element":         {input: []string{"a"}, want: []string{"a"}},
		"no_duplicates":          {input: []string{"a", "b", "c"}, want: []string{"a", "b", "c"}},
		"all_duplicates":         {input: []string{"a", "a", "a"}, want: []string{"a"}},
		"some_duplicates":        {input: []string{"a", "b", "a", "c", "b"}, want: []string{"a", "b", "c"}},
		"consecutive_duplicates": {input: []string{"a", "a", "b", "b", "c"}, want: []string{"a", "b", "c"}},
		"preserves_first_seen":   {input: []string{"b", "a", "b", "a"}, want: []string{"b", "a"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.Dedup(tc.input)
			// nil and empty should be treated identically
			if len(tc.want) == 0 && len(got) == 0 {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Dedup() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDedup_ints(t *testing.T) {
	tests := map[string]struct {
		input []int
		want  []int
	}{
		"no_duplicates": {input: []int{1, 2, 3}, want: []int{1, 2, 3}},
		"all_same":      {input: []int{42, 42, 42}, want: []int{42}},
		"mixed":         {input: []int{1, 2, 1, 3, 2, 3}, want: []int{1, 2, 3}},
		"zero_values":   {input: []int{0, 0, 1, 0}, want: []int{0, 1}},
		"negative":      {input: []int{-1, -2, -1, -3}, want: []int{-1, -2, -3}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.Dedup(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Dedup[int]() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- SortedKeys ---
// Rationale: SortedKeys is used for deterministic iteration over config maps
// and template data. Nondeterministic iteration would cause flaky tests and
// inconsistent output.

func TestSortedKeys(t *testing.T) {
	tests := map[string]struct {
		input map[string]any
		want  []string
	}{
		"nil_map":                    {input: nil, want: []string{}},
		"empty_map":                  {input: map[string]any{}, want: []string{}},
		"single_key":                 {input: map[string]any{"z": 1}, want: []string{"z"}},
		"reverse_alphabetical_input": {input: map[string]any{"c": 3, "b": 2, "a": 1}, want: []string{"a", "b", "c"}},
		"already_sorted":             {input: map[string]any{"a": 1, "b": 2, "c": 3}, want: []string{"a", "b", "c"}},
		"mixed_case":                 {input: map[string]any{"Z": 1, "a": 2, "m": 3}, want: []string{"Z", "a", "m"}},
		"numbers_as_strings": {
			input: map[string]any{"10": "x", "2": "y", "1": "z"},
			want:  []string{"1", "10", "2"},
		},
		"diverse_value_types": {
			input: map[string]any{"a": 1, "b": "two", "c": true},
			want:  []string{"a", "b", "c"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.SortedKeys(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SortedKeys() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- JoinStringsPtrs ---
// Rationale: JoinStringsPtrs formats batch operation error messages for user
// display. A bug would cause silent error swallowing or malformed output.

func TestJoinStringsPtrs(t *testing.T) {
	t.Run("nil_result", func(t *testing.T) {
		got := infra.JoinStringsPtrs(nil)
		assert.Equal(t, "", got)
	})

	t.Run("empty_items", func(t *testing.T) {
		result := &errs.BatchResult{Items: []errs.OperationResult{}}
		got := infra.JoinStringsPtrs(result)
		assert.Equal(t, "", got)
	})

	t.Run("all_empty_messages", func(t *testing.T) {
		result := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Item: "vm-1"},
				{Item: "vm-2"},
			},
		}
		got := infra.JoinStringsPtrs(result)
		assert.Equal(t, "", got)
	})

	t.Run("some_with_messages", func(t *testing.T) {
		result := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Message: "failed to stop", Item: "vm-1"},
				{Item: "vm-2"},
				{Message: "not found", Item: "vm-3"},
			},
		}
		got := infra.JoinStringsPtrs(result)
		want := "failed to stop; not found"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("all_with_messages", func(t *testing.T) {
		result := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Message: "err a", Item: "vm-1"},
				{Message: "err b", Item: "vm-2"},
			},
		}
		got := infra.JoinStringsPtrs(result)
		want := "err a; err b"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("single_item_with_message", func(t *testing.T) {
		result := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Message: "single error", Item: "vm-1"},
			},
		}
		got := infra.JoinStringsPtrs(result)
		assert.Equal(t, "single error", got)
	})
}

// --- IsTrue ---
// Rationale: IsTrue is used for config parsing and flag interpretation.
// Incorrect truthiness detection would cause silent misconfiguration.

func TestIsTrue(t *testing.T) {
	tests := map[string]struct {
		input any
		want  bool
	}{
		// True values
		"bool_true":       {input: true, want: true},
		"string_1":        {input: "1", want: true},
		"string_true":     {input: "true", want: true},
		"string_yes":      {input: "yes", want: true},
		"string_on":       {input: "on", want: true},
		"int_nonzero":     {input: 1, want: true},
		"int_negative":    {input: -1, want: true},
		"int64_nonzero":   {input: int64(99), want: true},
		"float64_nonzero": {input: float64(3.14), want: true},

		// False values
		"bool_false":    {input: false, want: false},
		"string_0":      {input: "0", want: false},
		"string_false":  {input: "false", want: false},
		"string_no":     {input: "no", want: false},
		"string_off":    {input: "off", want: false},
		"string_random": {input: "maybe", want: false},
		"string_empty":  {input: "", want: false},
		"int_zero":      {input: 0, want: false},
		"int64_zero":    {input: int64(0), want: false},
		"float64_zero":  {input: float64(0), want: false},
		"nil":           {input: nil, want: false},
		"slice_value":   {input: []string{"a"}, want: false},
		"struct_value":  {input: struct{}{}, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.IsTrue(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("IsTrue() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
