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
│   ├── manager.py          # 456 lines — ConsoleRelayManager lifecycle
│   └── process.py          # 186 lines — Standalone PTY relay subprocess
└── nocloud_server/          # HTTP server for cloud-init nocloud-net datasource
    ├── __init__.py
    ├── manager.py          # 488 lines — NoCloudNetServerManager lifecycle
    └── process.py          # 154 lines — Standalone HTTP server subprocess
```

## ARCHITECTURE

Services follow a manager+process pattern:

```
core/vm_lifecycle.py
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
- `is_relay_running(vm_name, vm_hash)` → checks PID file + process alive
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
- Port allocation: `socket.bind((gateway_ip, port))` tests availability before spawning
- `cleanup_orphans()` → cross-references `$MVM_CACHE_DIR/vms/*/nocloud-server.pid` with running VMs

**Process:** `nocloud_server/process.py`
- `main()` entry point with argparse (`--cloud-init-dir`, `--port`, `--host`, `--pid-file`)
- `HTTPServer` with `_CloudInitRequestHandler(SimpleHTTPRequestHandler)` — serves from `cloud_init_dir`
- Binds to `gateway_ip` only (never 0.0.0.0) — firewall-isolated
- SIGTERM/SIGINT graceful `server.shutdown()`
- Serves: `meta-data`, `user-data`, `network-config` (netplan v2)

**CLI enable:** `mvm vm create --name <vm>` (nocloud-net is default behavior)

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
```

## NOTES

- PID files: `$MVM_CACHE_DIR/vms/<vm-name>/console.pid` and `nocloud-server.pid`
- Port allocation for nocloud-net: tries 8000-9000, binds to gateway IP to test availability
- Firewall rules (iptables) managed by `core/firewall.py`, not services/
- Services auto-exit when parent process terminates; `cleanup_orphans()` handles stale PIDs
