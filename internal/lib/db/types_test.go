package db_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
)

// --- StringSlice.Scan ---
// Rationale: Implements sql.Scanner for reading JSON arrays from TEXT columns.

func TestStringSlice_Scan(t *testing.T) {
	t.Run("nil_src_nils_slice", func(t *testing.T) {
		var s db.StringSlice
		err := s.Scan(nil)
		require.NoError(t, err)
		assert.Nil(t, s)
	})

	t.Run("json_array_bytes", func(t *testing.T) {
		var s db.StringSlice
		err := s.Scan([]byte(`["a","b"]`))
		require.NoError(t, err)
		assert.Empty(t, cmp.Diff(db.StringSlice{"a", "b"}, s))
	})

	t.Run("json_array_string", func(t *testing.T) {
		var s db.StringSlice
		err := s.Scan(`["a","b"]`)
		require.NoError(t, err)
		assert.Empty(t, cmp.Diff(db.StringSlice{"a", "b"}, s))
	})

	t.Run("empty_string_returns_empty_slice", func(t *testing.T) {
		var s db.StringSlice
		err := s.Scan("")
		require.NoError(t, err)
		assert.Empty(t, s)
	})

	t.Run("unsupported_type_returns_error", func(t *testing.T) {
		var s db.StringSlice
		err := s.Scan(42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported scan type")
	})
}

// --- StringSlice.Value ---
// Rationale: Implements driver.Valuer for writing StringSlice as JSON array TEXT.

func TestStringSlice_Value(t *testing.T) {
	t.Run("nil_slice_returns_nil", func(t *testing.T) {
		var s db.StringSlice
		v, err := s.Value()
		require.NoError(t, err)
		assert.Nil(t, v)
	})

	t.Run("empty_slice_returns_json_array", func(t *testing.T) {
		s := db.StringSlice{}
		v, err := s.Value()
		require.NoError(t, err)
		assert.Equal(t, "[]", string(v.([]byte)))
	})

	t.Run("populated_slice_returns_json", func(t *testing.T) {
		s := db.StringSlice{"a", "b"}
		v, err := s.Value()
		require.NoError(t, err)
		assert.Equal(t, `["a","b"]`, string(v.([]byte)))
	})
}
