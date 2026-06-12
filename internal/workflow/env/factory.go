package env

import (
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
)

// StepFactory holds factory functions for creating a step from a YAML spec
// entry or from a previously persisted state.
type StepFactory struct {
	// StepType is the singular step type identifier (e.g. "network", "vm").
	// This is returned by step.Type().
	StepType string

	// FromSpec creates a Step from a YAML spec entry.
	// stepType is singular (e.g. "network"), name is the resource name.
	FromSpec func(stepType, name string, spec model.ResourceSpec, op *api.Operation) (workflow.Step, error)

	// FromState creates a Step from a previously saved state entry.
	// stepType is singular (e.g. "network"), name is the resource name.
	// deps contains the dependencies saved in the workflow state file
	// (the SavedResource.Dependencies field), which steps can use to
	// reconstruct their DAG edges during destroy.
	FromState func(stepType, name string, saved model.ResourceSpec, deps []string, op *api.Operation) (workflow.Step, error)
}

// Registry maps YAML step type keys (plural resource names, e.g. "networks")
// to factory functions. The key matches the YAML key in an env spec file
// directly, so UnmarshalYAML can look it up without any mapping.
// Adding a new step type means adding a step_*.go file and one entry here.
var Registry = map[string]StepFactory{
	"networks": {
		StepType:  "network",
		FromSpec:  newNetworkStepFromSpec,
		FromState: newNetworkStepFromState,
	},
	"keys": {
		StepType:  "key",
		FromSpec:  newKeyStepFromSpec,
		FromState: newKeyStepFromState,
	},
	"images": {
		StepType:  "image",
		FromSpec:  newImageStepFromSpec,
		FromState: newImageStepFromState,
	},
	"kernels": {
		StepType:  "kernel",
		FromSpec:  newKernelStepFromSpec,
		FromState: newKernelStepFromState,
	},
	"binaries": {
		StepType:  "binary",
		FromSpec:  newBinaryStepFromSpec,
		FromState: newBinaryStepFromState,
	},
	"vms": {
		StepType:  "vm",
		FromSpec:  newVMStepFromSpec,
		FromState: newVMStepFromState,
	},
	"ssh": {
		StepType:  "ssh",
		FromSpec:  newSSHStepFromSpec,
		FromState: newSSHStepFromState,
	},
	"copy": {
		StepType:  "copy",
		FromSpec:  newCopyStepFromSpec,
		FromState: newCopyStepFromState,
	},
}
