package jailer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSnapshotRestoreAdmissionCloseFailureNeverRetriesOrRecloses(t *testing.T) {
	// Rationale: a cleanup failure accompanying contention must stay terminal, and
	// repeated release must not close a reused descriptor number.
	fd := openProtectedBranchTestFD(t, t.TempDir())
	admission := &protectedBackingAdmission{retained: []int{fd}}
	require.NoError(t, unix.Close(fd))
	closeErr := admission.Release(context.Background())
	assert.ErrorIs(t, closeErr, unix.EBADF)
	busy := instanceLockContextError("protected backing object", unix.EAGAIN)
	assert.True(t, restoreProtectedLockContention(errors.Join(busy)))
	assert.False(t, restoreProtectedLockContention(errors.Join(busy, closeErr)),
		"successful partial cleanup is mandatory before retry")
	assert.False(t, restoreProtectedLockContention(managedVMResourceAtomicError("read backing", unix.EAGAIN)),
		"non-flock I/O is terminal even when it has the same errno")
	reused := openProtectedBranchTestFD(t, t.TempDir())
	if reused != fd {
		require.NoError(t, unix.Dup3(reused, fd, unix.O_CLOEXEC))
		require.NoError(t, unix.Close(reused))
	}
	defer func() { require.NoError(t, unix.Close(fd)) }()
	assert.ErrorIs(t, admission.Release(context.Background()), unix.EBADF)
	var stat unix.Stat_t
	assert.NoError(t, unix.Fstat(fd, &stat), "cached cleanup must leave the replacement descriptor open")
}
