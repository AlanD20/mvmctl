# mvmctl Python API Reference

## Introduction

Every CLI command maps 1:1 to a static method on an `*Operation` class in
`mvmctl.api.*`. The CLI is a thin presentation layer on top of these classes — it
handles argument parsing, output formatting, and exit codes, then calls the same
functions documented here.

You can import the API directly to build automation scripts, GUIs, or TUIs without
going through the CLI. All system interactions (KVM, iptables, bridge devices) happen
lazily — importing the package has no side effects.

---

## Installation

```bash
# From source
git clone https://github.com/your-org/mvmctl
cd mvmctl
uv sync
```

Then import:

```python
from mvmctl.api import VMOperation, VMCreateInput

VMOperation.create(VMCreateInput(name="my-vm", ssh_keys=["my-key"], ...))
```

---

## Import Pattern

All public types are re-exported from `mvmctl.api`:

```python
from mvmctl.api import (
    # Operation classes
    VMOperation,
    NetworkOperation,
    ImageOperation,
    KernelOperation,
    KeyOperation,
    BinaryOperation,
    HostOperation,
    CacheOperation,
    SSHOperation,
    InitOperation,
    ConsoleOperation,
    ConfigOperation,
    LogOperation,
    # Input classes
    VMCreateInput,
    VMInput,
    NetworkCreateInput,
    NetworkInput,
    ImagePullInput,
    ImageImportInput,
    ImageInput,
    KernelPullInput,
    KernelInput,
    KeyCreateInput,
    KeyInput,
    BinaryPullInput,
    BinaryInput,
    SSHInput,
    ConsoleInput,
    ConsoleRequest,
    LogInput,
    # Result types
    InitResult,
    InitStepResult,
    ConsoleConnectionInfo,
)
```

Deep imports from sub-modules are **not** part of the public API:

```python
from mvmctl.api import VMOperation  # ✅ CORRECT
from mvmctl.api.vm_operations import VMOperation  # ❌ WRONG — internal module
```

---

## Module Overview

| Operation Class | Responsibility |
|---|---|
| `VMOperation` | VM lifecycle: create, remove, import, export, list, inspect, get, start/stop, pause/resume, reboot, snapshot, load snapshot |
| `NetworkOperation` | Network management: create, remove, list, get, inspect, set default, restore, sync, create default |
| `ImageOperation` | Image operations: fetch, import, list, get, set default, remove, inspect, warm |
| `KernelOperation` | Kernel operations: fetch, list, get, inspect, set default, remove |
| `KeyOperation` | SSH key registry: add, create, list, get, remove, inspect, set defaults, get defaults, clear defaults, export |
| `BinaryOperation` | Binary management: fetch, get, list local/remote, set default, remove (by id/version), ensure default |
| `HostOperation` | Host init/reset/clean, state retrieval, privilege checks, KVM access, running VMs |
| `CacheOperation` | Cache lifecycle: init, prune per-asset-type, prune misc, prune all, clean |
| `SSHOperation` | SSH connection to VMs |
| `InitOperation` | Onboarding wizard: database, host, cache, binary setup |
| `ConsoleOperation` | Console relay: get connection info, get state, kill |
| `ConfigOperation` | User settings: get, set, reset, list all config overrides |
| `LogOperation` | VM log streaming and retrieval |

---

## Data Models

All data models are in `mvmctl.models.*`. Models are pure dataclasses — no business
logic. Every domain record uses the `*Item` suffix.

### `mvmctl.models.vm`

#### `VMStatus`

```python
class VMStatus(StrEnum):
    STARTING = auto()   # value = "STARTING"
    RUNNING = auto()    # value = "RUNNING"
    PAUSED = auto()      # value = "PAUSED"
    STOPPING = auto()    # value = "STOPPING"
    STOPPED = auto()     # value = "STOPPED"
    CRASHED = auto()    # value = "CRASHED"
    ERROR = auto()      # value = "ERROR"
```

#### `VMInstanceItem`

Runtime state for a registered VM.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | VM ID (hash) |
| `name` | `str` | VM name; also used as hostname inside the guest |
| `status` | `str` | Current lifecycle state |
| `pid` | `int` | Firecracker process PID |
| `ipv4` | `str` | Assigned guest IP address |
| `mac` | `str` | Assigned guest MAC address |
| `network_id` | `str` | Network ID this VM is attached to |
| `tap_device` | `str` | Host TAP interface name |
| `image_id` | `str` | Image ID |
| `kernel_id` | `str` | Kernel ID |
| `binary_id` | `str` | Firecracker binary ID |
| `api_socket_path` | `str` | Path to Firecracker API socket |
| `config_path` | `str` | Path to Firecracker JSON config |
| `cloud_init_mode` | `str` | Cloud-init mode used |
| `vcpu_count` | `int` | Number of vCPUs |
| `mem_size_mib` | `int` | Memory in MiB |
| `disk_size_mib` | `int` | Root filesystem size in MiB |
| `rootfs_path` | `str` | Path to rootfs image |
| `rootfs_suffix` | `str` | Root filesystem suffix (e.g. `ext4`) |
| `enable_pci` | `bool` | PCI device support enabled |
| `enable_logging` | `bool` | Logging enabled |
| `enable_metrics` | `bool` | Metrics enabled |
| `enable_console` | `bool` | Serial console enabled |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |
| `relay_socket_path` | `str \| None` | Console relay socket path |
| `process_start_time` | `int \| None` | Firecracker process start time |
| `nocloud_net_port` | `int \| None` | Port for nocloud-net server |
| `nocloud_net_pid` | `int \| None` | nocloud-net server PID |
| `relay_pid` | `int \| None` | Console relay PID |
| `exit_code` | `int \| None` | Firecracker exit code |
| `log_path` | `str \| None` | Firecracker log path |
| `serial_output_path` | `str \| None` | Serial console output path |
| `lsm_flags` | `str \| None` | Linux Security Module flags |
| `boot_args` | `str \| None` | Kernel boot arguments |

**Resolved relations** (populated on request):

| Field | Type | Description |
|-------|------|-------------|
| `kernel` | `KernelItem \| None` | Resolved kernel record |
| `image` | `ImageItem \| None` | Resolved image record |
| `binary` | `BinaryItem \| None` | Resolved binary record |
| `network` | `NetworkItem \| None` | Resolved network record |

#### `ConsoleInfo`

Console relay connection info.

| Field | Type | Description |
|-------|------|-------------|
| `socket_path` | `Path` | Path to the console relay socket |
| `vm_name` | `str` | VM name |

#### `ConsoleState`

Console relay state.

| Field | Type | Description |
|-------|------|-------------|
| `running` | `bool` | Whether the relay is currently running |
| `pid` | `int \| None` | Relay process PID, or None |
| `socket_path` | `str \| None` | Socket path string, or None |

#### `VMInspectInfo`

Full inspection data for a VM.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | VM ID (hash) |
| `name` | `str` | VM name |
| `status` | `str` | Lifecycle status |
| `created_at` | `str \| None` | ISO 8601 creation timestamp |
| `pid` | `int \| None` | Firecracker PID |
| `ip` | `str \| None` | Assigned IP |
| `mac` | `str \| None` | Assigned MAC |
| `network_name` | `str \| None` | Network name |
| `tap_device` | `str \| None` | TAP device name |
| `cloud_init_mode` | `str` | Cloud-init mode |
| `image_id` | `str \| None` | Image ID |
| `image_name` | `str \| None` | Image name |
| `kernel_id` | `str \| None` | Kernel ID |
| `kernel_name` | `str \| None` | Kernel name |
| `paths` | `dict[str, str \| None]` | Paths: vm_dir, rootfs, config |
| `features` | `dict[str, bool]` | Flags: api_socket, console, nocloud_net |
| `nocloud_net` | `dict[str, Any] \| None` | nocloud-net details |
| `console` | `dict[str, Any] \| None` | Console details |

### `mvmctl.models.cloudinit`

#### `CloudInitMode`

```python
class CloudInitMode(StrEnum):
    INJECT = "inject"   # Inject cloud-init files into rootfs via libguestfs (filesystem-agnostic)
    NET    = "net"      # Serve cloud-init files via HTTP (nocloud-net datasource)
    OFF    = "off"      # Skip cloud-init entirely (no ISO mounted)
    ISO    = "iso"      # Generate cloud-init ISO from config files
```

#### `CloudInitStatus`

```python
class CloudInitStatus(StrEnum):
    PENDING = "pending"
    RUNNING = "running"
    DONE = "done"
    ERROR = "error"
```

### `mvmctl.models.network`

#### `NetworkItem`

Network record — maps to the `networks` table.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | Network ID (hash) |
| `name` | `str` | Network name |
| `subnet` | `str` | IP subnet in CIDR notation, e.g. `"10.20.0.0/24"` |
| `bridge` | `str` | Linux bridge device name |
| `ipv4_gateway` | `str` | Host-side gateway IP |
| `bridge_active` | `bool` | Whether the bridge device exists |
| `nat_enabled` | `bool` | Whether NAT/masquerade rules are active |
| `is_default` | `bool` | Whether this is the default network |
| `is_present` | `bool` | Whether the record is active (not soft-deleted) |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |
| `deleted_at` | `str \| None` | ISO 8601 deletion timestamp |
| `nat_gateways` | `str \| None` | Comma-separated NAT gateway addresses |

**Resolved relations** (populated on request):

| Field | Type | Description |
|-------|------|-------------|
| `leases` | `list[NetworkLeaseItem] \| None` | IP leases for this network |
| `iptables_rules` | `list[IPTablesRuleItem] \| None` | iptables rules for this network |

#### `NetworkLeaseItem`

IP lease entry.

| Field | Type | Description |
|-------|------|-------------|
| `network_id` | `str` | Network ID |
| `ipv4` | `str` | Leased IP address |
| `leased_at` | `str` | ISO 8601 lease timestamp |
| `id` | `int \| None` | Lease ID |
| `vm_id` | `str \| None` | VM ID holding the lease |
| `expires_at` | `str \| None` | ISO 8601 expiry timestamp |

#### `IPTablesRuleItem`

iptables rule record.

| Field | Type | Description |
|-------|------|-------------|
| `table_name` | `IPTablesTable` | Table (filter, nat, mangle, raw, security) |
| `chain_name` | `IPTablesChain` | Chain name |
| `rule_type` | `IPTablesRuleType` | Rule type |
| `protocol` | `IPTablesProtocol` | Protocol (tcp, udp, icmp, all) |
| `source` | `str` | Source CIDR |
| `destination` | `str` | Destination CIDR |
| `in_interface` | `str` | Input interface |
| `out_interface` | `str` | Output interface |
| `target` | `IPTablesTarget` | Target (ACCEPT, DROP, MASQUERADE, etc.) |
| `sport` | `int` | Source port |
| `dport` | `int` | Destination port |
| `network_id` | `str` | Associated network |
| `is_active` | `bool` | Whether the rule is applied |

### `mvmctl.models.image`

#### `ImageItem`

Image record — maps to the `images` table.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | Image ID (hash) |
| `os_slug` | `str` | OS slug identifier (e.g. `ubuntu-24.04`) |
| `os_name` | `str` | Human-readable OS name |
| `arch` | `str` | Architecture (e.g. `x86_64`) |
| `path` | `str` | Relative path to the image file |
| `fs_type` | `str` | Filesystem type (e.g. `ext4`) |
| `minimum_rootfs_size_mib` | `int` | Minimum rootfs size in MiB |
| `original_size` | `int` | Original file size in bytes |
| `is_default` | `bool` | Whether this is the default image |
| `is_present` | `bool` | Whether the file exists on disk |
| `pulled_at` | `str` | ISO 8601 pull timestamp |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |
| `fs_uuid` | `str \| None` | Filesystem UUID (auto-detected) |
| `compressed_size` | `int \| None` | Compressed size in bytes |
| `compression_ratio` | `float \| None` | Compression ratio |
| `compressed_format` | `str \| None` | Compression format |
| `deleted_at` | `str \| None` | ISO 8601 deletion timestamp |

#### `ImageSpec`

Specification for downloading a VM rootfs image, loaded from bundled YAML.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | Unique identifier; used as output filename base |
| `image_type` | `str` | Image type |
| `version` | `str` | Version string |
| `name` | `str` | Human-readable display name |
| `source` | `str` | Download URL for the image |
| `format` | `str` | Source format (`"qcow2"`, `"tar-rootfs"`, or `"raw"`) |
| `arch` | `str` | Target architecture (default: host arch) |
| `sha256` | `str \| None` | Expected SHA256 checksum |
| `sha256_url` | `str \| None` | URL to SHA256 checksum file |
| `list_url_template` | `str \| None` | URL template for listing available versions |

### `mvmctl.models.kernel`

#### `KernelItem`

Kernel record — maps to the `kernels` table.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | Kernel ID (hash) |
| `name` | `str` | Full filename |
| `base_name` | `str` | Base name (without version/arch) |
| `version` | `str` | Kernel version (e.g. `"6.1.155"`) |
| `arch` | `str` | Architecture (e.g. `"x86_64"`) |
| `type` | `str` | Kernel type (`"firecracker"` or `"official"`) |
| `path` | `str` | Relative path to the kernel file |
| `is_default` | `bool` | Whether this is the default kernel |
| `is_present` | `bool` | Whether the file exists on disk |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |
| `deleted_at` | `str \| None` | ISO 8601 deletion timestamp |

#### `KernelFetchResult`

Result returned by kernel fetch/build operations.

| Field | Type | Description |
|-------|------|-------------|
| `path` | `Path` | Path to the built/fetched `vmlinux` |
| `version` | `str` | Kernel version |
| `arch` | `str` | Architecture |
| `kernel_type` | `str` | Kernel type |
| `warnings` | `list[str]` | Build warnings |
| `info_messages` | `list[str]` | Informational messages |

#### `KernelSpec`

Specification for building or fetching a kernel, loaded from bundled YAML.

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | Kernel name |
| `kernel_type` | `str` | Type (`"firecracker"` or `"official"`) |
| `version` | `str` | Version |
| `source` | `str` | Source URL |
| `output_name` | `str` | Output filename |

### `mvmctl.models.binary`

#### `BinaryItem`

Binary record — maps to the `binaries` table.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | Binary ID (hash) |
| `name` | `str` | Binary name (`"firecracker"` or `"jailer"`) |
| `version` | `str` | Semantic version (e.g. `"1.15.0"`) |
| `full_version` | `str` | Full version string |
| `ci_version` | `str \| None` | Firecracker CI version for template resolution |
| `path` | `str` | Relative path to the binary file |
| `is_default` | `bool` | Whether this is the active binary |
| `is_present` | `bool` | Whether the file exists on disk |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |
| `deleted_at` | `str \| None` | ISO 8601 deletion timestamp |

### `mvmctl.models.key`

#### `SSHKeyItem`

SSH key record — maps to the `ssh_keys` table.

| Field | Type | Description |
|-------|------|-------------|
| `id` | `str` | Key ID (hash) |
| `name` | `str` | Key name (identifier used in `--ssh-key`) |
| `fingerprint` | `str` | SHA256 fingerprint in `SHA256:...` format |
| `algorithm` | `str` | Key algorithm, e.g. `"ssh-ed25519"` |
| `comment` | `str` | Key comment from the `.pub` file |
| `public_key_path` | `str` | Path to the `.pub` file |
| `is_default` | `bool` | Whether this is a default key |
| `is_present` | `bool` | Whether the file exists on disk |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |
| `private_key_path` | `str \| None` | Path to the private key file |

### `mvmctl.models.host`

#### `HostStateItem`

Host state record (singleton).

| Field | Type | Description |
|-------|------|-------------|
| `id` | `int` | Always `1` (singleton) |
| `initialized` | `bool` | Whether host init has been run |
| `mvm_group_created` | `bool` | Whether the `mvm` unix group was created |
| `sudoers_configured` | `bool` | Whether the sudoers drop-in is active |
| `default_network_created` | `bool` | Whether the default network was created |
| `initialized_at` | `str` | ISO 8601 initialization timestamp |
| `updated_at` | `str` | ISO 8601 update timestamp |

#### `HostStateChangeItem`

A single change applied during `host init`.

| Field | Type | Description |
|-------|------|-------------|
| `session_id` | `str` | Session identifier |
| `init_timestamp` | `str` | ISO 8601 init timestamp |
| `setting` | `str` | Name of the setting changed |
| `mechanism` | `str` | How the change was made (`"sysctl"`, `"modprobe"`, `"file_create"`, etc.) |
| `applied_value` | `str` | Value that was applied |
| `reverted` | `bool` | Whether the change has been reverted |
| `change_order` | `int` | Order of the change |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `id` | `int \| None` | Change ID |
| `original_value` | `str \| None` | Value before the change |
| `reverted_at` | `str \| None` | ISO 8601 reversion timestamp |
| `revert_mechanism` | `str \| None` | How the change was reverted |

---

## Error Handling

All exceptions derive from `mvmctl.exceptions.MVMError`.

### Exception Hierarchy

```
MVMError
├── VMNotFoundError           — VM does not exist in state
├── BinaryNotFoundError       — Binary does not exist in registry
├── KernelNotFoundError       — Kernel does not exist in registry
├── NetworkNotFoundError      — Network does not exist in registry
├── KeyNotFoundError          — SSH key does not exist in registry
├── ImageNotFoundError        — Image does not exist in registry
├── ImageAcquireError         — Image fetch/import failure
├── NetworkError              — Network setup/teardown failure
├── IPTablesTrackerError      — IPTables action failure
├── ImageError                — Image download or conversion failure
│   ├── ImageCompressionError
│   ├── ImageDecompressionError
│   ├── ImageCorruptError
│   ├── ImageEmptyError
│   ├── ImageValidationError
│   └── ChecksumMismatchError
├── KernelError               — Kernel build or configuration failure
├── FirecrackerClientError    — Firecracker process or API failure
│   └── SocketNotFoundError
├── FirecrackerSpawnError     — Firecracker spawn failure
├── FirecrackerConfigError    — Firecracker config generation failure
├── ConfigError               — Configuration loading/validation failure
├── DatabaseError             — Database operation failure
│   └── MigrationError
├── HostError                 — Host configuration or prerequisite failure
│   └── PrivilegeError        — Insufficient privileges for an operation
├── ConsoleError              — Console or PTY operation failure
├── ProcessError              — Subprocess execution failure
├── AssetNotFoundError        — Asset not found locally or remotely
├── BundledAssetError         — Bundled asset access failure
│   └── BundledAssetNotFoundError
├── BinaryError               — Firecracker/jailer binary management failure
│   └── BinaryAlreadyExistsError
├── SSHError                  — SSH connection or configuration failure
├── MVMKeyError               — SSH key management failure
│   ├── KeyExportError
│   ├── KeyDependencyError
│   └── KeyFileError
├── CloudInitError            — Cloud-init ISO creation failure
│   ├── CloudInitProvisionError   — Cloud-init provisioning failure
│   ├── CloudInitModeError        — Cloud-init mode failure
│   ├── CloudInitOffModeError     — OFF mode guestfs provisioning failure
│   ├── CloudInitIsoModeError     — ISO creation failure
│   ├── CloudInitNetModeError     — Nocloud-net server or iptables rule failure
│   └── CloudInitInjectModeError  — Rootfs cloud-init injection failure
├── VMCreateError             — VM creation failure (partial cleanup)
├── VMRequestError            — Error during VM request resolution
├── VMBuilderError            — VM builder failure (partial cleanup)
├── GuestfsNotAvailableError  — libguestfs bindings not available
├── GuestfsLaunchError        — Guestfs appliance failed to launch
├── GuestfsMountError         — Unable to mount rootfs in guestfs
├── GuestfsWriteError         — Failed to write files to guestfs
├── RootPartitionDetectionError
├── TieDetectedError
├── DownloadError             — Download operation failure
└── HttpDownloadError         — HTTP download failure
```

### Example

```python
from mvmctl.api import NetworkOperation, NetworkCreateInput
from mvmctl.exceptions import MVMError, NetworkError

try:
    result = NetworkOperation.create(
        NetworkCreateInput(name="my-net", subnet="192.168.100.0/24")
    )
except NetworkError as e:
    print(f"Network setup failed: {e}")
except MVMError as e:
    print(f"Unexpected MVM error: {e}")
```

---

## Operation Reference

### `VMOperation`

All methods are `@staticmethod`. VM instances are identified using `VMInput` objects.

#### `VMOperation.create(inputs: VMCreateInput) -> OperationResult[VMInstanceItem] | NeedsInteraction`

Create and start a new Firecracker microVM. Copies the rootfs image, generates cloud-init
data, sets up bridge networking, writes the Firecracker JSON config, starts the Firecracker
process, and registers the VM in the database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.name` | `str` | — | VM name (required) |
| `inputs.ssh_keys` | `list[str]` | — | SSH key names to inject (required) |
| `inputs.vcpu_count` | `int \| None` | `None` | Number of vCPUs |
| `inputs.mem_size_mib` | `int \| None` | `None` | Memory in MiB |
| `inputs.user` | `str \| None` | `None` | SSH user for cloud-init |
| `inputs.enable_pci` | `bool \| None` | `None` | Enable PCI device support |
| `inputs.enable_console` | `bool \| None` | `None` | Enable serial console |
| `inputs.enable_logging` | `bool \| None` | `None` | Enable logging |
| `inputs.enable_metrics` | `bool \| None` | `None` | Enable metrics |
| `inputs.firecracker_bin` | `str \| None` | `None` | Firecracker binary ID |
| `inputs.image` | `str \| None` | `None` | Image name/ID (DB-backed) |
| `inputs.kernel_id` | `str \| None` | `None` | Kernel ID (DB-backed) |
| `inputs.binary_id` | `str \| None` | `None` | Binary ID (DB-backed) |
| `inputs.image_path` | `Path \| None` | `None` | Explicit image path |
| `inputs.kernel_path` | `Path \| None` | `None` | Explicit kernel path |
| `inputs.disk_size` | `str \| None` | `None` | Rootfs size (e.g. `"2G"`) |
| `inputs.requested_guest_ip` | `str \| None` | `None` | Static IP to assign |
| `inputs.network_name` | `str \| None` | `None` | Network name |
| `inputs.requested_guest_mac` | `str \| None` | `None` | MAC address |
| `inputs.custom_user_data` | `Path \| None` | `None` | Custom cloud-init user data |
| `inputs.cloud_init_mode` | `str \| None` | `None` | Cloud-init mode: `"inject"`, `"iso"`, `"net"`, `"off"` |
| `inputs.cloud_init_iso_path` | `Path \| None` | `None` | Custom cloud-init ISO path |
| `inputs.keep_cloud_init_iso` | `bool` | `False` | Keep the cloud-init ISO after boot |
| `inputs.nocloud_net_port` | `int \| None` | `None` | Port for nocloud-net server |
| `inputs.skip_ci_network_config` | `bool` | `False` | Skip network config in cloud-init |
| `inputs.boot_args` | `str \| None` | `None` | Override kernel boot arguments |
| `inputs.lsm_flags` | `str \| None` | `None` | Linux Security Module flags |
| `inputs.skip_cleanup` | `bool` | `False` | Skip cleanup on failure |

**Resolves (DB-backed defaults):**
- CPU count, memory, user, PCI, console, logging, metrics — from `constants.py`
- Image — from `ImageResolver` (default image if not specified)
- Kernel — from `KernelResolver` (default kernel if not specified)
- Network — from `NetworkResolver` (default network if not specified)
- Binary — from `BinaryResolver` (default firecracker binary if not specified)
- SSH keys — from `KeyResolver` (default keys if not specified)

**Raises:** `VMCreateError`, `NetworkError`, `FirecrackerSpawnError`, `PrivilegeError`.

**Example:**
```python
from mvmctl.api import VMOperation, VMCreateInput

VMOperation.create(
    VMCreateInput(
        name="my-vm",
        ssh_keys=["my-key"],
        vcpu_count=2,
        mem_size_mib=2048,
        image="ubuntu-24.04",
    )
)
```

---

#### `VMOperation.remove(inputs: VMInput) -> BatchResult[VMInstanceItem]`

Stop and remove one or more VMs. Sends SIGTERM (graceful shutdown), then SIGKILL if
still running. Tears down TAP device, iptables rules, deregisters the VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.name` | `list[str]` | `[]` | VM names to remove |
| `inputs.id` | `list[str]` | `[]` | VM IDs to remove |
| `inputs.guest_mac` | `list[str]` | `[]` | Guest MAC addresses to resolve |
| `inputs.guest_ip` | `list[str]` | `[]` | Guest IP addresses to resolve |
| `inputs.force` | `bool \| None` | `None` | Skip graceful shutdown |

---

#### `VMOperation.list_all(status: VMStatus | list[VMStatus] | None = None) -> list[VMInstanceItem]`

Return all registered VMs, optionally filtered by status.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `status` | `VMStatus \| list[VMStatus] \| None` | `None` | Filter by status(es); `None` returns all |

---

#### `VMOperation.get(inputs: VMInput) -> VMInstanceItem`

Look up a single VM by name, ID, IP, or MAC.

**Raises:** `VMNotFoundError` if not found or ambiguous.

---

#### `VMOperation.inspect(inputs: VMInput, tree: bool = False) -> dict[str, Any]`

Return full details for a VM as a dictionary. When `tree=True`, returns nested
groupings (vm, resources, networking, assets, filesystem, console). When `tree=False`
(default), returns a flat dictionary with all fields.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `VMInput` | — | Must resolve to exactly one VM |
| `tree` | `bool` | `False` | Use nested grouping for display |

---

#### `VMOperation.start(inputs: VMInput) -> BatchResult[VMInstanceItem]`

Start one or more stopped VMs.

---

#### `VMOperation.stop(inputs: VMInput) -> BatchResult[VMInstanceItem]`

Stop one or more running VMs gracefully.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.force` | `bool \| None` | `None` | Skip graceful shutdown |

---

#### `VMOperation.reboot(inputs: VMInput) -> BatchResult[VMInstanceItem]`

Reboot one or more VMs (stop then start).

---

#### `VMOperation.pause(inputs: VMInput) -> BatchResult[VMInstanceItem]`

Pause one or more running VMs.

---

#### `VMOperation.resume(inputs: VMInput) -> BatchResult[VMInstanceItem]`

Resume one or more paused VMs.

---

#### `VMOperation.snapshot(inputs: VMInput, mem_out: Path, state_out: Path) -> OperationResult[VMInstanceItem]`

Create a snapshot of a single VM's memory and state.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `VMInput` | — | Must resolve to exactly one VM |
| `mem_out` | `Path` | — | Output path for memory snapshot |
| `state_out` | `Path` | — | Output path for VM state |

---

#### `VMOperation.load_snapshot(inputs: VMInput, mem_in: Path, state_in: Path, resume_after: bool | None = None) -> OperationResult[VMInstanceItem]`

Restore a VM from a snapshot.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `VMInput` | — | Must resolve to exactly one VM |
| `mem_in` | `Path` | — | Path to memory snapshot file |
| `state_in` | `Path` | — | Path to VM state file |
| `resume_after` | `bool \| None` | `None` | Resume VM immediately after loading |

---

#### `VMOperation.export(inputs: VMInput) -> VMExportConfig`

Export a VM's runtime configuration as a portable config object. Resolves the VM
by any identifier (name, ID, IP, MAC) and queries the database for related asset metadata.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `VMInput` | — | Must resolve to exactly one VM |

**Returns:** `VMExportConfig` with sub-configs for binary, boot, cloud-init, compute, firecracker, image, kernel, and network.

---

#### `VMOperation.import_(inputs: VMImportInput) -> None`

Create a VM from a portable export config file. Resolves all asset references
against the database and creates a VM matching the exported configuration.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.config_path` | `Path` | — | Path to the export config YAML/JSON file |
| `inputs.name_override` | `str \| None` | `None` | Override the VM name from the export file |

---

### `NetworkOperation`

All methods are `@staticmethod`. Networks are identified using `NetworkInput` objects.

#### `NetworkOperation.create(inputs: NetworkCreateInput) -> OperationResult[NetworkItem] | NeedsInteraction`

Create a named bridge network: sets up the bridge device, assigns the gateway IP,
optionally configures NAT rules.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.name` | `str` | — | Network name (must be unique) |
| `inputs.subnet` | `str` | — | Subnet in CIDR notation, e.g. `"192.168.100.0/24"` |
| `inputs.ipv4_gateway` | `str \| None` | `None` | Host-side gateway IP (default: first usable host) |
| `inputs.nat_enabled` | `bool` | `True` | Configure NAT/masquerade for outbound access |
| `inputs.nat_gateways` | `list[str]` | `[]` | Additional NAT gateway addresses |

**Returns:** `OperationResult[NetworkItem]` wrapping the created `NetworkItem`, or `NeedsInteraction` if sudo is required.

---

#### `NetworkOperation.remove(inputs: NetworkInput, force: bool = False) -> OperationResult[NetworkItem]`

Remove a named network: tears down the bridge device and NAT rules, removes persisted state.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `NetworkInput` | — | Network identifiers |
| `force` | `bool` | `False` | Remove even if referenced by VMs |

**Raises:** `NetworkError` if the network does not exist.

---

#### `NetworkOperation.list_all() -> list[NetworkItem]`

List all named networks with lease enrichment.

---

#### `NetworkOperation.get(inputs: NetworkInput) -> NetworkItem`

Get a single network by name or ID.

**Raises:** `NetworkError` if not found or ambiguous.

---

#### `NetworkOperation.inspect(inputs: NetworkInput, is_json: bool = False) -> NetworkItem | dict[str, Any]`

Return full details for a network, including live bridge status, leases, and iptables rules.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `NetworkInput` | — | Network identifiers |
| `is_json` | `bool` | `False` | Return a JSON-serializable dict |

**Returns:** `NetworkItem` or `dict` depending on `is_json`.

---

#### `NetworkOperation.set_default(inputs: NetworkInput) -> None`

Set a network as the default.

---

#### `NetworkOperation.create_default_network() -> OperationResult[NetworkItem]`

Ensure the default network exists, creating it if needed. Called automatically by
`HostOperation.init()`. Idempotent.

**Returns:** The default `NetworkItem`.

---

#### `NetworkOperation.restore() -> OperationResult[list[str]]`

Restore all networks from DB after reboot (re-create bridges and NAT rules).

**Returns:** List of status messages for each restored network.

---

#### `NetworkOperation.sync(network_id: str | None = None) -> dict[str, dict[str, int]]`

Sync networks: first reconciles bridge state (DB vs kernel), then ensures all
active DB iptables rules exist in host iptables and detects orphaned host rules.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `network_id` | `str \| None` | `None` | Specific network ID, or `None` for all networks |

**Returns:** Dict mapping `network_id` → `{"added": int, "verified": int, "orphaned": int}`.
Metadata includes `network_count` and `bridges_reconciled`.

---

### `ImageOperation`

All methods are `@staticmethod`. Images are identified using `ImageInput` objects.

#### `ImageOperation.fetch(inputs: ImagePullInput, phase_callback: Callable[[str], None] | None = None) -> OperationResult[ImageItem] | NeedsInteraction`

Download and convert a VM rootfs image (qcow2, tar, or raw) to an ext4 file, then
register it in the database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.os_slug` | `str` | — | OS slug (e.g. `"ubuntu-24.04"`) |
| `inputs.type` | `str` | — | Image type from config |
| `inputs.force` | `bool` | `False` | Re-download even if cached |
| `inputs.set_default` | `bool` | `False` | Set this image as the default |
| `inputs.arch` | `str \| None` | `None` | Architecture (default: host arch) |
| `inputs.version` | `str \| None` | `None` | Version override |
| `inputs.partition` | `int \| None` | `None` | Partition number to extract |
| `inputs.skip_optimization` | `bool` | `False` | Skip filesystem optimization |
| `inputs.disabled_detectors` | `list[str]` | `[]` | Disabled partition detectors |
| `phase_callback` | `Callable \| None` | `None` | Callback for phase updates |

**Returns:** `ImageAcquireResult` with the created `ImageItem`.

---

#### `ImageOperation.import_(inputs: ImageImportInput) -> OperationResult[ImageItem]`

Import an existing local image file and register it in the database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.name` | `str` | — | Name for the imported image |
| `inputs.format` | `str` | — | Source format (`"qcow2"`, `"raw"`, etc.) |
| `inputs.source_path` | `Path` | — | Path to the source image file |
| `inputs.force` | `bool` | `False` | Re-import even if cached |
| `inputs.arch` | `str \| None` | `None` | Architecture |
| `inputs.set_default` | `bool` | `False` | Set this image as the default |
| `inputs.partition` | `int \| None` | `None` | Partition number to extract |
| `inputs.skip_optimization` | `bool` | `False` | Skip filesystem optimization |
| `inputs.disabled_detectors` | `list[str]` | `[]` | Disabled partition detectors |

---

#### `ImageOperation.remove(inputs: ImageInput, force: bool = False) -> None`

Remove an image from cache and database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `ImageInput` | — | Image identifiers |
| `force` | `bool` | `False` | Remove even if referenced by VMs |

---

#### `ImageOperation.list_(inputs: ImageInput | None = None, remote: bool = False) -> list[ImageItem] | list[ImageSpec]`

List images.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs` | `ImageInput \| None` | `None` | Filter by identifiers |
| `remote` | `bool` | `False` | List remote images from YAML instead |

---

#### `ImageOperation.get(inputs: ImageInput) -> ImageItem`

Get a single image by ID or OS slug.

---

#### `ImageOperation.set_default(inputs: ImageInput) -> None`

Set an image as the default.

---

#### `ImageOperation.inspect(inputs: ImageInput, is_json: bool = False) -> ImageItem | dict[str, Any]`

Inspect an image with full details.

---

#### `ImageOperation.warm(inputs: ImageInput) -> OperationResult[list[Path]]`

Pre-decompress images to the ready pool for fast VM creation.

---

### `KernelOperation`

All methods are `@staticmethod`. Kernels are identified using `KernelInput` objects.

#### `KernelOperation.fetch(inputs: KernelPullInput) -> OperationResult[KernelItem] | NeedsInteraction`

Fetch or build a Firecracker kernel.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.kernel_type` | `str` | — | Kernel type (`"firecracker"` or `"official"`) |
| `inputs.version` | `str \| None` | `None` | Kernel version (e.g. `"6.1.155"`) |
| `inputs.arch` | `str \| None` | `None` | Architecture (default: host arch) |
| `inputs.output_dir` | `Path \| None` | `None` | Output directory |
| `inputs.output_name` | `str \| None` | `None` | Custom output filename |
| `inputs.output_path` | `Path \| None` | `None` | Full output path |
| `inputs.jobs` | `int \| None` | `None` | Parallel make jobs |
| `inputs.keep_build_dir` | `bool` | `False` | Keep build directory |
| `inputs.clean_build` | `bool` | `False` | Clean before building |
| `inputs.kernel_config` | `Path \| None` | `None` | Kernel config overlay |
| `inputs.set_default` | `bool` | `False` | Set as default |

**Returns:** The created `KernelItem`.

---

#### `KernelOperation.remove(inputs: KernelInput, force: bool = False) -> None`

Remove a kernel from cache and database.

---

#### `KernelOperation.list_all() -> list[KernelItem]`

List all kernels, syncing `is_present` with the filesystem.

---

#### `KernelOperation.get(inputs: KernelInput) -> KernelItem`

Get a single kernel by ID or name.

---

#### `KernelOperation.inspect(inputs: KernelInput, is_json: bool = False) -> KernelItem | dict[str, Any]`

Inspect a kernel with full details.

---

#### `KernelOperation.set_default(inputs: KernelInput) -> OperationResult[KernelItem]`

Set a kernel as the default.

---

### `KeyOperation`

All methods are `@staticmethod`. Keys are identified using `KeyInput` objects.

#### `KeyOperation.add(name: str, pub_key_path: Path, overwrite: bool = False) -> OperationResult[SSHKeyItem]`

Import an existing `.pub` file into the cache.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Name to register the key under |
| `pub_key_path` | `Path` | — | Path to the `.pub` file |
| `overwrite` | `bool` | `False` | Replace existing key with same name |

**Returns:** `SSHKeyItem` with fingerprint, algorithm, comment, timestamp.

---

#### `KeyOperation.create(inputs: KeyCreateInput) -> OperationResult[SSHKeyItem]`

Generate a new SSH keypair via `ssh-keygen` and register it.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.name` | `str` | — | Key name and base filename |
| `inputs.algorithm` | `str \| None` | `None` | Algorithm (`"ed25519"`, `"rsa"`, `"ecdsa"`) |
| `inputs.bits` | `int \| None` | `None` | Key bits |
| `inputs.output_dir` | `Path \| None` | `None` | Output directory |
| `inputs.comment` | `str \| None` | `None` | Key comment |
| `inputs.overwrite` | `bool` | `False` | Overwrite existing key files |
| `inputs.set_default` | `bool` | `False` | Set as default key |

**Returns:** The created `SSHKeyItem`.

---

#### `KeyOperation.list_all() -> list[SSHKeyItem]`

List all keys in the cache.

---

#### `KeyOperation.get(inputs: KeyInput) -> SSHKeyItem`

Get a single key by name or ID.

---

#### `KeyOperation.remove(inputs: KeyInput) -> BatchResult[SSHKeyItem]`

Remove keys from the cache registry and delete their key files.

---

#### `KeyOperation.inspect(inputs: KeyInput, is_json: bool = False) -> SSHKeyItem | dict[str, Any]`

Inspect a key with full details.

---

#### `KeyOperation.set_default(inputs: KeyInput) -> OperationResult[SSHKeyItem]`

Set one or more keys as defaults for new VMs.

---

#### `KeyOperation.get_defaults() -> list[SSHKeyItem]`

Get all default keys.

---

#### `KeyOperation.clear_defaults() -> OperationResult[None]`

Clear all default keys.

---

#### `KeyOperation.export(inputs: KeyInput, destination: Path, overwrite: bool = False) -> tuple[Path, Path]`

Export a keypair to a destination directory.

**Returns:** `(public_key_path, private_key_path)` tuple.

---

### `BinaryOperation`

All methods are `@staticmethod`. Binaries are identified using `BinaryInput` objects.

#### `BinaryOperation.fetch(inputs: BinaryPullInput) -> OperationResult[list[BinaryItem]] | NeedsInteraction`

Download a specific Firecracker/jailer binary version from GitHub releases.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.version` | `str` | — | Semantic version string, e.g. `"1.15.0"` |
| `inputs.set_as_default` | `bool` | `False` | Set as default after download |
| `inputs.download_override` | `bool` | `True` | Re-download even if cached |

**Returns:** `OperationResult[list[BinaryItem]]` wrapping the downloaded binaries, or `NeedsInteraction` if sudo is required.

---

#### `BinaryOperation.remove(inputs: BinaryInput, force: bool = False) -> None`

Remove binaries by identifier.

---

#### `BinaryOperation.remove_by_version(version: str, force: bool = False) -> None`

Remove both firecracker and jailer binaries for a version (convenience).

---

#### `BinaryOperation.get(inputs: BinaryInput) -> list[BinaryItem]`

Get binaries by identifier.

---

#### `BinaryOperation.list_local() -> list[BinaryItem]`

List all locally installed binaries.

---

#### `BinaryOperation.list_remote(limit: int | None = None) -> list[str]`

List available remote versions from GitHub releases.

---

#### `BinaryOperation.set_default(inputs: BinaryInput) -> None`

Set a binary as the default.

---

#### `BinaryOperation.ensure_default() -> OperationResult[BinaryItem]`

Ensure a default Firecracker binary exists. If local binaries exist but none is
marked default, sets the latest Firecracker binary as default.

**Returns:** The default `BinaryItem`, or `None` if no local binaries exist.

---

### `HostOperation`

All methods are `@staticmethod`.

#### `HostOperation.init(cache_dir: Path) -> OperationResult[Any] | NeedsInteraction`

Apply host configuration: enable IP forwarding, persist sysctl, load KVM modules,
create the `mvm` unix group, configure sudoers, set up iptables chains, and ensure
the default network. Fully idempotent.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory |

**Returns:** List of `HostStateChangeItem` describing every change applied.

**Raises:** `HostError`, `PrivilegeError`.

---

#### `HostOperation.get_state() -> HostStateItem | None`

Load and return the saved host state snapshot.

---

#### `HostOperation.check_kvm_access() -> bool`

Return `True` if `/dev/kvm` exists and is accessible by the current user.

---

#### `HostOperation.check_required_binaries() -> list[str]`

Return a list of missing required binary names (`ip`, `iptables`, `qemu-img`,
`cloud-localds`, etc.). Empty list means all present.

---

#### `HostOperation.get_ip_forward_status() -> str`

Return the current value of `net.ipv4.ip_forward` (`"0"` or `"1"`).

---

#### `HostOperation.clean(cache_dir: Path) -> list[str]`

Remove all networking configuration (bridges, TAP devices, iptables rules). Does NOT
revert sysctl settings or remove the sudoers drop-in.

---

#### `HostOperation.reset(cache_dir: Path) -> list[str]`

Full rollback to pre-init state: networking config, sysctl changes, sudoers drop-in,
and project group removal.

---

#### `HostOperation.get_running_vms() -> list[VMInstanceItem]`

Return all currently running VMs.

---

### `CacheOperation`

All methods are `@staticmethod`.

#### `CacheOperation.init_all() -> OperationResult[dict[str, str | list[str] | None]]`

Initialize all cache directories and optionally build the libguestfs fixed appliance.

**Returns:** Dict with `cache_dir`, `directories` (list of created paths), and
`guestfs_appliance` path.

---

#### `CacheOperation.prune_vms(dry_run: bool = False, include_all: bool = False) -> OperationResult[list[str]]`

Prune VMs. By default, prunes all VMs EXCEPT those in RUNNING or STARTING state.
Use `include_all=True` to prune ALL VMs regardless of state.

---

#### `CacheOperation.prune_networks(dry_run: bool = False, include_all: bool = False) -> OperationResult[list[str]]`

Prune unused networks. Skips default and referenced networks by default.

---

#### `CacheOperation.prune_images(dry_run: bool = False, include_all: bool = False) -> OperationResult[list[str]]`

Prune unused images. Skips default and referenced images by default.

---

#### `CacheOperation.prune_kernels(dry_run: bool = False, include_all: bool = False) -> OperationResult[list[str]]`

Prune unused kernels. Skips default and referenced kernels by default.

---

#### `CacheOperation.prune_binaries(dry_run: bool = False, include_all: bool = False) -> OperationResult[list[str]]`

Prune unused binaries. Skips default version by default.

---

#### `CacheOperation.prune_misc(dry_run: bool = False) -> OperationResult[dict[str, bool]]`

Prune miscellaneous cache: libguestfs appliance, warm images, and stale guestfs state.

**Returns:** Dict with `"appliance"`, `"warm_images"`, and `"guestfs_state"` booleans.

---

#### `CacheOperation.prune_all(dry_run: bool = False, include_all: bool = False) -> OperationResult[PruneAllResult]`

Prune all cache resources in one call: VMs, networks, images, kernels, binaries, and misc.

**Returns:** `PruneAllResult` with `pruned_ids`, `failed_ids`, and `had_running_vms` fields.

---

#### `CacheOperation.clean(dry_run: bool = False) -> OperationResult[CleanResult]`

Completely clean all cache: host networking, prune everything, remove the cache directory.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `dry_run` | `bool` | `False` | Only report what would be removed |

**Returns:** `CleanResult` with `prune_result` (PruneAllResult), `cache_dir_removed` (bool), and `cache_dir` (str path).

---

### `SSHOperation`

#### `SSHOperation.connect(inputs: SSHInput) -> OperationResult[int]`

Open an interactive SSH session into a VM, or execute a command.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.vm_id` | `str \| None` | `None` | VM identifier |
| `inputs.user` | `str \| None` | `None` | SSH user |
| `inputs.key` | `Path \| None` | `None` | Path to private key |
| `inputs.cmd` | `str \| None` | `None` | Command to execute |
| `inputs.ip` | `str \| None` | `None` | Direct IP address |
| `inputs.name` | `str \| None` | `None` | VM name |
| `inputs.mac` | `str \| None` | `None` | VM MAC address |

**Returns:** Exit code from the SSH session.

---

### `InitOperation`

#### `InitOperation.init_database() -> None`

Initialize the local SQLite database (run migrations).

---

#### `InitOperation.setup_host(cache_dir: Path) -> list[HostStateChangeItem]`

Set up host configuration. Delegates to `HostOperation.init()`.

---

#### `InitOperation.run(skip_host: bool = False, non_interactive: bool = False, *, sudo_completed: bool = False, host_setup_message: str | None = None, download_version: str | None = None) -> InitResult`

Run the full init wizard: local state → host setup → cache init → binary fetch.
Returns `InitResult` with per-step status. If a step needs user interaction,
the corresponding `InitStepResult` has `needs_interaction=True`.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `skip_host` | `bool` | `False` | Skip the host privilege-setup step |
| `non_interactive` | `bool` | `False` | Use defaults, skip all user prompts |
| `sudo_completed` | `bool` | `False` | Host init was already done via `sudo mvm host init` |
| `download_version` | `str \| None` | `None` | Specific binary version to download |

---

### `ConsoleOperation`

All methods are `@staticmethod`.

#### `ConsoleOperation.get_connection_info(identifier: str) -> ConsoleConnectionInfo`

Get connection info for a VM's console relay.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `identifier` | `str` | — | VM name, ID, MAC, or IP address |

**Returns:** `ConsoleConnectionInfo` with `socket_path`, `vm_name`, and `vm_id`.

**Raises:** `MVMError` if the console relay is not running.

---

#### `ConsoleOperation.get_state(identifier: str) -> dict[str, Any]`

Get the console relay state for a VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `identifier` | `str` | — | VM name, ID, MAC, or IP address |

**Returns:** Dict with `running` (bool), `pid` (int|None), `socket_path` (str).

---

#### `ConsoleOperation.kill(identifier: str) -> OperationResult[bool]`

Kill the console relay process for a VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `identifier` | `str` | — | VM name, ID, MAC, or IP address |

**Returns:** `True` if relay was stopped, `False` if it was not running.

---

### `ConfigOperation`

All methods are `@staticmethod`.

#### `ConfigOperation.get(category: str, key: str | None = None) -> Any | dict[str, Any] | None`

Get a config override value.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `category` | `str` | — | Setting category (e.g. `"defaults.vm"`) |
| `key` | `str \| None` | `None` | Setting key (e.g. `"vcpu_count"`). If `None`, returns all keys in the category. |

**Returns:** The current override value, a dict of category keys when `key` is `None`, or `None` if not set.

---

#### `ConfigOperation.set(category: str, key: str, value: Any) -> None`

Set a config override value.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `category` | `str` | — | Setting category |
| `key` | `str` | — | Setting key |
| `value` | `Any` | — | Value to set |

---

#### `ConfigOperation.reset(category: str | None = None, key: str | None = None, all_overrides: bool = False) -> OperationResult[int]`

Reset config override(s) back to defaults.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `category` | `str \| None` | `None` | Setting category. Optional when `all_overrides` is `True`. |
| `key` | `str \| None` | `None` | Setting key. Optional for category-level reset. |
| `all_overrides` | `bool` | `False` | Delete ALL overrides globally. |

**Returns:** Number of overrides removed.

---

#### `ConfigOperation.list_all() -> dict[str, dict[str, Any]]`

List all config categories and their current override values.

**Returns:** Dict mapping category names to dicts of key-value overrides.

---

### `LogOperation`

All methods are `@staticmethod`.

#### `LogOperation.stream(inputs: LogInput) -> Generator[str]`

Stream log lines for a VM. If `follow=True`, yields lines indefinitely.
If `follow=False`, yields the last N lines then stops.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `inputs.identifier` | `str` | — | VM name, ID, MAC, or IP address |
| `inputs.log_type` | `str \| None` | `None` | Log type (`"serial"`, `"firecracker"`, or `None` for both) |
| `inputs.follow` | `bool` | `False` | Follow (tail -f) mode |
| `inputs.lines` | `int \| None` | `None` | Number of lines to show |

**Yields:** Log line strings.

---

## End-to-End Example

```python
#!/usr/bin/env python3
"""
End-to-end example: orchestrate microVM lifecycle using the mvmctl Python API.

Prerequisites:
    - Linux x86_64 with KVM (/dev/kvm accessible)
    - System packages: ip, iptables, cloud-localds, qemu-img
    - Root privileges for host operations
    - pip install mvmctl
"""

from pathlib import Path

from mvmctl.api import (
    BinaryOperation,
    BinaryPullInput,
    HostOperation,
    ImageOperation,
    ImagePullInput,
    InitOperation,
    KeyOperation,
    KeyCreateInput,
    NetworkOperation,
    NetworkCreateInput,
    VMOperation,
    VMCreateInput,
)
from mvmctl.exceptions import MVMError
from mvmctl.utils.common import CacheUtils

CACHE_DIR = CacheUtils.get_cache_dir()


def main() -> None:
    # 1. Initialise the SQLite database
    InitOperation.init_database()
    print("Database ready.")

    # 2. Initialise the host (idempotent)
    changes = HostOperation.init(CACHE_DIR)
    if changes:
        for change in changes:
            print(f"  Applied: {change.setting} = {change.applied_value}")
    else:
        print("Host already configured.")

    # 3. Ensure a Firecracker binary is available
    local = BinaryOperation.list_local()
    if not local:
        print("Downloading Firecracker 1.15.0 ...")
        BinaryOperation.fetch(BinaryPullInput(version="1.15.0"))

    # 4. Ensure a kernel is available (via CLI: mvm kernel pull)
    # or use KernelOperation.fetch() directly for custom kernels

    # 5. Ensure an image is available (via CLI: mvm image pull)
    # or use ImageOperation.fetch() directly

    # 6. Create or register an SSH key
    key = KeyOperation.create(
        KeyCreateInput(name="my-api-key", set_default=True)
    )
    print(f"Created SSH key: {key.name} ({key.fingerprint})")

    # 7. Ensure the default network exists
    default_net = NetworkOperation.create_default_network()
    print(f"Default network: {default_net.name} ({default_net.subnet})")

    # 8. Create a VM using the API
    VMOperation.create(
        VMCreateInput(
            name="my-api-vm",
            ssh_keys=["my-api-key"],
            vcpu_count=2,
            mem_size_mib=2048,
            image="ubuntu-24.04",       # resolved from DB at API layer
            network_name="net",          # default network name
        )
    )
    print("VM created.")

    # 9. List all VMs
    instances = VMOperation.list_all()
    print(f"\nRegistered VMs ({len(instances)}):")
    for vm in instances:
        print(f"  {vm.name:20s}  {vm.status:10s}  {vm.ipv4}")


if __name__ == "__main__":
    try:
        main()
    except MVMError as e:
        print(f"Error: {e}")
        raise SystemExit(1)
```
