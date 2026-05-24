package model

// ── BulkResultItem ──

// BulkResultItem matches Python's BulkResultItem.
type BulkResultItem struct {
	Item  any   `json:"item"`  // Item holds the result value — type varies per operation (VM, Network, etc.). Concrete typing not possible because this is a generic container used across all domain operations.
	Error error `json:"-"`     // Serialized via MarshalJSON
}

// ── BulkResult ──

// BulkResult matches Python's BulkResult.
type BulkResult struct {
	Items []BulkResultItem `json:"items"`
}
