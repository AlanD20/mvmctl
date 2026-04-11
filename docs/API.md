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
from mvmctl.api import vm, network, assets, keys, host, cache
```

---

## Module Overview

| Module | Responsibility |
|---|---|
| `api/vms.py` | VM lifecycle: create, list, inspect, start/stop, console, SSH, logs, snapshots |
| `api/network.py` | Network management: create, remove, list, inspect, IP allocation/release |
| `api/assets.py` | Binary management: fetch, list, set active, remove |
| `api/kernel.py` | Kernel operations: fetch, list, set default, remove |
| `api/image.py` | Image operations: fetch, import, list, set default, remove |
| `api/keys.py` | SSH key registry: add, create, remove, list, set default |
| `api/host.py` | Host init/reset/clean/prune, privilege checks, KVM access |
| `api/cache.py` | Cache lifecycle: init, prune per-asset-type, prune all |
| `api/vm_config.py` | VM config file load/merge/save |
| `api/init.py` | Onboarding/init wizard API |

---

## Data Models

All data models are in `mvmctl.models.*`. Models are pure dataclasses — no business logic.

### `mvmctl.models.vm`

#### `VMState`

```python
class VMState(str, Enum):
    RUNNING = "running"
    STOPPED = "stopped"
    ERROR   = "error"
```

#### `CloudInitMode`

```python
class CloudInitMode(str, Enum):
    INJECT  = "inject"   # Inject via --cwi boot parameter
    NOCloud = "nocloud"  # Attach via nocloud-net ISO
    OFF     = "off"      # No cloud-init
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

#### `VMCreateInput`

Bundled input for `vms.create_vm()`. All fields are explicit — no hidden defaults.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | `str` | — | VM name |
| `vcpus` | `int` | — | Number of vCPUs |
| `mem` | `int` | — | Memory in MiB |
| `user` | `str` | — | SSH user for cloud-init |
| `enable_api_socket` | `bool` | — | Enable Firecracker HTTP API socket |
| `enable_pci` | `bool` | — | Enable PCI device support |
| `enable_console` | `bool` | — | Enable serial console |
| `firecracker_bin` | `str` | — | Path to Firecracker binary |
| `lsm_flags` | `str` | — | Linux Security Module flags |
| `enable_logging` | `bool` | — | Enable logging |
| `enable_metrics` | `bool` | — | Enable metrics |
| `image` | `str \| None` | `None` | Image name/ID (DB-backed) |
| `kernel` | `str \| None` | `None` | Kernel name/ID (DB-backed) |
| `image_path` | `Path \| None` | `None` | Explicit image path |
| `kernel_path` | `Path \| None` | `None` | Explicit kernel path |
| `disk_size` | `str \| None` | `None` | Rootfs size (e.g. `"2G"`) |
| `ip` | `str \| None` | `None` | Static IP to assign |
| `network_name` | `str \| None` | `None` | Network name |
| `mac` | `str \| None` | `None` | MAC address |
| `ssh_key` | `str \| None` | `None` | SSH key name |
| `user_data` | `Path \| None` | `None` | Custom cloud-init user data |
| `cloud_init_mode` | `CloudInitMode` | `INJECT` | Cloud-init injection mode |
| `cloud_init_iso_path` | `Path \| None` | `None` | Custom cloud-init ISO path |
| `keep_cloud_init_iso` | `bool` | `False` | Keep the cloud-init ISO after boot |
| `nocloud_net_port` | `int` | `0` | Port for nocloud-net server |
| `image_fs_uuid` | `str \| None` | `None` | Filesystem UUID (auto-detected) |
| `image_fs_type` | `str \| None` | `None` | Filesystem type (auto-detected) |
| `image_hash` | `str \| None` | `None` | Image SHA256 hash |
| `binary_id` | `str \| None` | `None` | Firecracker binary ID |

#### `ConsoleInfo`

Console relay connection info returned by `attach_console()`.

| Field | Type | Description |
|-------|------|-------------|
| `socket_path` | `Path` | Path to the console relay socket |
| `vm_name` | `str` | VM name |

#### `ConsoleState`

Console relay state returned by `get_console_state()`.

| Field | Type | Description |
|-------|------|-------------|
| `running` | `bool` | Whether the relay is currently running |
| `pid` | `int \| None` | Relay process PID, or None |
| `socket_path` | `str \| None` | Socket path string, or None |

#### `VMInspectInfo`

Full inspection data returned by `inspect_vm()`.

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
| `paths` | `dict[str, str \| None]` | Paths: vm_dir, rootfs, rootfs_source, config |
| `features` | `dict[str, bool]` | Flags: api_socket, console, nocloud_net |
| `nocloud_net` | `dict[str, Any] \| None` | nocloud-net details |
| `console` | `dict[str, Any] \| None` | Console details |

### `mvmctl.models.image`

#### `ImageSpec`

Specification for downloading and converting a VM root filesystem image.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `id` | `str` | — | Unique identifier; used as output filename base |
| `name` | `str` | — | Human-readable display name |
| `source` | `str` | — | Download URL for the image |
| `format` | `str` | — | Source format: `"qcow2"`, `"tar-rootfs"`, or `"raw"` |
| `convert_to` | `str` | — | Target format after conversion (e.g., `"ext4"`) |
| `minimum_rootfs_size` | `int` | `2048` | Target filesystem size in MiB |
| `sha256` | `str \| None` | `None` | Expected SHA256 checksum |

#### `ImageFetchInput`

Input model for `fetch_image_and_register()`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `spec` | `ImageSpec` | — | Image specification |
| `output_dir` | `Path` | — | Directory for output `.ext4` file |
| `force` | `bool` | `False` | Re-download even if cached |
| `partition` | `int \| None` | `None` | Partition number to extract |
| `skip_optimization` | `bool` | `False` | Skip filesystem optimization |

#### `ImageFetchResult`

Result returned by `fetch_image_and_register()` and `import_image_and_register()`.

| Field | Type | Description |
|-------|------|-------------|
| `path` | `Path` | Path to the fetched/imported image |
| `full_hash` | `str` | Full 64-char SHA256 hash of the image |
| `short_id` | `str` | First 8 characters of the hash |
| `warnings` | `list[str]` | Any warnings from the fetch/import process |

### `mvmctl.models.kernel`

#### `KernelFetchInput`

Input model for `fetch_kernel()`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `kernel_type` | `str` | — | Kernel type (e.g. `"official"`, `"custom"`) |
| `version` | `str \| None` | `None` | Kernel version (e.g. `"6.19.9"`) |
| `arch` | `str` | — | Architecture (e.g. `"x86_64"`) |
| `output_dir` | `Path` | — | Directory for the output `vmlinux` |
| `output_name` | `str \| None` | `None` | Custom output filename |
| `output_path` | `Path \| None` | `None` | Full output path (overrides output_dir) |
| `jobs` | `int \| None` | `None` | Parallel make jobs (default: CPU count) |
| `keep_build_dir` | `bool` | `False` | Keep build directory after completion |
| `clean_build` | `bool` | `False` | Clean before building |
| `kernel_config` | `Path \| None` | `None` | Path to kernel config overlay |

#### `KernelFetchResult`

Result returned by `fetch_kernel()`.

| Field | Type | Description |
|-------|------|-------------|
| `path` | `Path` | Path to the built/fetched `vmlinux` |
| `version` | `str` | Kernel version |
| `arch` | `str` | Architecture |
| `kernel_type` | `str` | Kernel type |
| `warnings` | `list[str]` | Build warnings |
| `info_messages` | `list[str]` | Informational messages |
| `name` | `str` | Kernel name property |
| `exists` | `bool` | Whether kernel was already cached |

### `mvmctl.models.network`

#### `NetworkConfig`

Runtime network configuration.

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | Network name |
| `subnet` | `str` | IP subnet in CIDR notation, e.g. `"10.20.0.0/24"` |
| `ipv4_gateway` | `str` | Host-side gateway IP (first usable host in CIDR) |
| `bridge` | `str` | Linux bridge device name |
| `nat_enabled` | `bool` | Whether NAT/masquerade rules are active |
| `nat_gateways` | `list[str]` | List of NAT gateway addresses |
| `created_at` | `str` | ISO 8601 timestamp of network creation |
| `is_default` | `bool` | Whether this is the default network |

#### `NetworkLease`

IP lease entry.

| Field | Type | Description |
|-------|------|-------------|
| `vm_name` | `str` | VM name holding the lease |
| `ip` | `str` | Leased IP address |

#### `NetworkInspectInfo`

Full inspection data returned by `inspect_network()`.

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | Network name |
| `subnet` | `str` | Subnet in CIDR notation |
| `ipv4_gateway` | `str` | Gateway IP |
| `bridge` | `str` | Bridge device name |
| `nat_enabled` | `bool` | Whether NAT is enabled |
| `nat_gateways` | `list[str]` | NAT gateway addresses |
| `created_at` | `str` | ISO 8601 creation timestamp |
| `bridge_exists` | `bool` | Whether the bridge device exists |
| `vms` | `list[dict[str, Any]]` | List of attached VMs with id, ipv4, status, pid |

### `mvmctl.models.key`

#### `KeyCreateInput`

Input model for `create_key()`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | `str` | — | Key name and base filename |
| `output_dir` | `Path \| None` | `None` | Directory for private key (default: `~/.ssh/`) |
| `comment` | `str \| None` | `None` | Key comment (default: `name@hostname`) |
| `overwrite` | `bool` | `False` | Overwrite existing key files |

### `mvmctl.models.cache`

#### `PruneAllResult`

Result returned by `prune_all()`.

| Field | Type | Description |
|-------|------|-------------|
| `pruned_vms` | `list[str]` | Names of pruned VMs |
| `pruned_networks` | `list[str]` | Names of pruned networks |
| `pruned_images` | `list[str]` | IDs of pruned images |
| `pruned_kernels` | `list[str]` | IDs of pruned kernels |
| `had_running_vms` | `bool` | Whether any VMs were running at start |

### `mvmctl.models.binary`

#### `BinaryVersion`

| Field | Type | Description |
|-------|------|-------------|
| `version` | `str` | Semantic version string, e.g. `"1.12.0"` |
| `firecracker_path` | `Path \| None` | Path to the firecracker binary |
| `jailer_path` | `Path \| None` | Path to the jailer binary |
| `is_active` | `bool` | Whether this version is the active binary |

### `mvmctl.core.host`

#### `HostStateChange`

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
| `changes` | `list[HostStateChange]` | All changes applied during init |

### `mvmctl.core.key_manager`

#### `KeyInfo`

| Field | Type | Description |
|-------|------|-------------|
| `name` | `str` | Key name (identifier used in `--ssh-key`) |
| `fingerprint` | `str` | SHA256 fingerprint in `SHA256:...` format |
| `algorithm` | `str` | Key algorithm, e.g. `"ssh-ed25519"` |
| `comment` | `str` | Key comment from the `.pub` file |
| `added_at` | `str` | ISO 8601 timestamp when the key was added |

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
└── MVMKeyError          — SSH key management failure
```

### Example

```python
from mvmctl.api import network, keys
from mvmctl.exceptions import MVMError, NetworkError

try:
    net = network.create_network("my-net", subnet="192.168.100.0/24")
except NetworkError as e:
    print(f"Network setup failed: {e}")
except MVMError as e:
    print(f"Unexpected MVM error: {e}")
```

---

## Function Reference

### `mvmctl.api.vm`

#### `create_vm(input: VMCreateInput, vm_manager: VMManager | None = None) -> VMInstance`

Create and start a new Firecracker microVM. Copies the rootfs image, generates cloud-init
ISO, sets up bridge networking, writes the Firecracker JSON config, starts the Firecracker
process, and registers the VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `input` | `VMCreateInput` | — | Full VM creation input |
| `vm_manager` | `VMManager \| None` | `None` | Override state manager |

**Returns:** `VMInstance` with PID, IP, MAC, and runtime state populated.

**Raises:** `VMAlreadyExistsError`, `NetworkError`, `FirecrackerError`, `PrivilegeError`.

**Example:**
```python
from mvmctl.api import vm
from mvmctl.models.vm import VMCreateInput, CloudInitMode

input = VMCreateInput(
    name="my-vm",
    vcpus=2,
    mem=2048,
    user="root",
    enable_api_socket=False,
    enable_pci=False,
    enable_console=False,
    firecracker_bin="firecracker",
    lsm_flags="",
    enable_logging=True,
    enable_metrics=True,
    image="ubuntu-24.04",
    kernel="vmlinux-official",
    network_name="default",
    cloud_init_mode=CloudInitMode.INJECT,
)
instance = vm.create_vm(input)
print(f"VM started: PID={instance.pid}, IP={instance.ip}")
```

---

#### `list_vms(include_stopped: bool = False) -> list[VMInstance]`

Return all registered VMs.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_stopped` | `bool` | `False` | When `True`, includes STOPPED and ERROR VMs |

---

#### `get_vm(name: str) -> VMInstance | None`

Look up a VM by name.

---

#### `remove_vm(name: str, force: bool = False) -> None`

Stop and remove a Firecracker VM. Sends SIGTERM (graceful shutdown), waits up to 5 seconds,
then SIGKILL if still running. Tears down TAP device, iptables rules, deregisters the VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name to remove |
| `force` | `bool` | `False` | Skip graceful shutdown; go straight to SIGKILL |

---

#### `start_vm(name: str) -> VMInstance`

Start a stopped VM.

---

#### `stop_vm(name: str, timeout: int = 30) -> None`

Stop a running VM gracefully.

---

#### `pause_vm(name: str) -> None`

Pause a running VM.

---

#### `resume_vm(name: str) -> None`

Resume a paused VM.

---

#### `reboot_vm(name: str) -> None`

Reboot a running VM (stop then start).

---

#### `ssh_vm(name: str, ssh_args: list[str] | None = None) -> None`

Open an interactive SSH session into a VM, or pass `ssh_args` to execute a command.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name or IP address |
| `ssh_args` | `list[str] \| None` | `None` | SSH arguments; omit for interactive shell |

---

#### `get_logs(name: str, log_type: str = "boot", follow: bool = False, lines: int | None = None) -> str`

Retrieve log lines for a VM.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |
| `log_type` | `str` | `"boot"` | `"boot"` (serial console) or `"os"` (Firecracker log) |
| `follow` | `bool` | `False` | Stream new log lines continuously |
| `lines` | `int \| None` | `None` | Number of lines (default: all for boot, 50 for os) |

---

#### `attach_console(name: str) -> None`

Attach to a VM serial console via console relay.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |

---

#### `get_console_state(name: str) -> ConsoleState`

Get the current state of a VM's console relay.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |

**Returns:** `ConsoleState` with `running`, `pid`, `socket_path`.

---

#### `kill_console(name: str) -> None`

Kill the console relay for a VM.

---

#### `cleanup_vms() -> list[str]`

Remove all stopped VMs and clean up their resources. Returns list of VM names processed.

---

#### `snapshot_vm(name: str, snapshot_path: Path | None = None) -> Path`

Create a snapshot of a running VM. Pauses the VM, dumps memory to `snapshot_path`, saves
VM state, then resumes.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |
| `snapshot_path` | `Path \| None` | `None` | Output directory (default: VM cache dir) |

**Returns:** Path to the memory snapshot file.

---

#### `load_snapshot(name: str, snapshot_path: Path, resume: bool = True) -> None`

Restore a VM from a snapshot.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name to restore |
| `snapshot_path` | `Path` | — | Path to memory snapshot file |
| `resume` | `bool` | `True` | Resume VM immediately after loading |

---

#### `export_vm_config(name: str, output_path: Path | None = None) -> Path`

Export a VM's runtime config to a JSON file.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | VM name |
| `output_path` | `Path \| None` | `None` | Output path (default: `<vm_dir>/export.json`) |

---

#### `vm_cache_dir(name: str) -> Path`

Return the cache directory path for a VM.

---

### `mvmctl.api.network`

#### `create_network(name: str, subnet: str, ipv4_gateway: str | None = None, nat: bool = True, nat_gateways: list[str] | None = None) -> NetworkConfig`

Create a named bridge network: sets up the bridge device, assigns the gateway IP,
optionally configures NAT rules.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Network name (must be unique) |
| `subnet` | `str` | — | Subnet in CIDR notation, e.g. `"192.168.100.0/24"` |
| `ipv4_gateway` | `str \| None` | `None` | Host-side gateway IP (default: first usable host) |
| `nat` | `bool` | `True` | Configure NAT/masquerade for outbound access |
| `nat_gateways` | `list[str] \| None` | `None` | Additional NAT gateway addresses |

**Returns:** The created `NetworkConfig`.

---

#### `list_networks() -> list[NetworkConfig]`

List all named networks.

---

#### `get_network(name: str) -> NetworkConfig | None`

Get a named network by name.

---

#### `remove_network(name: str) -> None`

Remove a named network: tears down the bridge device and NAT rules, removes persisted state.

**Raises:** `NetworkError` if the network has VMs attached or does not exist.

---

#### `inspect_network(name: str) -> NetworkInspectInfo`

Return full details for a named network, including live bridge status and attached VMs.

**Returns:** `NetworkInspectInfo` with all network details and attached VM list.

---

#### `allocate_network_ip(network_name: str, vm_name: str) -> str`

Pick the next available IP from a network's subnet and register the lease.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `network_name` | `str` | — | Network name |
| `vm_name` | `str` | — | VM name to associate with the lease |

**Returns:** Allocated IP address string.

---

#### `release_network_ip(network_name: str, vm_name: str) -> None`

Release a VM's IP lease from a network, returning the address to the pool.

---

#### `get_network_leases(name: str) -> list[NetworkLease]`

Get all IP leases for a network.

---

#### `ensure_default_network() -> NetworkConfig`

Ensure the default network exists, creating it if needed. Called automatically by `host init`.

---

#### `set_default_network(name: str) -> None`

Set a network as the default network.

---

#### `reconcile_networks() -> list[NetworkInspectInfo]`

Reconcile network state: ensure each network in the DB has a bridge and correct iptables
rules. Returns list of inspected networks.

---

#### `restore_networks() -> list[str]`

Restore all networks from the database (re-create bridges and NAT rules).

---

### `mvmctl.api.image`

#### `fetch_image_and_register(input: ImageFetchInput) -> ImageFetchResult`

Download and convert a VM rootfs image (qcow2, tar, or raw) to an ext4 file, then register
it in the database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `input` | `ImageFetchInput` | — | Image fetch input with spec, output_dir, options |

**Returns:** `ImageFetchResult` with `path`, `full_hash`, `short_id`, `warnings`.

---

#### `import_image_and_register(input: ImageImportInput) -> ImageFetchResult`

Import an existing image file and register it in the database.

---

#### `set_default_image(os_slug: str) -> None`

Set an image as the default by its OS slug.

---

#### `set_default_image_by_id(image_id: str) -> None`

Set an image as the default by its full ID.

---

#### `remove_image(image_id: str, force: bool = False, images_dir: Path | None = None) -> tuple[list[Path], bool]`

Remove an image from cache and database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `image_id` | `str` | — | Image ID (or short prefix) |
| `force` | `bool` | `False` | Remove even if it's the default image |
| `images_dir` | `Path \| None` | `None` | Override images directory |

**Returns:** `(list_of_removed_paths, had_alias)` tuple.

---

#### `load_images_config(path: Path) -> list[ImageSpec]`

Load image specifications from a YAML config file.

---

### `mvmctl.api.kernel`

#### `fetch_kernel(input: KernelFetchInput) -> KernelFetchResult`

Fetch or build a Firecracker kernel.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `input` | `KernelFetchInput` | — | Kernel fetch input with type, version, arch, output_dir |

**Returns:** `KernelFetchResult` with `path`, `version`, `arch`, `kernel_type`, `warnings`.

---

#### `register_fetched_kernel(result: KernelFetchResult, spec: KernelSpec, set_default: bool = False) -> str`

Register a fetched kernel in the database.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `result` | `KernelFetchResult` | — | Result from `fetch_kernel()` |
| `spec` | `KernelSpec` | — | Kernel specification |
| `set_default` | `bool` | `False` | Set this kernel as the default |

**Returns:** Kernel ID (hash).

---

#### `list_kernels(kernels_dir: Path) -> list[KernelItem]`

List all locally cached kernels.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `kernels_dir` | `Path` | — | Path to the kernels cache directory |

---

#### `set_default_kernel(kernels_dir: Path, kernel_prefix: str) -> None`

Set a kernel as the default by its prefix.

---

#### `remove_kernel(prefix: str, kernels_dir: Path, force: bool = False) -> None`

Remove a cached kernel.

---

### `mvmctl.api.assets`

#### `fetch_binary(version: str, bin_dir: Path | None = None) -> BinaryVersion`

Download a specific Firecracker binary version from GitHub releases and extract it.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `version` | `str` | — | Semantic version string, e.g. `"1.12.0"` |
| `bin_dir` | `Path \| None` | `None` | Override cache directory |

**Returns:** `BinaryVersion` describing the downloaded binaries.

---

#### `list_local_versions(bin_dir: Path | None = None) -> list[BinaryVersion]`

List all locally cached Firecracker binary versions.

---

#### `list_binaries() -> list[BinaryEntry]`

List all registered binaries from the database.

---

#### `set_active_version(version: str, bin_dir: Path | None = None) -> None`

Set a cached version as the active Firecracker binary.

---

#### `remove_version(version: str, bin_dir: Path | None = None) -> None`

Remove a cached binary version.

---

#### `register_binary(result: BinaryVersion, is_default: bool = False) -> None`

Register a binary version in the database.

---

#### `ensure_default_binary(bin_dir: Path | None = None) -> str | None`

Ensure a default binary exists, downloading the latest version if needed.

---

### `mvmctl.api.keys`

#### `add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> KeyInfo`

Import an existing `.pub` file into the cache.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | `str` | — | Name to register the key under |
| `pub_key_path` | `str \| Path` | — | Path to the `.pub` file |
| `overwrite` | `bool` | `False` | Replace existing key with same name |

**Returns:** `KeyInfo` with fingerprint, algorithm, comment, timestamp.

---

#### `create_key(input: KeyCreateInput) -> tuple[KeyInfo, Path]`

Generate a new ED25519 keypair via `ssh-keygen` and register it.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `input` | `KeyCreateInput` | — | Key creation input |

**Returns:** `(KeyInfo, private_key_path)` tuple.

---

#### `list_keys() -> list[KeyInfo]`

List all keys in the cache.

---

#### `get_key(name: str) -> KeyInfo | None`

Get a key by name.

---

#### `remove_key(name: str) -> None`

Remove a key from the cache registry and delete its `.pub` file.

---

#### `set_default_keys(names: list[str]) -> None`

Set one or more keys as defaults for new VMs.

---

#### `get_default_keys() -> list[str]`

Get the list of default key names.

---

#### `clear_default_keys() -> None`

Clear all default keys.

---

#### `export_key(name: str, destination: str | Path | None = None, overwrite: bool = False) -> tuple[Path, Path]`

Export a cached key to a destination directory.

**Returns:** `(public_key_path, private_key_path)` tuple.

---

### `mvmctl.api.host`

#### `init_host(cache_dir: Path | None = None) -> list[HostStateChange]`

Apply host configuration: enable IP forwarding, persist sysctl, load KVM modules.
Fully idempotent — if already configured, returns an empty list.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache_dir` | `Path \| None` | `None` | Cache root directory |

**Returns:** List of `HostStateChange` describing every change applied.

---

#### `restore_host(cache_dir: Path | None = None) -> list[HostStateChange]`

Revert host changes using the saved snapshot created by `init_host`.

---

#### `clean_host(cache_dir: Path | None = None) -> list[str]`

Remove all networking config (bridges, TAP devices, iptables rules). Does NOT revert
sysctl settings or remove the sudoers drop-in.

---

#### `reset_host(cache_dir: Path | None = None) -> list[str]`

Full rollback to pre-init state: networking config, sysctl changes, sudoers drop-in,
and project group removal.

---

#### `prune_host(cache_dir: Path | None = None) -> list[str]`

Tear down all networking added by this tool.

---

#### `get_host_state(cache_dir: Path | None = None) -> HostState | None`

Load and return the saved host state snapshot.

---

#### `check_privileges(binary_path: str) -> None`

Check that the current process can invoke `binary_path` with elevated privileges.

**Raises:** `PrivilegeError` if the binary is not found or user lacks privileges.

---

#### `check_kvm_access() -> bool`

Return `True` if `/dev/kvm` exists and is accessible by the current user.

---

#### `check_required_binaries() -> list[str]`

Return a list of missing required binary names (`ip`, `iptables`, `qemu-img`,
`mkisofs`/`genisoimage`). Empty list means all present.

---

#### `get_ip_forward_status() -> str`

Return the current value of `net.ipv4.ip_forward` (`"0"` or `"1"`).

---

#### `get_vm_manager() -> VMManager`

Return the global `VMManager` singleton.

---

#### `get_running_vms() -> list[VMInstance]`

Return all currently running VMs.

---

### `mvmctl.api.cache`

#### `init_all() -> dict[str, str]`

Initialize the cache directory structure. Returns a dict mapping subsystem names to
their initialized directories.

---

#### `prune_vms(include_stopped: bool = False, include_running: bool = False, dry_run: bool = False) -> list[str]`

Prune stopped (and optionally running) VMs.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_stopped` | `bool` | `False` | Include stopped VMs |
| `include_running` | `bool` | `False` | Include running VMs (dangerous) |
| `dry_run` | `bool` | `False` | Show what would be pruned without doing it |

**Returns:** List of pruned VM names.

---

#### `prune_networks(dry_run: bool = False, include_all: bool = False) -> list[str]`

Prune networks not attached to any VM.

---

#### `prune_images(dry_run: bool = False, include_all: bool = False) -> list[str]`

Prune image files not registered in the database.

---

#### `prune_kernels(dry_run: bool = False, include_all: bool = False) -> list[str]`

Prune kernel files not registered in the database.

---

#### `prune_all(include_stopped: bool = False, include_running: bool = False, dry_run: bool = False) -> dict[str, list[str] | bool]`

Prune all asset types in one call.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_stopped` | `bool` | `False` | Include stopped VMs |
| `include_running` | `bool` | `False` | Include running VMs |
| `dry_run` | `bool` | `False` | Show what would be pruned |

**Returns:** Dict with `pruned_vms`, `pruned_networks`, `pruned_images`, `pruned_kernels`,
and `had_running_vms`.

---

## End-to-End Example

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

from mvmctl.api import assets, host, keys, network, vm
from mvmctl.exceptions import MVMError
from mvmctl.models.vm import VMCreateInput, CloudInitMode

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

    # 3. Ensure a kernel is available (via CLI: mvm kernel fetch)
    # or use build_kernel_pipeline() directly for custom kernels

    # 4. Ensure an image is available (via CLI: mvm image fetch ubuntu-24.04)
    # or use fetch_image_and_register() directly

    # 5. Register an SSH key
    pub_key_path = Path.home() / ".ssh" / "id_ed25519.pub"
    if pub_key_path.exists():
        from mvmctl.api import keys as key_api
        existing = key_api.get_key("my-key")
        if existing is None:
            key_info = key_api.add_key("my-key", pub_key_path)
            print(f"Registered key: {key_info.name}  {key_info.fingerprint}")

    # 6. Create a VM using the API
    vm_input = VMCreateInput(
        name="my-api-vm",
        vcpus=2,
        mem=2048,
        user="root",
        enable_api_socket=False,
        enable_pci=False,
        enable_console=False,
        firecracker_bin="firecracker",
        lsm_flags="",
        enable_logging=True,
        enable_metrics=True,
        image="ubuntu-24.04",  # resolved from DB at API layer
        kernel="vmlinux-official",  # resolved from DB at API layer
        network_name="default",
        cloud_init_mode=CloudInitMode.INJECT,
    )
    instance = vms.create_vm(vm_input)
    print(f"VM started: {instance.name} — PID={instance.pid}, IP={instance.ip}")

    # 7. List VMs
    all_vms = vm.list_vms()
    print(f"\nRegistered VMs ({len(all_vms)}):")
    for vm in all_vms:
        print(f"  {vm.name:20s}  {vm.status.value:10s}  {vm.ip}")


if __name__ == "__main__":
    try:
        main()
    except MVMError as e:
        print(f"Error: {e}")
        raise SystemExit(1)
```
