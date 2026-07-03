package errs

type Code string

const (
	// --- VM domain ---
	CodeVMNotFound           Code = "vm.not_found"
	CodeVMAlreadyExists      Code = "vm.already_exists"
	CodeVMStateInvalid       Code = "vm.state.invalid"
	CodeVMCreateFailed       Code = "vm.create.failed"
	CodeVMBuilderFailed      Code = "vm.create.builder_failed"
	CodeVMResolveFailed      Code = "vm.resolve.failed"
	CodeVMResourceExhausted  Code = "vm.resource.exhausted"
	CodeVMBinaryNotFound     Code = "vm.create.binary_not_found"
	CodeVMImageNotFound      Code = "vm.create.image_not_found"
	CodeVMKernelNotFound     Code = "vm.create.kernel_not_found"
	CodeVMNetworkNotFound    Code = "vm.create.network_not_found"
	CodeVMSSHKeyNotFound     Code = "vm.create.ssh_key_not_found"
	CodeVMNameCollision      Code = "vm.name_collision"
	CodeVMAtomicFailed       Code = "vm.atomic_failed"
	CodeVMCreateFailure      Code = "vm.create_failure"
	CodeVMSnapshotFailed     Code = "vm.snapshot_failed"
	CodeVMLoadSnapshotFailed Code = "vm.load_snapshot_failed"

	// --- Network domain ---
	CodeNetworkSubnetOverlap       Code = "network.subnet.overlap"
	CodeNetworkNotFound            Code = "network.not_found"
	CodeNetworkAlreadyExists       Code = "network.already_exists"
	CodeNetworkBridgeFailed        Code = "network.bridge.failed"
	CodeNetworkNATFailed           Code = "network.nat.failed"
	CodeNetworkLeaseFailed         Code = "network.lease.failed"
	CodeNetworkLeaseExhausted      Code = "network.lease.exhausted"
	CodeNetworkFirewallFailed      Code = "network.firewall.failed"
	CodeNetworkCreateFailed        Code = "network.create_failed"
	CodeNetworkRemoveFailed        Code = "network.remove_failed"
	CodeNetworkDefaultSetFailed    Code = "network.default_set_failed"
	CodeNetworkDefaultCreateFailed Code = "network.default_created_failed"

	// --- Image domain ---
	CodeImageNotFound           Code = "image.not_found"
	CodeImageAlreadyExists      Code = "image.already_exists"
	CodeImagePullFailed         Code = "image.pull.failed"
	CodeImageImportFailed       Code = "image.import.failed"
	CodeImageChecksumMismatch   Code = "image.checksum.mismatch"
	CodeImageCorrupt            Code = "image.corrupt"
	CodeImageEmpty              Code = "image.empty"
	CodeImageFormatInvalid      Code = "image.format.invalid"
	CodeImageError              Code = "image.error"
	CodeImageCompressionError   Code = "image.compression.failed"
	CodeImageDecompressionError Code = "image.decompression.failed"
	CodeRootPartitionDetection  Code = "image.root_partition_detection"
	CodeTieDetected             Code = "image.tie_detected"
	CodeImageAcquireFailed      Code = "image.acquire_failed"
	CodeImageWarmFailed         Code = "image.warm_failed"

	// --- Kernel domain ---
	CodeKernelNotFound         Code = "kernel.not_found"
	CodeKernelBuildFailed      Code = "kernel.build.failed"
	CodeKernelConfigFailed     Code = "kernel.config.failed"
	CodeKernelPullFailed       Code = "kernel.pull_failed"
	CodeKernelImportFailed     Code = "kernel.import_failed"
	CodeKernelDefaultSetFailed Code = "kernel.default_set_failed"

	// --- Binary domain ---
	CodeBinaryNotFound            Code = "binary.not_found"
	CodeBinaryAlreadyExists       Code = "binary.already_exists"
	CodeBinaryVersionGate         Code = "binary.version.gate"
	CodeBinaryError               Code = "binary.error"
	CodeBinaryPullFailed          Code = "binary.pull_failed"
	CodeBinaryRemoveFailed        Code = "binary.remove_failed"
	CodeBinaryDefaultSetFailed    Code = "binary.default_set_failed"
	CodeBinaryEnsureDefaultFailed Code = "binary.ensure_default_failed"
	CodeBinaryNoCIVersion         Code = "binary.no_ci_version"

	// --- Volume domain ---
	CodeVolumeNotFound      Code = "volume.not_found"
	CodeVolumeAlreadyExists Code = "volume.already_exists"
	CodeVolumeError         Code = "volume.error"
	CodeVolumeResizeFailed  Code = "volume.resize_failed"

	// --- Key domain ---
	CodeKeyNotFound            Code = "key.not_found"
	CodeKeyAlreadyExists       Code = "key.already_exists"
	CodeKeyExportFailed        Code = "key.export.failed"
	CodeKeyDependencyMissing   Code = "key.dependency.missing"
	CodeKeyCreateFailed        Code = "key.create_failed"
	CodeKeyAddFailed           Code = "key.add_failed"
	CodeKeyDefaultSetFailed    Code = "key.default_set_failed"
	CodeKeyDefaultsClearFailed Code = "key.defaults_clear_failed"

	// --- Host domain ---
	CodeHostInitFailed     Code = "host.init.failed"
	CodeHostCleanFailed    Code = "host.clean.failed"
	CodeHostResetFailed    Code = "host.reset.failed"
	CodePrivilegeRequired  Code = "host.privilege.required"
	CodePrivilegeSudoers   Code = "host.init.sudoers.failed"
	CodeHostInfoFailed     Code = "host.info_failed"
	CodeHostCapacityFailed Code = "host.capacity_failed"

	// --- Cloud-init domain ---
	CodeCloudInitProvisionFailed Code = "cloudinit.provision.failed"
	CodeCloudInitNetModeFailed   Code = "cloudinit.net_mode.failed"
	CodeCloudInitISOModeFailed   Code = "cloudinit.iso_mode.failed"
	CodeCloudInitInjectFailed    Code = "cloudinit.inject.failed"
	CodeCloudInitModeError       Code = "cloudinit.mode.error"
	CodeCloudInitOffModeError    Code = "cloudinit.off_mode.error"

	// --- Console domain ---
	CodeConsoleRelayFailed Code = "console.relay.failed"
	CodeConsoleNotRunning  Code = "console.not_running"
	CodeConsoleKillFailed  Code = "console.kill_failed"

	// --- Logs domain ---
	CodeLogsError Code = "logs.error"

	// --- Firecracker domain ---
	CodeFirecrackerError          Code = "firecracker.error"
	CodeFirecrackerClientError    Code = "firecracker.client.error"
	CodeFirecrackerSpawnError     Code = "firecracker.spawn.failed"
	CodeFirecrackerConfigError    Code = "firecracker.config.failed"
	CodeFirecrackerSocketNotFound Code = "firecracker.socket.not_found"

	// --- GuestFS domain ---
	CodeGuestfsError        Code = "guestfs.error"
	CodeGuestfsNotAvailable Code = "guestfs.not_available"
	CodeGuestfsWriteError   Code = "guestfs.write.failed"

	// --- LoopMount domain ---
	CodeLoopMountError          Code = "loopmount.error"
	CodeLoopMountBinaryNotFound Code = "loopmount.binary.not_found"
	CodeLoopMountTimeout        Code = "loopmount.timeout"

	// --- SSH/CP domain ---
	CodeSSHError              Code = "ssh.error"
	CodeCPError               Code = "cp.error"
	CodeCPSourceNotFound      Code = "cp.source.not_found"
	CodeCPSourceFailed        Code = "cp.source.failed"
	CodeCPCopyFailed          Code = "cp.copy.failed"
	CodeCPDestinationExists   Code = "cp.destination.exists"
	CodeCPDestinationFailed   Code = "cp.destination.failed"
	CodeCPDestinationNotDir   Code = "cp.destination.not_directory"
	CodeCPMultiSourceNoVMDest Code = "cp.multi_source_no_vm_destination"
	CodeCPResolveFailed       Code = "cp.resolve_failed"
	CodeCPNoVMSpecified       Code = "cp.no_vm_specified"
	CodeCPVMNoIP              Code = "cp.vm_no_ip"
	CodeCPVMNotFound          Code = "cp.vm_not_found"

	// --- Vsock domain ---
	CodeVsockNotFound          Code = "vsock.not_found"
	CodeVsockConnectionFailed  Code = "vsock.connection_failed"
	CodeVsockHandshakeFailed   Code = "vsock.handshake_failed"
	CodeAgentUnreachable  Code = "agent.unreachable"
	CodeVsockExecFailed        Code = "vsock.exec_failed"
	CodeVsockUpgradeInProgress Code = "vsock.upgrade_in_progress"
	CodeVsockConfigNotFound    Code = "vsock.config.not_found"

	// --- Remote exec domain ---
	CodeUnauthorized Code = "auth.unauthorized"
	CodeVMNotRunning Code = "vm.not_running"

	// --- BundledAsset domain ---
	CodeBundledAssetError    Code = "bundled_asset.error"
	CodeBundledAssetNotFound Code = "bundled_asset.not_found"

	// --- Snapshot domain ---
	CodeSnapshotNotFound      Code = "snapshot.not_found"
	CodeSnapshotAlreadyExists Code = "snapshot.already_exists"
	CodeSnapshotCreateFailed  Code = "snapshot.create_failed"
	CodeSnapshotRestoreFailed Code = "snapshot.restore_failed"
	CodeSnapshotRemoveFailed  Code = "snapshot.remove_failed"

	// --- Common ---
	CodeNetworkError         Code = "network.error"
	CodeKeyError             Code = "key.error"
	CodeVersionResolveFailed Code = "version.resolve.failed"
	CodeInternal             Code = "internal"
	CodeNotImplemented       Code = "not_implemented"
	CodeValidationFailed     Code = "validation.failed"
	CodeDatabaseError        Code = "database.error"
	CodeMigrationFailed      Code = "database.migration.failed"
	CodeProcessError         Code = "process.error"
	CodeDownloadFailed       Code = "download.failed"
	CodeHTTPError            Code = "http.error"
	CodeConfigError          Code = "config.error"
	CodeCacheCleanFailed     Code = "cache.clean_failed"
)
