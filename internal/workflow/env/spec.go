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
// The Registry map defines which top-level keys are valid step types —
// any key not in Registry (other than "version") is silently ignored.
type EnvSpec struct {
	Version string                         `yaml:"version"`
	Steps   map[string][]model.ResourceMap `yaml:"-"` // populated by UnmarshalYAML
}

// UnmarshalYAML decodes a YAML mapping into EnvSpec. The "version" key is
// decoded explicitly; all remaining keys that match an entry in Registry are
// decoded as []model.ResourceMap and stored in Steps.
func (s *EnvSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("env spec must be a mapping")
	}

	s.Steps = make(map[string][]model.ResourceMap)

	for i := 0; i < len(value.Content); i += 2 {
		keyNode := value.Content[i]
		valNode := value.Content[i+1]

		var key string
		if err := keyNode.Decode(&key); err != nil {
			return fmt.Errorf("env spec: invalid key: %v", err)
		}

		if key == "version" {
			if err := valNode.Decode(&s.Version); err != nil {
				return fmt.Errorf("env spec: invalid version: %v", err)
			}
			continue
		}

		if _, ok := Registry[key]; !ok {
			continue // silently skip unknown keys
		}

		var specs []model.ResourceMap
		if err := valNode.Decode(&specs); err != nil {
			return fmt.Errorf("env spec %q: %v", key, err)
		}
		s.Steps[key] = specs
	}

	return nil
}

// ResolveSpec reads a YAML spec file, validates it, and converts each
// entry into a workflow.Step using the appropriate factory from Registry.
func ResolveSpec(ctx context.Context, specPath string, op *api.Operation) ([]workflow.Step, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("resolve env spec %s: %v", specPath, err),
			err,
		)
	}

	data, err := os.ReadFile(specPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("env spec file not found: %s", specPath))
		}
		return nil, errs.WrapMsg(
			errs.CodeInternal,
			fmt.Sprintf("read env spec %s: %v", specPath, err),
			err,
		)
	}

	var spec EnvSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("env spec validation: invalid YAML: %v", err))
	}

	if spec.Version == "" {
		return nil, errs.New(errs.CodeValidationFailed, "env spec version is required")
	}

	var steps []workflow.Step
	var resolveErr error

	switch spec.Version {
	case "1":
		steps, resolveErr = resolveSpecV1(spec, op)
	default:
		return nil, errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("unsupported env spec version: %q (supported: \"1\")", spec.Version))
	}
	if resolveErr != nil {
		return nil, resolveErr
	}

	return steps, nil
}

func resolveSpecV1(spec EnvSpec, op *api.Operation) ([]workflow.Step, error) {
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
