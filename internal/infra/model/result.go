package model

// ── OpStatus ──

// OpStatus is a typed constant for operation result status.
// Named OpStatus (not OperationStatus) to avoid collision with VM Status type.
type OpStatus string

const (
	OpStatusSuccess OpStatus = "success"
	OpStatusSkipped OpStatus = "skipped"
	OpStatusWarning OpStatus = "warning"
	OpStatusError   OpStatus = "error"
	OpStatusFailure OpStatus = "failure"
)

// ── OperationResult ──

// OperationResult matches Python's OperationResult.
type OperationResult struct {
	Status    string         `json:"status"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Item      any            `json:"item,omitempty"` // Item is any because OperationResult is a generic container used across all domain operations (VM, Network, Image, etc.). Concrete typing not feasible — each operation type would need its own result type.
	Exception error          `json:"-"`              // Serialized via MarshalJSON
	Metadata  map[string]any `json:"-"`              // Arbitrary per-operation metadata; schema varies by operation type
	Warnings  []string       `json:"-"`              // Serialized via MarshalJSON
}

// ── NeedsInteraction ──

// NeedsInteraction matches Python's NeedsInteraction.
// Returned instead of OperationResult when the API cannot proceed without user input.
type NeedsInteraction struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	InputType string         `json:"input_type"`
	Context   map[string]any `json:"context,omitempty"` // Optional context from external systems; structure not under our control
}
