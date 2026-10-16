package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAgentTokenFailsClosedForRequestedTokenFile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path    string
		content []byte
		readErr error
		wantErr string
	}{
		"missing_path": {wantErr: "token file path"},
		"read_failure": {
			path: "/etc/mvmctl/agent.token", readErr: errors.New("permission denied"), wantErr: "read agent token file",
		},
		"empty_file": {
			path: "/etc/mvmctl/agent.token", content: []byte(" \n\t"), wantErr: "is empty",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := resolveAgentToken("", tc.path, func(path string) ([]byte, error) {
				assert.Equal(t, tc.path, path)
				return tc.content, tc.readErr
			})
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Empty(t, got)
		})
	}
}

func TestResolveAgentTokenUsesExplicitValueWithoutReadingFile(t *testing.T) {
	t.Parallel()

	read := false
	got, err := resolveAgentToken("explicit-token", "/missing", func(string) ([]byte, error) {
		read = true
		return nil, errors.New("must not read")
	})
	require.NoError(t, err)
	assert.Equal(t, "explicit-token", got)
	assert.False(t, read)
}

func TestResolveAgentTokenLoadsTrimmedPersistentValue(t *testing.T) {
	t.Parallel()

	got, err := resolveAgentToken("", "/etc/mvmctl/agent.token", func(string) ([]byte, error) {
		return []byte("persisted-token\n"), nil
	})
	require.NoError(t, err)
	assert.Equal(t, "persisted-token", got)
}
