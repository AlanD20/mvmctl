package vsock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

type failingVsockRepo struct {
	vsock.Repository
	err         error
	upsertCalls int
}

func (r *failingVsockRepo) Upsert(_ context.Context, _ *model.VsockConfigItem) error {
	r.upsertCalls++
	return r.err
}

// Rationale: reserved or unrepresentable CIDs must fail before persistence so
// retry allocation cannot turn invalid caller intent into a different device.
func TestServicePersistConfigRejectsInvalidCIDBeforeRepositoryWrite(t *testing.T) {
	for name, cid := range map[string]int{
		"reserved":      model.MinVsockGuestCID - 1,
		"above maximum": int(model.MaxVsockGuestCID) + 1,
	} {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVsockRepo()
			err := vsock.NewService(repo).PersistConfig(
				t.Context(), cid, "vm-1", "example", "/ignored.sock", 1024, "token",
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
			got, getErr := repo.GetByVMID(t.Context(), "vm-1")
			require.NoError(t, getErr)
			assert.Nil(t, got)
		})
	}
}

// Rationale: persistence failures must retain the repository cause while
// crossing the core boundary as the project's single typed error.
func TestServicePersistConfigWrapsRepositoryFailure(t *testing.T) {
	cause := errors.New("database unavailable")
	repo := &failingVsockRepo{Repository: testutil.NewVsockRepo(), err: cause}

	err := vsock.NewService(repo).PersistConfig(
		t.Context(), 42, "vm-1", "example", "/vsock.sock", 1024, "token",
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeDatabaseError, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, 1, repo.upsertCalls)
}

// Rationale: Firecracker processes have independent CID namespaces. Snapshot
// restore must persist the CID fixed in vmstate even while its source VM keeps
// the same CID, without silently selecting a different value.
func TestServicePersistConfigAllowsExactCIDInIndependentVMNamespace(t *testing.T) {
	database := testutil.NewInMemoryDB(t)
	_, err := database.ExecContext(t.Context(), "PRAGMA foreign_keys = OFF")
	require.NoError(t, err)
	repo := vsock.NewRepository(database)
	require.NoError(t, repo.Upsert(t.Context(), &model.VsockConfigItem{
		ID: "vsock-existing", VmID: "vm-existing", GuestCID: 42,
		UDSPath: "/existing.sock", Port: 1024, Token: "existing-token",
	}))

	err = vsock.NewService(repo).PersistConfig(
		t.Context(), 42, "vm-restored", "restored", "/restored.sock", 2048, "restored-token",
	)
	require.NoError(t, err)

	got, getErr := repo.GetByVMID(t.Context(), "vm-restored")
	require.NoError(t, getErr)
	require.NotNil(t, got)
	assert.Equal(t, 42, got.GuestCID)
	existing, getErr := repo.GetByVMID(t.Context(), "vm-existing")
	require.NoError(t, getErr)
	require.NotNil(t, existing)
	assert.Equal(t, 42, existing.GuestCID)
}
