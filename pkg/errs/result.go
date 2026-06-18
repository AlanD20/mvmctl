package errs
import "encoding/json"
// --- OperationStatus typed constants ---
type OperationStatus string
const (
	StatusSuccess OperationStatus = "success"
	StatusSkipped OperationStatus = "skipped"
	StatusWarning OperationStatus = "warning"
	StatusError   OperationStatus = "error"
	StatusFailure OperationStatus = "failure"
)
// OperationResult represents the result of a domain operation.
// Uses any for the Item field — each operation stores a different result type.
type OperationResult struct {
	Status    string         `json:"status"`         // "success", "error", "failure", "warning", "skipped"
	Code      string         `json:"code"`           // e.g. "vm.created", "vm.not_found"
	Message   string         `json:"message"`        // Human-readable message
	Item      any            `json:"item,omitempty"` // Item is any because OperationResult is a generic container. Concrete typing not feasible since each domain operation sets a different type.
	Exception error          `json:"-"`              // Optional exception — serialized via MarshalJSON
	Metadata  map[string]any `json:"-"`              // Structured extra data — serialized via MarshalJSON
	Warnings  []string       `json:"-"`              // Non-fatal warnings — serialized via MarshalJSON
}
// MarshalJSON implements json.Marshaler for OperationResult.
// The struct serializes all fields normally (exception included). Go's
// error type cannot be serialized directly, so we convert Exception to its
// string form. Metadata and Warnings are initialized to non-nil to match expected serialization.
func (r *OperationResult) MarshalJSON() ([]byte, error) {
	// Initialize Metadata to non-nil
	metadata := r.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	// Initialize Warnings to non-nil for consumers that range over the slice.
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	// Serialize Exception as string or null
	var exceptionStr *string
	if r.Exception != nil {
		s := r.Exception.Error()
		exceptionStr = &s
	}
	type Alias OperationResult // avoid infinite recursion
	return json.Marshal(&struct {
		Exception *string        `json:"exception"`
		Metadata  map[string]any `json:"metadata"`
		Warnings  []string       `json:"warnings"`
		*Alias
	}{
		Exception: exceptionStr,
		Metadata:  metadata,
		Warnings:  warnings,
		Alias:     (*Alias)(r),
	})
}
// IsOK returns true if the operation completed without error.
func (r *OperationResult) IsOK() bool {
	return r.Status == string(StatusSuccess) || r.Status == string(StatusSkipped) || r.Status == string(StatusWarning)
}
// IsError returns true if the operation failed.
func (r *OperationResult) IsError() bool {
	return r.Status == string(StatusError) || r.Status == string(StatusFailure)
}
// ToError converts an error-status OperationResult to a DomainError.
// Returns nil if the result is not an error status.
// This replaces the pattern of unwrapping result.Message into fmt.Errorf,
// which loses the DomainError type and makes errors appear "unexpected".
func (r *OperationResult) ToError() *DomainError {
	if r == nil || !r.IsError() {
		return nil
	}
	return &DomainError{
		Code:    Code(r.Code),
		Message: r.Message,
		Op:      opForCode(Code(r.Code)),
		Class:   classForCode(Code(r.Code)),
		Err:     r.Exception,
	}
}
// NeedsInteraction specifies needs interaction.
// Returned instead of OperationResult when the API cannot proceed without user input.
// This is NOT an exception — it is normal control flow.
// Implements the error interface so it can flow through (T, error) return types.
type NeedsInteraction struct {
	Code      string         `json:"code"`              // Machine-readable reason code
	Message   string         `json:"message"`           // Human-readable prompt
	InputType string         `json:"input_type"`        // "sudo", "confirm", "choice", "input"
	Context   map[string]any `json:"context,omitempty"` // Structured context
}
func (n *NeedsInteraction) Error() string { return n.Message }
// --- BatchResult ---
// BatchResult aggregates multiple BulkResultItems into a single response.
// Aggregated results of a batch operation with OperationResult items.
type BatchResult struct {
	Items    []OperationResult `json:"items"`
	Warnings []string          `json:"warnings,omitempty"`
	Metadata map[string]any    `json:"metadata,omitempty"`
}
// Errors returns all failed items (status == "error" or "failure").
func (br *BatchResult) Errors() []OperationResult {
	var result []OperationResult
	for _, item := range br.Items {
		if item.Status == "error" || item.Status == "failure" {
			result = append(result, item)
		}
	}
	return result
}
// HasErrors returns true if any item has an error/failure status.
func (br *BatchResult) HasErrors() bool {
	for _, item := range br.Items {
		if item.Status == "error" || item.Status == "failure" {
			return true
		}
	}
	return false
}
