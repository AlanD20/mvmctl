package loopmount

const maxRootfsPartitionCount = 16

type rootfsBlockIdentity struct {
	major uint32
	minor uint32
}

type rootfsFilesystemProbe struct {
	identity   rootfsBlockIdentity
	partition  uint8
	filesystem Filesystem
}

type rootfsMountSourceKind uint8

const (
	rootfsMountSourceRaw rootfsMountSourceKind = iota + 1
	rootfsMountSourcePartition
)

type rootfsMountSource struct {
	kind      rootfsMountSourceKind
	identity  rootfsBlockIdentity
	partition uint8
}

// selectRootfsMountSource applies the closed raw-or-single-matching-partition policy to receiver-derived probes.
// The probes contain no caller path or device name; discovery must verify those identities before invoking this helper.
func selectRootfsMountSource(
	expected Filesystem,
	raw rootfsFilesystemProbe,
	partitions []rootfsFilesystemProbe,
) (rootfsMountSource, error) {
	if err := expected.Validate(); err != nil {
		return rootfsMountSource{}, rootfsAtomicError("requested rootfs filesystem is invalid", err)
	}
	if !validRootfsBlockIdentity(raw.identity) || raw.partition != 0 {
		return rootfsMountSource{}, rootfsAtomicError("raw block-device probe is invalid", nil)
	}
	if err := validateRootfsProbedFilesystem(raw.filesystem); err != nil {
		return rootfsMountSource{}, rootfsAtomicError("raw probed filesystem is invalid", err)
	}
	if len(partitions) > maxRootfsPartitionCount {
		return rootfsMountSource{}, rootfsAtomicError("rootfs partition count exceeds the supported bound", nil)
	}

	seenPartitions := make(map[uint8]struct{}, len(partitions))
	seenIdentities := make(map[rootfsBlockIdentity]struct{}, len(partitions))
	for _, partition := range partitions {
		if !validRootfsBlockIdentity(partition.identity) {
			return rootfsMountSource{}, rootfsAtomicError("partition block-device identity is invalid", nil)
		}
		if partition.identity == raw.identity {
			return rootfsMountSource{}, rootfsAtomicError(
				"partition block-device identity matches the raw device",
				nil,
			)
		}
		if partition.partition == 0 || partition.partition > maxRootfsPartitionCount {
			return rootfsMountSource{}, rootfsAtomicError("partition number is outside the supported bound", nil)
		}
		if _, exists := seenPartitions[partition.partition]; exists {
			return rootfsMountSource{}, rootfsAtomicError("partition number is duplicated", nil)
		}
		seenPartitions[partition.partition] = struct{}{}
		if _, exists := seenIdentities[partition.identity]; exists {
			return rootfsMountSource{}, rootfsAtomicError("partition block-device identity is duplicated", nil)
		}
		seenIdentities[partition.identity] = struct{}{}
		if err := validateRootfsProbedFilesystem(partition.filesystem); err != nil {
			return rootfsMountSource{}, rootfsAtomicError("partition probed filesystem is invalid", err)
		}
	}

	if len(partitions) == 0 {
		if raw.filesystem != expected {
			return rootfsMountSource{}, rootfsAtomicError("raw rootfs filesystem does not match the request", nil)
		}
		return rootfsMountSource{kind: rootfsMountSourceRaw, identity: raw.identity}, nil
	}
	if raw.filesystem == expected {
		return rootfsMountSource{}, rootfsAtomicError("raw filesystem and partition layout are ambiguous", nil)
	}

	var selected rootfsFilesystemProbe
	matches := 0
	for _, partition := range partitions {
		if partition.filesystem != expected {
			continue
		}
		selected = partition
		matches++
	}
	switch matches {
	case 0:
		return rootfsMountSource{}, rootfsAtomicError("rootfs layout has no matching root partition", nil)
	case 1:
		return rootfsMountSource{
			kind:      rootfsMountSourcePartition,
			identity:  selected.identity,
			partition: selected.partition,
		}, nil
	default:
		return rootfsMountSource{}, rootfsAtomicError("rootfs layout has multiple matching root partitions", nil)
	}
}

func validateRootfsProbedFilesystem(filesystem Filesystem) error {
	if filesystem == "" {
		return nil
	}
	return filesystem.Validate()
}

func validRootfsBlockIdentity(identity rootfsBlockIdentity) bool {
	return identity.major != 0
}
