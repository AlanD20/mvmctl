# fcm Developer API Reference

This document describes the public Python API surface of the `fcm.core.*` modules.
All functions are callable without Typer (as a library).

---

## Table of Contents

1. [fcm.core.key_manager](#fcmcorekey_manager)
2. [fcm.core.network_manager](#fcmcorenetwork_manager)
3. [fcm.core.network](#fcmcorenetwork)
4. [fcm.core.vm_manager](#fcmcorevm_manager)
5. [fcm.core.firecracker](#fcmcorefirecracker)
6. [fcm.core.host](#fcmcorehost)
7. [fcm.models.vm](#fcmmodelsvm)
8. [fcm.exceptions](#fcmexceptions)

---

## fcm.core.key_manager

Named SSH key store backed by `~/.cache/firecracker-manager/keys/`.

### `list_keys() -> list[KeyInfo]`

List all keys in the cache.

**Returns:** List of `KeyInfo` dataclass instances.

---

### `get_key(name: str) -> KeyInfo | None`

Get a key by name.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Key name |

**Returns:** `KeyInfo` if found, `None` if not found.

---

### `add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> KeyInfo`

Import an existing public key into the cache.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Name to register the key under |
| `pub_key_path` | `str \| Path` | Path to `.pub` file on disk |
| `overwrite` | `bool` | When `True`, replace an existing key with the same name |

**Returns:** `KeyInfo` with fingerprint, algorithm, and metadata.

**Raises:** `fcm.exceptions.KeyError` if file not found, file empty, or key already exists (when `overwrite=False`).

---

### `create_key(name: str, output_dir: str | Path | None = None, comment: str | None = None, overwrite: bool = False) -> tuple[KeyInfo, Path]`

Generate a new ED25519 keypair via `ssh-keygen`.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Key name and filename |
| `output_dir` | `str \| Path \| None` | Directory for private key file (default: `~/.ssh/`) |
| `comment` | `str \| None` | Key comment (default: `name@hostname`) |
| `overwrite` | `bool` | When `True`, overwrite existing key files and registry entry |

**Returns:** `(KeyInfo, private_key_path)` tuple.

**Raises:** `fcm.exceptions.KeyError` if `ssh-keygen` fails or key already exists (when `overwrite=False`).

---

### `remove_key(name: str) -> None`

Remove a key from the cache (does not delete key files from disk outside the cache).

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Key name |

**Raises:** `fcm.exceptions.KeyError` if key not found.

---

### `inspect_key(name: str) -> dict[str, object]`

Return detailed info about a named key, including the public key content.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Key name |

**Returns:** Dict with keys: `name`, `fingerprint`, `algorithm`, `comment`, `added_at`, `public_key`.

**Raises:** `fcm.exceptions.KeyError` if key not found.

---

### `KeyInfo` dataclass

```python
@dataclass
class KeyInfo:
    name: str
    fingerprint: str
    algorithm: str
    comment: str
    added_at: str
```

---

## fcm.core.network_manager

Named network management. Networks are persisted under `~/.cache/firecracker-manager/networks/`.

### `list_networks() -> list[NetworkConfig]`

List all named networks.

**Returns:** List of `NetworkConfig` instances.

---

### `get_network(name: str) -> NetworkConfig | None`

Get a named network by name.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Network name |

**Returns:** `NetworkConfig` if found, `None` if not found.

---

### `create_network(name: str, cidr: str | None = None, gateway: str | None = None, nat: bool = True) -> NetworkConfig`

Create a named network: sets up bridge device, IP range, and optionally NAT rules.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Network name |
| `cidr` | `str \| None` | IP subnet in CIDR notation, e.g. `"192.168.100.0/24"`. Auto-allocated if `None`. |
| `gateway` | `str \| None` | Gateway IP for the bridge. Defaults to first host in subnet. |
| `nat` | `bool` | Configure NAT/masquerade. Default `True`. |

**Returns:** The created `NetworkConfig`.

**Raises:** `fcm.exceptions.NetworkError` if the network already exists, CIDR overlaps with existing network, or bridge setup fails.

---

### `remove_network(name: str) -> None`

Remove a named network: tears down bridge and NAT rules, removes persisted state.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Network name |

**Raises:** `fcm.exceptions.NetworkError` if network has VMs attached or doesn't exist.

---

### `inspect_network(name: str) -> dict[str, object]`

Return full details for a named network.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Network name |

**Returns:** Dict with keys: `name`, `cidr`, `gateway`, `bridge`, `nat_enabled`, `created_at`, `bridge_exists`, `vms`.

**Raises:** `fcm.exceptions.NetworkError` if network not found.

---

### `allocate_network_ip(network_name: str, vm_name: str) -> str`

Allocate the next available IP from a network's subnet and register the lease.

| Parameter | Type | Description |
|-----------|------|-------------|
| `network_name` | `str` | Network name |
| `vm_name` | `str` | VM name for the lease |

**Returns:** Allocated IP address string.

**Raises:** `fcm.exceptions.NetworkError` if network not found or no IPs available.

---

### `release_network_ip(network_name: str, vm_name: str) -> None`

Release a VM's IP lease from a network.

| Parameter | Type | Description |
|-----------|------|-------------|
| `network_name` | `str` | Network name |
| `vm_name` | `str` | VM name whose lease to release |

---

### `get_network_leases(name: str) -> list[NetworkLease]`

Get all IP leases for a network.

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `str` | Network name |

**Returns:** List of `NetworkLease` instances.

---

### `ensure_default_network() -> NetworkConfig`

Ensure the default network exists, creating it if needed.

**Returns:** The default `NetworkConfig` (existing or newly created).

**Raises:** `fcm.exceptions.NetworkError` if creation fails.

---

### `NetworkConfig` dataclass

```python
@dataclass
class NetworkConfig:
    name: str
    cidr: str
    gateway: str
    bridge: str
    nat_enabled: bool = True
    created_at: str = ...  # ISO timestamp
```

---

### `NetworkLease` dataclass

```python
@dataclass
class NetworkLease:
    vm_name: str
    ip: str
```

---

## fcm.core.network

Low-level Linux network infrastructure management.

### `setup_bridge(bridge: str, cidr: str = BRIDGE_CIDR, gateway_cidr: str | None = None) -> None`

Create and configure a bridge interface. Idempotent.

| Parameter | Type | Description |
|-----------|------|-------------|
| `bridge` | `str` | Bridge interface name |
| `cidr` | `str` | CIDR to assign to bridge |
| `gateway_cidr` | `str \| None` | Override CIDR for bridge IP assignment |

**Raises:** `fcm.exceptions.NetworkError` on failure.

---

### `teardown_bridge(bridge: str) -> None`

Remove a bridge interface.

**Raises:** `fcm.exceptions.NetworkError` on failure.

---

### `setup_nat(bridge: str, host_iface: str | None = None) -> None`

Set up NAT MASQUERADE and FORWARD rules for the bridge subnet. Idempotent.

**Raises:** `fcm.exceptions.NetworkError` on failure.

---

### `teardown_nat(bridge: str, force: bool = False) -> None`

Remove NAT rules for the bridge. Only removes MASQUERADE if `force=True` or no TAP devices remain.

**Raises:** `fcm.exceptions.NetworkError` on failure.

---

### `create_tap(tap_name: str, bridge: str) -> None`

Create a TAP device and attach it to the bridge.

**Raises:** `fcm.exceptions.NetworkError` if tap already exists or creation fails.

---

### `delete_tap(tap_name: str) -> None`

Delete a TAP device. Safe to call if tap doesn't exist (logs warning).

**Raises:** `fcm.exceptions.NetworkError` on failure.

---

### `allocate_ip(existing_ips: list[str], subnet: str, gateway: str) -> str`

Allocate the next available IP in the subnet, skipping gateway and reserved addresses.

**Returns:** Available IP address string.

**Raises:** `fcm.exceptions.NetworkError` if no IPs available.

---

### `generate_mac() -> str`

Generate a random MAC address with `02:FC:` prefix.

**Returns:** MAC address string in format `02:FC:XX:XX:XX:XX`.

---

### `bridge_exists(bridge: str) -> bool`

Return `True` if the bridge interface exists.

---

### `tap_exists(tap_name: str) -> bool`

Return `True` if the TAP device exists.

---

### `get_tap_devices(bridge: str) -> list[str]`

List all TAP devices currently attached to the bridge.

**Returns:** List of interface name strings.

---

### `get_default_interface() -> str`

Get the default network interface by parsing `ip route show default`.

**Returns:** Interface name (e.g., `"eth0"`, `"ens3"`).

**Raises:** `fcm.exceptions.NetworkError` if not found.

---

### `get_iptables_rules_for_bridge(bridge: str) -> list[str]`

Return iptables FORWARD and NAT POSTROUTING rules that reference the given bridge.

**Returns:** List of matching rule strings (may be empty).

---

## fcm.core.vm_manager

VM state persistence under `~/.cache/firecracker-manager/vms/`.

### `VMManager(run_dir: Path | None = None)`

| Parameter | Type | Description |
|-----------|------|-------------|
| `run_dir` | `Path \| None` | Override for state directory. Defaults to `get_vms_dir()`. |

### `VMManager.register(vm: VMInstance) -> None`

Register a new VM in state.

**Parameters:** `vm` — a `VMInstance` dataclass.

---

### `VMManager.deregister(name: str) -> None`

Remove a VM from state.

---

### `VMManager.get(name: str) -> VMInstance | None`

Get VM by name. Returns `None` if not found.

---

### `VMManager.list_all() -> list[VMInstance]`

List all registered VMs regardless of status.

---

### `VMManager.update_status(name: str, status: VMState) -> None`

Update the status of a registered VM.

**Raises:** `fcm.exceptions.VMNotFoundError` if VM not found.

---

## fcm.core.firecracker

Firecracker HTTP API client over Unix domain socket.

### `FirecrackerClient(socket_path: Path)`

| Parameter | Type | Description |
|-----------|------|-------------|
| `socket_path` | `Path` | Path to Firecracker Unix socket |

### `FirecrackerClient.send_ctrl_alt_del() -> bool`

Send Ctrl+Alt+Del action to the VM (graceful shutdown request).

**Returns:** `True` if successful (HTTP 204), `False` otherwise.

---

### `FirecrackerClient.pause_vm() -> bool`

Pause the VM via the Firecracker API.

**Returns:** `True` if successful.

---

### `FirecrackerClient.resume_vm() -> bool`

Resume the VM via the Firecracker API.

**Returns:** `True` if successful.

---

### `FirecrackerClient.create_snapshot(mem_path: Path, snapshot_path: Path) -> bool`

Create a VM snapshot.

**Returns:** `True` if successful.

---

### `FirecrackerClient.load_snapshot(mem_path: Path, snapshot_path: Path, resume: bool = True) -> bool`

Load VM from snapshot.

**Returns:** `True` if successful.

---

### `FirecrackerClient.get_instance_info() -> dict[str, object] | None`

Get VM instance information.

**Returns:** Instance info dict, or `None` on failure.

---

### `FirecrackerClient.close() -> None`

Close the connection.

---

### `get_vm_socket_path(vm_name: str) -> Path | None`

Get socket path for a named VM from the cache directory.

**Returns:** `Path` to socket file if found, `None` otherwise.

---

## fcm.core.host

Host configuration management for Firecracker prerequisites.

### `init_host(cache_dir: Path) -> list[HostChange]`

Apply host configuration: enable IP forwarding, persist sysctl, load KVM modules.
Idempotent — returns empty list if no changes needed.

**Returns:** List of `HostChange` describing applied changes.

**Raises:** `fcm.exceptions.HostError` if KVM not accessible, required binaries missing, or operations fail.

---

### `restore_host(cache_dir: Path) -> list[HostChange]`

Revert host changes using the saved snapshot.

**Returns:** List of `HostChange` describing reverted changes.

**Raises:** `fcm.exceptions.HostError` if no saved state or revert fails.

---

### `prune_host(cache_dir: Path) -> list[str]`

Tear down all named networks (bridges, TAP devices, iptables rules) and revert
sysctl changes. Does NOT remove VM cache files, images, kernels, or binaries.

**Returns:** List of summary strings describing what was torn down.

---

### `get_host_state(cache_dir: Path) -> HostState | None`

Load and return the saved host state snapshot.

**Returns:** `HostState` if snapshot exists, `None` otherwise.

**Raises:** `fcm.exceptions.HostError` if state file is corrupt.

---

### `check_kvm_access() -> bool`

Return `True` if `/dev/kvm` exists and is readable/writable.

---

### `check_required_binaries() -> list[str]`

Return list of missing required binary names (`ip`, `iptables`, `qemu-img`, one of `mkisofs`/`genisoimage`).
Empty list means all required binaries are present.

---

### `HostChange` dataclass

```python
@dataclass
class HostChange:
    setting: str
    original_value: str | None
    applied_value: str
    mechanism: str
```

---

## fcm.models.vm

### `VMState` enum

```python
class VMState(str, Enum):
    RUNNING = "running"
    STOPPED = "stopped"
    PAUSED  = "paused"
    ERROR   = "error"
```

---

### `VMConfig` dataclass

Configuration for launching a Firecracker VM.

```python
@dataclass
class VMConfig:
    name: str
    vcpu_count: int
    mem_size_mib: int
    kernel_path: Path
    rootfs_path: Path
    guest_ip: str
    guest_mac: str
    tap_device: str
    enable_api_socket: bool = False
    enable_pci: bool = False
```

---

### `VMInstance` dataclass

Runtime state for a registered VM.

```python
@dataclass
class VMInstance:
    name: str
    pid: int | None
    socket_path: Path | None
    ip: str | None
    mac: str | None
    created_at: datetime
    status: VMState
```

---

## fcm.exceptions

### Exception hierarchy

```
FCMError
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
├── ProcessError          — Subprocess execution failure
├── AssetNotFoundError    — Asset not found locally or remotely
├── BinaryError           — Firecracker/jailer binary management failure
└── KeyError              — SSH key management failure
```

All exceptions derive from `fcm.exceptions.FCMError` which derives from `Exception`.
