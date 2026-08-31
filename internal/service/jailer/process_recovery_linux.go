package jailer

import (
	"context"

	"golang.org/x/sys/unix"
)

func realProcessRecoveryDeps() processRecoveryDeps {
	return processRecoveryDeps{
		pidfdOpen: func(ctx context.Context, pid int) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.PidfdOpen(pid, 0)
		},
		pidfdAlive:           processPIDFDAlive,
		descriptorFlags:      processDescriptorFlags,
		openProcessDirectory: openFixedProcessDirectory,
		readProcessMetadata:  readFixedProcessMetadata,
		close:                closeProcessDescriptor,
	}
}
