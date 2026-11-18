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

// FloatingIPState binds a reservation to the exact identity retained before creation.
type FloatingIPState struct {
	ResourceID string `yaml:"resource_id"`
	NetworkID  string `yaml:"network_id"`
	Name       string `yaml:"name"`
	IPv4       string `yaml:"ipv4"`
	Acquired   bool   `yaml:"acquired"`
}

type floatingIPDeclaration struct {
	Network  string `yaml:"network"`
	Name     string `yaml:"name"`
	IPv4     string `yaml:"ipv4"`
	Existing string `yaml:"existing"`
}

type floatingIPStepAPI interface {
	api.EnvironmentFloatingIPAPI
	api.NetworkAPI
}

// FloatingIPStep reserves an address without associating it with a VM.
type FloatingIPStep struct {
	resourceStepRecord
	input floatingIPDeclaration
	op    floatingIPStepAPI
}

func (s *FloatingIPStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceState,
	write workflow.StateWriter, _ event.OnProgressCallback) error {
	if err := s.restore(saved); err != nil {
		return err
	}
	output := StateFromMap[FloatingIPState](s.record.Output)
	if output == nil {
		selector, err := resourceStepReference(state, s.input.Network, "network")
		if err != nil {
			return err
		}
		network, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{selector}})
		if err != nil {
			return err
		}
		if network == nil || (strings.HasPrefix(s.input.Network, "@") && network.ID != selector) {
			return resourceIdentityMismatch(s.Name())
		}
		output = &FloatingIPState{NetworkID: network.ID, Name: s.input.Name, IPv4: s.input.IPv4}
		if s.input.Existing != "" {
			item, err := s.op.FloatingIPGet(
				ctx,
				inputs.FloatingIPInput{Network: network.ID, Identifier: s.input.Existing},
			)
			if err != nil {
				return err
			}
			if item == nil || item.Retired || item.NetworkID != network.ID {
				return resourceIdentityMismatch(s.Name())
			}
			output.ResourceID, output.Name, output.IPv4, output.Acquired = item.ID, item.Name, item.IPv4, true
		} else {
			output.ResourceID = strings.ReplaceAll(crypto.UUIDV4(), "-", "")
			// Persist this identity before creation, including an uncertain creation result.
			s.record.Meta.WasCreated = true
		}
		s.record.Output = StructToMap(output)
		if err := s.persist(ctx, write); err != nil {
			return err
		}
	}
	if output.ResourceID == "" || output.NetworkID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	if output.Acquired {
		item, err := s.op.FloatingIPGet(
			ctx,
			inputs.FloatingIPInput{Network: output.NetworkID, Identifier: output.ResourceID},
		)
		if err != nil {
			return err
		}
		if !output.matches(item) {
			return resourceIdentityMismatch(s.Name())
		}
	} else {
		item, err := s.op.FloatingIPEnsure(ctx, inputs.FloatingIPCreateInput{
			Network: output.NetworkID, Name: output.Name, IPv4: output.IPv4,
		}, inputs.EnvironmentFloatingIPOwnershipInput{ResourceID: output.ResourceID})
		if item != nil {
			if item.ID != output.ResourceID || item.NetworkID != output.NetworkID || item.Name != output.Name ||
				item.Retired ||
				(output.IPv4 != "" && item.IPv4 != output.IPv4) {
				return resourceIdentityMismatch(s.Name())
			}
			output.IPv4, output.Acquired = item.IPv4, true
			s.record.Output = StructToMap(output)
		}
		if err != nil {
			return errors.Join(err, s.persist(ctx, write))
		}
		if item == nil {
			return errs.New(errs.CodeInternal, "floating IP ensure returned no reservation")
		}
	}
	state.Set(s.Name(), output)
	return s.persist(ctx, write)
}

func (output *FloatingIPState) matches(item *model.FloatingIPItem) bool {
	return item != nil && !item.Retired && item.ID == output.ResourceID && item.NetworkID == output.NetworkID &&
		item.Name == output.Name && item.IPv4 == output.IPv4
}

func (s *FloatingIPStep) Destroy(ctx context.Context, saved model.ResourceState,
	write workflow.StateWriter, _ event.OnProgressCallback) error {
	if saved.Output != nil {
		s.record = saved
	}
	if !s.record.Meta.WasCreated {
		return s.persist(ctx, write)
	}
	output := StateFromMap[FloatingIPState](s.record.Output)
	if output == nil || output.ResourceID == "" || output.NetworkID == "" {
		return resourceIdentityMismatch(s.Name())
	}
	selector := inputs.FloatingIPInput{Network: output.NetworkID, Identifier: output.ResourceID}
	item, err := s.op.FloatingIPGet(ctx, selector)
	if errs.IsNotFound(err) {
		return s.persist(ctx, write)
	}
	if err != nil {
		return err
	}
	if item == nil {
		return resourceIdentityMismatch(s.Name())
	}
	if item.ID != output.ResourceID || item.NetworkID != output.NetworkID || item.Name != output.Name ||
		(output.IPv4 != "" && item.IPv4 != output.IPv4) {
		return resourceIdentityMismatch(s.Name())
	}
	if !item.Retired {
		if _, err := s.op.FloatingIPRemove(ctx, selector); err != nil && !errs.IsNotFound(err) {
			return err
		}
	}
	return s.persist(ctx, write)
}

func newFloatingIPStepFromSpec(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error) {
	if op == nil {
		return nil, errs.New(errs.CodeInternal, "operation not initialized")
	}
	record, data, err := newResourceStepRecord(stepType, name, spec)
	if err != nil {
		return nil, err
	}
	var input floatingIPDeclaration
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	if input.Network == "" {
		return nil, errs.New(errs.CodeValidationFailed, "floating IP environment steps require an explicit network")
	}
	if input.Existing == "" {
		if input.Name == "" {
			input.Name = name
		}
		if err := (inputs.FloatingIPCreateInput{Name: input.Name, Network: input.Network, IPv4: input.IPv4}).Validate(); err != nil {
			return nil, err
		}
	} else if err := (inputs.FloatingIPInput{Network: input.Network, Identifier: input.Existing}).Validate(); err != nil {
		return nil, err
	}
	return &FloatingIPStep{resourceStepRecord: record, input: input, op: op}, nil
}

func newFloatingIPStepFromState(stepType, name string, saved model.ResourceState, deps []string,
	op api.API) (workflow.Step, error) {
	step, err := newFloatingIPStepFromSpec(stepType, name, saved.Spec, op)
	if err != nil {
		return nil, err
	}
	s := step.(*FloatingIPStep)
	s.record, s.deps = saved, deps
	return s, nil
}
