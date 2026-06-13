package env

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
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

// ResolveWorkflowID returns the workflow ID for a given spec path or ID.
// If the input looks like a file path (contains / or .), it's treated as
// a spec path and hashed. Otherwise it tries exact match, then prefix match
// against existing workflow state directories.
func ResolveWorkflowID(specOrID string) string {
	if strings.Contains(specOrID, "/") || strings.Contains(specOrID, "\\") || strings.Contains(specOrID, ".") {
		return crypto.WorkflowID(specOrID)
	}

	statesDir := infra.GetWorkflowsStateDir()
	// Try exact match first
	if _, err := os.Stat(filepath.Join(statesDir, specOrID)); err == nil {
		return specOrID
	}
	// Prefix match: scan directories for one starting with the given prefix
	entries, err := os.ReadDir(statesDir)
	if err != nil {
		return specOrID
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), specOrID) {
			return entry.Name()
		}
	}
	return specOrID
}

// BareStepName extracts the resource name from the full step name.
// For steps named "type:name", returns "name".
func BareStepName(stepName, stepType string) string {
	prefix := stepType + ":"
	if after, found := strings.CutPrefix(stepName, prefix); found && after != "" {
		return after
	}
	return stepName
}

// resolveDiffInput resolves specOrID to a spec file path and state directory.
// If specOrID matches a workflow ID (exact or prefix), the spec path is read
// from the saved state. Otherwise it's treated as a file path.
func resolveDiffInput(specOrID string) (specPath, stateDir string) {
	wfID := ResolveWorkflowID(specOrID)
	sd := infra.GetWorkflowsStateDirByID(wfID)

	if _, err := os.Stat(sd); err == nil {
		// Resolved as workflow ID — read saved spec path.
		if wfState, rErr := workflow.ReadWorkflowState(sd); rErr == nil && wfState != nil && wfState.SpecPath != "" {
			return wfState.SpecPath, sd
		}
	}

	// Not a workflow ID — use as file path, no state dir.
	return specOrID, ""
}
