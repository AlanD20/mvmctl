package errs

import "encoding/json"

// BulkResultItem matches Python's models/bulk.py:BulkResultItem(Generic[T]).
// Represents the result of a single item in a bulk operation.
type BulkResultItem struct {
	Item  any    `json:"item"`  // Item is any because BatchResultItem stores results of different types per operation. Concrete typing not feasible — it's a generic container.
	Error error  `json:"-"` // Serialized via MarshalJSON
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

// Success returns true if the item completed without error.
// Python: @property def success(self) -> bool: return self.error is None
func (i *BulkResultItem) Success() bool {
	return i.Error == nil
}

// BulkResult matches Python's models/bulk.py:BulkResult(Generic[T]).
// Aggregated results of a bulk operation with BulkResultItem items.
type BulkResult struct {
	Items []BulkResultItem `json:"items"`
}

// Successes returns all successfully completed items.
// Python: @property def successes(self) -> list[T]: return [i.item for i in self.items if i.success]
func (br *BulkResult) Successes() []any {
	var result []any
	for _, i := range br.Items {
		if i.Success() {
			result = append(result, i.Item)
		}
	}
	return result
}

// Failures returns all failed items as (item, error) pairs.
// Python: @property def failures(self) -> list[tuple[T, Exception]]:
func (br *BulkResult) Failures() []struct{ Item any; Error error } {
	var result []struct{ Item any; Error error }
	for _, i := range br.Items {
		if i.Error != nil {
			result = append(result, struct{ Item any; Error error }{i.Item, i.Error})
		}
	}
	return result
}

// HasErrors returns true if any item has an error.
// Python: @property def has_errors(self) -> bool:
func (br *BulkResult) HasErrors() bool {
	for _, i := range br.Items {
		if i.Error != nil {
			return true
		}
	}
	return false
}

// SuccessCount returns the count of successful items.
// Python: @property def success_count(self) -> int:
func (br *BulkResult) SuccessCount() int {
	count := 0
	for _, i := range br.Items {
		if i.Success() {
			count++
		}
	}
	return count
}

// FailureCount returns the count of failed items.
// Python: @property def failure_count(self) -> int:
func (br *BulkResult) FailureCount() int {
	count := 0
	for _, i := range br.Items {
		if i.Error != nil {
			count++
		}
	}
	return count
}

// Total returns the total number of items.
// Python: @property def total(self) -> int:
func (br *BulkResult) Total() int {
	return len(br.Items)
}
