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
	stepType  string
	name      string
	deps      []string
	removes   []string
	specHash  string
	input     inputs.NetworkCreateInput
	inputSpec model.ResourceMap
	op        api.NetworkAPI
	saved     *NetworkState
	meta      model.ResourceMeta
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

	lookup := s.input.Name
	prior := StateFromMap[NetworkState](saved.Output)
	if saved.Output != nil {
		if prior == nil || prior.NetworkID == "" {
			return fmt.Errorf("%s: saved resource identity is invalid", s.Name())
		}
		if saved.Meta.SpecHash != s.specHash {
			return fmt.Errorf(
				"%s: declaration changed; destroy the saved resource before changing its declaration",
				s.Name(),
			)
		}
		lookup = prior.NetworkID
	} else if selected := s.inputSpec.GetString("existing"); selected != "" {
		lookup = selected
	}
	// Recover WasCreated from saved meta.
	wasCreated := saved.Meta.WasCreated

	onProgress(event.Progress{Phase: s.Name(), Status: "running", Message: "checking if exists"})
	existing, err := s.op.NetworkGet(ctx, inputs.NetworkInput{Identifiers: []string{lookup}})
	if err != nil && !errs.IsNotFound(err) {
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			fmt.Sprintf("check network %q: %v", s.input.Name, err),
			err,
		)
	}

	if prior != nil && (existing == nil || existing.ID != prior.NetworkID) {
		return fmt.Errorf("%s: saved resource %s is missing; refusing replacement", s.Name(), prior.NetworkID)
	}
	if s.inputSpec.GetString("existing") != "" && existing == nil {
		return fmt.Errorf("%s: selected existing resource was not found", s.Name())
	}
	if existing != nil {
		if prior == nil && s.inputSpec.GetString("existing") == "" {
			return fmt.Errorf("%s: resource already exists; select it explicitly with existing", s.Name())
		}
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
	if s.saved == nil {
		if saved.Output != nil {
			s.saved = StateFromMap[NetworkState](saved.Output)
		}
		if s.saved == nil && saved.Spec != nil {
			s.saved = StateFromMap[NetworkState](saved.Spec)
		}
		s.meta = saved.Meta
	}

	shouldRemove := s.meta.WasCreated
	if shouldRemove && (s.saved == nil || s.saved.NetworkID == "") {
		return fmt.Errorf("%s: owned network identity is missing", s.Name())
	}

	if s.saved == nil || !shouldRemove {
		if err := write(ctx, s.StateData()); err != nil {
			return fmt.Errorf("persist step state after destroy skip: %w", err)
		}
		return nil
	}

	item, err := s.op.NetworkGet(
		ctx,
		inputs.NetworkInput{Identifiers: []string{s.saved.NetworkID}, IncludeDeleted: true},
	)
	if err != nil {
		if errs.IsNotFound(err) {
			return write(ctx, s.StateData())
		}
		return err
	}
	if item == nil {
		return write(ctx, s.StateData())
	}
	if item.ID != s.saved.NetworkID {
		return fmt.Errorf("%s: saved network identity does not match resolved resource", s.Name())
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
		Spec:   s.inputSpec,
		Output: StructToMap(s.saved),
		Meta:   s.meta,
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
	input, err := networkInputFromSpec(name, spec)
	if err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, err
	}

	return &NetworkStep{
		stepType:  stepType,
		name:      name,
		deps:      spec.GetStringList("depends_on"),
		removes:   spec.GetStringList("removes"),
		specHash:  crypto.SHA256(data),
		input:     input,
		inputSpec: spec,
		op:        op,
	}, nil
}

func networkInputFromSpec(name string, spec model.ResourceMap) (inputs.NetworkCreateInput, error) {
	var input inputs.NetworkCreateInput
	if err := decodeStepInput(spec, &input); err != nil {
		return input, err
	}
	if input.Name == "" {
		input.Name = name
	}
	_, natSpecified := spec["nat_enabled"]
	if !natSpecified {
		input.NATEnabled = true
	}
	switch input.NATUplink {
	case "auto":
		if !input.NATEnabled {
			return input, errors.New("network nat_uplink auto requires NAT")
		}
		input.NATUplink = ""
	case "none":
		if natSpecified && input.NATEnabled {
			return input, errors.New("network nat_uplink none conflicts with nat_enabled true")
		}
		input.NATEnabled = false
		input.NATUplink = ""
	}
	return input, nil
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

	var ns *NetworkState
	if saved.Output != nil {
		ns = StateFromMap[NetworkState](saved.Output)
	}
	if ns == nil && saved.Spec != nil {
		ns = StateFromMap[NetworkState](saved.Spec)
	}
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
