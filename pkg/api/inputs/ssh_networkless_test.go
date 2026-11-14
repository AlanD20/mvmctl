package inputs_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestSSHResolveRejectsNetworklessVMBeforeDial(t *testing.T) {
	vmID := strings.Repeat("a", 32)
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
		ID: vmID, Name: "offline", NetworkPlacement: model.VMNetworkPlacementNetworkless,
	}))
	resolved, err := (&inputs.SSHInput{Identifier: "offline"}).Resolve(t.Context(), nil, vmRepo, nil)
	require.Nil(t, resolved)
	require.Error(t, err)
	assert.Equal(t, errs.CodeSSHError, errs.AsDomainError(err).Code)
	assert.ErrorContains(t, err, "use mvm exec over vsock")
}
