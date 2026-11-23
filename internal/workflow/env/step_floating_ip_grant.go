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

// FloatingIPGrantState retains the whole plan before the first grant is created.
type FloatingIPGrantState struct {
	Grants []floatingIPGrantState `yaml:"grants"`
}

// floatingIPGrantState retains the exact intent before granting and the root receipt afterwards.
type floatingIPGrantState struct {
	GrantID    string                          `yaml:"grant_id"`
	NetworkID  string                          `yaml:"network_id"`
	ResourceID string                          `yaml:"resource_id"`
	VMID       string                          `yaml:"vm_id"`
	Method     string                          `yaml:"method"`
	Lease      string                          `yaml:"lease"`
	Receipt    *model.FloatingIPDelegationItem `yaml:"receipt,omitempty"`
}

type floatingIPGrantMethod struct {
	Method string `yaml:"method"`
	Lease  string `yaml:"lease"`
}

func (method *floatingIPGrantMethod) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errs.New(errs.CodeValidationFailed, "floating IP grant method must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if field := node.Content[i].Value; field != "method" && field != "lease" {
			return errs.New(errs.CodeValidationFailed, "unknown floating IP grant method field "+field)
		}
	}
	type fields floatingIPGrantMethod
	return node.Decode((*fields)(method))
}

type floatingIPGrantDeclaration struct {
	Network  string                  `yaml:"network"`
	Resource string                  `yaml:"resource"`
	VMs      []string                `yaml:"vms"`
	Methods  []floatingIPGrantMethod `yaml:"methods"`
}

type floatingIPGrantStepAPI interface {
	api.EnvironmentFloatingIPAPI
	api.NetworkAPI
	api.VMAPI
}

// FloatingIPGrantStep delegates explicit methods to explicitly selected VM launches.
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
	if s.record.Output == nil {
		var err error
		output, err = s.prepare(ctx, state)
		if err != nil {
			return err
		}
		s.record.Meta.WasCreated = true
		if err := s.persistGrants(ctx, write, output); err != nil {
			return err
		}
	}
	if !output.valid() {
		return resourceIdentityMismatch(s.Name())
	}
	launches := make(map[string]*model.FloatingIPGrantIncarnation, len(s.input.VMs))
	for i := range output.Grants {
		grant := &output.Grants[i]
		expected := launches[grant.VMID]
		if expected == nil && grant.Receipt != nil {
			expected = grant.incarnation()
		}
		item, err := s.op.FloatingIPDelegationEnsure(ctx, inputs.FloatingIPDelegationEnsureInput{
			ID: grant.GrantID, FloatingIPDelegationGrantInput: grant.grantInput(), Expected: expected,
		})
		if item != nil {
			if !grant.matches(item) || item.State != "active" ||
				(expected != nil && !expected.Matches(item.BootID, item.LaunchGeneration, item.ExecutionUID)) {
				return resourceIdentityMismatch(s.Name())
			}
			grant.Receipt = item
			launches[grant.VMID] = grant.incarnation()
			err = errors.Join(err, s.persistGrants(ctx, write, output))
		}
		if err != nil {
			return err
		}
		if item == nil {
			return errs.New(errs.CodeInternal, "floating IP grant ensure returned no receipt")
		}
	}
	state.Set(s.Name(), output)
	return nil
}

func (s *FloatingIPGrantStep) prepare(ctx context.Context, state *workflow.SharedState) (*FloatingIPGrantState, error) {
	resource, err := resourceStepReference(state, s.input.Resource, "floating_ip")
	if err != nil {
		return nil, err
	}
	network, err := resourceStepReference(state, s.input.Network, "network")
	if err != nil {
		return nil, err
	}
	networkItem, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{network}})
	if err != nil {
		return nil, err
	}
	if networkItem == nil || (strings.HasPrefix(s.input.Network, "@") && networkItem.ID != network) {
		return nil, resourceIdentityMismatch(s.Name())
	}
	item, err := s.op.FloatingIPGet(ctx, inputs.FloatingIPInput{Network: networkItem.ID, Identifier: resource})
	if err != nil {
		return nil, err
	}
	if item == nil || item.Retired || item.NetworkID != networkItem.ID ||
		(strings.HasPrefix(s.input.Resource, "@") && item.ID != resource) {
		return nil, resourceIdentityMismatch(s.Name())
	}

	output := &FloatingIPGrantState{}
	seen := make(map[string]bool, len(s.input.VMs))
	for _, selector := range s.input.VMs {
		vm, err := resourceStepReference(state, selector, "vm")
		if err != nil {
			return nil, err
		}
		vmItem, err := s.op.VMGet(ctx, inputs.VMInput{Identifiers: []string{vm}})
		if err != nil {
			return nil, err
		}
		if vmItem == nil || string(vmItem.NetworkID) != networkItem.ID ||
			(strings.HasPrefix(selector, "@") && vmItem.ID != vm) {
			return nil, resourceIdentityMismatch(s.Name())
		}
		if seen[vmItem.ID] {
			return nil, errs.New(errs.CodeValidationFailed, "floating IP grant selects the same VM more than once")
		}
		seen[vmItem.ID] = true
		for _, method := range s.input.Methods {
			output.Grants = append(output.Grants, floatingIPGrantState{
				GrantID:   strings.ReplaceAll(crypto.UUIDV4(), "-", ""),
				NetworkID: networkItem.ID, ResourceID: item.ID, VMID: vmItem.ID,
				Method: method.Method, Lease: method.Lease,
			})
		}
	}
	return output, nil
}

func (s *FloatingIPGrantStep) persistGrants(
	ctx context.Context, write workflow.StateWriter, output *FloatingIPGrantState,
) error {
	s.record.Output = StructToMap(output)
	return s.persist(ctx, write)
}

func (output *FloatingIPGrantState) valid() bool {
	if output == nil || len(output.Grants) == 0 {
		return false
	}
	for _, grant := range output.Grants {
		input := inputs.FloatingIPDelegationEnsureInput{
			ID:                             grant.GrantID,
			FloatingIPDelegationGrantInput: grant.grantInput(),
		}
		if input.Validate() != nil || (grant.Receipt != nil && !grant.matches(grant.Receipt)) {
			return false
		}
	}
	return true
}

func (grant *floatingIPGrantState) incarnation() *model.FloatingIPGrantIncarnation {
	return &model.FloatingIPGrantIncarnation{
		BootID: grant.Receipt.BootID, LaunchGeneration: grant.Receipt.LaunchGeneration,
		ExecutionUID: grant.Receipt.ExecutionUID,
	}
}

func (output *floatingIPGrantState) grantInput() inputs.FloatingIPDelegationGrantInput {
	return inputs.FloatingIPDelegationGrantInput{Network: output.NetworkID, Resource: output.ResourceID,
		VM: output.VMID, Method: output.Method, Lease: output.Lease}
}

func (output *floatingIPGrantState) matches(item *model.FloatingIPDelegationItem) bool {
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
	if !output.valid() {
		return resourceIdentityMismatch(s.Name())
	}
	for i := len(output.Grants) - 1; i >= 0; i-- {
		if err := s.revokeGrant(ctx, &output.Grants[i]); err != nil {
			return err
		}
	}
	return s.persist(ctx, write)
}

func (s *FloatingIPGrantStep) revokeGrant(ctx context.Context, output *floatingIPGrantState) error {
	receipt := output.Receipt
	if receipt == nil {
		// A committed grant may have lost its response. Recover its exact root identity without requiring a live VM.
		item, err := s.op.FloatingIPDelegationGet(
			ctx,
			inputs.FloatingIPDelegationInput{Network: output.NetworkID, ID: output.GrantID},
		)
		if errs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !output.matches(item) {
			return resourceIdentityMismatch(s.Name())
		}
		// The destroy writer removes the whole batch from state. Keep the saved ID until every revoke succeeds.
		receipt = item
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
	return nil
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
	if len(input.VMs) == 0 || len(input.Methods) == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "floating IP grant requires nonempty vms and methods lists")
	}
	grantInput := inputs.FloatingIPDelegationGrantInput{
		Network: input.Network, Resource: input.Resource, Method: input.Methods[0].Method, Lease: input.Methods[0].Lease,
	}
	vms := make(map[string]bool, len(input.VMs))
	for _, vm := range input.VMs {
		grantInput.VM = vm
		if err := grantInput.Validate(); err != nil {
			return nil, err
		}
		if vms[vm] {
			return nil, errs.New(errs.CodeValidationFailed, "floating IP grant requires distinct VM selectors")
		}
		vms[vm] = true
	}
	methods := make(map[string]bool, len(input.Methods))
	for _, method := range input.Methods {
		if methods[method.Method] {
			return nil, errs.New(errs.CodeValidationFailed, "floating IP grant requires distinct methods")
		}
		methods[method.Method] = true
		grantInput.Method, grantInput.Lease = method.Method, method.Lease
		if _, err := grantInput.LeaseDuration(); err != nil {
			return nil, err
		}
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
