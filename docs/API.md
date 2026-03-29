# mvmctl Python API Reference

## Introduction

Every CLI command maps 1:1 to a Python function in `mvmctl.api.*`. The CLI is a thin
presentation layer on top of these modules — it handles argument parsing, output
formatting, and exit codes, then calls the same functions documented here.

You can import the API directly to build automation scripts, GUIs, or TUIs without
going through the CLI. All system interactions (KVM, iptables, bridge devices) happen
lazily — importing the package has no side effects.

---

## Installation

Install the package so the API is importable:

```bash
# From PyPI
pip install mvmctl

# From source
git clone https://github.com/your-org/mvmctl
cd mvmctl
pip install -e .

# Using uv
uv sync
```

Then import:

```python
from mvmctl.api import vms, network, assets, keys, host
```

---

## Module Overview

| Module | Responsibility |
|---|---|
| `api/vms.py` | VM lifecycle: list, get, deregister, cache directory |
| `api/network.py` | Network management: create, remove, list, inspect, IP allocation |
| `api/assets.py` | Asset management: kernels, images, Firecracker binaries |
| `api/keys.py` | SSH key registry: add, create, remove, list, inspect |
| `api/host.py` | Host initialisation, state inspection, prune, clean, reset, privileges |

> **Note:** All `api/` modules are thin wrappers that re-export functions from their
> corresponding `core/` counterparts. Business logic lives in `core/`; `api/` provides a
> stable, documented entry point without containing business logic itself.

---

## Utility Modules

| Module | Responsibility |
|---|---|
| `utils/fs.py` | Filesystem helpers: VM directory paths, cache directory resolution |
| `utils/console.py` | Rich console output helpers: tables, panels, status messages |
| `utils/process.py` | Subprocess wrapper with logging and error handling |
| `utils/http.py` | HTTP download helper with progress reporting and SHA256 verification |
| `utils/audit.py` | Audit logging for privileged operations |
| `utils/validation.py` | Input validation helpers (VM name, IP address, CIDR) |

---

## Data Models

### `mvmctl.models.vm`

#### `VMState`

```python
class VMState(str, Enum):
    RUNNING = "running"
    STOPPED = "stopped"
    ERROR   = "error"
```

#### `VMConfig`

Configuration for launching a Firecracker VM.

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | VM name; also used as hostname inside the guest |
| `vcpu_count` | `int` | Number of vCPUs (default: 2) |
| `mem_size_mib` | `int` | Memory in MiB (default: 2048) |
| `kernel_path` | `Path` | Path to the vmlinux kernel image |
| `rootfs_path` | `Path` | Path to the root filesystem ext4 image |
| `guest_ip` | `str \| None` | Static IP address for the guest NIC |
| `guest_mac` | `str \| None` | MAC address for the guest NIC |
| `tap_device` | `str \| None` | Host TAP interface name |
| `boot_args` | `str \| None` | Override kernel boot arguments |
| `root_uuid` | `str \| None` | Filesystem UUID for the root partition (used in boot args) |
| `root_fs_type` | `str \| None` | Filesystem type of the root image (e.g. ext4, btrfs) |
| `enable_api_socket` | `bool` | Enable Firecracker HTTP API socket (default: False) |
| `enable_pci` | `bool` | Enable PCI device support (default: False) |
| `lsm_flags` | `str` | Linux Security Module flags for boot args |

#### `VMInstance`

Runtime state for a registered VM.

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | VM name |
| `pid` | `int \| None` | Firecracker process PID, or None if stopped |
| `socket_path` | `Path \| None` | Path to Firecracker API socket, or None |
| `ip` | `str \| None` | Assigned guest IP address |
| `mac` | `str \| None` | Assigned guest MAC address |
| `network_name` | `str \| None` | Name of the network this VM is attached to |
| `created_at` | `datetime` | UTC timestamp of VM creation |
| `status` | `VMState` | Current lifecycle state |
| `config` | `VMConfig \| None` | Launch config, if persisted |

### `mvmctl.models.image`

#### `ImageSpec`

Specification for downloading and converting a VM root filesystem image.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `id` | `str` | — | Unique identifier for the image; used as the output filename base |
| `name` | `str` | — | Human-readable display name |
| `source` | `str` | — | Download URL for the image |
| `format` | `str` | — | Source format: `"qcow2"`, `"tar-rootfs"`, or `"raw"` |
| `convert_to` | `str` | — | Target format after conversion (e.g., `"ext4"`) |
| `size_mib` | `int` | `2048` | Target filesystem size in MiB (used for `tar-rootfs` images) |
| `sha256` | `str \| None` | `None` | Expected SHA256 checksum for integrity verification |

### `mvmctl.core.network_manager`

#### `NetworkConfig`

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | Network name |
| `cidr` | `str` | IP subnet in CIDR notation, e.g. `"10.20.0.0/24"` |
| `gateway` | `str` | Host-side gateway IP (first usable host in CIDR) |
| `bridge` | `str` | Linux bridge device name |
| `nat_enabled` | `bool` | Whether NAT/masquerade rules are active |
| `created_at` | `str` | ISO 8601 timestamp of network creation |

#### `NetworkLease`

| Field | Type | Description |
|-------|------|-------------|
| `vm_name` | `str` | VM name holding the lease |
| `ip` | `str` | Leased IP address |

### `mvmctl.core.key_manager`

#### `KeyInfo`

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | Key name (identifier used in `--ssh-key`) |
| `fingerprint` | `str` | SHA256 fingerprint in `SHA256:...` format |
| `algorithm` | `str` | Key algorithm, e.g. `"ssh-ed25519"` |
| `comment` | `str` | Key comment from the `.pub` file |
| `added_at` | `str` | ISO 8601 timestamp when the key was added |

### `mvmctl.core.host`

#### `HostChange`

| Field | Type | Description |
|-------|------|-------------|
| `setting` | `str` | Name of the setting that was changed |
| `original_value` | `str \| None` | Value before the change, or None if not previously set |
| `applied_value` | `str` | Value that was applied |
| `mechanism` | `str` | How the change was made (`"sysctl"`, `"modprobe"`, `"file_create"`) |

#### `HostState`

| Field | Type | Description |
|-------|------|-------------|
| `init_timestamp` | `str` | ISO 8601 timestamp when `host init` was last run |
| `changes` | `list[HostChange]` | All changes applied during init (used to restore) |

### `mvmctl.core.binary_manager`

#### `BinaryVersion`

| Field | Type | Description |
|-------|------|-------------|
| `version` | `str` | Semantic version string, e.g. `"1.12.0"` |
| `firecracker_path` | `Path \| None` | Path to the firecracker binary |
| `jailer_path` | `Path \| None` | Path to the jailer binary |
| `is_active` | `bool` | Whether this version is symlinked as the active binary |

---

## Error Handling

All exceptions derive from `mvmctl.exceptions.MVMError`.

### Exception Hierarchy

```
MVMError
├── VMNotFoundError       — VM does not exist in state
├── VMAlreadyExistsError  — VM name already registered
├── NetworkError          — Network setup/teardown failure
├── ImageError            — Image download or conversion failure
│   └── ChecksumMismatchError
├── KernelError           — Kernel build or configuration failure
├── FirecrackerError      — Firecracker process or API failure
│   └── SocketNotFoundError
├── ConfigError           — Configuration loading/validation failure
├── HostError             — Host configuration or prerequisite failure
│   └── PrivilegeError    — Insufficient privileges for an operation
├── ProcessError          — Subprocess execution failure
├── AssetNotFoundError    — Asset not found locally or remotely
├── BinaryError           — Firecracker/jailer binary management failure
└── MVMError           — SSH key management failure
```

### Catching Typed Exceptions

```python
from mvmctl.api import network, keys
from mvmctl.exceptions import MVMError, NetworkError

try:
    net = network.create_network("my-net", cidr="192.168.100.0/24")
except NetworkError as e:
    print(f"Network setup failed: {e}")
except MVMError as e:
    print(f"Unexpected MVM error: {e}")

try:
    key_info = keys.add_key("my-key", "/home/user/.ssh/id_ed25519.pub")
except MVMError as e:
    print(f"Key error: {e}")
```

---

## Function Reference

### `mvmctl.api.vms`

#### `list_vms(include_stopped: bool = True) -> list[VMInstance]`

Return all registered VMs.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_stopped` | `bool` | `True` | When `False`, only return VMs with `RUNNING` status |

**Returns:** List of `VMInstance` objects.

---

#### `get_vm(name: str) -> VMInstance | None`

Look up a VM by name.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name as registered in the cache |

**Returns:** `VMInstance` if found, `None` otherwise.

---

#### `deregister_vm(name: str) -> None`

Remove a VM entry from the state registry. Does not stop the process or clean up
networking — use `mvm vm remove` (or the CLI) for the full teardown sequence.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name to deregister |

**Raises:** `VMNotFoundError` if the VM does not exist.

---

#### `vm_cache_dir(name: str) -> Path`

Return the cache directory path for a VM. The directory may not yet exist.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |

**Returns:** Absolute path to `<cache-root>/vms/<name>/`.

---

#### `create_vm(config: VMConfig, firecracker_bin: str = "firecracker", vm_manager: VMManager | None = None) -> VMInstance`

Create and start a new Firecracker microVM. Copies the rootfs image, generates cloud-init
ISO, sets up bridge networking (TAP device + iptables rules), writes the Firecracker JSON
config, starts the Firecracker process, and registers the VM in the state registry.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `config` | `VMConfig` | — | Full VM launch configuration |
| `firecracker_bin` | `str` | `"firecracker"` | Path or name of the Firecracker binary |
| `vm_manager` | `VMManager \| None` | `None` | Override state manager (default: global singleton) |

**Returns:** `VMInstance` with PID, IP, MAC, and runtime state populated.

**Raises:** `VMAlreadyExistsError` if a VM with the same name already exists.
`NetworkError` if bridge/TAP/iptables setup fails.
`FirecrackerError` if the Firecracker process fails to start.
`PrivilegeError` if the calling user lacks required group membership.

**Example:**
```python
from pathlib import Path
from mvmctl.api import vms
from mvmctl.models.vm import VMConfig

config = VMConfig(
    name="my-vm",
    kernel_path=Path("/home/user/.cache/mvmctl/kernels/vmlinux"),
    rootfs_path=Path("/home/user/.cache/mvmctl/images/ubuntu-24.04.ext4"),
    vcpu_count=2,
    mem_size_mib=2048,
)
instance = vms.create_vm(config)
print(f"VM started: PID={instance.pid}, IP={instance.ip}")
```

---

#### `remove_vm(name: str, force: bool = False, vm_manager: VMManager | None = None) -> None`

Stop and remove a Firecracker VM. Sends SIGTERM (graceful shutdown), waits up to 5 seconds,
then SIGKILL if still running. Tears down the TAP device, removes iptables forwarding rules,
deregisters the VM, and deletes its cache directory.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name to remove |
| `force` | `bool` | `False` | Skip graceful shutdown; go straight to SIGKILL |
| `vm_manager` | `VMManager \| None` | `None` | Override state manager |

**Raises:** `VMNotFoundError` if the VM does not exist in the registry.
`PrivilegeError` if the calling user lacks required group membership.

**Example:**
```python
from mvmctl.api import vms

# Graceful shutdown
vms.remove_vm("my-vm")

# Force-kill immediately
vms.remove_vm("my-vm", force=True)
```

---

#### `ssh_vm(name: str, user: str = "root", key: Path | None = None, cmd: str | None = None) -> int`

Open an interactive SSH session into a VM, or execute a single command and return.
Resolves the VM IP from the registry, then calls `ssh` with appropriate flags.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name or IP address |
| `user` | `str` | `"root"` | SSH username |
| `key` | `Path \| None` | `None` | Path to SSH private key. Defaults to auto-detected key in `~/.ssh/`. |
| `cmd` | `str \| None` | `None` | Command to run instead of opening an interactive shell |

**Returns:** SSH process exit code (`0` = success).

**Example:**
```python
from mvmctl.api import vms

# Interactive shell
vms.ssh_vm("my-vm")

# Run a command and capture exit code
rc = vms.ssh_vm("my-vm", cmd="uname -a")
print(f"Exit code: {rc}")
```

---

#### `get_logs(name: str, log_type: str = "os", lines: int = 50, follow: bool = False) -> list[str]`

Retrieve log lines for a VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |
| `log_type` | `str` | `"os"` | Log type: `"os"` (Firecracker process log) or `"boot"` (serial console output) |
| `lines` | `int` | `50` | Number of lines to return |
| `follow` | `bool` | `False` | Stream new log lines continuously (blocks until interrupted) |

**Returns:** List of log line strings.

**Example:**
```python
from mvmctl.api import vms

# Get last 100 lines of boot log
boot_lines = vms.get_logs("my-vm", log_type="boot", lines=100)
for line in boot_lines:
    print(line)

# Get OS (Firecracker process) log
os_lines = vms.get_logs("my-vm", log_type="os")
```

---

#### `cleanup_vms(all_vms: bool = False, dry_run: bool = False, vm_manager: VMManager | None = None) -> list[VMInstance]`

Remove stopped (or all) VMs and clean up their resources. Used by `mvm vm prune`.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `all_vms` | `bool` | `False` | Remove all VMs, not just stopped ones |
| `dry_run` | `bool` | `False` | Show what would be removed without actually removing |
| `vm_manager` | `VMManager \| None` | `None` | Override state manager |

**Returns:** List of `VMInstance` objects that were (or would be) processed.

---

#### `snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None`

Create a snapshot of a running VM. Pauses the VM, dumps memory to `mem_out`, saves VM state to `state_out`, then resumes.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |
| `mem_out` | `Path` | — | Output path for memory snapshot file |
| `state_out` | `Path` | — | Output path for VM state file |

**Raises:** `VMNotFoundError` if VM doesn't exist. `FirecrackerError` if snapshot fails.

---

#### `load_snapshot(name: str, mem_in: Path, state_in: Path, resume_after: bool = True) -> None`

Restore a VM from a snapshot created by `snapshot_vm`.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name to restore |
| `mem_in` | `Path` | — | Path to memory snapshot file |
| `state_in` | `Path` | — | Path to VM state file |
| `resume_after` | `bool` | `True` | Resume VM immediately after loading |

**Raises:** `VMNotFoundError` if VM doesn't exist. `FirecrackerError` if load fails.

---

### Console API (`mvmctl.api.vms`)

Console functions provide PTY-over-vsock access to VMs without SSH.

#### `attach_console(name: str) -> dict[str, Any]`

Attach to a VM console. Ensures the console relay is running and returns connection info.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |

**Returns:** Dict with `socket_path` and `vm_name`.

**Raises:** `VMNotFoundError` if VM doesn't exist. `MVMError` if console relay is not running.

---

#### `get_console_state(name: str) -> dict[str, Any]`

Get the current state of a VM's console relay without attaching.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |

**Returns:** Dict with console state info (running status, PID, socket path).

**Raises:** `VMNotFoundError` if VM doesn't exist.

---

#### `kill_console(name: str) -> bool`

Kill the console relay for a VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |

**Returns:** `True` if relay was killed, `False` if not running.

**Raises:** `VMNotFoundError` if VM doesn't exist.

---

### `mvmctl.api.network`

#### `list_networks() -> list[NetworkConfig]`

List all named networks.

**Returns:** List of `NetworkConfig` instances.

---

#### `get_network(name: str) -> NetworkConfig | None`

Get a named network by name.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Network name |

**Returns:** `NetworkConfig` if found, `None` if not found.

---

#### `get_network_leases(name: str) -> list[NetworkLease]`

Get all IP leases for a network.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Network name |

**Returns:** List of `NetworkLease` instances.

---

#### `create_network(name: str, cidr: str | None = None, gateway: str | None = None, nat: bool = True) -> NetworkConfig`

Create a named bridge network: sets up the bridge device, assigns the gateway IP, and optionally configures NAT rules.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Network name (must be unique) |
| `cidr` | `str \| None` | `None` | Subnet in CIDR notation, e.g. `"192.168.100.0/24"`. Auto-allocated if `None`. |
| `gateway` | `str \| None` | `None` | Host-side gateway IP. Defaults to the first usable host in the CIDR. |
| `nat` | `bool` | `True` | Configure NAT/masquerade for outbound internet access |

**Returns:** The created `NetworkConfig`.

**Raises:** `NetworkError` if the name already exists, the CIDR overlaps an existing network, or bridge/iptables setup fails.

---

#### `remove_network(name: str) -> None`

Remove a named network: tears down the bridge device and NAT rules, removes persisted state.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Network name |

**Raises:** `NetworkError` if the network has VMs attached or does not exist.

---

#### `inspect_network(name: str) -> dict[str, object]`

Return full details for a named network, including live bridge status and attached VMs.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Network name |

**Returns:** Dict with keys: `name`, `cidr`, `gateway`, `bridge`, `nat_enabled`, `created_at`, `bridge_exists`, `vms`.

**Raises:** `NetworkError` if network not found.

---

#### `allocate_network_ip(network_name: str, vm_name: str) -> str`

Pick the next available IP from a network's subnet and register the lease.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `network_name` | `str` | — | Network name |
| `vm_name` | `str` | — | VM name to associate with the lease |

**Returns:** Allocated IP address string.

**Raises:** `NetworkError` if the network does not exist or no IPs are available.

---

#### `release_network_ip(network_name: str, vm_name: str) -> None`

Release a VM's IP lease from a network, returning the address to the pool.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `network_name` | `str` | — | Network name |
| `vm_name` | `str` | — | VM name whose lease to release |

---

#### `ensure_default_network() -> NetworkConfig`

Ensure the default network exists, creating it if needed. Called automatically by `host init`.

**Returns:** The default `NetworkConfig` (existing or newly created).

**Raises:** `NetworkError` if creation fails.

---

### `mvmctl.api.assets`

#### `fetch_binary(version: str, bin_dir: Path | None = None) -> BinaryVersion`

Download a specific Firecracker binary version from GitHub releases and extract it.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `version` | `str` | — | Semantic version string, e.g. `"1.12.0"` |
| `bin_dir` | `Path \| None` | `None` | Override cache directory. Defaults to `<cache-root>/bin/`. |

**Returns:** `BinaryVersion` describing the downloaded binaries.

**Raises:** `BinaryError` on download or extraction failure.

---

#### `list_local_versions(bin_dir: Path | None = None) -> list[BinaryVersion]`

List all locally cached Firecracker binary versions.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `bin_dir` | `Path \| None` | `None` | Override cache directory |

**Returns:** List of `BinaryVersion` instances, sorted newest first. `is_active=True` for the currently symlinked version.

---

#### `list_remote_versions(limit: int = 10) -> list[str]`

Fetch available Firecracker versions from the GitHub releases API.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | `int` | `10` | Maximum number of versions to return |

**Returns:** List of version strings (no `v` prefix), newest first.

**Raises:** `BinaryError` on network failure.

---

#### `set_active_version(version: str, bin_dir: Path | None = None) -> None`

Symlink a specific version as the active Firecracker binary.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `version` | `str` | — | Version to activate |
| `bin_dir` | `Path \| None` | `None` | Override cache directory |

**Raises:** `BinaryError` if the version is not locally cached.

---

#### `remove_version(version: str, bin_dir: Path | None = None) -> None`

Remove a cached Firecracker binary version.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `version` | `str` | — | Version to remove |
| `bin_dir` | `Path \| None` | `None` | Override cache directory |

**Raises:** `BinaryError` if the version is not found.

---

#### `fetch_image(spec: object, output_dir: Path, force: bool = False) -> Path`

Download and convert a VM rootfs image (qcow2, tar, or raw) to an ext4 file.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `spec` | `ImageSpec` | — | Image specification (from `load_images_config`) |
| `output_dir` | `Path` | — | Directory to write the output `.ext4` file |
| `force` | `bool` | `False` | Re-download even if already cached |

**Returns:** Path to the output `.ext4` file.

**Raises:** `ImageError` on download or conversion failure.

---

#### `load_images_config(config_path: Path) -> list[ImageSpec]`

Load the built-in image catalogue from YAML.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `config_path` | `Path` | *(required)* | Path to the images YAML config file (e.g. the bundled `assets/images.yaml`). |

**Returns:** `list[ImageSpec]` — list of image specification objects.

---

#### `build_kernel_pipeline(version: str, source_url: str, output_path: Path, build_dir: Path | None = None, sha256: str | None = None, jobs: int | None = None, keep_build_dir: bool = False, user_config_path: Path | None = None, arch: str | None = None, kernel_spec: KernelSpec | None = None, use_cache: bool = True) -> KernelPipelineResult`

Run the full kernel build pipeline: download source, extract, configure, compile, copy `vmlinux`.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `version` | `str` | — | Kernel version string, e.g. `"6.1.102"` |
| `source_url` | `str` | — | URL to download the kernel tarball from |
| `output_path` | `Path` | — | Destination path for the compiled `vmlinux` |
| `build_dir` | `Path \| None` | `None` | Directory for intermediate build artifacts |
| `sha256` | `str \| None` | `None` | Expected SHA256 checksum for integrity verification |
| `jobs` | `int \| None` | `None` | Parallel make jobs. Defaults to CPU count. |
| `keep_build_dir` | `bool` | `False` | Keep the build directory after completion |
| `user_config_path` | `Path \| None` | `None` | Optional path to user config overlay |
| `arch` | `str \| None` | `None` | Target architecture |
| `kernel_spec` | `KernelSpec \| None` | `None` | Custom kernel specification |
| `use_cache` | `bool` | `True` | Whether to use the kernel build cache |

**Returns:** `KernelPipelineResult` describing the build outcome.

**Raises:** `KernelError` on any build step failure.

---

#### `list_assets() -> list[AssetInfo]`

List all assets (binaries, kernels, images) in a consolidated inventory.

**Returns:** List of `AssetInfo` TypedDict with fields for each asset type.

---

#### `remove_asset(asset_type: Literal["binary", "kernel", "image"], name: str) -> None`

Remove an asset from the cache.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `asset_type` | `Literal["binary", "kernel", "image"]` | — | Type of asset to remove |
| `name` | `str` | — | Asset name/version/ID |

**Raises:** `AssetNotFoundError` if asset not found. `FileNotFoundError` if kernel/image file missing.

---

### `mvmctl.api.keys`

#### `list_keys() -> list[KeyInfo]`

List all keys in the cache.

**Returns:** List of `KeyInfo` instances.

---

#### `get_key(name: str) -> KeyInfo | None`

Get a key by name.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Key name |

**Returns:** `KeyInfo` if found, `None` otherwise.

---

#### `add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> KeyInfo`

Import an existing `.pub` file into the cache under a given name.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Name to register the key under |
| `pub_key_path` | `str \| Path` | — | Path to the `.pub` file on disk |
| `overwrite` | `bool` | `False` | Replace an existing key with the same name |

**Returns:** `KeyInfo` with fingerprint, algorithm, comment, and timestamp.

**Raises:** `MVMError` if the file is not found, empty, or the name already exists (when `overwrite=False`).

---

#### `create_key(name: str, output_dir: str | Path | None = None, comment: str | None = None, overwrite: bool = False) -> tuple[KeyInfo, Path]`

Generate a new ED25519 keypair via `ssh-keygen` and register the public key in the cache.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Key name and base filename |
| `output_dir` | `str \| Path \| None` | `None` | Directory for the private key file. Defaults to `~/.ssh/`. |
| `comment` | `str \| None` | `None` | Key comment. Defaults to `name@hostname`. |
| `overwrite` | `bool` | `False` | Overwrite existing key files and registry entry |

**Returns:** `(KeyInfo, private_key_path)` tuple.

**Raises:** `MVMError` if `ssh-keygen` fails or the key already exists (when `overwrite=False`).

---

#### `remove_key(name: str) -> None`

Remove a key from the cache registry and delete its `.pub` file from the cache.
Does not touch private key files on disk.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Key name |

**Raises:** `MVMError` if the key is not found.

---

#### `inspect_key(name: str) -> dict[str, object]`

Return detailed info about a named key, including the full public key content.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Key name |

**Returns:** Dict with keys: `name`, `fingerprint`, `algorithm`, `comment`, `added_at`, `public_key`.

**Raises:** `MVMError` if the key is not found.

---

#### `set_default_keys(names: list[str]) -> None`

Set one or more keys as the default keys for new VMs. When creating a VM without `--ssh-key`, all default keys are injected via cloud-init.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `names` | `list[str]` | — | List of key names to set as defaults |

**Raises:** `MVMKeyError` if any key name does not exist in the registry.

**Example:**
```python
from mvmctl.api.keys import set_default_keys
set_default_keys(["work-key", "personal-key"])
```

---

#### `get_default_keys() -> list[str]`

Get the list of default key names.

**Returns:** List of key names set as defaults. Empty list if no defaults are set.

**Example:**
```python
from mvmctl.api.keys import get_default_keys
defaults = get_default_keys()  # ["work-key", "personal-key"]
```

---

#### `clear_default_keys() -> None`

Clear all default keys. After calling this, no keys will be automatically injected into new VMs.

**Example:**
```python
from mvmctl.api.keys import clear_default_keys
clear_default_keys()
```

---

### `mvmctl.api.host`

#### `init_host(cache_dir: Path) -> list[HostChange]`

Apply host configuration: enable IP forwarding, persist sysctl, load KVM modules.
Fully idempotent — if already configured, returns an empty list.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory for saving the host state snapshot |

**Returns:** List of `HostChange` describing every change applied.

**Raises:** `HostError` if KVM is not accessible, required binaries are missing, or an operation fails.

---

#### `restore_host(cache_dir: Path) -> list[HostChange]`

Revert host changes using the saved snapshot created by `init_host`.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory containing the state snapshot |

**Returns:** List of `HostChange` describing reverted changes.

**Raises:** `HostError` if no saved state exists or a revert operation fails.

---

#### `prune_host(cache_dir: Path) -> list[str]`

Tear down all networking added by this tool: every bridge device, every TAP device, every
iptables rule, and the IP forwarding sysctl change. Does not remove VM cache files, images,
kernels, or binaries.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory |

**Returns:** List of summary strings describing what was torn down.

---

#### `clean_host(cache_dir: Path) -> list[str]`

Remove all networking config (bridges, TAP devices, iptables rules). Does NOT revert
sysctl settings, remove the sudoers drop-in, or remove the project group.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory |

**Returns:** List of summary strings describing what was torn down.

---

#### `reset_host(cache_dir: Path) -> list[str]`

Full rollback to pre-init state. Removes networking config, reverts sysctl changes,
removes the sudoers drop-in file, and removes the project group.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory |

**Returns:** List of summary strings describing what was torn down.

---

#### `check_privileges(binary: str) -> None`

Check that the current process can invoke `binary` with elevated privileges. Verifies
that the binary exists and the current user is either root or a member of the `mvm` group.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `binary` | `str` | — | Absolute path to a system binary (e.g. `/usr/sbin/ip`) |

**Raises:** `PrivilegeError` if the binary is not found or the user lacks privileges.

---

#### `get_host_state(cache_dir: Path) -> HostState | None`

Load and return the saved host state snapshot.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path` | — | Cache root directory |

**Returns:** `HostState` if a snapshot exists, `None` otherwise.

**Raises:** `HostError` if the state file is corrupt.

---

#### `check_kvm_access() -> bool`

Return `True` if `/dev/kvm` exists and is readable and writable by the current user.

---

#### `check_required_binaries() -> list[str]`

Return a list of missing required binary names (`ip`, `iptables`, `qemu-img`, and one of
`mkisofs`/`genisoimage`). An empty list means all required binaries are present.

---

#### `get_ip_forward_status() -> str | None`

Return the current value of `net.ipv4.ip_forward` (`"0"` or `"1"`), or `None` on error.

---

#### `default_cache_dir() -> Path`

Return the default cache root directory (`~/.cache/mvmctl` or `$MVM_CACHE_DIR`).

---

## End-to-End Example

The following complete script creates a VM from scratch using the Python API:

```python
#!/usr/bin/env python3
"""
End-to-end example: create a Firecracker VM using the mvm Python API.

Prerequisites:
    - Linux x86_64 with KVM (/dev/kvm accessible)
    - System packages: ip, iptables, genisoimage/mkisofs, qemu-img
    - Run as root (networking operations require root)
    - pip install mvmctl
"""

from pathlib import Path

from mvmctl.api import assets, host, keys, network, vms
from mvmctl.exceptions import MVMError

CACHE_DIR = host.default_cache_dir()


def main() -> None:
    # 1. Initialise the host (idempotent — safe to run repeatedly)
    changes = host.init_host(CACHE_DIR)
    if changes:
        for change in changes:
            print(f"  Applied: {change.setting} = {change.applied_value}")
    else:
        print("Host already configured.")

    # 2. Ensure a Firecracker binary is available
    local = assets.list_local_versions()
    if not local:
        print("Downloading Firecracker 1.12.0 ...")
        assets.fetch_binary("1.12.0")
        assets.set_active_version("1.12.0")

    # 3. Ensure a kernel is available
    kernels_dir = CACHE_DIR / "kernels"
    vmlinux = kernels_dir / "vmlinux"
    if not vmlinux.exists():
        print("Fetching prebuilt kernel ...")
        # This requires the CLI: run `mvm asset kernel fetch` instead,
        # or call build_kernel_pipeline() to build from source.

    # 4. Ensure an image is available
    images_dir = CACHE_DIR / "images"
    image_path = images_dir / "ubuntu-24.04.ext4"
    if not image_path.exists():
        print("Fetching Ubuntu 24.04 image ...")
        image_specs = assets.load_images_config()
        spec = next(s for s in image_specs if s.id == "ubuntu-24.04")  # type: ignore[attr-defined]
        assets.fetch_image(spec, images_dir)

    # 5. Register an SSH key
    pub_key_path = Path.home() / ".ssh" / "id_ed25519.pub"
    if pub_key_path.exists() and not keys.get_key("my-key"):
        key_info = keys.add_key("my-key", pub_key_path)
        print(f"Registered key: {key_info.name}  {key_info.fingerprint}")

    # 6. Create a named network
    my_network = network.get_network("example-net")
    if my_network is None:
        my_network = network.create_network(
            name="example-net",
            cidr="192.168.200.0/24",
            nat=True,
        )
        print(f"Created network '{my_network.name}' — bridge: {my_network.bridge}")

    # 7. Allocate an IP for the VM
    vm_ip = network.allocate_network_ip("example-net", "my-api-vm")
    print(f"Allocated IP: {vm_ip}")

    # 8. List VMs
    all_vms = vms.list_vms()
    print(f"\nRegistered VMs ({len(all_vms)}):")
    for vm in all_vms:
        print(f"  {vm.name:20s}  {vm.status.value:10s}  {vm.ip}")

    # 9. Clean up: release the IP and remove the network
    network.release_network_ip("example-net", "my-api-vm")
    network.remove_network("example-net")
    print("Cleaned up.")


if __name__ == "__main__":
    try:
        main()
    except MVMError as e:
        print(f"Error: {e}")
        raise SystemExit(1)
```
