package results

import "mvmctl/pkg/errs"

// CachePruneFailure preserves one resource family's failure in aggregate pruning.
type CachePruneFailure struct {
	Resource string         `json:"resource"`
	Code     errs.Code      `json:"code"`
	Class    errs.Class     `json:"class"`
	Message  string         `json:"message"`
	Op       string         `json:"op,omitempty"`
	Entity   string         `json:"entity,omitempty"`
	Details  map[string]any `json:"details"`
}
