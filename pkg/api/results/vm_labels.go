package results

import "mvmctl/internal/lib/model"

type VMLabels struct {
	VMID     string         `json:"vm_id"`
	Revision uint64         `json:"revision"`
	Labels   model.VMLabels `json:"labels"`
}
