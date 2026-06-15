// Package results holds API result types returned by the public API layer.
package results

import "mvmctl/pkg/errs"

// InitStepResult holds the result of a single init step.
type InitStepResult struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// InitResult holds the result of the full init wizard.
type InitResult struct {
	Steps            []InitStepResult       `json:"steps"`
	HostReady        bool                   `json:"host_ready"`
	NeedsInteraction *errs.NeedsInteraction `json:"needs_interaction,omitempty"`
}
