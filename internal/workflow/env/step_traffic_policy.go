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
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// TrafficPolicyState binds a closed traffic grant to its creation claim and root incarnation.
type TrafficPolicyState struct {
	SourceLabels      model.VMLabelSelector `yaml:"source_labels,omitempty"`
	DestinationLabels model.VMLabelSelector `yaml:"destination_labels,omitempty"`

	PolicyID                string `yaml:"policy_id"`
	ClaimID                 string `yaml:"claim_id"`
	Incarnation             string `yaml:"incarnation"`
	SourceNetworkID         string `yaml:"source_network_id,omitempty"`
	SourceVMID              string `yaml:"source_vm_id,omitempty"`
	DestinationVMID         string `yaml:"destination_vm_id,omitempty"`
	DestinationFloatingIPID string `yaml:"destination_floating_ip_id,omitempty"`
	DestinationNetworkID    string `yaml:"destination_network_id,omitempty"`
	DestinationHost         bool   `yaml:"destination_host,omitempty"`
	Protocol                string `yaml:"protocol"`
	Port                    string `yaml:"port"`
}

type trafficPolicyDeclaration struct {
	SourceLabels      model.VMLabelSelector `yaml:"source_labels"`
	DestinationLabels model.VMLabelSelector `yaml:"destination_labels"`

	SourceNetwork         string `yaml:"source_network"`
	SourceVM              string `yaml:"source_vm"`
	DestinationVM         string `yaml:"destination_vm"`
	DestinationFloatingIP string `yaml:"destination_floating_ip"`
	DestinationNetwork    string `yaml:"destination_network"`
	DestinationHost       bool   `yaml:"destination_host"`
	Protocol              string `yaml:"protocol"`
	Port                  string `yaml:"port"`
	Existing              string `yaml:"existing"`
}

type trafficPolicyStepAPI interface {
	api.PolicyAPI
	api.EnvironmentPolicyAPI
	api.VMAPI
	api.NetworkAPI
	api.EnvironmentFloatingIPAPI
}

// TrafficPolicyStep applies typed VM or gateway grants through root policy authority.
type TrafficPolicyStep struct {
	resourceStepRecord
	input trafficPolicyDeclaration
	op    trafficPolicyStepAPI
}

func (s *TrafficPolicyStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if err := s.restore(saved); err != nil {
		return err
	}
	output := StateFromMap[TrafficPolicyState](s.record.Output)
	if output == nil {
		var err error
		output, err = s.resolveEndpoints(ctx, state)
		if err != nil {
			return err
		}
		if s.input.Existing == "" {
			output.ClaimID = crypto.UUIDV4()
		}
		s.record.Output = StructToMap(output)
		// CRITICAL: Retain the operation claim before root can apply a grant.
		if err := s.persist(ctx, write); err != nil {
			return err
		}
	}
	if s.input.Existing != "" {
		if err := s.inspectExisting(ctx, output); err != nil {
			return err
		}
		output.PolicyID = s.input.Existing
		s.record.Meta.WasCreated = false
		s.record.Output = StructToMap(output)
		state.Set(s.Name(), output)
		return s.persist(ctx, write)
	}
	if output.ClaimID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	var receipt *results.EnvironmentPolicyReceipt
	var applyErr error
	if output.usesLabels() {
		receipt, applyErr = s.op.LabelTrafficPolicyEnsure(ctx, output.labelInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else if output.DestinationFloatingIPID != "" {
		receipt, applyErr = s.ensureFloatingIPPolicy(ctx, output)
	} else if output.DestinationHost && output.SourceNetworkID != "" {
		receipt, applyErr = s.op.NetworkToHostTrafficPolicyEnsure(ctx, output.networkHostInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else if output.DestinationHost {
		receipt, applyErr = s.op.VMToHostTrafficPolicyEnsure(ctx, output.vmHostInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else if output.SourceNetworkID != "" {
		receipt, applyErr = s.op.NetworkToVMTrafficPolicyEnsure(ctx, output.networkInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else {
		receipt, applyErr = s.op.VMToVMTrafficPolicyEnsure(ctx, output.vmInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	}
	if receipt != nil {
		output.PolicyID, output.Incarnation = receipt.PolicyID, receipt.Incarnation
		s.record.Meta.WasCreated = receipt.WasCreated
		s.record.Output = StructToMap(output)
	}
	if applyErr != nil {
		return errors.Join(applyErr, s.persist(ctx, write))
	}
	if receipt == nil || receipt.PolicyID == "" || receipt.Incarnation == "" {
		return errs.New(errs.CodeInternal, "traffic policy ensure returned no incarnation receipt")
	}
	state.Set(s.Name(), output)
	return s.persist(ctx, write)
}

func (s *TrafficPolicyStep) resolveEndpoints(
	ctx context.Context,
	state *workflow.SharedState,
) (*TrafficPolicyState, error) {
	output := &TrafficPolicyState{
		DestinationHost: s.input.DestinationHost, Protocol: s.input.Protocol, Port: s.input.Port,
		SourceLabels: s.input.SourceLabels, DestinationLabels: s.input.DestinationLabels,
	}
	if s.input.DestinationFloatingIP != "" {
		if err := s.resolveFloatingIPDestination(ctx, state, output); err != nil {
			return nil, err
		}
	} else if !s.input.DestinationHost && s.input.DestinationLabels == nil {
		destination, err := resourceStepReference(state, s.input.DestinationVM, "vm")
		if err != nil {
			return nil, err
		}
		destinationVM, err := s.op.VMGet(ctx, inputs.VMInput{Identifiers: []string{destination}})
		if err != nil {
			return nil, err
		}
		if destinationVM == nil || (strings.HasPrefix(s.input.DestinationVM, "@") && destinationVM.ID != destination) {
			return nil, resourceIdentityMismatch(s.Name())
		}
		output.DestinationVMID = destinationVM.ID
	}
	if s.input.SourceNetwork != "" {
		source, err := resourceStepReference(state, s.input.SourceNetwork, "network")
		if err != nil {
			return nil, err
		}
		network, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{source}})
		if err != nil {
			return nil, err
		}
		if network == nil {
			return nil, resourceIdentityMismatch(s.Name())
		}
		if strings.HasPrefix(s.input.SourceNetwork, "@") && network.ID != source {
			return nil, resourceIdentityMismatch(s.Name())
		}
		output.SourceNetworkID = network.ID
	} else if s.input.SourceLabels == nil {
		source, err := resourceStepReference(state, s.input.SourceVM, "vm")
		if err != nil {
			return nil, err
		}
		vm, err := s.op.VMGet(ctx, inputs.VMInput{Identifiers: []string{source}})
		if err != nil {
			return nil, err
		}
		if vm == nil {
			return nil, resourceIdentityMismatch(s.Name())
		}
		if strings.HasPrefix(s.input.SourceVM, "@") && vm.ID != source {
			return nil, resourceIdentityMismatch(s.Name())
		}
		output.SourceVMID = vm.ID
	}
	return output, nil
}

func (s *TrafficPolicyStep) inspectExisting(ctx context.Context, output *TrafficPolicyState) error {
	if output.usesLabels() {
		return s.inspectExistingLabelPolicy(ctx, output)
	}
	// Port admission is identical for VM and host destinations; this parses no endpoint identity.
	input := inputs.VMToHostTrafficPolicyCreateInput{
		SourceVM: "source", Protocol: output.Protocol, DestinationPort: output.Port,
	}
	protocol, first, last, err := input.ResolvedTraffic()
	if err != nil {
		return err
	}
	if output.DestinationFloatingIPID != "" {
		return s.inspectExistingFloatingIPPolicy(ctx, output, protocol, first, last)
	}
	if output.DestinationHost {
		return s.inspectExistingHostPolicy(ctx, output, protocol, first, last)
	}
	if output.SourceNetworkID != "" {
		item, err := s.op.NetworkToVMTrafficPolicyInspect(ctx, inputs.NetworkToVMTrafficPolicyInput{
			Identifiers: []string{s.input.Existing},
		})
		if err != nil {
			return err
		}
		if item == nil || item.ID != s.input.Existing || item.SourceNetworkID != output.SourceNetworkID ||
			item.DestinationVMID != output.DestinationVMID || item.Protocol != protocol ||
			item.DestinationPortStart != first || item.DestinationPortEnd != last {
			return resourceIdentityMismatch(s.Name())
		}
		return nil
	}
	item, err := s.op.VMToVMTrafficPolicyInspect(
		ctx,
		inputs.VMToVMTrafficPolicyInput{Identifiers: []string{s.input.Existing}},
	)
	if err != nil {
		return err
	}
	if item == nil || item.ID != s.input.Existing || item.SourceVMID != output.SourceVMID ||
		item.DestinationVMID != output.DestinationVMID || item.Protocol != protocol ||
		item.DestinationPortStart != first || item.DestinationPortEnd != last {
		return resourceIdentityMismatch(s.Name())
	}
	return nil
}

func (output *TrafficPolicyState) networkInput() inputs.NetworkToVMTrafficPolicyCreateInput {
	return inputs.NetworkToVMTrafficPolicyCreateInput{SourceNetwork: output.SourceNetworkID,
		DestinationVM: output.DestinationVMID, Protocol: output.Protocol, DestinationPort: output.Port}
}

func (output *TrafficPolicyState) vmInput() inputs.VMToVMTrafficPolicyCreateInput {
	return inputs.VMToVMTrafficPolicyCreateInput{SourceVM: output.SourceVMID,
		DestinationVM: output.DestinationVMID, Protocol: output.Protocol, DestinationPort: output.Port}
}

func (s *TrafficPolicyStep) Destroy(ctx context.Context, saved model.ResourceState,
	write workflow.StateWriter, onProgress event.OnProgressCallback) error {
	if saved.Output != nil {
		s.record = saved
	}
	output := StateFromMap[TrafficPolicyState](s.record.Output)
	if output == nil || output.ClaimID == "" || (output.Incarnation != "" && !s.record.Meta.WasCreated) {
		return s.persist(ctx, write)
	}
	var err error
	if output.usesLabels() {
		err = s.op.LabelTrafficPolicyRemoveOwned(ctx, output.labelInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else if output.DestinationFloatingIPID != "" {
		err = s.removeOwnedFloatingIPPolicy(ctx, output)
	} else if output.DestinationHost && output.SourceNetworkID != "" {
		err = s.op.NetworkToHostTrafficPolicyRemoveOwned(ctx, output.networkHostInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else if output.DestinationHost {
		err = s.op.VMToHostTrafficPolicyRemoveOwned(ctx, output.vmHostInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else if output.SourceNetworkID != "" {
		err = s.op.NetworkToVMTrafficPolicyRemoveOwned(ctx, output.networkInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	} else {
		err = s.op.VMToVMTrafficPolicyRemoveOwned(ctx, output.vmInput(),
			inputs.EnvironmentPolicyOwnershipInput{ClaimID: output.ClaimID, Incarnation: output.Incarnation})
	}
	if err != nil {
		return err
	}
	return s.persist(ctx, write)
}

func newTrafficPolicyStepFromSpec(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	record, data, err := newResourceStepRecord(stepType, name, spec)
	if err != nil {
		return nil, err
	}
	var input trafficPolicyDeclaration
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	sources := 0
	if input.SourceNetwork != "" {
		sources++
	}
	if input.SourceVM != "" {
		sources++
	}
	if input.SourceLabels != nil {
		sources++
	}
	if sources != 1 {
		return nil, errs.New(errs.CodeValidationFailed, "traffic policy requires exactly one source")
	}
	destinations := 0
	if input.DestinationHost {
		destinations++
	}
	if input.DestinationVM != "" {
		destinations++
	}
	if input.DestinationFloatingIP != "" {
		destinations++
	}
	if input.DestinationLabels != nil {
		destinations++
	}
	if destinations != 1 {
		return nil, errs.New(errs.CodeValidationFailed, "traffic policy requires exactly one destination")
	}
	if (input.DestinationFloatingIP != "") != (input.DestinationNetwork != "") {
		return nil, errs.New(
			errs.CodeValidationFailed,
			"destination_network is required only with destination_floating_ip",
		)
	}
	if input.SourceLabels != nil || input.DestinationLabels != nil {
		if input.SourceNetwork != "" || input.DestinationHost || input.DestinationFloatingIP != "" {
			return nil, errs.New(errs.CodeValidationFailed, "label traffic endpoints must select VMs")
		}
		validation := inputs.LabelTrafficPolicyCreateInput{
			Source:      inputs.VMPolicySelectorInput{VM: input.SourceVM, Labels: input.SourceLabels},
			Destination: inputs.VMPolicySelectorInput{VM: input.DestinationVM, Labels: input.DestinationLabels},
			Protocol:    input.Protocol, DestinationPort: input.Port,
		}
		_, _, _, err = validation.ResolveTraffic()
	} else if input.DestinationFloatingIP != "" {
		err = validateFloatingIPTrafficDeclaration(input)
	} else if input.DestinationHost && input.SourceNetwork != "" {
		validation := inputs.NetworkToHostTrafficPolicyCreateInput{SourceNetwork: input.SourceNetwork,
			Protocol: input.Protocol, DestinationPort: input.Port}
		err = validation.Validate()
	} else if input.DestinationHost {
		validation := inputs.VMToHostTrafficPolicyCreateInput{SourceVM: input.SourceVM,
			Protocol: input.Protocol, DestinationPort: input.Port}
		err = validation.Validate()
	} else if input.SourceNetwork != "" {
		validation := inputs.NetworkToVMTrafficPolicyCreateInput{SourceNetwork: input.SourceNetwork,
			DestinationVM: input.DestinationVM, Protocol: input.Protocol, DestinationPort: input.Port}
		err = validation.Validate()
	} else {
		validation := inputs.VMToVMTrafficPolicyCreateInput{SourceVM: input.SourceVM,
			DestinationVM: input.DestinationVM, Protocol: input.Protocol, DestinationPort: input.Port}
		err = validation.Validate()
	}
	if err != nil {
		return nil, err
	}
	if input.Existing != "" && len(input.Existing) != 64 {
		return nil, errs.New(errs.CodeValidationFailed, "existing traffic policy requires its exact 64-character ID")
	}
	if input.Existing != "" {
		selector := inputs.VMToVMTrafficPolicyInput{Identifiers: []string{input.Existing}}
		if err := selector.Validate(); err != nil {
			return nil, err
		}
	}
	return &TrafficPolicyStep{resourceStepRecord: record, input: input, op: op}, nil
}

func newTrafficPolicyStepFromState(
	stepType, name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	step, err := newTrafficPolicyStepFromSpec(stepType, name, saved.Spec, op)
	if err != nil {
		return nil, err
	}
	s := step.(*TrafficPolicyStep)
	s.record, s.deps = saved, deps
	return s, nil
}
