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
	FromSpec func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error)

	// FromState creates a Step from a previously saved state entry.
	// stepType is singular (e.g. "network"), name is the resource name.
	// deps contains the dependencies saved in the workflow state file
	// (the AppliedResource.Dependencies field), which steps can use to
	// reconstruct their DAG edges during destroy.
	FromState func(stepType, name string, saved model.ResourceState, deps []string, op api.API) (workflow.Step, error)
}

// Registry maps singular step type keys (e.g. "network", "vm") to factory
// functions. The key matches the YAML key in an env spec file directly, so
// UnmarshalYAML can look it up without any mapping. The key also equals
// StepType, so callers can do a direct Registry[stepType] lookup instead
// of scanning. Adding a new step type means adding a step_*.go file and
// one entry here.
var Registry = map[string]StepFactory{
	"network": {
		StepType:  "network",
		FromSpec:  newNetworkStepFromSpec,
		FromState: newNetworkStepFromState,
	},
	"key": {
		StepType:  "key",
		FromSpec:  newKeyStepFromSpec,
		FromState: newKeyStepFromState,
	},
	"image": {
		StepType:  "image",
		FromSpec:  newImageStepFromSpec,
		FromState: newImageStepFromState,
	},
	"kernel": {
		StepType:  "kernel",
		FromSpec:  newKernelStepFromSpec,
		FromState: newKernelStepFromState,
	},
	"binary": {
		StepType:  "binary",
		FromSpec:  newBinaryStepFromSpec,
		FromState: newBinaryStepFromState,
	},
	"vm": {
		StepType:  "vm",
		FromSpec:  newVMStepFromSpec,
		FromState: newVMStepFromState,
	},
	"ssh": {
		StepType:  "ssh",
		FromSpec:  newSSHStepFromSpec,
		FromState: newSSHStepFromState,
	},
	"exec": {
		StepType:  "exec",
		FromSpec:  newExecStepFromSpec,
		FromState: newExecStepFromState,
	},
	"copy": {
		StepType:  "copy",
		FromSpec:  newCopyStepFromSpec,
		FromState: newCopyStepFromState,
	},
}
