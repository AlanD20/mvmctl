package authorityfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

var priorBootIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// PriorBootVMAbsence binds a real held VM lock to a prior-boot cleaning record
// and an absent fixed cgroup. It has no caller-evidence constructor or mutable
// exported fields. It owns no additional descriptors and cannot outlive its VM
// authority: Verify requires a hold descended from the same still-live parent.
type PriorBootVMAbsence struct {
	parent            *jailerVMState
	ownerUID          uint32
	key               VMKey
	launchGeneration  uint64
	cleanupGeneration uint64
	oldBootID         string
	currentBootID     string
}

type priorBootRecord struct {
	SchemaVersion     uint32          `json:"schema_version"`
	OwnerUID          uint32          `json:"owner_uid"`
	VMID              string          `json:"vm_id"`
	NetworkPlacement  string          `json:"network_placement"`
	Lifecycle         string          `json:"lifecycle"`
	LaunchGeneration  uint64          `json:"launch_generation"`
	CleanupGeneration uint64          `json:"cleanup_generation"`
	Launch            priorBootLaunch `json:"launch"`
	Active            json.RawMessage `json:"active,omitempty"`
}

type priorBootLaunch struct {
	OwnerGID          uint32          `json:"owner_gid"`
	NetworkPlacement  string          `json:"network_placement"`
	ExecutionIdentity json.RawMessage `json:"execution_identity"`
	SnapshotID        string          `json:"snapshot_id"`
	CacheLocator      string          `json:"cache_locator"`
	OutputIdentity    json.RawMessage `json:"output_identity,omitempty"`
	HostBootID        string          `json:"host_boot_id"`
	PID               int             `json:"pid"`
	StartTimeTicks    uint64          `json:"start_time_ticks"`
	Relay             json.RawMessage `json:"relay"`
	ManagedCache      json.RawMessage `json:"managed_cache"`
	Release           json.RawMessage `json:"release"`
}

// ProvePriorBootVMAbsence derives all evidence from fixed receiver-owned paths
// while retaining this concrete authority. It never opens a recorded PID.
func (hold *TopologyHold) ProvePriorBootVMAbsence(ctx context.Context) (*PriorBootVMAbsence, error) {
	if ctx == nil || hold == nil || hold.state == nil {
		return nil, fmt.Errorf("authorityfs: prior-boot proof requires context and retained topology authority")
	}
	state := hold.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.parent == nil {
		return nil, fmt.Errorf("authorityfs: prior-boot topology authority is inactive")
	}
	parent := state.parent
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if err := requirePriorBootParent(ctx, state); err != nil {
		return nil, err
	}
	current, err := readPriorBootCurrentBoot(ctx)
	if err != nil {
		return nil, err
	}
	record, err := readPriorBootRecord(ctx, state.ownerUID, state.key)
	if err != nil {
		return nil, err
	}
	if record.Launch.HostBootID == current {
		return nil, fmt.Errorf("authorityfs: VM record belongs to the current boot")
	}
	proof := &PriorBootVMAbsence{
		parent: parent, ownerUID: state.ownerUID, key: state.key,
		launchGeneration: record.LaunchGeneration, cleanupGeneration: record.CleanupGeneration,
		oldBootID: record.Launch.HostBootID, currentBootID: current,
	}
	if err := proof.verifyEvidence(ctx); err != nil {
		return nil, err
	}
	return proof, nil
}

// Verify accepts transferred holds only from the original VM lock and repeats
// the real absence and boot evidence before a consumer commits recovery.
func (proof *PriorBootVMAbsence) Verify(
	ctx context.Context,
	hold *TopologyHold,
	oldBootID string,
	launchGeneration uint64,
) (string, error) {
	if ctx == nil || proof == nil || proof.parent == nil || hold == nil || hold.state == nil {
		return "", fmt.Errorf("authorityfs: prior-boot proof is unavailable")
	}
	state := hold.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.parent != proof.parent || state.ownerUID != proof.ownerUID || state.key != proof.key ||
		oldBootID != proof.oldBootID || launchGeneration != proof.launchGeneration {
		return "", fmt.Errorf("authorityfs: prior-boot proof does not bind this retained VM generation")
	}
	state.parent.mu.Lock()
	defer state.parent.mu.Unlock()
	if err := requirePriorBootParent(ctx, state); err != nil {
		return "", err
	}
	if err := proof.verifyEvidence(ctx); err != nil {
		return "", err
	}
	return proof.currentBootID, nil
}

func requirePriorBootParent(ctx context.Context, hold *topologyHoldState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := hold.parent
	if parent.released || parent.lock == nil || parent.topologyBroken || parent.topologyHolds == 0 ||
		parent.ownerUID != hold.ownerUID || parent.key != hold.key || parent.lock.state == nil {
		return fmt.Errorf("authorityfs: prior-boot VM parent is inactive")
	}
	lock := parent.lock.state
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if !lock.active || lock.released || lock.fd < 0 {
		return fmt.Errorf("authorityfs: prior-boot VM lock is inactive")
	}
	// A capability opened under a private fixture policy cannot authorize
	// production recovery. Match the actual held inode to the canonical lock.
	return comparePriorBootVMLock(ctx, hold.ownerUID, hold.key, lock.fd)
}

func (proof *PriorBootVMAbsence) verifyEvidence(ctx context.Context) error {
	before, err := readPriorBootCurrentBoot(ctx)
	if err != nil {
		return err
	}
	if before != proof.currentBootID || before == proof.oldBootID {
		return fmt.Errorf("authorityfs: prior-boot proof boot binding changed")
	}
	first, err := readPriorBootRecord(ctx, proof.ownerUID, proof.key)
	if err != nil {
		return err
	}
	if first.LaunchGeneration != proof.launchGeneration || first.CleanupGeneration != proof.cleanupGeneration ||
		first.Launch.HostBootID != proof.oldBootID {
		return fmt.Errorf("authorityfs: prior-boot cleaning generation changed")
	}
	if err := requirePriorBootCgroupAbsent(ctx, proof.key); err != nil {
		return err
	}
	second, err := readPriorBootRecord(ctx, proof.ownerUID, proof.key)
	if err != nil {
		return err
	}
	if second.LaunchGeneration != first.LaunchGeneration || second.CleanupGeneration != first.CleanupGeneration ||
		second.Launch.HostBootID != first.Launch.HostBootID {
		return fmt.Errorf("authorityfs: prior-boot cleaning record changed during proof")
	}
	after, err := readPriorBootCurrentBoot(ctx)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("authorityfs: host boot changed during prior-boot proof")
	}
	return nil
}

func readPriorBootRecord(ctx context.Context, owner uint32, key VMKey) (_ priorBootRecord, returnErr error) {
	// The shared state base may be safely traversable; only instances and its
	// owner directories are private. Mount crossing stays forbidden below mvmctl.
	dir, err := openPriorBootDirectory(ctx, []string{"var", "lib", "mvmctl", "instances", formatUID(owner)}, 2, 3, 0700)
	if err != nil {
		return priorBootRecord{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(dir)) }()
	fd, err := unix.Openat2(dir, key.value+".json", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: resolveManaged,
	})
	if err != nil {
		return priorBootRecord{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return priorBootRecord{}, err
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size <= 0 ||
		stat.Size > 16*1024 {
		return priorBootRecord{}, fmt.Errorf("authorityfs: prior-boot root record metadata is not admitted")
	}
	raw, err := readPriorBootBytes(ctx, fd, 16*1024)
	if err != nil {
		return priorBootRecord{}, err
	}
	record, err := decodePriorBootRecord(raw, owner, key)
	if err != nil {
		return priorBootRecord{}, err
	}
	var after, entry unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return priorBootRecord{}, err
	}
	if err := unix.Fstatat(dir, key.value+".json", &entry, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT); err != nil {
		return priorBootRecord{}, err
	}
	if after.Dev != stat.Dev || after.Ino != stat.Ino || after.Mode != stat.Mode || after.Size != stat.Size ||
		after.Uid != stat.Uid || after.Gid != stat.Gid || after.Nlink != stat.Nlink || after.Mtim != stat.Mtim ||
		after.Ctim != stat.Ctim || entry.Dev != stat.Dev || entry.Ino != stat.Ino || entry.Mode != stat.Mode ||
		entry.Uid != stat.Uid || entry.Gid != stat.Gid || entry.Nlink != stat.Nlink {
		return priorBootRecord{}, fmt.Errorf("authorityfs: prior-boot root record changed during admission")
	}
	return record, nil
}

// decodePriorBootRecord projects the schema-6 fields needed by prior-boot absence.
func decodePriorBootRecord(raw []byte, owner uint32, key VMKey) (priorBootRecord, error) {
	if err := scanPriorBootJSON(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return priorBootRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record priorBootRecord
	if err := decoder.Decode(&record); err != nil {
		return priorBootRecord{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return priorBootRecord{}, fmt.Errorf("authorityfs: prior-boot record contains trailing data")
	}
	if record.SchemaVersion != 6 || record.OwnerUID != owner || record.VMID != key.value ||
		(record.NetworkPlacement != "managed" && record.NetworkPlacement != "networkless") ||
		record.Lifecycle != "cleaning" ||
		record.LaunchGeneration == 0 || record.CleanupGeneration != record.LaunchGeneration ||
		record.Launch.OwnerGID == 0 || record.Launch.NetworkPlacement != record.NetworkPlacement ||
		len(record.Launch.ExecutionIdentity) == 0 || record.Launch.CacheLocator == "" ||
		record.Launch.PID <= 0 || record.Launch.StartTimeTicks == 0 ||
		!priorBootIDPattern.MatchString(record.Launch.HostBootID) ||
		len(record.Launch.Relay) == 0 || len(record.Launch.ManagedCache) == 0 ||
		len(record.Launch.Release) == 0 {
		return priorBootRecord{}, fmt.Errorf("authorityfs: prior-boot root record binding is invalid")
	}
	return record, nil
}

// This scanner rejects duplicate/case-folded fields at every depth before the
// narrow boot/generation projection is decoded; it is not a record migration.
func scanPriorBootJSON(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return fmt.Errorf("authorityfs: prior-boot record nesting is excessive")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("authorityfs: prior-boot JSON delimiter is invalid")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] || name != strings.ToLower(name) {
				return fmt.Errorf("authorityfs: prior-boot JSON field is duplicated or noncanonical")
			}
			seen[name] = true
		}
		if err := scanPriorBootJSON(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func readPriorBootCurrentBoot(ctx context.Context) (_ string, returnErr error) {
	fd, err := unix.Open("/proc/sys/kernel/random/boot_id", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	raw, err := readPriorBootBytes(ctx, fd, 64)
	if err != nil {
		return "", err
	}
	if len(raw) != 37 || raw[36] != '\n' || !priorBootIDPattern.MatchString(string(raw[:36])) {
		return "", fmt.Errorf("authorityfs: host boot ID is not canonical")
	}
	return string(raw[:36]), nil
}

func readPriorBootBytes(ctx context.Context, fd, limit int) ([]byte, error) {
	raw := make([]byte, limit+1)
	used := 0
	for used < len(raw) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := unix.Read(fd, raw[used:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return raw[:used], nil
		}
		used += n
	}
	return nil, fmt.Errorf("authorityfs: prior-boot evidence exceeds its bound")
}

func openPriorBootDirectory(
	ctx context.Context,
	components []string,
	managedStart int,
	privateStart int,
	mode uint32,
) (_ int, returnErr error) {
	fd, err := unix.Open("/", directoryFlags, 0)
	if err != nil {
		return -1, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	for index := -1; index < len(components); index++ {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		if index >= 0 {
			resolve := uint64(resolveBeneath)
			if index > managedStart {
				resolve = resolveManaged
			}
			next, err := unix.Openat2(fd, components[index], &unix.OpenHow{Flags: directoryFlags, Resolve: resolve})
			if err != nil {
				return -1, err
			}
			closeErr := unix.Close(fd)
			fd = next
			if closeErr != nil {
				return -1, closeErr
			}
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return -1, err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0022 != 0 ||
			(index >= privateStart && stat.Mode&07777 != mode) {
			return -1, fmt.Errorf("authorityfs: prior-boot directory metadata is not admitted")
		}
	}
	return fd, nil
}

func comparePriorBootVMLock(ctx context.Context, owner uint32, key VMKey, held int) (returnErr error) {
	// /run/mvmctl is 0711; authority and owner directories are 0700.
	dir, err := openPriorBootDirectory(ctx, []string{"run", "mvmctl", "authority", formatUID(owner)}, 2, 2, 0700)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(dir)) }()
	var original, current unix.Stat_t
	if err := unix.Fstat(held, &original); err != nil {
		return err
	}
	if err := unix.Fstatat(
		dir,
		key.value+".lock",
		&current,
		unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT,
	); err != nil {
		return err
	}
	if current.Mode != unix.S_IFREG|0600 || current.Uid != 0 || current.Gid != 0 || current.Nlink != 1 ||
		current.Dev != original.Dev || current.Ino != original.Ino || current.Mode != original.Mode ||
		current.Uid != original.Uid || current.Gid != original.Gid || current.Nlink != original.Nlink {
		return fmt.Errorf("authorityfs: retained VM lock is not the canonical production lock")
	}
	return nil
}
