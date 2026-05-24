package errs

import "encoding/json"

// ── OperationStatus typed constants ──
// Python: OperationStatus = Literal["success", "skipped", "warning", "error", "failure"]

type OperationStatus string

const (
	StatusSuccess OperationStatus = "success"
	StatusSkipped OperationStatus = "skipped"
	StatusWarning OperationStatus = "warning"
	StatusError   OperationStatus = "error"
	StatusFailure OperationStatus = "failure"
)

// ProgressEvent matches Python's models/result.py ProgressEvent(phase, status, percent, message).
type ProgressEvent struct {
	Phase   string   `json:"phase"`
	Status  string   `json:"status"`
	Percent *float64 `json:"percent,omitempty"`
	Message string   `json:"message"`
}

// OperationResult matches Python's OperationResult(status, code, message, item, exception,
// metadata, warnings). T is generic in Python but we use any for the Item field in Go.
type OperationResult struct {
	Status    string         `json:"status"`              // "success", "error", "failure", "warning", "skipped"
	Code      string         `json:"code"`                // e.g. "vm.created", "vm.not_found"
	Message   string         `json:"message"`             // Human-readable message
	Item      any            `json:"item,omitempty"`       // Item is any because OperationResult is a generic container. Concrete typing not feasible since each domain operation sets a different type.
	Exception error          `json:"-"`                    // Optional exception — serialized via MarshalJSON
	Metadata  map[string]any `json:"-"`                    // Structured extra data — serialized via MarshalJSON
	Warnings  []string       `json:"-"`                    // Non-fatal warnings — serialized via MarshalJSON
}

// MarshalJSON implements json.Marshaler for OperationResult.
// Python's dataclass serializes all fields normally (exception included). Go's
// error type cannot be serialized directly, so we convert Exception to its
// string form. Metadata and Warnings are initialized to non-nil (matching
// Python's field(default_factory=dict/list) behavior).
func (r *OperationResult) MarshalJSON() ([]byte, error) {
	// Initialize Metadata to non-nil (matches Python's default_factory=dict)
	metadata := r.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	// Initialize Warnings to non-nil (matches Python's default_factory=list for BatchResult,
	// and provides safety for consumers that range over the slice)
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
		Exception *string         `json:"exception"`
		Metadata  map[string]any  `json:"metadata"`
		Warnings  []string        `json:"warnings"`
		*Alias
	}{
		Exception: exceptionStr,
		Metadata:  metadata,
		Warnings:  warnings,
		Alias:     (*Alias)(r),
	})
}

// Init ensures Metadata and Warnings are non-nil, matching Python's
// field(default_factory=dict/list) semantics. Safe to call multiple times.
func (r *OperationResult) Init() {
	if r.Metadata == nil {
		r.Metadata = make(map[string]any)
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
}

// IsOK returns true if the operation completed without error.
// Python: @property def is_ok(self) -> bool: return self.status in ("success", "skipped", "warning")
func (r *OperationResult) IsOK() bool {
	return r.Status == string(StatusSuccess) || r.Status == string(StatusSkipped) || r.Status == string(StatusWarning)
}

// IsError returns true if the operation failed.
// Python: @property def is_error(self) -> bool: return self.status in ("error", "failure")
func (r *OperationResult) IsError() bool {
	return r.Status == string(StatusError) || r.Status == string(StatusFailure)
}

// NeedsInteraction matches Python's NeedsInteraction.
// Returned instead of OperationResult when the API cannot proceed without user input.
// This is NOT an exception — it is normal control flow.
type NeedsInteraction struct {
	Code      string         `json:"code"`                // Machine-readable reason code
	Message   string         `json:"message"`             // Human-readable prompt
	InputType string         `json:"input_type"`          // "sudo", "confirm", "choice", "input"
	Context   map[string]any `json:"context,omitempty"`   // Structured context
}

// ── BatchResult (Python-matching) ──

// BatchResult matches the spec section 5 BatchResult.
// Aggregated results of a batch operation with OperationResult items.
type BatchResult struct {
	Items    []OperationResult `json:"items"`
	Warnings []string          `json:"warnings,omitempty"`
	Metadata map[string]any    `json:"metadata,omitempty"`
}

// Init ensures Warnings and Metadata are non-nil, matching Python's
// field(default_factory=list/dict) semantics. Safe to call multiple times.
func (br *BatchResult) Init() {
	if br.Metadata == nil {
		br.Metadata = make(map[string]any)
	}
	if br.Warnings == nil {
		br.Warnings = []string{}
	}
}

// StatusSummary returns a count of each status across all items.
// Python: @property def status_summary(self) -> dict[str, int]:
func (br *BatchResult) StatusSummary() map[string]int {
	counts := make(map[string]int)
	for _, r := range br.Items {
		counts[r.Status] = counts[r.Status] + 1
	}
	return counts
}

// Successes returns all successful items (status == "success").
// Python: @property def successes(self) -> list[OperationResult[T]]:
func (br *BatchResult) Successes() []OperationResult {
	var result []OperationResult
	for _, item := range br.Items {
		if item.Status == "success" {
			result = append(result, item)
		}
	}
	return result
}

// Skipped returns all skipped items (status == "skipped").
// Python: @property def skipped(self) -> list[OperationResult[T]]:
func (br *BatchResult) Skipped() []OperationResult {
	var result []OperationResult
	for _, item := range br.Items {
		if item.Status == "skipped" {
			result = append(result, item)
		}
	}
	return result
}

// Errors returns all failed items (status == "error" or "failure").
// Python: @property def errors(self) -> list[OperationResult[T]]:
func (br *BatchResult) Errors() []OperationResult {
	var result []OperationResult
	for _, item := range br.Items {
		if item.Status == "error" || item.Status == "failure" {
			result = append(result, item)
		}
	}
	return result
}

// HasAnyError returns true if any item has an error/failure status.
// Python: @property def has_any_error(self) -> bool:
func (br *BatchResult) HasAnyError() bool {
	for _, item := range br.Items {
		if item.Status == "error" || item.Status == "failure" {
			return true
		}
	}
	return false
}

// HasErrors returns true if any item has an error/failure status.
// Alias for HasAnyError. Prefer HasAnyError() for Python compatibility.
func (br *BatchResult) HasErrors() bool {
	return br.HasAnyError()
}

// AllOK returns true if all items have an OK status (success, skipped, or warning).
// Python: @property def all_ok(self) -> bool:
func (br *BatchResult) AllOK() bool {
	for _, item := range br.Items {
		if !item.IsOK() {
			return false
		}
	}
	return true
}
