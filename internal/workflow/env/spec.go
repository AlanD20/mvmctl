package env

import (
	"context"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/errs"

	"gopkg.in/yaml.v3"
)

// EnvSpec is the top-level YAML structure for an environment spec file.
// Known top-level fields (version, ephemeral) are decoded by YAML struct tags.
// All remaining top-level keys (network, vm, copy, etc.) go into Steps via
// the inline tag. Each type section is a map from step name to its params.
//
// Example:
//
//	version: "1"
//	network:
//	  default:
//	    subnet: "172.27.0.0/24"
//	vm:
//	  dev-vm:
//	    network: "@network:default"
//	    depends_on: ["@network:default"]
type EnvSpec struct {
	Version   string                                  `yaml:"version"`
	Ephemeral bool                                    `yaml:"ephemeral"`
	Steps     map[string]map[string]model.ResourceMap `yaml:",inline"`
}

// stripRefPrefix strips the "@" sigil from a single value. In v2 format,
// all cross-resource references use "@type:name" (e.g. "@network:default").
// This normalizes them to "type:name" for internal use. Dependencies and
// removals keep the full "type:name" format; step reference fields (network,
// key, image, etc.) are further reduced to bare names by stripBareName.
func stripRefPrefix(v any) any {
	switch val := v.(type) {
	case string:
		return strings.TrimPrefix(val, "@")
	case []any:
		for i, item := range val {
			if s, ok := item.(string); ok {
				val[i] = strings.TrimPrefix(s, "@")
			}
		}
		return val
	}
	return v
}

// stripSpecRefs applies stripRefPrefix to every value in a ResourceMap.
// This is called on each entry in resolveSpecV1 so factories never see the
// "@" sigil — they work with bare "type:name" references throughout.
func stripSpecRefs(spec model.ResourceMap) {
	for k, v := range spec {
		spec[k] = stripRefPrefix(v)
	}
}

// stripBareName reduces a "type:name" string to just the bare name.
// For values without a colon, returns as-is. This is used by step factories
// to normalize "@type:name" references (stripped of "@" by stripSpecRefs)
// to the bare step name expected by downstream resolvers.
func stripBareName(s string) string {
	_, after, found := strings.Cut(s, ":")
	if found && after != "" {
		return after
	}
	return s
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

	var (
		data []byte
		err  error
	)
	if strings.HasPrefix(specPath, "http://") || strings.HasPrefix(specPath, "https://") {
		data, err = download.New().GetBody(ctx, specPath)
	} else {
		data, err = os.ReadFile(specPath)
	}
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
		for name, entry := range entries {
			if name == "" {
				return nil, fmt.Errorf("env spec %q entry has empty name key", resourceKey)
			}
			// Strip the "@" sigil prefix from all string values in the entry.
			// In v2 format, cross-resource references use "@type:name" (e.g.
			// "@network:default", "@key:main-key"). Internally the system
			// uses "type:name" without the sigil.
			stripSpecRefs(entry)
			step, err := factory.FromSpec(factory.StepType, name, entry, op)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
		}
	}

	return steps, nil
}
