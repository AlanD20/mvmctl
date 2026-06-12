package env

import (
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
)

// extractDependsOn reads the "depends_on" field from a spec entry and
// returns it as a []string of full step names (e.g. "network:my-net").
// Returns nil if the field is missing or not a list of strings.
func extractDependsOn(spec model.ResourceMap) []string {
	v, ok := spec["depends_on"]
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	deps := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			deps = append(deps, s)
		}
	}
	return deps
}

// InferStepType extracts the step type from a full step name.
// For steps named "type:name", returns "type". Falls back to "unknown".
// This is the canonical function; extractStepType in the api package delegates to it.
func InferStepType(stepName string) string {
	idx := strings.Index(stepName, ":")
	if idx > 0 {
		return stepName[:idx]
	}
	return "unknown"
}

// FormatStepName returns a display name for a step.
func FormatStepName(stepType, name string) string {
	return fmt.Sprintf("%s:%s", stepType, name)
}

// StateFromMap converts a model.ResourceMap to a typed state struct via YAML
// round-trip. This is used by step implementations to unmarshal persisted
// state back into a concrete state struct.
func StateFromMap[T any](m model.ResourceMap) *T {
	if m == nil {
		return nil
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		slog.Error("StateFromMap marshal failed", "error", err)
		return nil
	}
	var result T
	if err := yaml.Unmarshal(data, &result); err != nil {
		slog.Error("StateFromMap unmarshal failed", "error", err)
		return nil
	}
	return &result
}

// StructToMap converts a struct to model.ResourceMap via YAML round-trip.
// The returned map uses the yaml tags as keys.
func StructToMap(v any) model.ResourceMap {
	if v == nil {
		return nil
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil
	}
	var m model.ResourceMap
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}
