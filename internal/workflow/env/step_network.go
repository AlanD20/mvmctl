package env

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// NetworkState is the persisted state for a network step.
type NetworkState struct {
	NetworkID  string `yaml:"network_id"`
	Subnet     string `yaml:"subnet"`
	WasCreated bool   `yaml:"was_created"`
}

// NetworkStep implements workflow.Step for creating and destroying networks.
type NetworkStep struct {
	stepType string // singular, e.g. "network"
	name     string
	deps     []string
	input    inputs.NetworkCreateInput
	op       *api.Operation
	saved    *NetworkState
}

func (s *NetworkStep) Type() string { return s.stepType }

func (s *NetworkStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *NetworkStep) Dependencies() []string { return s.deps }

func (s *NetworkStep) Apply(ctx context.Context, state *workflow.SharedState, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	var prev *NetworkState
	if saved != nil {
		prev = StateFromMap[NetworkState](saved)
	}
	existing, err := s.op.Repos.Network.GetByName(ctx, s.input.Name)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check network %q: %v", s.input.Name, err),
			err,
		)
	}

	if existing != nil {
		wasCreated := prev != nil && prev.WasCreated
		s.saved = &NetworkState{
			NetworkID:  existing.ID,
			Subnet:     existing.Subnet,
			WasCreated: wasCreated,
		}
		state.Set(s.Name(), s.saved)
		return nil
	}

	net, err := s.op.NetworkCreate(ctx, s.input)
	if err != nil {
		return err
	}

	s.saved = &NetworkState{
		NetworkID:  net.ID,
		Subnet:     net.Subnet,
		WasCreated: true,
	}
	state.Set(s.Name(), s.saved)
	return nil
}

func (s *NetworkStep) Destroy(ctx context.Context, saved model.ResourceSpec) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved != nil {
		s.saved = StateFromMap[NetworkState](saved)
	}

	if s.saved == nil || !s.saved.WasCreated {
		return nil
	}

	return s.op.NetworkRemove(ctx, inputs.NetworkInput{
		Identifiers: []string{s.saved.NetworkID},
		Force:       true,
	}, true)
}

func (s *NetworkStep) StateData() model.ResourceSpec {
	if s.saved == nil {
		return nil
	}
	return StructToMap(s.saved)
}

func newNetworkStepFromSpec(
	stepType string,
	name string,
	spec model.ResourceSpec,
	op *api.Operation,
) (workflow.Step, error) {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var input inputs.NetworkCreateInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	input.Name = name

	// Canonical spec key is "nat". The "nat_enabled" key is accepted but
	// deprecated — selecting both with different values is undefined.
	input.NATEnabled = true // default to true
	if v, ok := spec["nat"]; ok {
		switch val := v.(type) {
		case bool:
			input.NATEnabled = val
		case string:
			input.NATEnabled = val == "true" || val == "yes"
		}
	}

	return &NetworkStep{
		stepType: stepType,
		name:     name,
		deps:     extractDependsOn(spec),
		input:    input,
		op:       op,
	}, nil
}

func newNetworkStepFromState(
	stepType string,
	name string,
	saved model.ResourceSpec,
	op *api.Operation,
) (workflow.Step, error) {
	ns := StateFromMap[NetworkState](saved)
	return &NetworkStep{
		stepType: stepType,
		name:     name,
		input: inputs.NetworkCreateInput{
			Name: name,
		},
		op:    op,
		saved: ns,
	}, nil
}
