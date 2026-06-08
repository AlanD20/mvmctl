package ptr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/ptr"
)

// ─── StrNonEmpty ─────────────────────────────────────────────────────────────
// Rationale: StrNonEmpty converts empty strings to nil (matching Python's None
// for empty strings). A nil vs empty distinction affects JSON serialization
// and database writes — wrong behavior would silently misrepresent data.

func TestStrNonEmpty(t *testing.T) {
	t.Run("non_empty_returns_pointer", func(t *testing.T) {
		got := ptr.StrNonEmpty("hello")
		require.NotNil(t, got)
		assert.Equal(t, "hello", *got)
	})

	t.Run("empty_returns_nil", func(t *testing.T) {
		got := ptr.StrNonEmpty("")
		assert.Nil(t, got)
	})

	t.Run("whitespace_is_non_empty", func(t *testing.T) {
		got := ptr.StrNonEmpty("   ")
		require.NotNil(t, got)
		assert.Equal(t, "   ", *got)
	})
}

// ─── SafeDeref ───────────────────────────────────────────────────────────────
// Rationale: SafeDeref replaces nil pointer checks with a helper. Must return
// zero value for nil and the dereferenced value for non-nil.

func TestSafeDeref(t *testing.T) {
	t.Run("non_nil_returns_value", func(t *testing.T) {
		s := "hello"
		got := ptr.SafeDeref(&s)
		assert.Equal(t, "hello", got)
	})

	t.Run("nil_returns_empty", func(t *testing.T) {
		got := ptr.SafeDeref(nil)
		assert.Equal(t, "", got)
	})

	t.Run("empty_string_not_nil", func(t *testing.T) {
		s := ""
		got := ptr.SafeDeref(&s)
		assert.Equal(t, "", got)
	})
}

// ─── SafeDerefInt ────────────────────────────────────────────────────────────
// Rationale: Same as SafeDeref but for int. Must distinguish nil from zero.

func TestSafeDerefInt(t *testing.T) {
	t.Run("non_nil_returns_value", func(t *testing.T) {
		n := 42
		got := ptr.SafeDerefInt(&n)
		assert.Equal(t, 42, got)
	})

	t.Run("nil_returns_zero", func(t *testing.T) {
		got := ptr.SafeDerefInt(nil)
		assert.Equal(t, 0, got)
	})

	t.Run("zero_value_not_nil", func(t *testing.T) {
		n := 0
		got := ptr.SafeDerefInt(&n)
		assert.Equal(t, 0, got)
	})

	t.Run("negative_values_preserved", func(t *testing.T) {
		n := -1
		got := ptr.SafeDerefInt(&n)
		assert.Equal(t, -1, got)
	})
}

// ─── Ptr ─────────────────────────────────────────────────────────────────────
// Rationale: Ptr returns a pointer to any value. Used for creating pointer
// literals (Go doesn't allow &42 or &true directly). Must always return a
// non-nil pointer to the exact value.

func TestPtr(t *testing.T) {
	t.Run("int_literal", func(t *testing.T) {
		got := ptr.Ptr(42)
		require.NotNil(t, got)
		assert.Equal(t, 42, *got)
	})

	t.Run("bool_true", func(t *testing.T) {
		got := ptr.Ptr(true)
		require.NotNil(t, got)
		assert.Equal(t, true, *got)
	})

	t.Run("bool_false", func(t *testing.T) {
		got := ptr.Ptr(false)
		require.NotNil(t, got)
		assert.Equal(t, false, *got)
	})

	t.Run("string_value", func(t *testing.T) {
		got := ptr.Ptr("hello")
		require.NotNil(t, got)
		assert.Equal(t, "hello", *got)
	})

	t.Run("struct_value", func(t *testing.T) {
		type s struct{ X int }
		got := ptr.Ptr(s{X: 99})
		require.NotNil(t, got)
		assert.Equal(t, 99, got.X)
	})

	t.Run("pointer_to_pointer", func(t *testing.T) {
		inner := 42
		got := ptr.Ptr(&inner)
		require.NotNil(t, got)
		assert.Equal(t, 42, **got)
	})

	t.Run("modifying_pointer_does_not_affect_original", func(t *testing.T) {
		// Ptr creates a copy
		p := ptr.Ptr(42)
		*p = 99
		// Verify a new call still gives 42
		q := ptr.Ptr(42)
		assert.Equal(t, 42, *q)
	})

	// Boundary / edge cases
	t.Run("zero_int", func(t *testing.T) {
		got := ptr.Ptr(0)
		require.NotNil(t, got)
		assert.Equal(t, 0, *got)
	})

	t.Run("empty_string", func(t *testing.T) {
		got := ptr.Ptr("")
		require.NotNil(t, got)
		assert.Equal(t, "", *got)
	})

	t.Run("nil_interface", func(t *testing.T) {
		var v any = nil
		got := ptr.Ptr(v)
		require.NotNil(t, got)
		assert.Nil(t, *got)
	})

	t.Run("nil_pointer_type", func(t *testing.T) {
		var s *string = nil
		got := ptr.Ptr(s)
		require.NotNil(t, got)
		assert.Nil(t, *got)
	})
}
