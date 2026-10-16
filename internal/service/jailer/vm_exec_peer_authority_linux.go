//go:build linux

package jailer

import (
	"context"

	"golang.org/x/sys/unix"
)

func realVMExecPeerAuthorityDeps() vmExecPeerAuthorityDeps {
	return vmExecPeerAuthorityDeps{
		peerCredentials: func(ctx context.Context, connectionFD int) (*unix.Ucred, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return unix.GetsockoptUcred(connectionFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
		},
		peerPIDFD: func(ctx context.Context, connectionFD int) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.GetsockoptInt(connectionFD, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		},
		descriptorFlags: processDescriptorFlags,
		deriveLocator:   deriveProcessLocatorFromPIDFD,
		pidfdAlive:      processPIDFDAlive,
		close:           closeProcessDescriptor,
	}
}
