package env

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// NetworkState is the persisted state for a network step.
type NetworkState struct {
	NetworkID string `yaml:"network_id"`
	Subnet    string `yaml:"subnet"`
}

// NetworkStep implements workflow.Step for creating and destroying networks.
type NetworkStep struct {
	stepType string
	name     string
	deps     []string
	removes  []string
	specHash string
	input    inputs.NetworkCreateInput
	op       api.NetworkAPI
	saved    *NetworkState
	meta     model.ResourceMeta
}

func (s *NetworkStep) Type() string { return s.stepType }

func (s *NetworkStep) Name() string { return FormatStepName(s.stepType, s.name) }

func (s *NetworkStep) Dependencies() []string { return s.deps }

func (s *NetworkStep) SpecHash() string  { return s.specHash }
func (s *NetworkStep) Removes() []string { return s.removes }

func (s *NetworkStep) Apply(
	ctx context.Context,
	state *workflow.SharedState,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}

	// Recover WasCreated from saved meta.
	wasCreated := saved.Meta.WasCreated

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "checking if exists"})
	existing, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{s.input.Name}})
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check network %q: %v", s.input.Name, err),
			err,
		)
	}

	if existing != nil {
		onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "already exists, skipping"})
		s.saved = &NetworkState{
			NetworkID: existing.ID,
			Subnet:    existing.Subnet,
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

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "creating network"})
	net, err := s.op.NetworkCreate(ctx, s.input)
	if err != nil {
		return err
	}

	s.saved = &NetworkState{
		NetworkID: net.ID,
		Subnet:    net.Subnet,
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

func (s *NetworkStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	onProgress event.OnProgressCallback,
) error {
	if s.op == nil {
		return fmt.Errorf("%s: operation not initialized (nil op)", s.Name())
	}
	if s.saved == nil && saved.Spec != nil {
		s.saved = StateFromMap[NetworkState](saved.Spec)
		s.meta = saved.Meta
	}

	if s.saved == nil || !s.meta.WasCreated {
		if err := write(ctx, s.StateData()); err != nil {
			return fmt.Errorf("persist step state after destroy skip: %w", err)
		}
		return nil
	}

	if err := s.op.NetworkRemove(ctx, inputs.NetworkInput{
		Identifiers:    []string{s.saved.NetworkID},
		Force:          true,
		IncludeDeleted: true,
	}, true); err != nil {
		if errs.IsNotFound(err) {
			slog.Debug("network already removed, skipping destroy", "network", s.saved.NetworkID)
			if err := write(ctx, s.StateData()); err != nil {
				return fmt.Errorf("persist step state after destroy skip: %w", err)
			}
			return nil
		}
		return err
	}

	if err := write(ctx, s.StateData()); err != nil {
		return fmt.Errorf("persist step state after destroy: %w", err)
	}
	return nil
}

func (s *NetworkStep) StateData() model.ResourceState {
	if s.saved == nil {
		return model.ResourceState{}
	}
	return model.ResourceState{
		Spec: StructToMap(s.saved),
		Meta: s.meta,
	}
}

// NewNetworkStep creates a NetworkStep with the given API interface for testing.
// Only for use in tests.
func NewNetworkStep(op api.NetworkAPI, name string, input inputs.NetworkCreateInput) *NetworkStep {
	return &NetworkStep{
		op:       op,
		name:     name,
		stepType: "network",
		input:    input,
		specHash: crypto.SHA256([]byte(name)),
	}
}

func newNetworkStepFromSpec(
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

	var input inputs.NetworkCreateInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	input.Name = name

	input.NATEnabled = true
	if _, exists := spec["nat_enabled"]; exists {
		input.NATEnabled = spec.GetBool("nat_enabled")
	}

	return &NetworkStep{
		stepType: stepType,
		name:     name,
		deps:     spec.GetStringList("depends_on"),
		removes:  spec.GetStringList("removes"),
		specHash: crypto.SHA256(data),
		input:    input,
		op:       op,
	}, nil
}

func newNetworkStepFromState(
	stepType string,
	name string,
	saved model.ResourceState,
	deps []string,
	op api.API,
) (workflow.Step, error) {
	if op == nil {
		return nil, errors.New("operation not initialized")
	}

	ns := StateFromMap[NetworkState](saved.Spec)
	return &NetworkStep{
		stepType: stepType,
		name:     name,
		deps:     deps,
		input: inputs.NetworkCreateInput{
			Name: name,
		},
		op:    op,
		saved: ns,
		meta:  saved.Meta,
	}, nil
}
