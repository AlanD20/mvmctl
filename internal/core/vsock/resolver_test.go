package vsock_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- GetByVMID ---
// Rationale: Resolver.GetByVMID is the enrichment hook for "vsock" relations.
// If it returns wrong data or fails silently, VMs get incorrect vsock config.

func TestResolver_GetByVMID(t *testing.T) {
	repo := testutil.NewVsockRepo()
	resolver := vsock.NewResolver(repo)

	item := &model.VsockConfigItem{
		ID:       "vsock-1",
		VmID:     "vm-1",
		GuestCID: 3,
		UDSPath:  "/tmp/vm-1.sock",
		Port:     1024,
		Token:    "test-token",
	}
	require.NoError(t, repo.Upsert(ctx, item))

	got, err := resolver.GetByVMID(ctx, "vm-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, item.ID, got.ID)
	assert.Equal(t, item.VmID, got.VmID)
	assert.Equal(t, item.GuestCID, got.GuestCID)
	assert.Equal(t, item.UDSPath, got.UDSPath)
	assert.Equal(t, item.Port, got.Port)
	assert.Equal(t, item.Token, got.Token)
}

// --- GetByVMID: Not found ---
// Rationale: When no vsock config exists for a VM, the resolver must return
// a DomainError with CodeVsockNotFound so the enricher can soft-fail.

func TestResolver_GetByVMID_NotFound(t *testing.T) {
	repo := testutil.NewVsockRepo()
	resolver := vsock.NewResolver(repo)

	_, err := resolver.GetByVMID(ctx, "nonexistent-vm")
	require.Error(t, err)

	var de *errs.DomainError
	ok := errors.As(err, &de)
	require.True(t, ok, "error must be a *DomainError")
	assert.Equal(t, errs.CodeVsockNotFound, de.Code)
	assert.Contains(t, de.Message, "nonexistent-vm")
}
