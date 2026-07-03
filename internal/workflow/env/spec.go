package env

import (
	"context"
	"fmt"
	"os"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/errs"

	"gopkg.in/yaml.v3"
)

// EnvSpec is the top-level YAML structure for an environment spec file.
// Known top-level fields (version, ephemeral) are decoded by YAML struct tags.
// All remaining top-level keys (network, vm, copy, etc.) go into Steps via
// the inline tag. resolveSpecV1 filters Steps against Registry, so unknown
// keys are silently ignored.
type EnvSpec struct {
	Version   string                         `yaml:"version"`
	Ephemeral bool                           `yaml:"ephemeral"`
	Steps     map[string][]model.ResourceMap `yaml:",inline"`
}

// ResolveSpec reads a YAML spec file, validates it, and converts each
// entry into a workflow.Step using the appropriate factory from Registry.
func ResolveSpec(ctx context.Context, specPath string, op api.API) (*EnvSpec, []workflow.Step, error) {
	// Ensure op is non-nil — the factory constructors now reject nil op.
	// Diff/Resolve create steps only for Name/SpecHash/Dependencies access,
	// none of which require a real API connection.
	if op == nil {
		op = &api.Operation{}
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("resolve env spec %s: %v", specPath, err),
			err,
		)
	}

	data, err := os.ReadFile(specPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("env spec file not found: %s", specPath))
		}
		return nil, nil, errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("read env spec %s: %v", specPath, err),
			err,
		)
	}

	var spec EnvSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("env spec validation: invalid YAML: %v", err))
	}

	if spec.Version == "" {
		return nil, nil, errs.New(errs.CodeValidationFailed, "env spec version is required")
	}

	var steps []workflow.Step
	var resolveErr error

	switch spec.Version {
	case "1":
		steps, resolveErr = resolveSpecV1(spec, op)
	default:
		return nil, nil, errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("unsupported env spec version: %q (supported: \"1\")", spec.Version))
	}
	if resolveErr != nil {
		return nil, nil, resolveErr
	}

	return &spec, steps, nil
}

func resolveSpecV1(spec EnvSpec, op api.API) ([]workflow.Step, error) {
	var steps []workflow.Step

	for resourceKey, factory := range Registry {
		entries := spec.Steps[resourceKey]
		if len(entries) == 0 {
			continue
		}
		for _, entry := range entries {
			name, ok := entry["name"].(string)
			if !ok || name == "" {
				return nil, fmt.Errorf("env spec %q entry missing required 'name' field", resourceKey)
			}
			step, err := factory.FromSpec(factory.StepType, name, entry, op)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
		}
	}

	return steps, nil
}
