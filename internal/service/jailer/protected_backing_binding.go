package jailer

import (
	"bytes"
	"encoding/json"
	"io"
	"math"

	"golang.org/x/sys/unix"
)

type protectedBindingWire struct {
	Kind      string                    `json:"kind"`
	LogicalID string                    `json:"logical_id"`
	OwnerUID  uint32                    `json:"owner_uid"`
	Cache     protectedBindingCacheWire `json:"cache"`
	Identity  protectedBindingStatWire  `json:"identity"`
	Bytes     uint64                    `json:"bytes"`
}

type protectedBindingCacheWire struct {
	Major       uint32                        `json:"major"`
	Minor       uint32                        `json:"minor"`
	Inode       uint64                        `json:"inode"`
	MountID     uint64                        `json:"mount_id"`
	MountIDKind managedCacheMountIdentityKind `json:"mount_id_kind"`
}

type protectedBindingStatWire struct {
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
	Inode uint64 `json:"inode"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Mode  uint16 `json:"mode"`
	Nlink uint32 `json:"nlink"`
	Size  uint64 `json:"size"`
}

func (binding protectedBackingBinding) MarshalJSON() ([]byte, error) {
	cache, stat := binding.cacheIdentity, binding.identity
	return json.Marshal(protectedBindingWire{
		Kind: binding.kind, LogicalID: binding.logicalID, OwnerUID: binding.ownerUID, Bytes: binding.bytes,
		Cache:    protectedBindingCacheWire{cache.deviceMajor, cache.deviceMinor, cache.inode, cache.mountID, cache.mountIDKind},
		Identity: protectedBindingStatWire{stat.deviceMajor, stat.deviceMinor, stat.inode, stat.uid, stat.gid, stat.mode, stat.nlink, stat.size},
	})
}

// Decoding supplies only a selector. Reopening still proves the root-owned logical index,
// cache/store anchor, object record, exact inode and current ACL; JSON alone grants no authority.
func (binding *protectedBackingBinding) UnmarshalJSON(data []byte) error {
	if len(data) > protectedBackingRecordLimit {
		return managedVMResourceValidationError("protected backing binding exceeds metadata limit")
	}
	var wire protectedBindingWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return managedVMResourceAtomicError("decode protected backing binding", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return managedVMResourceValidationError("protected backing binding contains trailing data")
	}
	canonical, err := json.Marshal(wire)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, data) != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return managedVMResourceValidationError("protected backing binding is not canonical")
	}
	if protectedKindIDLength(wire.Kind) == 0 || !isCanonicalManagedResourceID(wire.LogicalID, protectedKindIDLength(wire.Kind)) ||
		wire.OwnerUID == 0 || wire.OwnerUID == math.MaxUint32 || wire.Cache.Inode == 0 || wire.Identity.Inode == 0 ||
		wire.Identity.UID != 0 || wire.Identity.GID != 0 || wire.Identity.Nlink != 1 ||
		wire.Identity.Mode&unix.S_IFMT != unix.S_IFREG || wire.Identity.Mode&07007 != 0 ||
		wire.Bytes == 0 || wire.Bytes > math.MaxInt64 || wire.Identity.Size != wire.Bytes {
		return managedVMResourceValidationError("protected backing binding identity is invalid")
	}
	*binding = protectedBackingBinding{
		kind: wire.Kind, logicalID: wire.LogicalID, ownerUID: wire.OwnerUID, bytes: wire.Bytes,
		cacheIdentity: managedCacheIdentity{deviceMajor: wire.Cache.Major, deviceMinor: wire.Cache.Minor,
			inode: wire.Cache.Inode, mountID: wire.Cache.MountID, mountIDKind: wire.Cache.MountIDKind},
		identity: managedVMConfigStat{deviceMajor: wire.Identity.Major, deviceMinor: wire.Identity.Minor,
			inode: wire.Identity.Inode, uid: wire.Identity.UID, gid: wire.Identity.GID, mode: wire.Identity.Mode,
			nlink: wire.Identity.Nlink, size: wire.Identity.Size},
	}
	return nil
}
