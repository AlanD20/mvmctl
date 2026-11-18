package env

import (
	"context"
	"errors"
	"strings"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// FloatingIPGrantState retains the exact intent before granting and the root receipt afterwards.
type FloatingIPGrantState struct {
	GrantID    string                          `yaml:"grant_id"`
	NetworkID  string                          `yaml:"network_id"`
	ResourceID string                          `yaml:"resource_id"`
	VMID       string                          `yaml:"vm_id"`
	Method     string                          `yaml:"method"`
	Lease      string                          `yaml:"lease"`
	Receipt    *model.FloatingIPDelegationItem `yaml:"receipt,omitempty"`
}

type floatingIPGrantDeclaration struct {
	Network  string `yaml:"network"`
	Resource string `yaml:"resource"`
	VM       string `yaml:"vm"`
	Method   string `yaml:"method"`
	Lease    string `yaml:"lease"`
}

type floatingIPGrantStepAPI interface {
	api.EnvironmentFloatingIPAPI
	api.NetworkAPI
	api.VMAPI
}

// FloatingIPGrantStep grants a single method; it does not claim or renew ownership.
type FloatingIPGrantStep struct {
	resourceStepRecord
	input floatingIPGrantDeclaration
	op    floatingIPGrantStepAPI
}

func (s *FloatingIPGrantStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceState,
	write workflow.StateWriter, _ event.OnProgressCallback) error {
	if err := s.restore(saved); err != nil {
		return err
	}
	output := StateFromMap[FloatingIPGrantState](s.record.Output)
	if output == nil {
		resource, err := resourceStepReference(state, s.input.Resource, "floating_ip")
		if err != nil {
			return err
		}
		network, err := resourceStepReference(state, s.input.Network, "network")
		if err != nil {
			return err
		}
		networkItem, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{network}})
		if err != nil {
			return err
		}
		if networkItem == nil || (strings.HasPrefix(s.input.Network, "@") && networkItem.ID != network) {
			return resourceIdentityMismatch(s.Name())
		}
		item, err := s.op.FloatingIPGet(ctx, inputs.FloatingIPInput{Network: networkItem.ID, Identifier: resource})
		if err != nil {
			return err
		}
		if item == nil || item.Retired || item.NetworkID != networkItem.ID ||
			(strings.HasPrefix(s.input.Resource, "@") && item.ID != resource) {
			return resourceIdentityMismatch(s.Name())
		}
		vm, err := resourceStepReference(state, s.input.VM, "vm")
		if err != nil {
			return err
		}
		vmItem, err := s.op.VMGet(ctx, inputs.VMInput{Identifiers: []string{vm}})
		if err != nil {
			return err
		}
		if vmItem == nil || string(vmItem.NetworkID) != networkItem.ID ||
			(strings.HasPrefix(s.input.VM, "@") && vmItem.ID != vm) {
			return resourceIdentityMismatch(s.Name())
		}
		output = &FloatingIPGrantState{
			GrantID:    strings.ReplaceAll(crypto.UUIDV4(), "-", ""),
			NetworkID:  networkItem.ID,
			ResourceID: item.ID,
			VMID:       vmItem.ID,
			Method:     s.input.Method,
			Lease:      s.input.Lease,
		}
		s.record.Output = StructToMap(output)
		s.record.Meta.WasCreated = true
		if err := s.persist(ctx, write); err != nil {
			return err
		}
	}
	if output.GrantID == "" || output.NetworkID == "" || output.ResourceID == "" || output.VMID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	item, err := s.op.FloatingIPDelegationEnsure(ctx, inputs.FloatingIPDelegationEnsureInput{
		ID: output.GrantID, FloatingIPDelegationGrantInput: output.grantInput(),
	})
	if item != nil {
		if !output.matches(item) || item.State != "active" {
			return resourceIdentityMismatch(s.Name())
		}
		if output.Receipt != nil && !sameFloatingIPGrantLifetime(output.Receipt, item) {
			return resourceIdentityMismatch(s.Name())
		}
		output.Receipt = item
		s.record.Output = StructToMap(output)
	}
	if err != nil {
		return errors.Join(err, s.persist(ctx, write))
	}
	if item == nil {
		return errs.New(errs.CodeInternal, "floating IP grant ensure returned no receipt")
	}
	state.Set(s.Name(), output)
	return s.persist(ctx, write)
}

func (output *FloatingIPGrantState) grantInput() inputs.FloatingIPDelegationGrantInput {
	return inputs.FloatingIPDelegationGrantInput{Network: output.NetworkID, Resource: output.ResourceID,
		VM: output.VMID, Method: output.Method, Lease: output.Lease}
}

func (output *FloatingIPGrantState) matches(item *model.FloatingIPDelegationItem) bool {
	duration, err := output.grantInput().LeaseDuration()
	return err == nil && item != nil && item.ID == output.GrantID && item.NetworkID == output.NetworkID &&
		item.ResourceID == output.ResourceID && item.VMID == output.VMID && item.Method == output.Method &&
		item.LeaseDuration == duration
}

func sameFloatingIPGrantLifetime(first, second *model.FloatingIPDelegationItem) bool {
	return first.ID == second.ID && first.NetworkID == second.NetworkID && first.ResourceID == second.ResourceID &&
		first.VMID == second.VMID && first.Method == second.Method && first.LeaseDuration == second.LeaseDuration &&
		first.BootID == second.BootID && first.LaunchGeneration == second.LaunchGeneration && first.ExecutionUID == second.ExecutionUID
}

func (s *FloatingIPGrantStep) Destroy(ctx context.Context, saved model.ResourceState,
	write workflow.StateWriter, _ event.OnProgressCallback) error {
	if saved.Output != nil {
		s.record = saved
	}
	if !s.record.Meta.WasCreated {
		return s.persist(ctx, write)
	}
	output := StateFromMap[FloatingIPGrantState](s.record.Output)
	if output == nil || output.GrantID == "" || output.NetworkID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	receipt := output.Receipt
	if receipt == nil {
		// A committed grant may have lost its response. Recover its exact root identity without requiring a live VM.
		item, err := s.op.FloatingIPDelegationGet(
			ctx,
			inputs.FloatingIPDelegationInput{Network: output.NetworkID, ID: output.GrantID},
		)
		if errs.IsNotFound(err) {
			return s.persist(ctx, write)
		}
		if err != nil {
			return err
		}
		if !output.matches(item) {
			return resourceIdentityMismatch(s.Name())
		}
		receipt = item
		output.Receipt = item
		s.record.Output = StructToMap(output)
		if err := s.persist(ctx, write); err != nil {
			return err
		}
	}
	if !output.matches(receipt) {
		return resourceIdentityMismatch(s.Name())
	}
	item, err := s.op.FloatingIPDelegationRemoveOwned(ctx, inputs.FloatingIPDelegationRemoveOwnedInput{Grant: *receipt})
	if err != nil && !errs.IsNotFound(err) {
		return err
	}
	if err == nil && item == nil {
		return errs.New(errs.CodeInternal, "floating IP grant removal returned no receipt")
	}
	if item != nil &&
		(!output.matches(item) || !sameFloatingIPGrantLifetime(receipt, item) || item.State != "revoked") {
		return resourceIdentityMismatch(s.Name())
	}
	return s.persist(ctx, write)
}

func newFloatingIPGrantStepFromSpec(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	record, data, err := newResourceStepRecord(stepType, name, spec)
	if err != nil {
		return nil, err
	}
	var input floatingIPGrantDeclaration
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	if input.Network == "" {
		return nil, errs.New(errs.CodeValidationFailed, "floating IP environment steps require an explicit network")
	}
	if err := (inputs.FloatingIPDelegationGrantInput{Network: input.Network, Resource: input.Resource,
		VM: input.VM, Method: input.Method, Lease: input.Lease}).Validate(); err != nil {
		return nil, err
	}
	return &FloatingIPGrantStep{resourceStepRecord: record, input: input, op: op}, nil
}

func newFloatingIPGrantStepFromState(stepType, name string, saved model.ResourceState, deps []string,
	op api.API) (workflow.Step, error) {
	step, err := newFloatingIPGrantStepFromSpec(stepType, name, saved.Spec, op)
	if err != nil {
		return nil, err
	}
	s := step.(*FloatingIPGrantStep)
	s.record, s.deps = saved, deps
	return s, nil
}
