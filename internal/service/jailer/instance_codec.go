package jailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxInstanceJSONDepth = 4

type instanceRecordWire struct {
	SchemaVersion     uint32              `json:"schema_version"`
	OwnerUID          uint32              `json:"owner_uid"`
	VMID              string              `json:"vm_id"`
	Lifecycle         instanceLifecycle   `json:"lifecycle"`
	LaunchGeneration  uint64              `json:"launch_generation"`
	CleanupGeneration uint64              `json:"cleanup_generation"`
	Launch            *instanceLaunchWire `json:"launch,omitempty"`
	Active            *instanceActiveWire `json:"active,omitempty"`
}

type instanceLaunchWire struct {
	OwnerGID          uint32                        `json:"owner_gid"`
	ExecutionIdentity instanceExecutionIdentityWire `json:"execution_identity"`
	SnapshotID        string                        `json:"snapshot_id"`
	CacheLocator      string                        `json:"cache_locator"`
	OutputIdentity    *instanceOutputIdentitiesWire `json:"output_identity,omitempty"`
	HostBootID        string                        `json:"host_boot_id"`
	PID               int                           `json:"pid"`
	StartTimeTicks    uint64                        `json:"start_time_ticks"`
	Relay             instanceLaunchRelayWire       `json:"relay"`
	ManagedCache      managedCacheIdentityWire      `json:"managed_cache"`
	Release           releaseIdentityWire           `json:"release"`
}

type instanceExecutionIdentityWire struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type instanceOutputIdentitiesWire struct {
	Log     instanceObjectIdentityWire  `json:"log"`
	Metrics *instanceObjectIdentityWire `json:"metrics,omitempty"`
}

type instanceLaunchRelayWire struct {
	Kind           instanceRelayKind `json:"kind"`
	PID            *int              `json:"pid,omitempty"`
	StartTimeTicks *uint64           `json:"start_time_ticks,omitempty"`
}

type releaseIdentityWire struct {
	Version           string `json:"version"`
	Architecture      string `json:"architecture"`
	FirecrackerSHA256 string `json:"firecracker_sha256"`
	JailerSHA256      string `json:"jailer_sha256"`
}

type managedCacheIdentityWire struct {
	DeviceMajor   uint32                        `json:"device_major"`
	DeviceMinor   uint32                        `json:"device_minor"`
	Inode         uint64                        `json:"inode"`
	MountIdentity managedCacheMountIdentityWire `json:"mount_identity"`
}

type managedCacheMountIdentityWire struct {
	Kind managedCacheMountIdentityKindWire `json:"kind"`
	ID   uint64                            `json:"id"`
}

type managedCacheMountIdentityKindWire string

const (
	managedCacheMountIdentityUnavailableWire managedCacheMountIdentityKindWire = "unavailable"
	managedCacheMountIdentityLegacyWire      managedCacheMountIdentityKindWire = "legacy"
	managedCacheMountIdentityUniqueWire      managedCacheMountIdentityKindWire = "unique"
)

type instanceActiveWire struct {
	UserIDs             processCredentialIdentityWire             `json:"user_ids"`
	GroupIDs            processCredentialIdentityWire             `json:"group_ids"`
	SupplementaryGroups []uint32                                  `json:"supplementary_groups"`
	MountNamespace      instanceObjectIdentityWire                `json:"mount_namespace"`
	JailRoot            instanceObjectIdentityWire                `json:"jail_root"`
	Executable          instanceFirecrackerExecutableIdentityWire `json:"executable"`
	Relay               instanceActiveRelayWire                   `json:"relay"`
}

type processCredentialIdentityWire struct {
	Real       uint32 `json:"real"`
	Effective  uint32 `json:"effective"`
	Saved      uint32 `json:"saved"`
	Filesystem uint32 `json:"filesystem"`
}

type instanceObjectIdentityWire struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type instanceFirecrackerExecutableIdentityWire struct {
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	SizeBytes      uint64 `json:"size_bytes"`
	Mode           uint32 `json:"mode"`
	UID            uint32 `json:"uid"`
	GID            uint32 `json:"gid"`
	Links          uint64 `json:"links"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type instanceActiveRelayWire struct {
	Kind                instanceRelayKind                    `json:"kind"`
	UserIDs             *processCredentialIdentityWire       `json:"user_ids,omitempty"`
	GroupIDs            *processCredentialIdentityWire       `json:"group_ids,omitempty"`
	SupplementaryGroups *[]uint32                            `json:"supplementary_groups,omitempty"`
	MountNamespace      *instanceObjectIdentityWire          `json:"mount_namespace,omitempty"`
	Root                *instanceObjectIdentityWire          `json:"root,omitempty"`
	Executable          *instanceRelayExecutableIdentityWire `json:"executable,omitempty"`
}

type instanceRelayExecutableIdentityWire struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	SizeBytes uint64 `json:"size_bytes"`
	Mode      uint32 `json:"mode"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Links     uint64 `json:"links"`
	SHA256    string `json:"sha256"`
}

type instanceJSONValueKind uint8

const (
	instanceJSONScalar instanceJSONValueKind = iota
	instanceJSONObject
	instanceJSONArray
)

type instanceJSONField struct {
	kind  instanceJSONValueKind
	child string
}

func encodeInstanceRecord(record instanceRecord) ([]byte, error) {
	if err := validateInstanceRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(instanceRecordToWire(record))
	if err != nil {
		return nil, instanceAtomicError("encode instance authority record", err)
	}
	if len(raw) > maxInstanceRecordBytes {
		return nil, instanceAtomicError("encoded instance authority record exceeds size limit", nil)
	}
	return raw, nil
}

func decodeInstanceRecord(raw []byte) (instanceRecord, error) {
	if len(raw) == 0 {
		return instanceRecord{}, instanceAtomicError("instance authority record is empty", nil)
	}
	if len(raw) > maxInstanceRecordBytes {
		return instanceRecord{}, instanceAtomicError("instance authority record exceeds size limit", nil)
	}
	wire, err := decodeInstanceRecordWire(raw)
	if err != nil {
		return instanceRecord{}, err
	}
	if err := validateInstanceJSON(raw, wire); err != nil {
		return instanceRecord{}, err
	}
	record, err := instanceRecordFromWire(wire)
	if err != nil {
		return instanceRecord{}, err
	}
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	return record, nil
}

func decodeInstanceRecordWire(raw []byte) (instanceRecordWire, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire instanceRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return instanceRecordWire{}, instanceAtomicError("decode instance authority record", err)
	}
	if err := requireInstanceJSONEOF(decoder); err != nil {
		return instanceRecordWire{}, err
	}
	return wire, nil
}

// CRITICAL: Persistent root authority is untrusted after storage tampering. The typed decoder cannot detect duplicate
// keys or distinguish every omitted zero-valued field, so this second pass enforces the exact lifecycle-specific shape.
func validateInstanceJSON(raw []byte, wire instanceRecordWire) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanInstanceJSONValue(decoder, "record", wire, 0); err != nil {
		return instanceAtomicError("decode strict instance authority record", err)
	}
	return requireInstanceJSONEOF(decoder)
}

func scanInstanceJSONValue(
	decoder *json.Decoder,
	objectName string,
	wire instanceRecordWire,
	depth int,
) error {
	if depth > maxInstanceJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxInstanceJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("object %q must be a JSON object", objectName)
	}
	expected, err := instanceJSONObjectFields(objectName, wire)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(expected))
	for decoder.More() {
		fieldToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("object field is not a string")
		}
		spec, exists := expected[field]
		if !exists {
			return fmt.Errorf("unknown JSON field %q in object %q", field, objectName)
		}
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return fmt.Errorf("duplicate JSON field %q conflicts with %q", field, existing)
			}
		}
		seen[field] = struct{}{}
		switch spec.kind {
		case instanceJSONScalar:
			if err := scanInstanceJSONScalar(decoder); err != nil {
				return err
			}
		case instanceJSONObject:
			if err := scanInstanceJSONValue(decoder, spec.child, wire, depth+1); err != nil {
				return err
			}
		case instanceJSONArray:
			if err := scanInstanceJSONScalarArray(decoder, depth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("JSON field %q has an invalid schema", field)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("JSON object %q is missing required fields", objectName)
	}
	return nil
}

func instanceJSONObjectFields(
	objectName string,
	wire instanceRecordWire,
) (map[string]instanceJSONField, error) {
	switch objectName {
	case "record":
		return instanceRecordJSONFields(wire), nil
	case "launch":
		fields := instanceLaunchJSONFields()
		if wire.Launch != nil && wire.Launch.OutputIdentity != nil {
			fields["output_identity"] = instanceJSONField{kind: instanceJSONObject, child: "output_identity"}
		}
		return fields, nil
	case "output_identity":
		fields := map[string]instanceJSONField{"log": {kind: instanceJSONObject, child: "object_identity"}}
		if wire.Launch != nil && wire.Launch.OutputIdentity != nil && wire.Launch.OutputIdentity.Metrics != nil {
			fields["metrics"] = instanceJSONField{kind: instanceJSONObject, child: "object_identity"}
		}
		return fields, nil
	case "execution_identity":
		return instanceScalarJSONFields("uid", "gid"), nil
	case "launch_relay":
		return instanceLaunchRelayJSONFields(wire), nil
	case "managed_cache":
		return instanceObjectJSONFields(
			[]string{"device_major", "device_minor", "inode"},
			map[string]string{"mount_identity": "mount_identity"},
		), nil
	case "mount_identity":
		return instanceScalarJSONFields("kind", "id"), nil
	case "release":
		return instanceScalarJSONFields("version", "architecture", "firecracker_sha256", "jailer_sha256"), nil
	case "active":
		return instanceActiveJSONFields(), nil
	case "credentials":
		return instanceScalarJSONFields("real", "effective", "saved", "filesystem"), nil
	case "object_identity":
		return instanceScalarJSONFields("device", "inode"), nil
	case "firecracker_executable":
		return instanceScalarJSONFields(
			"device", "inode", "size_bytes", "mode", "uid", "gid", "links", "manifest_sha256",
		), nil
	case "active_relay":
		return instanceActiveRelayJSONFields(wire), nil
	case "relay_executable":
		return instanceScalarJSONFields(
			"device", "inode", "size_bytes", "mode", "uid", "gid", "links", "sha256",
		), nil
	default:
		return nil, fmt.Errorf("unexpected object %q", objectName)
	}
}

func instanceRecordJSONFields(wire instanceRecordWire) map[string]instanceJSONField {
	result := instanceScalarJSONFields(
		"schema_version",
		"owner_uid",
		"vm_id",
		"lifecycle",
		"launch_generation",
		"cleanup_generation",
	)
	switch wire.Lifecycle {
	case instanceLifecycleLaunching:
		result["launch"] = instanceJSONField{kind: instanceJSONObject, child: "launch"}
	case instanceLifecycleActive:
		result["launch"] = instanceJSONField{kind: instanceJSONObject, child: "launch"}
		result["active"] = instanceJSONField{kind: instanceJSONObject, child: "active"}
	case instanceLifecycleCleaning:
		result["launch"] = instanceJSONField{kind: instanceJSONObject, child: "launch"}
		if wire.Active != nil {
			result["active"] = instanceJSONField{kind: instanceJSONObject, child: "active"}
		}
	case instanceLifecycleCleaned:
	}
	return result
}

func instanceLaunchJSONFields() map[string]instanceJSONField {
	return instanceObjectJSONFields(
		[]string{"owner_gid", "host_boot_id", "pid", "start_time_ticks", "snapshot_id", "cache_locator"},
		map[string]string{
			"execution_identity": "execution_identity",
			"relay":              "launch_relay",
			"managed_cache":      "managed_cache",
			"release":            "release",
		},
	)
}

func instanceLaunchRelayJSONFields(wire instanceRecordWire) map[string]instanceJSONField {
	result := instanceScalarJSONFields("kind")
	if wire.Launch != nil && wire.Launch.Relay.Kind == instanceRelayPresent {
		result["pid"] = instanceJSONField{kind: instanceJSONScalar}
		result["start_time_ticks"] = instanceJSONField{kind: instanceJSONScalar}
	}
	return result
}

func instanceActiveJSONFields() map[string]instanceJSONField {
	result := instanceObjectJSONFields(
		nil,
		map[string]string{
			"user_ids":        "credentials",
			"group_ids":       "credentials",
			"mount_namespace": "object_identity",
			"jail_root":       "object_identity",
			"executable":      "firecracker_executable",
			"relay":           "active_relay",
		},
	)
	result["supplementary_groups"] = instanceJSONField{kind: instanceJSONArray}
	return result
}

func instanceActiveRelayJSONFields(wire instanceRecordWire) map[string]instanceJSONField {
	result := instanceScalarJSONFields("kind")
	if wire.Active == nil || wire.Active.Relay.Kind != instanceRelayPresent {
		return result
	}
	for field, child := range map[string]string{
		"user_ids":        "credentials",
		"group_ids":       "credentials",
		"mount_namespace": "object_identity",
		"root":            "object_identity",
		"executable":      "relay_executable",
	} {
		result[field] = instanceJSONField{kind: instanceJSONObject, child: child}
	}
	result["supplementary_groups"] = instanceJSONField{kind: instanceJSONArray}
	return result
}

func instanceObjectJSONFields(
	scalars []string,
	objects map[string]string,
) map[string]instanceJSONField {
	result := instanceScalarJSONFields(scalars...)
	for field, child := range objects {
		result[field] = instanceJSONField{kind: instanceJSONObject, child: child}
	}
	return result
}

func instanceScalarJSONFields(names ...string) map[string]instanceJSONField {
	result := make(map[string]instanceJSONField, len(names))
	for _, name := range names {
		result[name] = instanceJSONField{kind: instanceJSONScalar}
	}
	return result
}

func scanInstanceJSONScalarArray(decoder *json.Decoder, depth int) error {
	if depth > maxInstanceJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxInstanceJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return fmt.Errorf("expected JSON array")
	}
	for decoder.More() {
		if err := scanInstanceJSONScalar(decoder); err != nil {
			return err
		}
	}
	endToken, err := decoder.Token()
	if err != nil {
		return err
	}
	if endToken != json.Delim(']') {
		return fmt.Errorf("JSON array has an invalid terminator")
	}
	return nil
}

func scanInstanceJSONScalar(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if _, ok := token.(json.Delim); ok {
		return fmt.Errorf("unexpected nested JSON value")
	}
	if token == nil {
		return fmt.Errorf("null JSON value is not permitted")
	}
	return nil
}

func requireInstanceJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return instanceAtomicError("instance authority record contains trailing input", nil)
	}
	return instanceAtomicError("decode trailing instance authority input", err)
}

func instanceRecordToWire(record instanceRecord) instanceRecordWire {
	wire := instanceRecordWire{
		SchemaVersion:     record.schemaVersion,
		OwnerUID:          record.ownerUID,
		VMID:              record.vmID,
		Lifecycle:         record.lifecycle,
		LaunchGeneration:  record.launchGeneration,
		CleanupGeneration: record.cleanupGeneration,
	}
	if record.launch != nil {
		wire.Launch = instanceLaunchToWire(*record.launch)
	}
	if record.active != nil {
		wire.Active = instanceActiveToWire(*record.active)
	}
	return wire
}

func instanceLaunchToWire(launch instanceLaunchRecord) *instanceLaunchWire {
	wire := &instanceLaunchWire{
		OwnerGID:          launch.ownerGID,
		ExecutionIdentity: instanceExecutionIdentityWire{UID: launch.executionIdentity.uid, GID: launch.executionIdentity.gid},
		SnapshotID:        launch.snapshotID,
		CacheLocator:      "/" + strings.Join(launch.cacheLocator.components, "/"),
		HostBootID:        launch.hostBootID,
		PID:               launch.process.pid,
		StartTimeTicks:    launch.process.startTimeTicks,
		Relay:             instanceLaunchRelayToWire(launch.relay),
		ManagedCache: managedCacheIdentityWire{
			DeviceMajor: launch.cacheIdentity.deviceMajor,
			DeviceMinor: launch.cacheIdentity.deviceMinor,
			Inode:       launch.cacheIdentity.inode,
			MountIdentity: managedCacheMountIdentityWire{
				Kind: managedCacheMountIdentityKindToWire(launch.cacheIdentity.mountIDKind),
				ID:   launch.cacheIdentity.mountID,
			},
		},
		Release: releaseIdentityWire{
			Version:           launch.release.version,
			Architecture:      launch.release.architecture,
			FirecrackerSHA256: launch.release.firecrackerSHA256,
			JailerSHA256:      launch.release.jailerSHA256,
		},
	}
	if launch.outputIdentity != nil {
		wire.OutputIdentity = &instanceOutputIdentitiesWire{Log: instanceObjectIdentityWire{Device: launch.outputIdentity.log.device, Inode: launch.outputIdentity.log.inode}}
		if launch.outputIdentity.metrics != nil {
			wire.OutputIdentity.Metrics = &instanceObjectIdentityWire{Device: launch.outputIdentity.metrics.device, Inode: launch.outputIdentity.metrics.inode}
		}
	}
	return wire
}

func instanceLaunchRelayToWire(relay instanceLaunchRelay) instanceLaunchRelayWire {
	wire := instanceLaunchRelayWire{Kind: relay.kind}
	if relay.kind == instanceRelayPresent {
		pid := relay.locator.pid
		startTimeTicks := relay.locator.startTimeTicks
		wire.PID = &pid
		wire.StartTimeTicks = &startTimeTicks
	}
	return wire
}

func instanceActiveToWire(active instanceActiveIdentity) *instanceActiveWire {
	return &instanceActiveWire{
		UserIDs:             processCredentialIdentityToWire(active.userIDs),
		GroupIDs:            processCredentialIdentityToWire(active.groupIDs),
		SupplementaryGroups: append([]uint32{}, active.supplementaryGroups...),
		MountNamespace:      instanceObjectIdentityToWire(active.mountNamespace),
		JailRoot:            instanceObjectIdentityToWire(active.jailRoot),
		Executable: instanceFirecrackerExecutableIdentityWire{
			Device:         active.executable.device,
			Inode:          active.executable.inode,
			SizeBytes:      active.executable.sizeBytes,
			Mode:           active.executable.mode,
			UID:            active.executable.uid,
			GID:            active.executable.gid,
			Links:          active.executable.links,
			ManifestSHA256: active.executable.manifestSHA256,
		},
		Relay: instanceActiveRelayToWire(active.relay),
	}
}

func instanceActiveRelayToWire(relay instanceActiveRelay) instanceActiveRelayWire {
	wire := instanceActiveRelayWire{Kind: relay.kind}
	if relay.kind != instanceRelayPresent {
		return wire
	}
	userIDs := processCredentialIdentityToWire(relay.userIDs)
	groupIDs := processCredentialIdentityToWire(relay.groupIDs)
	mountNamespace := instanceObjectIdentityToWire(relay.mountNamespace)
	root := instanceObjectIdentityToWire(relay.root)
	executable := instanceRelayExecutableIdentityWire{
		Device:    relay.executable.device,
		Inode:     relay.executable.inode,
		SizeBytes: relay.executable.sizeBytes,
		Mode:      relay.executable.mode,
		UID:       relay.executable.uid,
		GID:       relay.executable.gid,
		Links:     relay.executable.links,
		SHA256:    relay.executable.sha256,
	}
	wire.UserIDs = &userIDs
	wire.GroupIDs = &groupIDs
	supplementaryGroups := append([]uint32{}, relay.supplementaryGroups...)
	wire.SupplementaryGroups = &supplementaryGroups
	wire.MountNamespace = &mountNamespace
	wire.Root = &root
	wire.Executable = &executable
	return wire
}

func instanceRecordFromWire(wire instanceRecordWire) (instanceRecord, error) {
	record := instanceRecord{
		schemaVersion:     wire.SchemaVersion,
		ownerUID:          wire.OwnerUID,
		vmID:              wire.VMID,
		lifecycle:         wire.Lifecycle,
		launchGeneration:  wire.LaunchGeneration,
		cleanupGeneration: wire.CleanupGeneration,
	}
	if wire.Launch != nil {
		launch, err := instanceLaunchFromWire(*wire.Launch)
		if err != nil {
			return instanceRecord{}, err
		}
		record.launch = &launch
	}
	if wire.Active != nil {
		active, err := instanceActiveFromWire(*wire.Active)
		if err != nil {
			return instanceRecord{}, err
		}
		record.active = &active
	}
	return record, nil
}

func instanceLaunchFromWire(wire instanceLaunchWire) (instanceLaunchRecord, error) {
	locator, err := parseManagedCacheLocator(wire.CacheLocator)
	if err != nil {
		return instanceLaunchRecord{}, err
	}
	cacheIdentity, err := managedCacheIdentityFromWire(wire.ManagedCache)
	if err != nil {
		return instanceLaunchRecord{}, err
	}
	relay := instanceLaunchRelay{kind: wire.Relay.Kind}
	if wire.Relay.PID != nil {
		relay.locator.pid = *wire.Relay.PID
	}
	if wire.Relay.StartTimeTicks != nil {
		relay.locator.startTimeTicks = *wire.Relay.StartTimeTicks
	}
	launch := instanceLaunchRecord{
		ownerGID:          wire.OwnerGID,
		executionIdentity: vmExecutionIdentity{uid: wire.ExecutionIdentity.UID, gid: wire.ExecutionIdentity.GID},
		snapshotID:        wire.SnapshotID,
		cacheLocator:      locator,
		hostBootID:        wire.HostBootID,
		process:           instanceProcessLocator{pid: wire.PID, startTimeTicks: wire.StartTimeTicks},
		relay:             relay,
		cacheIdentity:     cacheIdentity,
		release: releaseIdentity{
			version:           wire.Release.Version,
			architecture:      wire.Release.Architecture,
			firecrackerSHA256: wire.Release.FirecrackerSHA256,
			jailerSHA256:      wire.Release.JailerSHA256,
		},
	}
	if wire.OutputIdentity != nil {
		launch.outputIdentity = &instanceOutputIdentities{log: instanceObjectIdentity{device: wire.OutputIdentity.Log.Device, inode: wire.OutputIdentity.Log.Inode}}
		if wire.OutputIdentity.Metrics != nil {
			launch.outputIdentity.metrics = &instanceObjectIdentity{device: wire.OutputIdentity.Metrics.Device, inode: wire.OutputIdentity.Metrics.Inode}
		}
	}
	return launch, nil
}

func instanceActiveFromWire(wire instanceActiveWire) (instanceActiveIdentity, error) {
	relay, err := instanceActiveRelayFromWire(wire.Relay)
	if err != nil {
		return instanceActiveIdentity{}, err
	}
	return instanceActiveIdentity{
		userIDs:             processCredentialIdentityFromWire(wire.UserIDs),
		groupIDs:            processCredentialIdentityFromWire(wire.GroupIDs),
		supplementaryGroups: append([]uint32{}, wire.SupplementaryGroups...),
		mountNamespace:      instanceObjectIdentityFromWire(wire.MountNamespace),
		jailRoot:            instanceObjectIdentityFromWire(wire.JailRoot),
		executable: instanceFirecrackerExecutableIdentity{
			device:         wire.Executable.Device,
			inode:          wire.Executable.Inode,
			sizeBytes:      wire.Executable.SizeBytes,
			mode:           wire.Executable.Mode,
			uid:            wire.Executable.UID,
			gid:            wire.Executable.GID,
			links:          wire.Executable.Links,
			manifestSHA256: wire.Executable.ManifestSHA256,
		},
		relay: relay,
	}, nil
}

func instanceActiveRelayFromWire(wire instanceActiveRelayWire) (instanceActiveRelay, error) {
	relay := instanceActiveRelay{kind: wire.Kind}
	if wire.Kind != instanceRelayPresent {
		return relay, nil
	}
	if wire.UserIDs == nil || wire.GroupIDs == nil || wire.SupplementaryGroups == nil ||
		wire.MountNamespace == nil ||
		wire.Root == nil || wire.Executable == nil {
		return instanceActiveRelay{}, instanceAtomicError("active instance relay identity is incomplete", nil)
	}
	relay.userIDs = processCredentialIdentityFromWire(*wire.UserIDs)
	relay.groupIDs = processCredentialIdentityFromWire(*wire.GroupIDs)
	relay.supplementaryGroups = append([]uint32{}, (*wire.SupplementaryGroups)...)
	relay.mountNamespace = instanceObjectIdentityFromWire(*wire.MountNamespace)
	relay.root = instanceObjectIdentityFromWire(*wire.Root)
	relay.executable = instanceRelayExecutableIdentity{
		device:    wire.Executable.Device,
		inode:     wire.Executable.Inode,
		sizeBytes: wire.Executable.SizeBytes,
		mode:      wire.Executable.Mode,
		uid:       wire.Executable.UID,
		gid:       wire.Executable.GID,
		links:     wire.Executable.Links,
		sha256:    wire.Executable.SHA256,
	}
	return relay, nil
}

func processCredentialIdentityToWire(identity processCredentialIdentity) processCredentialIdentityWire {
	return processCredentialIdentityWire{
		Real:       identity.real,
		Effective:  identity.effective,
		Saved:      identity.saved,
		Filesystem: identity.filesystem,
	}
}

func processCredentialIdentityFromWire(wire processCredentialIdentityWire) processCredentialIdentity {
	return processCredentialIdentity{
		real:       wire.Real,
		effective:  wire.Effective,
		saved:      wire.Saved,
		filesystem: wire.Filesystem,
	}
}

func instanceObjectIdentityToWire(identity instanceObjectIdentity) instanceObjectIdentityWire {
	return instanceObjectIdentityWire{Device: identity.device, Inode: identity.inode}
}

func instanceObjectIdentityFromWire(wire instanceObjectIdentityWire) instanceObjectIdentity {
	return instanceObjectIdentity{device: wire.Device, inode: wire.Inode}
}

func managedCacheMountIdentityKindToWire(kind managedCacheMountIdentityKind) managedCacheMountIdentityKindWire {
	switch kind {
	case managedCacheMountIdentityUnavailable:
		return managedCacheMountIdentityUnavailableWire
	case managedCacheMountIdentityLegacy:
		return managedCacheMountIdentityLegacyWire
	case managedCacheMountIdentityUnique:
		return managedCacheMountIdentityUniqueWire
	default:
		return ""
	}
}

func managedCacheIdentityFromWire(wire managedCacheIdentityWire) (managedCacheIdentity, error) {
	var kind managedCacheMountIdentityKind
	switch wire.MountIdentity.Kind {
	case managedCacheMountIdentityUnavailableWire:
		kind = managedCacheMountIdentityUnavailable
	case managedCacheMountIdentityLegacyWire:
		kind = managedCacheMountIdentityLegacy
	case managedCacheMountIdentityUniqueWire:
		kind = managedCacheMountIdentityUnique
	default:
		return managedCacheIdentity{}, instanceAtomicError(
			"instance authority managed cache mount identity kind is invalid",
			nil,
		)
	}
	return managedCacheIdentity{
		deviceMajor: wire.DeviceMajor,
		deviceMinor: wire.DeviceMinor,
		inode:       wire.Inode,
		mountID:     wire.MountIdentity.ID,
		mountIDKind: kind,
	}, nil
}
