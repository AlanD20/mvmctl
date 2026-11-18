package env

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/errs"
)

// resourceStepRecord keeps declarations and their acquired identities together.
type resourceStepRecord struct {
	stepType string
	name     string
	deps     []string
	removes  []string
	specHash string
	record   model.ResourceState
}

func (s *resourceStepRecord) Type() string                   { return s.stepType }
func (s *resourceStepRecord) Name() string                   { return FormatStepName(s.stepType, s.name) }
func (s *resourceStepRecord) Dependencies() []string         { return append([]string(nil), s.deps...) }
func (s *resourceStepRecord) Removes() []string              { return append([]string(nil), s.removes...) }
func (s *resourceStepRecord) SpecHash() string               { return s.specHash }
func (s *resourceStepRecord) StateData() model.ResourceState { return s.record }

func newResourceStepRecord(stepType, name string, spec model.ResourceMap) (resourceStepRecord, []byte, error) {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return resourceStepRecord{}, nil, err
	}
	hash := crypto.SHA256(data)
	return resourceStepRecord{stepType: stepType, name: name, deps: spec.GetStringList("depends_on"),
		removes: spec.GetStringList("removes"), specHash: hash,
		record: model.ResourceState{Spec: spec, Meta: model.ResourceMeta{SpecHash: hash}}}, data, nil
}

func (s *resourceStepRecord) restore(saved model.ResourceState) error {
	if saved.Output == nil {
		if saved.Meta.WasCreated {
			return resourceIdentityMismatch(s.Name())
		}
		return nil
	}
	if saved.Meta.SpecHash == "" || saved.Meta.SpecHash != s.specHash {
		return errs.New(
			errs.CodeValidationFailed,
			s.Name()+": declaration changed; restore the saved declaration or destroy the step before reapplying",
		)
	}
	s.record = saved
	return nil
}

func (s *resourceStepRecord) persist(ctx context.Context, write workflow.StateWriter) error {
	if err := write(ctx, s.record); err != nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			s.Name()+": failed to persist resource identity; preserve the state and retry",
			err,
		)
	}
	return nil
}

func resourceStepReference(state *workflow.SharedState, value, expectedType string) (string, error) {
	if !strings.HasPrefix(value, "@") {
		return value, nil
	}
	key := strings.TrimPrefix(value, "@")
	if !strings.HasPrefix(key, expectedType+":") {
		return "", errs.New(errs.CodeValidationFailed, fmt.Sprintf("%q must reference a %s step", value, expectedType))
	}
	raw, ok := state.Get(key)
	if !ok {
		return "", errs.New(errs.CodeValidationFailed, "reference has no applied identity: "+value)
	}
	var id string
	switch output := raw.(type) {
	case *FloatingIPState:
		if expectedType == "floating_ip" && output != nil {
			id = output.ResourceID
		}
	case *VMState:
		if expectedType == "vm" && output != nil {
			id = output.VMID
		}
	case *NetworkState:
		if expectedType == "network" && output != nil {
			id = output.NetworkID
		}
	case *VolumeState:
		if expectedType == "volume" && output != nil {
			id = output.VolumeID
		}
	}
	if id == "" {
		return "", errs.New(errs.CodeValidationFailed, "reference has an invalid resource identity: "+value)
	}
	return id, nil
}

func resourceIdentityMismatch(name string) error {
	return errs.New(
		errs.CodeValidationFailed,
		name+": saved resource identity is missing or replaced; refusing to adopt a replacement",
	)
}
