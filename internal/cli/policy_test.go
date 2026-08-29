package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func capturePolicyStdout(t *testing.T, run func() error) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = writer
	err = run()
	require.NoError(t, writer.Close())
	os.Stdout = original
	require.NoError(t, err)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return string(data)
}

func TestNewPolicyCmdShape(t *testing.T) {
	cmd := cli.NewPolicyCmd(&testutil.MockPolicyAPI{})
	assert.Equal(t, "policy", cmd.Use)
	for _, name := range []string{"create", "ls", "inspect", "rm", "sync"} {
		sub, _, err := cmd.Find([]string{name})
		require.NoError(t, err)
		require.NotNil(t, sub)
	}
	remove, _, err := cmd.Find([]string{"rm"})
	require.NoError(t, err)
	assert.Equal(t, []string{"remove", "delete", "del"}, remove.Aliases)
	assert.NotNil(t, remove.Flags().Lookup("force"))
	for _, name := range []string{"ls", "inspect", "sync"} {
		sub, _, findErr := cmd.Find([]string{name})
		require.NoError(t, findErr)
		assert.NotNil(t, sub.Flags().Lookup("json"))
	}
}

func TestPolicyCreatePassesTypedArguments(t *testing.T) {
	var got inputs.PolicyCreateInput
	mock := &testutil.MockPolicyAPI{
		PolicyCreateFunc: func(_ context.Context, input inputs.PolicyCreateInput) (*results.Policy, error) {
			got = input
			return &results.Policy{ID: "policy-1"}, nil
		},
	}
	cmd := cli.NewPolicyCmd(mock)
	create, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)
	require.NoError(t, create.Args(create, []string{"source", "vm", "tcp", "443"}))
	require.NoError(t, create.RunE(create, []string{"source", "vm", "tcp", "443"}))
	assert.Equal(t, inputs.PolicyCreateInput{SourceNetwork: "source", DestinationVM: "vm", Protocol: "tcp",
		DestinationPort: "443"}, got)
	require.Error(t, create.Args(create, []string{"source"}))
}

func TestPolicyJSONOutput(t *testing.T) {
	policy := &results.Policy{ID: "policy-1", SourceNetworkName: "source", DestinationVMName: "vm",
		Protocol: model.ServiceAccessPolicyProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443}
	mock := &testutil.MockPolicyAPI{
		PolicyListFunc:    func(context.Context) ([]*results.Policy, error) { return []*results.Policy{policy}, nil },
		PolicyInspectFunc: func(context.Context, inputs.PolicyInput) (*results.Policy, error) { return policy, nil },
		PolicySyncFunc:    func(context.Context) (*results.PolicySync, error) { return &results.PolicySync{Policies: 1}, nil },
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"list", []string{"ls"}}, {"inspect", []string{"inspect", "policy-1"}}, {"sync", []string{"sync"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := cli.NewPolicyCmd(mock)
			sub, remaining, err := cmd.Find(tc.args)
			require.NoError(t, err)
			require.NoError(t, sub.Flags().Set("json", "true"))
			output := capturePolicyStdout(t, func() error { return sub.RunE(sub, remaining) })
			var decoded any
			require.NoError(t, json.Unmarshal([]byte(output), &decoded))
		})
	}
}

func TestPolicyRemoveAliasesAndForce(t *testing.T) {
	var got []string
	mock := &testutil.MockPolicyAPI{PolicyRemoveFunc: func(_ context.Context, input inputs.PolicyInput) error {
		got = input.Identifiers
		return nil
	}}
	for _, alias := range []string{"rm", "remove", "delete", "del"} {
		cmd := cli.NewPolicyCmd(mock)
		remove, _, err := cmd.Find([]string{alias})
		require.NoError(t, err)
		require.NoError(t, remove.Flags().Set("force", "true"))
		require.NoError(t, remove.RunE(remove, []string{"a", "b"}))
		assert.Equal(t, []string{"a", "b"}, got)
	}
}
