package model

// --- OpStatus ---

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
