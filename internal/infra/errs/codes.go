package errs

type Code string

const (
	// ── VM domain ──
	CodeVMNotFound          Code = "vm.not_found"
	CodeVMAlreadyExists     Code = "vm.already_exists"
	CodeVMStateInvalid      Code = "vm.state.invalid"
	CodeVMCreateFailed      Code = "vm.create.failed"
	CodeVMBuilderFailed     Code = "vm.create.builder_failed"
	CodeVMResolveFailed     Code = "vm.resolve.failed"
	CodeVMResourceExhausted Code = "vm.resource.exhausted"
	CodeVMBinaryNotFound    Code = "vm.create.binary_not_found"
	CodeVMImageNotFound     Code = "vm.create.image_not_found"
	CodeVMKernelNotFound    Code = "vm.create.kernel_not_found"
	CodeVMNetworkNotFound   Code = "vm.create.network_not_found"
	CodeVMSSHKeyNotFound    Code = "vm.create.ssh_key_not_found"

	// ── Network domain ──
	CodeNetworkSubnetOverlap  Code = "network.subnet.overlap"
	CodeNetworkNotFound       Code = "network.not_found"
	CodeNetworkAlreadyExists  Code = "network.already_exists"
	CodeNetworkBridgeFailed   Code = "network.bridge.failed"
	CodeNetworkNATFailed      Code = "network.nat.failed"
	CodeNetworkLeaseFailed    Code = "network.lease.failed"
	CodeNetworkLeaseExhausted Code = "network.lease.exhausted"
	CodeNetworkFirewallFailed Code = "network.firewall.failed"

	// ── Image domain ──
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

	// ── Kernel domain ──
	CodeKernelNotFound     Code = "kernel.not_found"
	CodeKernelBuildFailed  Code = "kernel.build.failed"
	CodeKernelConfigFailed Code = "kernel.config.failed"

	// ── Binary domain ──
	CodeBinaryNotFound      Code = "binary.not_found"
	CodeBinaryAlreadyExists Code = "binary.already_exists"
	CodeBinaryVersionGate   Code = "binary.version.gate"
	CodeBinaryError         Code = "binary.error"

	// ── Volume domain ──
	CodeVolumeNotFound      Code = "volume.not_found"
	CodeVolumeAlreadyExists Code = "volume.already_exists"
	CodeVolumeError         Code = "volume.error"

	// ── Key domain ──
	CodeKeyNotFound          Code = "key.not_found"
	CodeKeyAlreadyExists     Code = "key.already_exists"
	CodeKeyExportFailed      Code = "key.export.failed"
	CodeKeyDependencyMissing Code = "key.dependency.missing"

	// ── Host domain ──
	CodeHostInitFailed    Code = "host.init.failed"
	CodeHostCleanFailed   Code = "host.clean.failed"
	CodeHostResetFailed   Code = "host.reset.failed"
	CodePrivilegeRequired Code = "host.privilege.required"
	CodePrivilegeSudoers  Code = "host.init.sudoers.failed"

	// ── Cloud-init domain ──
	CodeCloudInitProvisionFailed Code = "cloudinit.provision.failed"
	CodeCloudInitNetModeFailed   Code = "cloudinit.net_mode.failed"
	CodeCloudInitISOModeFailed   Code = "cloudinit.iso_mode.failed"
	CodeCloudInitInjectFailed    Code = "cloudinit.inject.failed"
	CodeCloudInitModeError       Code = "cloudinit.mode.error"
	CodeCloudInitOffModeError    Code = "cloudinit.off_mode.error"

	// ── Console domain ──
	CodeConsoleRelayFailed Code = "console.relay.failed"

	// ── Logs domain ──
	CodeLogsError Code = "logs.error"

	// ── Firecracker domain ──
	CodeFirecrackerError          Code = "firecracker.error"
	CodeFirecrackerClientError    Code = "firecracker.client.error"
	CodeFirecrackerSpawnError     Code = "firecracker.spawn.failed"
	CodeFirecrackerConfigError    Code = "firecracker.config.failed"
	CodeFirecrackerSocketNotFound Code = "firecracker.socket.not_found"

	// ── GuestFS domain ──
	CodeGuestfsError        Code = "guestfs.error"
	CodeGuestfsNotAvailable Code = "guestfs.not_available"
	CodeGuestfsWriteError   Code = "guestfs.write.failed"

	// ── LoopMount domain ──
	CodeLoopMountError          Code = "loopmount.error"
	CodeLoopMountBinaryNotFound Code = "loopmount.binary.not_found"
	CodeLoopMountTimeout        Code = "loopmount.timeout"

	// ── SSH/CP domain ──
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

	// ── BundledAsset domain ──
	CodeBundledAssetError    Code = "bundled_asset.error"
	CodeBundledAssetNotFound Code = "bundled_asset.not_found"

	// ── Common ──
	CodeInternal         Code = "internal"
	CodeNotImplemented   Code = "not_implemented"
	CodeValidationFailed Code = "validation.failed"
	CodeDatabaseError    Code = "database.error"
	CodeMigrationFailed  Code = "database.migration.failed"
	CodeProcessError     Code = "process.error"
	CodeDownloadFailed   Code = "download.failed"
	CodeHTTPError        Code = "http.error"
	CodeConfigError      Code = "config.error"
)
