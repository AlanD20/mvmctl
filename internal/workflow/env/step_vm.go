package env

import (
	"context"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// VMState is the persisted state for a VM step.
type VMState struct {
	VMID        string `yaml:"vm_id"`
	VMDir       string `yaml:"vm_dir"` // Path to the rootfs image file (not a directory)
	NocloudPort int    `yaml:"nocloud_port,omitempty"`
	TapName     string `yaml:"tap_name,omitempty"`
}

// VMStep implements workflow.Step for creating VMs via the API layer.
type VMStep struct {
	stepType string
	name     string
	deps     []string
	specHash string
	input    inputs.VMCreateInput
	op       api.VMAPI
	saved    *VMState
	meta     model.ResourceMeta
}

func (s *VMStep) Type() string { return s.stepType }

func (s *VMStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *VMStep) Dependencies() []string {
	if len(s.deps) == 0 {
		return nil
	}
	deps := make([]string, len(s.deps))
	copy(deps, s.deps)
	return deps
}

func (s *VMStep) SpecHash() string { return s.specHash }

func (s *VMStep) Apply(
	ctx context.Context,
	state *workflow.SharedState,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// Pass raw YAML values directly to the API input. The API resolvers
	// (resolveNetwork, resolveImage, resolveKernel, resolveBinary,
	// resolveSSHKeys) handle looking up resources by identifier (name or
	// ID). If a field is unset (e.g. binary not specified), the API falls
	// back to the default — no need to resolve or inject anything here.

	// Check if VM already exists — skip creation if so.
	// Preserve WasCreated from previous state if this is a re-apply.
	wasCreated := saved.Meta.WasCreated

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "checking if exists"})
	existing, err := s.op.VMGet(ctx, inputs.VMInput{Identifiers: []string{s.name}})
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check vm %q: %v", s.name, err),
			err,
		)
	}
	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &VMState{
			VMID:  existing.ID,
			VMDir: existing.RootfsPath,
		}
		s.meta = model.ResourceMeta{
			WasCreated: wasCreated,
			SpecHash:   s.specHash,
		}
		state.Set(s.Name(), s.saved)
		if err := write(ctx, s.StateData()); err != nil {
			return fmt.Errorf("persist step state after skip: %w", err)
		}
		return nil
	}

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "creating vm"})
	// Wrap onProgress to inject step name into API-level progress events.
	stepProgress := func(e event.Progress) { e.Phase = s.Name(); onProgress(e) }
	vms, err := s.op.VMCreate(ctx, s.input, stepProgress)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		return errs.New(errs.CodeInternal, fmt.Sprintf("vm create returned zero VMs for %q", s.input.Name))
	}

	vmInstance := vms[0]
	nocloudPort := 0
	if vmInstance.NocloudNetPort != nil {
		nocloudPort = *vmInstance.NocloudNetPort
	}
	s.saved = &VMState{
		VMID:        vmInstance.ID,
		VMDir:       vmInstance.RootfsPath,
		NocloudPort: nocloudPort,
		TapName:     vmInstance.TapDevice,
	}
	s.meta = model.ResourceMeta{
		WasCreated: true,
		SpecHash:   s.specHash,
	}
	state.Set(s.Name(), s.saved)
	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state: %w", err)
	}
	return nil
}

func (s *VMStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[VMState](saved.Spec)
		s.meta = saved.Meta
	}

	if s.saved == nil || !s.meta.WasCreated {
		if err := write(ctx, s.StateData()); err != nil {
			return fmt.Errorf("persist step state after destroy skip: %w", err)
		}
		return nil
	}

	result := s.op.VMRemove(ctx, inputs.VMInput{
		Identifiers: []string{s.saved.VMID},
		Force:       true,
	})
	if result.HasErrors() {
		for _, r := range result.Errors() {
			if r.ToError() != nil {
				return r.ToError()
			}
		}
	}

	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *VMStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

func newVMStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceMap,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var input inputs.VMCreateInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	input.Name = name

	// spec uses "key" as a single string but VMCreateInput stores SSHKeys as []string
	if key := spec.GetString("key"); key != "" && len(input.SSHKeys) == 0 {
		input.SSHKeys = []string{key}
	}

	// YAML omitempty produces empty string pointers instead of nil when a key
	// is present with an empty value. Convert them to nil for consistent handling.
	for _, p := range []**string{&input.NetworkID, &input.ImageID, &input.KernelID, &input.BinaryID} {
		if *p != nil && **p == "" {
			*p = nil
		}
	}

	return &VMStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

// NewVMStep creates a VMStep with the given API interface for testing.
// It bypasses the nil-op check in newVMStepFromSpec/newVMStepFromState.
// Only for use in tests.
func NewVMStep(op api.VMAPI, name string, input inputs.VMCreateInput) *VMStep {
	return &VMStep{
		op:       op,
		name:     name,
		stepType: "vm",
		input:    input,
		specHash: crypto.SHA256([]byte(name)),
	}
}

func newVMStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	vs := StateFromMap[VMState](saved.Spec)
	return &VMStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		input: inputs.VMCreateInput{
			Name: name,
		},
		op:    op,
		saved: vs,
		meta:  saved.Meta,
	}, nil
}
