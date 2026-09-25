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

func TestExecPolicyCommandsPassFamilySpecificInputs(t *testing.T) {
	var createInput inputs.VMToVMExecPolicyCreateInput
	var removeInput inputs.VMToVMExecPolicyInput
	mock := &testutil.MockPolicyAPI{
		ExecPolicyCreateFunc: func(
			_ context.Context,
			input inputs.VMToVMExecPolicyCreateInput,
		) (*results.VMToVMExecPolicy, error) {
			createInput = input
			return &results.VMToVMExecPolicy{ID: "exec-policy"}, nil
		},
		ExecPolicyRemoveFunc: func(_ context.Context, input inputs.VMToVMExecPolicyInput) error {
			removeInput = input
			return nil
		},
	}

	cmd := cli.NewPolicyCmd(mock)
	create, _, err := cmd.Find([]string{"exec", "create"})
	require.NoError(t, err)
	require.NoError(t, create.RunE(create, []string{"source", "destination", "app"}))
	assert.Equal(t, inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "source", DestinationVM: "destination", TargetUser: "app",
	}, createInput)

	remove, _, err := cmd.Find([]string{"exec", "rm"})
	require.NoError(t, err)
	require.NoError(t, remove.Flags().Set("force", "true"))
	require.NoError(t, remove.RunE(remove, []string{"a", "b"}))
	assert.Equal(t, []string{"a", "b"}, removeInput.Identifiers)
}

func TestExecPolicyListJSONUsesArrayForNoPolicies(t *testing.T) {
	mock := &testutil.MockPolicyAPI{
		ExecPolicyListFunc: func(context.Context) ([]*results.VMToVMExecPolicy, error) { return nil, nil },
	}
	cmd := cli.NewPolicyCmd(mock)
	list, _, err := cmd.Find([]string{"exec", "ls"})
	require.NoError(t, err)
	require.NoError(t, list.Flags().Set("json", "true"))
	output := capturePolicyStdout(t, func() error { return list.RunE(list, nil) })
	var decoded []any
	require.NoError(t, json.Unmarshal([]byte(output), &decoded))
	assert.Empty(t, decoded)
}
