package cli_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestHostTrafficCLIRejectsAmbiguousSelectionBeforeMutation(t *testing.T) {
	// Rationale: Selector conflicts must never choose a broader host/network grant as a fallback.
	for name, args := range map[string][]string{
		"no source":                              {"--to-host"},
		"two sources":                            {"--from-network", "net", "--from-vm", "vm", "--to-host"},
		"no destination":                         {"--from-vm", "vm"},
		"two destinations":                       {"--from-vm", "vm", "--to-vm", "other", "--to-host"},
		"empty explicit VM destination and host": {"--from-vm", "vm", "--to-vm=", "--to-host"},
		"false host without VM destination":      {"--from-vm", "vm", "--to-host=false"},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := false
			unexpected := func() error { mutated = true; return errors.New("unexpected policy mutation") }
			op := &testutil.MockPolicyAPI{
				VMToHostTrafficPolicyCreateFunc: func(context.Context, inputs.VMToHostTrafficPolicyCreateInput) (*results.VMToHostTrafficPolicy, error) {
					return nil, unexpected()
				},
				NetworkToHostTrafficPolicyCreateFunc: func(context.Context, inputs.NetworkToHostTrafficPolicyCreateInput) (*results.NetworkToHostTrafficPolicy, error) {
					return nil, unexpected()
				},
				VMToVMTrafficPolicyCreateFunc: func(context.Context, inputs.VMToVMTrafficPolicyCreateInput) (*results.VMToVMTrafficPolicy, error) {
					return nil, unexpected()
				},
				NetworkToVMTrafficPolicyCreateFunc: func(context.Context, inputs.NetworkToVMTrafficPolicyCreateInput) (*results.NetworkToVMTrafficPolicy, error) {
					return nil, unexpected()
				},
			}
			cmd := cli.NewPolicyCmd(op)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(append([]string{"traffic", "create"}, args...))
			require.Error(t, cmd.ExecuteContext(t.Context()))
			assert.False(t, mutated, "invalid source/destination choice must not reach any policy mutation")
		})
	}
}
