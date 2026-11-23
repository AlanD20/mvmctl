package system

import "mvmctl/internal/lib/authorityfs"

// PriorBootVMAbsence is issued only by a concrete retained TopologyHold after
// root-record, fixed-cgroup absence, canonical-lock, and stable-boot admission.
type PriorBootVMAbsence = authorityfs.PriorBootVMAbsence
