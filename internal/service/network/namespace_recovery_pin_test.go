package network

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRecoveryPinNeverUnlocksWorkerAfterFailedHostRestoration(t *testing.T) {
	t.Parallel()

	fixture := newNamespaceEffectFixture()
	deps := fixture.dependencies()
	restoreFailure := errors.New("injected host namespace restoration failure")
	deps.setns = func(ctx context.Context, fd, namespaceType int) error {
		if namespaceType != unix.CLONE_NEWNET {
			return errors.New("unexpected namespace type")
		}
		switch fd {
		case 17:
			return fixture.recordContext("setns.target", ctx, false)
		case 13:
			return restoreFailure
		default:
			return errors.New("unexpected namespace descriptor")
		}
	}

	fd, err := openRecoveryNamespacePin(
		t.Context(),
		deps,
		fixture.policy(),
		17,
		namespaceIdentity{device: 5, inode: 400},
	)
	assert.Equal(t, -1, fd)
	require.ErrorIs(t, err, restoreFailure)
	assert.Equal(t, 0, fixture.eventCount("thread.unlock"))
	assert.Equal(t, 1, fixture.eventCount("close.14"))
	assert.Equal(t, 1, fixture.eventCount("worker.exit"))
}
