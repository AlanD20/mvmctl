package system

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandEnvironmentReplacesInheritedValuesWithClosedSet(t *testing.T) {
	t.Parallel()

	environment := commandEnvironment(RunCmdOpts{
		Env:        map[string]string{"LC_ALL": "C", "LANG": "C"},
		ReplaceEnv: true,
	})
	want := []string{"LANG=C", "LC_ALL=C"}
	if diff := cmp.Diff(want, environment); diff != "" {
		t.Errorf("closed command environment mismatch (-want +got):\n%s", diff)
	}
}

func TestBoundedCaptureWriterRejectsOverflowWithoutRetainingExcess(t *testing.T) {
	t.Parallel()

	writer := newBoundedCaptureWriter(4)
	written, err := writer.Write([]byte("abcdef"))
	require.Error(t, err)
	assert.ErrorIs(t, err, errCommandOutputLimit)
	assert.Equal(t, 4, written)
	assert.Equal(t, "abcd", writer.String())
	assert.True(t, writer.Exceeded())

	written, err = writer.Write([]byte("z"))
	assert.Equal(t, 0, written)
	assert.ErrorIs(t, err, errCommandOutputLimit)
	assert.Equal(t, "abcd", writer.String())
}

func TestCommandOutputLimitErrorTakesPriorityOverProcessFailure(t *testing.T) {
	t.Parallel()

	stdout := newBoundedCaptureWriter(4)
	_, writeErr := stdout.Write([]byte("oversized"))
	require.Error(t, writeErr)
	stderr := newBoundedCaptureWriter(8)

	result, err := capturedRunResult(stdout, stderr, errors.New("process stopped"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "stdout exceeded")
	assert.Equal(t, -1, result.ExitCode)
	assert.Equal(t, "over", result.Stdout)
	assert.Empty(t, result.Stderr)
}
