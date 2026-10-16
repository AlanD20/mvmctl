package env

import (
	"testing"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
)

// TestVMFromSpec_AllowsEmptyOp ensures FromSpec accepts an empty Operation.
func TestVMFromSpec_AllowsEmptyOp(t *testing.T) {
	spec := model.ResourceMap{
		"name":    "test-vm",
		"vcpu":    2,
		"network": "test-net",
	}

	_, err := Registry["vm"].FromSpec("vm", "test-vm", spec, &api.Operation{})
	if err != nil {
		t.Fatalf("FromSpec with empty Operation: %v", err)
	}
}

// TestVMDependencies asserts that the env.Dependencies() helper includes
// the correct reference dependencies from spec.
func TestVMDependencies(t *testing.T) {
	tests := []struct {
		name     string
		spec     model.ResourceMap
		stepName string
		wantDeps []string
	}{
		{
			name: "no_deps",
			spec: model.ResourceMap{
				"name": "test-vm",
			},
			wantDeps: nil,
		},
		{
			name: "resource_refs_only",
			spec: model.ResourceMap{
				"name":    "test-vm",
				"network": "my-net",
				"key":     "my-key",
				"image":   "my-image",
			},
			wantDeps: nil, // resource refs don't create implicit DAG deps
		},
		{
			name: "explicit_depends_on",
			spec: model.ResourceMap{
				"name":       "test-vm",
				"depends_on": []any{"network:my-net", "key:my-key"},
			},
			wantDeps: []string{"network:my-net", "key:my-key"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			step, err := Registry["vm"].FromSpec("vm", "test-vm", tc.spec, &api.Operation{})
			if err != nil {
				t.Fatalf("FromSpec: %v", err)
			}
			deps := step.Dependencies()

			if len(deps) != len(tc.wantDeps) {
				t.Errorf("Dependencies() = %v, want %v", deps, tc.wantDeps)
				return
			}
			for i := range deps {
				if deps[i] != tc.wantDeps[i] {
					t.Errorf("Dependencies()[%d] = %q, want %q", i, deps[i], tc.wantDeps[i])
				}
			}
		})
	}
}
