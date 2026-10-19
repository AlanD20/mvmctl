package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

func TestApplyRejectsInvalidVMScalarsBeforeDependencies(t *testing.T) {
	// Rationale: VM validation must precede creation of an otherwise valid dependency network.
	for name, field := range map[string]string{"zero_cpu": "vcpu: 0", "bad_memory": "mem: invalid", "zero_memory": "mem: 0", "bad_disk": "disk_size: invalid"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spec.yaml")
			spec := fmt.Sprintf(
				"version: '1'\nnetwork:\n  net:\n    subnet: 10.82.0.0/24\nvm:\n  machine:\n    network: '@network:net'\n    depends_on: ['@network:net']\n    %s\n",
				field,
			)
			require.NoError(t, os.WriteFile(path, []byte(spec), 0600))
			effects := false
			op := &testutil.MockOperation{}
			op.NetworkCreateFunc = func(context.Context, inputs.NetworkCreateInput) (*model.NetworkItem, error) {
				effects = true
				return nil, fmt.Errorf("unexpected dependency creation")
			}
			err := Apply(t.Context(), op, path, nil, nil)
			require.Error(t, err)
			want := strings.Split(field, ":")[0]
			if want == "mem" {
				want = "memory"
			}
			if want == "disk_size" {
				want = "disk size"
			}
			assert.Contains(t, strings.ToLower(err.Error()), want)
			assert.False(t, effects, "invalid VM input must fail before creating network")
			assert.NotContains(t, err.Error(), "unexpected dependency creation")
		})
	}
}

func TestApplyRejectsInvalidGraphBeforeEffects(t *testing.T) {
	// Rationale: admission must reject the entire document before any valid sibling can create a resource.
	for name, spec := range map[string]string{
		"unknown section":    "volumes: {}\n",
		"unknown field":      "network:\n  fixture-net:\n    subnet: 10.82.0.0/24\n    typo: true\n",
		"missing dependency": "vm:\n  guest:\n    depends_on: [network:absent]\n",
		"missing reference":  "vm:\n  guest:\n    network: '@network:absent'\n",
		"reference type":     "key:\n  identity: {}\nvm:\n  guest:\n    network: '@key:identity'\n    depends_on: [key:identity]\n",
		"cycle":              "key:\n  first:\n    depends_on: [key:second]\n  second:\n    depends_on: [key:first]\n",
		"batch ownership":    "vm:\n  guest:\n    count: 2\n",
		"kernel removal":     "kernel:\n  boot:\n    type: firecracker\nkey:\n  identity:\n    removes: [kernel:boot]\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spec.yaml")
			require.NoError(t, os.WriteFile(path, []byte("version: '1'\n"+spec), 0600))
			// Require resolution failure, not an error from an effect using an uninitialized API.
			err := Apply(t.Context(), nil, path, nil, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "resolve env spec")
			if name == "kernel removal" {
				assert.Contains(t, err.Error(), "kernel removes is unsupported")
			}
			assert.NotContains(t, err.Error(), "operation not initialized")
		})
	}
}
