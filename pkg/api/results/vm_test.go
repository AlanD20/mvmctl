package results_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/api/results"
)

func TestVMItemInfoOmitsRemovedRemoteExecField(t *testing.T) {
	encoded, err := json.Marshal(results.VMItemInfo{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "allow_remote_exec")
}
