package errs

import "encoding/json"

// BulkResultItem matches Python's models/bulk.py:BulkResultItem(Generic[T]).
// Represents the result of a single item in a bulk operation.
type BulkResultItem struct {
	Item  any   `json:"item"` // Item is any because BatchResultItem stores results of different types per operation. Concrete typing not feasible — it's a generic container.
	Error error `json:"-"`    // Serialized via MarshalJSON
}

// MarshalJSON implements json.Marshaler for BulkResultItem.
// Python's dataclass serializes error normally (default serialization).
// Go's error type cannot be serialized directly, so we convert it to its
// string form when non-nil, or null when nil.
func (i *BulkResultItem) MarshalJSON() ([]byte, error) {
	var errorStr *string
	if i.Error != nil {
		s := i.Error.Error()
		errorStr = &s
	}
	type Alias BulkResultItem
	return json.Marshal(&struct {
		Error *string `json:"error"`
		*Alias
	}{
		Error: errorStr,
		Alias: (*Alias)(i),
	})
}

// BulkResult matches Python's models/bulk.py:BulkResult(Generic[T]).
// Aggregated results of a bulk operation with BulkResultItem items.
type BulkResult struct {
	Items []BulkResultItem `json:"items"`
}
