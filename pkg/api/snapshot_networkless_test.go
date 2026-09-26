package api

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

// Rationale: until captured snapshot authority carries networkless placement,
// reject before any vsock lookup or root snapshot effect can start.
func TestSnapshotCreateRejectsNetworklessBeforeEffects(t *testing.T) {
	vmID := strings.Repeat("a", 32)
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
		ID: vmID, Name: "offline", NetworkPlacement: model.VMNetworkPlacementNetworkless,
	}))
	op := &Operation{Repos: Repos{VM: vmRepo}}
	item, err := op.SnapshotCreate(t.Context(), inputs.SnapshotCreateInput{Identifier: vmID}, nil)
	require.Nil(t, item)
	require.Error(t, err)
	assert.Equal(t, errs.CodeSnapshotCreateFailed, errs.AsDomainError(err).Code)
	assert.ErrorContains(t, err, "networkless volume-consistent restore")
}
