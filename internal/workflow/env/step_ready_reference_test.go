package env_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestReadyReferenceRequiresDependencyAndUsesCompletedVMIdentity(t *testing.T) {
	// Rationale: factory normalization used to erase @vm:web before graph admission and
	// execution, silently treating the step alias as a VM selector instead of using its output.
	for _, tc := range []struct {
		name       string
		dependency string
		wantError  bool
	}{
		{name: "missing dependency", wantError: true},
		{name: "completed differently named VM", dependency: "    depends_on: [vm:web]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const vmID = "abcdef0123456789abcdef0123456789"
			var target, command string
			op := &testutil.MockOperation{}
			op.ExecReadyFunc = func(_ context.Context, in inputs.ReadyInput) (*results.ExecResult, error) {
				target, command = in.Identifier, in.Command
				return &results.ExecResult{}, nil
			}
			path := filepath.Join(t.TempDir(), "env.yaml")
			content := fmt.Sprintf(`version: "1"
vm:
  web:
    existing: differently-named-guest
ready:
  health:
    target: "@vm:web"
    cmd: "test -f /tmp/@literal"
%s`, tc.dependency)
			require.NoError(t, os.WriteFile(path, []byte(content), 0600))
			_, steps, err := envpkg.ResolveSpec(t.Context(), path, op)
			if tc.wantError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "depends_on")
				assert.Empty(t, target, "invalid readiness graph must never execute a guest command")
				return
			}
			require.NoError(t, err)
			var ready workflow.Step
			for _, step := range steps {
				if step.Type() == "ready" {
					ready = step
				}
			}
			require.NotNil(t, ready)
			state := workflow.NewSharedState()
			state.Set("vm:web", &envpkg.VMState{VMID: vmID})
			var saved model.ResourceState
			require.NoError(
				t,
				ready.Apply(
					t.Context(),
					state,
					model.ResourceState{},
					func(_ context.Context, value model.ResourceState) error {
						saved = value
						return nil
					},
					nil,
				),
			)
			assert.Equal(t, vmID, target, "guest readiness must target the completed VM, not the step alias")
			assert.Equal(t, "test -f /tmp/@literal", command)
			assert.Equal(t, vmID, saved.Output["target"])
			assert.Equal(t, "@vm:web", saved.Spec["target"], "persist the declaration independently of resolved output")
		})
	}
}
