package jailer

import (
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

const (
	instanceRecordSchemaVersion = uint32(5)
	maxInstanceRecordBytes      = 16 * 1024
)

var (
	authorityHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	hostBootIDPattern    = regexp.MustCompile(
		`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`,
	)
)

type instanceLifecycle string

const (
	instanceLifecycleLaunching instanceLifecycle = "launching"
	instanceLifecycleActive    instanceLifecycle = "active"
	instanceLifecycleCleaning  instanceLifecycle = "cleaning"
	instanceLifecycleCleaned   instanceLifecycle = "cleaned"
)

type instanceRelayKind string

const (
	instanceRelayAbsent  instanceRelayKind = "absent"
	instanceRelayPresent instanceRelayKind = "present"
)

type instanceCaller struct {
	uid uint32
	gid uint32
}

type releaseSlot struct {
	version      string
	architecture string
}

type releaseIdentity struct {
	version           string
	architecture      string
	firecrackerSHA256 string
	jailerSHA256      string
}

type instanceProcessLocator struct {
	pid            int
	startTimeTicks uint64
}

type instanceLaunchRelay struct {
	kind    instanceRelayKind
	locator instanceProcessLocator
}

type launchIntent struct {
	vmID              string
	networkPlacement  model.VMNetworkPlacement
	hostBootID        string
	cacheIdentity     managedCacheIdentity
	cacheLocator      managedCacheLocator
	executionIdentity vmExecutionIdentity
	snapshotID        string
}

type instanceLaunchRecord struct {
	ownerGID          uint32
	networkPlacement  model.VMNetworkPlacement
	executionIdentity vmExecutionIdentity
	snapshotID        string
	outputIdentity    *instanceOutputIdentities
	hostBootID        string
	process           instanceProcessLocator
	relay             instanceLaunchRelay
	cacheIdentity     managedCacheIdentity
	cacheLocator      managedCacheLocator
	release           releaseIdentity
}

type instanceOutputIdentities struct {
	log     instanceObjectIdentity
	metrics *instanceObjectIdentity
}

type processCredentialIdentity struct {
	real       uint32
	effective  uint32
	saved      uint32
	filesystem uint32
}

type instanceObjectIdentity struct {
	device uint64
	inode  uint64
}

type instanceFirecrackerExecutableIdentity struct {
	device         uint64
	inode          uint64
	sizeBytes      uint64
	mode           uint32
	uid            uint32
	gid            uint32
	links          uint64
	manifestSHA256 string
}

type instanceRelayExecutableIdentity struct {
	device    uint64
	inode     uint64
	sizeBytes uint64
	mode      uint32
	uid       uint32
	gid       uint32
	links     uint64
	sha256    string
}

type instanceActiveRelay struct {
	kind                instanceRelayKind
	userIDs             processCredentialIdentity
	groupIDs            processCredentialIdentity
	supplementaryGroups []uint32
	mountNamespace      instanceObjectIdentity
	root                instanceObjectIdentity
	executable          instanceRelayExecutableIdentity
}

type instanceActiveIdentity struct {
	userIDs             processCredentialIdentity
	groupIDs            processCredentialIdentity
	supplementaryGroups []uint32
	mountNamespace      instanceObjectIdentity
	jailRoot            instanceObjectIdentity
	executable          instanceFirecrackerExecutableIdentity
	relay               instanceActiveRelay
}

type instanceRecord struct {
	schemaVersion     uint32
	ownerUID          uint32
	vmID              string
	networkPlacement  model.VMNetworkPlacement
	lifecycle         instanceLifecycle
	launchGeneration  uint64
	cleanupGeneration uint64
	launch            *instanceLaunchRecord
	active            *instanceActiveIdentity
}

func registerLaunchingInstanceRecord(
	caller instanceCaller,
	intent launchIntent,
	processes launchProcessLocators,
	release releaseIdentity,
	existing *instanceRecord,
) (instanceRecord, error) {
	if err := validateInstanceCaller(caller); err != nil {
		return instanceRecord{}, err
	}
	if err := validateLaunchIntent(intent); err != nil {
		return instanceRecord{}, err
	}
	if err := validateLaunchProcessLocators(processes); err != nil {
		return instanceRecord{}, err
	}
	if err := validateReleaseIdentityValue(release); err != nil {
		return instanceRecord{}, instanceAtomicError(err.Error(), nil)
	}
	launchGeneration := uint64(1)
	cleanupGeneration := uint64(0)
	if existing != nil {
		if err := validateInstanceRecord(*existing); err != nil {
			return instanceRecord{}, err
		}
		if err := authorizeInstanceRecord(caller, *existing); err != nil {
			return instanceRecord{}, err
		}
		if existing.vmID != intent.vmID {
			return instanceRecord{}, instanceValidationError("launching VM identity does not match existing authority")
		}
		if existing.lifecycle != instanceLifecycleCleaned {
			return instanceRecord{}, errs.AlreadyExists(
				errs.CodeVMAlreadyExists,
				"vm already has live privileged authority",
				errs.WithEntity(intent.vmID),
			)
		}
		if existing.networkPlacement != intent.networkPlacement {
			return instanceRecord{}, instanceValidationError("launch network placement differs from cleaned authority")
		}
		if existing.launchGeneration == math.MaxUint64 {
			return instanceRecord{}, instanceStateError(intent.vmID, "launch generation is exhausted")
		}
		launchGeneration = existing.launchGeneration + 1
		cleanupGeneration = existing.cleanupGeneration
	}
	launch := &instanceLaunchRecord{
		ownerGID:          caller.gid,
		networkPlacement:  intent.networkPlacement,
		executionIdentity: intent.executionIdentity,
		snapshotID:        intent.snapshotID,
		hostBootID:        intent.hostBootID,
		process:           processes.process,
		relay:             processes.relay,
		cacheIdentity:     intent.cacheIdentity,
		cacheLocator:      intent.cacheLocator,
		release:           release,
	}
	record := instanceRecord{
		schemaVersion:     instanceRecordSchemaVersion,
		ownerUID:          caller.uid,
		vmID:              intent.vmID,
		networkPlacement:  intent.networkPlacement,
		lifecycle:         instanceLifecycleLaunching,
		launchGeneration:  launchGeneration,
		cleanupGeneration: cleanupGeneration,
		launch:            launch,
	}
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	return record, nil
}

func activateInstanceRecord(
	record instanceRecord,
	identity instanceActiveIdentity,
) (instanceRecord, error) {
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	if record.lifecycle != instanceLifecycleLaunching {
		return instanceRecord{}, instanceStateError(record.vmID, "vm launch is not awaiting activation")
	}
	if err := validateInstanceActiveIdentity(record, identity); err != nil {
		return instanceRecord{}, instanceStateError(record.vmID, err.Error())
	}
	identity.supplementaryGroups = append([]uint32{}, identity.supplementaryGroups...)
	if identity.relay.kind == instanceRelayPresent {
		identity.relay.supplementaryGroups = append([]uint32{}, identity.relay.supplementaryGroups...)
	}
	record.lifecycle = instanceLifecycleActive
	record.active = &identity
	return record, nil
}

func beginInstanceCleanup(caller instanceCaller, record instanceRecord) (instanceRecord, error) {
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	if err := authorizeInstanceRecord(caller, record); err != nil {
		return instanceRecord{}, err
	}
	switch record.lifecycle {
	case instanceLifecycleLaunching, instanceLifecycleActive:
		record.lifecycle = instanceLifecycleCleaning
		record.cleanupGeneration++
	case instanceLifecycleCleaning:
		return record, nil
	case instanceLifecycleCleaned:
		return instanceRecord{}, errs.New(
			errs.CodeVMNotRunning,
			"vm has no live privileged runtime",
			errs.WithEntity(record.vmID),
		)
	default:
		return instanceRecord{}, instanceStateError(record.vmID, "vm authority lifecycle is invalid")
	}
	return record, nil
}

func completeInstanceCleanup(record instanceRecord) (instanceRecord, error) {
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	if record.lifecycle != instanceLifecycleCleaning {
		return instanceRecord{}, instanceStateError(record.vmID, "vm cleanup is not in progress")
	}
	record.lifecycle = instanceLifecycleCleaned
	record.launch = nil
	record.active = nil
	return record, nil
}

func authorizeInstanceRecord(caller instanceCaller, record instanceRecord) error {
	if err := validateInstanceCaller(caller); err != nil {
		return err
	}
	if caller.uid != record.ownerUID {
		return errs.New(
			errs.CodeUnauthorized,
			"vm privileged authority belongs to another user",
			errs.WithEntity(record.vmID),
		)
	}
	return nil
}

func validateInstanceCaller(caller instanceCaller) error {
	if caller.uid == 0 || caller.uid == math.MaxUint32 {
		return instanceValidationError("invoking user UID is invalid")
	}
	if caller.gid == 0 || caller.gid == math.MaxUint32 {
		return instanceValidationError("invoking user GID is invalid")
	}
	return nil
}

func validateLaunchIntent(intent launchIntent) error {
	if !vmIDPattern.MatchString(intent.vmID) {
		return instanceValidationError("vm ID is invalid")
	}
	if intent.networkPlacement != model.VMNetworkPlacementManaged &&
		intent.networkPlacement != model.VMNetworkPlacementNetworkless {
		return instanceValidationError("instance launch network placement is invalid")
	}
	if intent.networkPlacement == model.VMNetworkPlacementNetworkless && intent.snapshotID != "" {
		return instanceValidationError("networkless snapshot restore is not admitted")
	}
	if !hostBootIDPattern.MatchString(intent.hostBootID) {
		return instanceValidationError("instance authority host boot ID is invalid")
	}
	if err := validateManagedCacheIdentityValue(intent.cacheIdentity); err != nil {
		return instanceValidationError(err.Error())
	}
	return nil
}

func validateLaunchProcessLocators(processes launchProcessLocators) error {
	if err := validateInstanceProcessLocator(processes.process); err != nil {
		return instanceValidationError(err.Error())
	}
	if err := validateInstanceLaunchRelay(processes.relay); err != nil {
		return instanceValidationError(err.Error())
	}
	if processes.relay.kind == instanceRelayPresent && processes.relay.locator.pid == processes.process.pid {
		return instanceValidationError("instance authority relay PID matches the Firecracker child PID")
	}
	return nil
}

func validateInstanceRecord(record instanceRecord) error {
	if record.schemaVersion != instanceRecordSchemaVersion {
		return instanceAtomicError("unsupported instance authority schema", nil)
	}
	if record.ownerUID == 0 || record.ownerUID == math.MaxUint32 {
		return instanceAtomicError("instance authority owner UID is invalid", nil)
	}
	if !vmIDPattern.MatchString(record.vmID) {
		return instanceAtomicError("instance authority VM ID is invalid", nil)
	}
	if record.networkPlacement != model.VMNetworkPlacementManaged &&
		record.networkPlacement != model.VMNetworkPlacementNetworkless {
		return instanceAtomicError("instance authority network placement is invalid", nil)
	}
	if record.launchGeneration == 0 {
		return instanceAtomicError("instance authority launch generation is invalid", nil)
	}
	switch record.lifecycle {
	case instanceLifecycleLaunching:
		if record.cleanupGeneration == math.MaxUint64 || record.cleanupGeneration+1 != record.launchGeneration {
			return instanceAtomicError("instance authority generations are inconsistent", nil)
		}
		if record.launch == nil || record.active != nil {
			return instanceAtomicError("launching instance authority shape is invalid", nil)
		}
	case instanceLifecycleActive:
		if record.cleanupGeneration == math.MaxUint64 || record.cleanupGeneration+1 != record.launchGeneration {
			return instanceAtomicError("instance authority generations are inconsistent", nil)
		}
		if record.launch == nil || record.active == nil {
			return instanceAtomicError("active instance authority shape is invalid", nil)
		}
	case instanceLifecycleCleaning:
		if record.cleanupGeneration != record.launchGeneration || record.launch == nil {
			return instanceAtomicError("cleaning instance authority shape is invalid", nil)
		}
	case instanceLifecycleCleaned:
		if record.cleanupGeneration != record.launchGeneration || record.launch != nil || record.active != nil {
			return instanceAtomicError("cleaned instance authority shape is invalid", nil)
		}
	default:
		return instanceAtomicError("instance authority lifecycle is invalid", nil)
	}
	if record.launch != nil {
		if record.launch.networkPlacement != record.networkPlacement {
			return instanceAtomicError("instance authority network placement differs from launch", nil)
		}
		if record.launch.executionIdentity.uid == record.ownerUID {
			return instanceAtomicError("execution identity must be isolated from the operator", nil)
		}
		if err := validateInstanceLaunchRecord(*record.launch); err != nil {
			return instanceAtomicError(err.Error(), nil)
		}
	}
	if record.active != nil {
		if err := validateInstanceActiveIdentity(record, *record.active); err != nil {
			return instanceAtomicError(err.Error(), nil)
		}
	}
	return nil
}

func validateInstanceLaunchRecord(launch instanceLaunchRecord) error {
	if launch.networkPlacement != model.VMNetworkPlacementManaged &&
		launch.networkPlacement != model.VMNetworkPlacementNetworkless {
		return fmt.Errorf("instance launch network placement is invalid")
	}
	if launch.networkPlacement == model.VMNetworkPlacementNetworkless && launch.snapshotID != "" {
		return fmt.Errorf("networkless snapshot restore is not admitted")
	}
	if launch.outputIdentity != nil {
		if launch.outputIdentity.log.device == 0 || launch.outputIdentity.log.inode == 0 {
			return fmt.Errorf("instance log identity is invalid")
		}
		if launch.outputIdentity.metrics != nil &&
			(launch.outputIdentity.metrics.device == 0 || launch.outputIdentity.metrics.inode == 0) {
			return fmt.Errorf("instance metrics identity is invalid")
		}
	}
	if err := validateManagedCacheLocator(launch.cacheLocator); err != nil {
		return err
	}
	if launch.snapshotID != "" && !isCanonicalManagedResourceID(launch.snapshotID, 64) {
		return fmt.Errorf("instance snapshot binding is invalid")
	}
	if err := validateInstanceCaller(
		instanceCaller{uid: launch.executionIdentity.uid, gid: launch.executionIdentity.gid},
	); err != nil {
		return fmt.Errorf("instance authority execution identity is invalid: %w", err)
	}
	if launch.ownerGID == 0 || launch.ownerGID == math.MaxUint32 {
		return fmt.Errorf("instance authority owner GID is invalid")
	}
	if err := validateInstanceLaunchValues(
		launch.hostBootID,
		launch.process,
		launch.relay,
		launch.cacheIdentity,
	); err != nil {
		return err
	}
	return validateReleaseIdentityValue(launch.release)
}

func validateInstanceLaunchValues(
	hostBootID string,
	process instanceProcessLocator,
	relay instanceLaunchRelay,
	cacheIdentity managedCacheIdentity,
) error {
	if !hostBootIDPattern.MatchString(hostBootID) {
		return fmt.Errorf("instance authority host boot ID is invalid")
	}
	if err := validateInstanceProcessLocator(process); err != nil {
		return err
	}
	if err := validateInstanceLaunchRelay(relay); err != nil {
		return err
	}
	if relay.kind == instanceRelayPresent && relay.locator.pid == process.pid {
		return fmt.Errorf("instance authority relay PID matches the Firecracker child PID")
	}
	if err := validateManagedCacheIdentityValue(cacheIdentity); err != nil {
		return err
	}
	return nil
}

func validateInstanceProcessLocator(locator instanceProcessLocator) error {
	if locator.pid <= 0 || locator.pid > math.MaxInt32 || locator.startTimeTicks == 0 {
		return fmt.Errorf("instance authority process locator is invalid")
	}
	return nil
}

func validateInstanceLaunchRelay(relay instanceLaunchRelay) error {
	switch relay.kind {
	case instanceRelayAbsent:
		if relay.locator != (instanceProcessLocator{}) {
			return fmt.Errorf("absent instance relay has a process locator")
		}
	case instanceRelayPresent:
		if err := validateInstanceProcessLocator(relay.locator); err != nil {
			return fmt.Errorf("instance authority relay locator is invalid")
		}
	default:
		return fmt.Errorf("instance authority relay kind is invalid")
	}
	return nil
}

func validateInstanceActiveIdentity(record instanceRecord, identity instanceActiveIdentity) error {
	if record.launch == nil {
		return fmt.Errorf("instance authority launch identity is absent")
	}
	if !processCredentialIdentityMatches(record.launch.executionIdentity.uid, identity.userIDs) ||
		!processCredentialIdentityMatches(record.launch.executionIdentity.gid, identity.groupIDs) {
		return fmt.Errorf("instance authority Firecracker credentials do not match its owner")
	}
	if len(identity.supplementaryGroups) != 0 {
		return fmt.Errorf("instance authority Firecracker supplementary groups are not empty")
	}
	if err := validateInstanceObjectIdentity("mount namespace", identity.mountNamespace); err != nil {
		return err
	}
	if err := validateInstanceObjectIdentity("jail root", identity.jailRoot); err != nil {
		return err
	}
	if err := validateFirecrackerExecutableIdentity(identity.executable); err != nil {
		return err
	}
	if identity.executable.manifestSHA256 != record.launch.release.firecrackerSHA256 {
		return fmt.Errorf("instance authority Firecracker executable digest does not match its release")
	}
	return validateInstanceActiveRelay(
		record.ownerUID,
		record.launch.ownerGID,
		record.launch.relay.kind,
		identity.relay,
	)
}

func validateInstanceActiveRelay(
	ownerUID uint32,
	ownerGID uint32,
	launchKind instanceRelayKind,
	relay instanceActiveRelay,
) error {
	if relay.kind != launchKind {
		return fmt.Errorf("instance authority relay presence changed during activation")
	}
	switch relay.kind {
	case instanceRelayAbsent:
		if !instanceActiveRelayIdentityIsEmpty(relay) {
			return fmt.Errorf("absent active instance relay has final identity")
		}
		return nil
	case instanceRelayPresent:
	default:
		return fmt.Errorf("active instance authority relay kind is invalid")
	}
	if !processCredentialIdentityMatches(ownerUID, relay.userIDs) ||
		!processCredentialIdentityMatches(ownerGID, relay.groupIDs) {
		return fmt.Errorf("instance authority relay credentials do not match its owner")
	}
	if len(relay.supplementaryGroups) != 0 {
		return fmt.Errorf("instance authority relay supplementary groups are not empty")
	}
	if err := validateInstanceObjectIdentity("relay mount namespace", relay.mountNamespace); err != nil {
		return err
	}
	if err := validateInstanceObjectIdentity("relay root", relay.root); err != nil {
		return err
	}
	return validateRelayExecutableIdentity(relay.executable)
}

func instanceActiveRelayIdentityIsEmpty(relay instanceActiveRelay) bool {
	return relay.userIDs == (processCredentialIdentity{}) &&
		relay.groupIDs == (processCredentialIdentity{}) &&
		relay.supplementaryGroups == nil &&
		relay.mountNamespace == (instanceObjectIdentity{}) &&
		relay.root == (instanceObjectIdentity{}) &&
		relay.executable == (instanceRelayExecutableIdentity{})
}

func processCredentialIdentityMatches(value uint32, identity processCredentialIdentity) bool {
	return identity == (processCredentialIdentity{
		real: value, effective: value, saved: value, filesystem: value,
	})
}

func validateInstanceObjectIdentity(name string, identity instanceObjectIdentity) error {
	if identity.device == 0 || identity.inode == 0 {
		return fmt.Errorf("instance authority %s identity is invalid", name)
	}
	return nil
}

func validateFirecrackerExecutableIdentity(identity instanceFirecrackerExecutableIdentity) error {
	if identity.device == 0 || identity.inode == 0 ||
		identity.sizeBytes < trustedReleaseExecutableMinBytes ||
		identity.sizeBytes > trustedReleaseExecutableMaxBytes {
		return fmt.Errorf("instance authority Firecracker executable identity is invalid")
	}
	if identity.mode != unix.S_IFREG|trustedReleaseStoreExecutableMode ||
		identity.uid != 0 || identity.gid != 0 || identity.links != 1 {
		return fmt.Errorf("instance authority Firecracker executable metadata is invalid")
	}
	if !authorityHashPattern.MatchString(identity.manifestSHA256) {
		return fmt.Errorf("instance authority Firecracker executable digest is invalid")
	}
	return nil
}

func validateRelayExecutableIdentity(identity instanceRelayExecutableIdentity) error {
	if identity.device == 0 || identity.inode == 0 || identity.sizeBytes == 0 {
		return fmt.Errorf("instance authority relay executable identity is invalid")
	}
	if identity.mode != unix.S_IFREG|trustedReleaseStoreExecutableMode ||
		identity.uid != 0 || identity.gid != 0 || identity.links != 1 {
		return fmt.Errorf("instance authority relay executable metadata is invalid")
	}
	if !authorityHashPattern.MatchString(identity.sha256) {
		return fmt.Errorf("instance authority relay executable digest is invalid")
	}
	return nil
}

func validateManagedCacheIdentityValue(identity managedCacheIdentity) error {
	if (identity.deviceMajor == 0 && identity.deviceMinor == 0) || identity.inode == 0 {
		return fmt.Errorf("instance authority managed cache identity is invalid")
	}
	switch identity.mountIDKind {
	case managedCacheMountIdentityUnavailable:
		if identity.mountID != 0 {
			return fmt.Errorf("instance authority managed cache mount identity is inconsistent")
		}
	case managedCacheMountIdentityLegacy, managedCacheMountIdentityUnique:
		if identity.mountID == 0 {
			return fmt.Errorf("instance authority managed cache mount identity is incomplete")
		}
	default:
		return fmt.Errorf("instance authority managed cache mount identity kind is invalid")
	}
	return nil
}

func validateReleaseIdentityValue(release releaseIdentity) error {
	if err := validateReleaseSlotValue(releaseSlotForIdentity(release)); err != nil {
		return err
	}
	if !authorityHashPattern.MatchString(release.firecrackerSHA256) ||
		!authorityHashPattern.MatchString(release.jailerSHA256) {
		return fmt.Errorf(
			"instance authority release hashes must be %d lowercase hexadecimal characters",
			sha256.Size*2,
		)
	}
	return nil
}

func validateReleaseSlotValue(slot releaseSlot) error {
	if !versionPattern.MatchString(slot.version) || strings.Contains(slot.version, "..") {
		return fmt.Errorf("instance authority release version is invalid")
	}
	if slot.architecture != "x86_64" && slot.architecture != "aarch64" {
		return fmt.Errorf("instance authority release architecture is invalid")
	}
	return nil
}

func releaseSlotForIdentity(release releaseIdentity) releaseSlot {
	return releaseSlot{version: release.version, architecture: release.architecture}
}

func instanceValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}

func instanceStateError(vmID, message string) *errs.DomainError {
	return errs.New(errs.CodeVMStateInvalid, message, errs.WithEntity(vmID))
}

func instanceAtomicError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(errs.ClassInternal))
}
