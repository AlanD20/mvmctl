package host

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository matches Python's mvmctl.core.host._repository.Repository.
type Repository interface {
	// Count returns total count of all host state changes.
	Count(ctx context.Context) (int, error)

	// GetState returns the singleton host state row, or nil if not yet initialized.
	GetState(ctx context.Context) (*model.HostStateItem, error)

	// InitializeState inserts the singleton row (id=1) if it doesn't exist.
	InitializeState(ctx context.Context) (*model.HostStateItem, error)

	// SetInitialized marks host as fully initialized.
	SetInitialized(ctx context.Context, initializedAt string) error

	// UpdateComponent updates a single host initialization component flag.
	UpdateComponent(ctx context.Context, component string, value bool) error

	// ResetState resets all host state flags to False (for mvm host reset).
	ResetState(ctx context.Context) error

	// SaveCapacity upserts host capacity detection results into host_state row id=1.
	SaveCapacity(ctx context.Context,
		hostname string,
		cpuModel string,
		cpuVendor string,
		cpuCores int,
		cpuArchitecture string,
		numaNodes int,
		memoryTotalMiB int,
		storageTotalBytes int,
		kernelVersion string,
		osRelease string,
		pidMax int,
		fdMax int,
		conntrackMax int,
		tapDevicesMax int,
		ipLocalPortRange [2]int,
		detectedAt string,
		cpuHasVMX bool,
		cpuHypervisor bool,
		nestedVirtAvailable bool,
		eptAvailable bool,
		hugepageCount2MB int,
		ksmDisabled bool,
		cgroupVersion int,
		swapTotalMiB int,
		kernelMinimumMet bool,
	) error

	// AddChange records a host configuration change.
	AddChange(ctx context.Context, change *model.HostStateChangeItem) error

	// AddChanges bulk inserts host state changes atomically.
	AddChanges(ctx context.Context, changes []*model.HostStateChangeItem) error

	// DeleteChangesExceptSession deletes all host state changes except for the given session.
	DeleteChangesExceptSession(ctx context.Context, sessionID string) error

	// ListChanges returns host state changes, optionally filtered by session.
	ListChanges(ctx context.Context, sessionID *string, includeReverted bool) ([]*model.HostStateChangeItem, error)

	// MarkChangeReverted marks a single host change as reverted.
	MarkChangeReverted(ctx context.Context, changeID int, revertedAt string, revertMechanism *string) error

	// RevertChanges marks all unreverted changes for a session as reverted (LIFO order).
	RevertChanges(ctx context.Context, sessionID string, revertedAt string) ([]*model.HostStateChangeItem, error)
}
