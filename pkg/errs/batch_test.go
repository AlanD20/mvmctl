package errs_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

// ─── BulkResultItem.MarshalJSON ────────────────────────────────────────────
// Rationale: BulkResultItem is serialized in bulk operation responses.
// An incorrectly serialised error field breaks the client's ability to
// distinguish between success and failure per item.

func TestBulkResultItem_MarshalJSON(t *testing.T) {
	t.Run("error_serialized_as_null_when_nil", func(t *testing.T) {
		item := &errs.BulkResultItem{
			Item: "vm-1",
		}
		data, err := json.Marshal(item)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Nil(t, result["error"])
		assert.Equal(t, "vm-1", result["item"])
	})

	t.Run("error_serialized_as_string_when_non_nil", func(t *testing.T) {
		item := &errs.BulkResultItem{
			Item:  "vm-2",
			Error: errors.New("creation failed"),
		}
		data, err := json.Marshal(item)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Equal(t, "creation failed", result["error"])
		assert.Equal(t, "vm-2", result["item"])
	})

	t.Run("item_field_preserved", func(t *testing.T) {
		item := &errs.BulkResultItem{
			Item: map[string]any{"id": "vm-3", "name": "test"},
		}
		data, err := json.Marshal(item)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		itemMap, ok := result["item"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "vm-3", itemMap["id"])
		assert.Equal(t, "test", itemMap["name"])
	})
}
