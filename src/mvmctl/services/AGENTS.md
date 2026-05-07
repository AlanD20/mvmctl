# mvmctl/services/ — Runtime Services

**Scope:** Subprocess-based runtime services for VM console access and cloud-init datasource serving
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Services run as standalone subprocesses; managers handle lifecycle in core/

## STRUCTURE

```
src/mvmctl/services/
├── __init__.py              # Package marker only
├── console_relay/           # PTY-to-socket relay for VM serial console
│   ├── __init__.py
│   ├── _defaults.py         # Default configuration values
│   ├── client.py            # ConsoleRelayClient — socket connection
│   ├── exceptions.py        # Console relay exceptions
│   ├── manager.py           # ConsoleRelayManager lifecycle
│   └── process.py           # Standalone PTY relay subprocess
├── loopmount/               # Loop-mount rootfs provisioning
│   ├── __init__.py
│   └── process.py           # Standalone mvm-provision subprocess (stdin/stdout JSON protocol)
└── nocloud_server/          # HTTP server for cloud-init nocloud-net datasource
    ├── __init__.py
    ├── _defaults.py         # Default configuration values
    ├── exceptions.py        # Nocloud server exceptions
    ├── manager.py           # NoCloudNetServerManager lifecycle
    └── process.py           # Standalone HTTP server subprocess
```

## ARCHITECTURE

Services follow a manager+process pattern:

```
api/vm_operations.py (or core/vm/_controller.py)
        │
        ▼ calls
┌─────────────────┐     spawns    ┌──────────────────┐
│  Manager class  │ ─────────────►│  process.py      │
│  (in services/)   │   subprocess  │  (standalone)    │
└─────────────────┘               └──────────────────┘
        │
        ▼ manages
   VM resource
```

**Key distinction:**
- **Manager** (manager.py): Imported by core/; handles start/stop/restart; manages PID files; monitors health
- **Process** (process.py): Has `main()` entry point; runs standalone with `if __name__ == "__main__"`; minimal dependencies

## CONSOLE RELAY

**Purpose:** Bridge between Firecracker's serial console (Unix socket) and host PTY

**Manager:** `console_relay/manager.py:ConsoleRelayManager`
- `start_relay(vm_name, pty_master_fd, vm_dir)` → spawns `process.py`, returns `(socket_path, pid)`
- `stop_relay(vm_name, vm_hash)` → sends SIGTERM, cleans up PID/socket files
- `kill_relay(vm_name, vm_hash)` → SIGTERM → wait → SIGKILL
- `get_relay_pid(vm_name, vm_hash)` → returns PID from file or None
- `is_relay_running(vm_name, vm_hash)` → checks PID file + process alive
- `get_socket_path(vm_name, vm_hash)` → returns path to Unix socket
- `cleanup_orphans()` → scans `$MVM_CACHE_DIR/vms/*/console.pid`, kills stale processes

**Process:** `console_relay/process.py`
- `main()` entry point with argparse (`--vm-name`, `--pty-master-fd`, `--socket-path`, `--pid-file`, `--log-file`, `--buffer-size`)
- PTY master → reads → writes to `console.log` + forwards to Unix socket
- Bidirectional relay: PTY ↔ socket client
- `select.select()` loop multiplexing PTY + socket I/O
- SIGTERM/SIGINT graceful shutdown

**CLI access:** `mvm vm console --name <vm>` (from `cli/console.py`)

## NOCLOUD-NET SERVER

**Purpose:** HTTP server serving cloud-init meta-data/user-data/network-config to VMs

**Manager:** `nocloud_server/manager.py:NoCloudNetServerManager`
- `start_server(vm_name, cloud_init_dir, gateway_ip, vm_hash, preferred_port)` → auto-allocates port 8000–9000, returns `(url, port)`
- `stop_server(vm_name, vm_hash)` → sends SIGTERM, cleans up PID file
- `get_server(vm_name, vm_hash)` → returns server details if running
- `get_server_pid(vm_name, vm_hash)` → returns PID from file or None
- `is_server_running(vm_name, vm_hash)` → checks PID file + process alive
- Port allocation: `socket.bind((gateway_ip, port))` tests availability before spawning
- `cleanup_orphans()` → cross-references `$MVM_CACHE_DIR/vms/*/nocloud-server.pid` with running VMs

**Process:** `nocloud_server/process.py`
- `main()` entry point with argparse (`--cloud-init-dir`, `--port`, `--host`, `--pid-file`)
- `HTTPServer` with `_CloudInitRequestHandler(SimpleHTTPRequestHandler)` — serves from `cloud_init_dir`
- Binds to `gateway_ip` only (never 0.0.0.0) — firewall-isolated
- SIGTERM/SIGINT graceful `server.shutdown()`
- Serves: `meta-data`, `user-data`, `network-config` (netplan v2)

**CLI enable:** `mvm vm create --name <vm>` (nocloud-net is default behavior)

## LOOP-MOUNT PROVISIONER

**Purpose:** Provision root filesystem images via loop-mount (SSH keys, hostname, DNS, resize) without libguestfs

**Process:** `loopmount/process.py:Provisioner` (standalone binary — no manager in services/)
- `main()` entry point — reads JSON operations from stdin, writes JSON results to stdout
- `Provisioner` class — handles partition detection, mounting, file operations, chroot commands
- Operation types: `resize`, `set_hostname`, `inject_dns`, `setup_ssh`, `disable_cloud_init`, `inject_cloud_init`, `fix_fstab`, `detect_os`, `deblob`
- Filesystem grow and shrink for ext4 (resize2fs/e2fsck) and btrfs (btrfs filesystem resize)
- Chroot execution for post-mount operations (user creation, SSH setup, package manager cache clean)
- PARTUUID → /dev/vda fstab fixing for Firecracker compatibility
- Graceful error handling with JSON error responses
- `--input-json` flag for file-based input (testing) or stdin-based (production)
- No manager in services/ — lifecycle managed by `LoopMountManager` in `core/_shared/_loopmount/`

**Architecture:**

```
VMProvisioner → ProvisionerBackend → LoopMountProvisioner → LoopMountManager → spawns → mvm-provision
                                                                                             │
                                         JSON ops stdin ──→ losetup/mount/chroot
                                         JSON results ←── stdout
```

**CLI integration:** `mvm vm create` uses loop-mount as the default provisioning backend

**Related:**
- `core/_shared/_loopmount/_manager.py` — `LoopMountManager` (binary execution/lifecycle)
- `core/_shared/_loopmount/_provisioner.py` — `LoopMountProvisioner` (operation builder)
- `core/_shared/_provisioner/_backend.py` — `_LoopMountBackend` (backend adapter)
- `core/vm/_provisioner.py` — `VMProvisioner` (unified builder API)
- `core/image/_provisioner.py` — `ImageProvisioner` (image conversion backend)
- `models/provisioner.py` — `ProvisionerType` enum

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import from `core/` in process.py | Process.py has no upward deps; only stdlib + minimal utils |
| Run manager methods in process.py | Manager runs in parent (core/), process runs standalone |
| Share state via globals | Use PID files + signal handling for coordination |
| Bind HTTP server to 0.0.0.0 | Bind to bridge gateway IP only (firewall-isolated) |

## COMMANDS

```bash
# Console relay tests
uv run pytest tests/unit/services/console_relay/ -v

# Nocloud server tests
uv run pytest tests/unit/services/nocloud_server/ -v

# Loop-mount provisioner tests
uv run pytest tests/unit/services/loopmount/ -v
```

## NOTES

- PID files: `$MVM_CACHE_DIR/vms/<vm-name>/console.pid` and `nocloud-server.pid`
- Manager runs in parent (core/ or services/), process runs standalone
- Port allocation for nocloud-net: tries 8000-9000, binds to gateway IP
- Firewall rules (iptables) managed by `core/_shared/_iptables_tracker/`, not services/
- Services auto-exit when parent process terminates; `cleanup_orphans()` handles stale PIDs
