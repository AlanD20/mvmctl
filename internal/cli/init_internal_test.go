package cli

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

func TestInitStatePreservesExplicitBinaryVersionAfterSuccessfulSudo(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("y\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	originalStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = originalStdin
		require.NoError(t, reader.Close())
	})

	originalRunner := system.DefaultRunner
	system.DefaultRunner = &testutil.FakeRunner{}
	t.Cleanup(func() { system.DefaultRunner = originalRunner })

	statusCalls := 0
	state := &initState{
		initAPI: &testutil.MockInitAPI{
			InitCheckReadinessFunc: func(context.Context) *model.ProbeResult {
				return &model.ProbeResult{}
			},
		},
		hostAPI: &testutil.MockHostAPI{
			HostStatusCheckFunc: func(context.Context) *results.HostStatusCheck {
				statusCalls++
				if statusCalls == 1 {
					return &results.HostStatusCheck{}
				}
				return &results.HostStatusCheck{
					GroupExists:   true,
					SudoersExists: true,
					UserInGroup:   true,
				}
			},
		},
		downloadVersion: "1.15.2",
	}

	err = state.handleSudoRequired(context.Background(), &errs.NeedsInteraction{
		Code:    "privilege.sudo_required",
		Context: map[string]any{"command": "sudo mvm host init"},
	})
	require.NoError(t, err)
	assert.True(t, state.sudoCompleted)
	assert.Equal(t, "1.15.2", state.downloadVersion)
	assert.Equal(t, 2, statusCalls)
}
