package host

import "mvmctl/internal/infra/errs"

// PrivilegeDetails carries structured metadata about a privilege failure,
// matching Python's PrivilegeError rich `details` dict.
type PrivilegeDetails struct {
	Message             string   `json:"message"`
	MissingCapabilities []string `json:"missing_capabilities"`
	MissingBinaries     []string `json:"missing_binaries,omitempty"`
	Suggestions         []string `json:"suggestions,omitempty"`
}

// ToMap converts PrivilegeDetails to the flat map[string]any used by
// DomainError.Details, matching Python's PrivilegeError.details dict.
// Python always includes missing_capabilities: [] even when empty.
func (pd *PrivilegeDetails) ToMap() map[string]any {
	m := map[string]any{
		"message":              pd.Message,
		"missing_capabilities": pd.MissingCapabilities,
	}
	if len(pd.MissingCapabilities) == 0 {
		m["missing_capabilities"] = []string{}
	}
	if len(pd.MissingBinaries) > 0 {
		m["missing_binaries"] = pd.MissingBinaries
	}
	if len(pd.Suggestions) > 0 {
		m["suggestions"] = pd.Suggestions
	}
	return m
}

// hostError creates a DomainError matching Python's HostError(message).
// The code parameter selects the appropriate error code for the context.
// Python's HostError does not carry codes; this mapping assigns semantic
// codes for the Go error system.
func hostError(code errs.Code, msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    code,
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// privilegeError creates a PrivilegeError matching Python's PrivilegeError
// with structured details dict.
//
// The variadic details argument can be either a map[string]any (legacy style)
// or a *PrivilegeDetails (preferred). If neither is provided, an empty
// details dict is used.
func privilegeError(msg string, details ...any) *errs.DomainError {
	err := &errs.DomainError{
		Code:    errs.CodePrivilegeRequired,
		Op:      "host",
		Message: msg,
		Class:   errs.ClassNeedsInteraction,
	}
	if len(details) > 0 {
		switch d := details[0].(type) {
		case *PrivilegeDetails:
			err.Details = d.ToMap()
		case map[string]any:
			err.Details = d
		default:
			err.Details = map[string]any{"message": msg, "missing_capabilities": []string{}}
		}
	} else {
		err.Details = map[string]any{"message": msg, "missing_capabilities": []string{}}
	}
	return err
}

// NewPrivilegeError creates a privilege error with structured PrivilegeDetails.
// Convenience wrapper around privilegeError.
func NewPrivilegeError(msg string, details *PrivilegeDetails) *errs.DomainError {
	return privilegeError(msg, details)
}
