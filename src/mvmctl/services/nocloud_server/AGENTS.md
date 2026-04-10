# nocloud_server

## OVERVIEW
Runtime subprocess service providing a NoCloud HTTP data source for VM cloud-init metadata during boot.

## STRUCTURE
- `manager.py`: `NoCloudNetServerManager` class — coordinates server lifecycle, port allocation, and orphan cleanup.
- `process.py`: Standalone HTTP server subprocess — implements the `_CloudInitRequestHandler` and request loop.
- `__init__.py`: Package exports for `NoCloudNetServerManager`.

## WHERE TO LOOK
| Component | Responsibility |
|-----------|----------------|
| `start_server()` | Allocates port (8000-9000), spawns `process.py`, and returns metadata URL. |
| `stop_server()` | Sends SIGTERM and cleans up PID files/registry entries. |
| `cleanup_orphans()` | Scans for stale PID files in `$MVM_CACHE_DIR/vms/*/` and stops orphaned processes. |
| `_CloudInitRequestHandler` | Serves `user-data` and `meta-data` with cache-disabling headers. |

## CONVENTIONS
- **Manager-Process Pattern**: The manager handles orchestration and registry, while `process.py` runs as an isolated subprocess using only standard library imports.
- **Port Allocation**: Dynamically scans `8000-9000` (`CONST_NO_CLOUD_NET_PORT_RANGE`) and binds specifically to the gateway IP for isolation.
- **PID-Based Recovery**: PID files are stored in the VM's cache directory (`nocloud-server.pid`), allowing the manager to recover state across restarts.
- **Graceful Shutdown**: Subprocess handles SIGTERM to ensure PID file cleanup and socket release.

## NOTES
- **Security**: The server binds ONLY to the bridge gateway IP, never `0.0.0.0`. Access is typically managed via iptables port forwarding.
- **Lifecycle**: Usually started during `create_vm()` and stopped once the VM reaches a "running" or "configured" state (though may persist for late-joining services).
- **Isolation**: `process.py` is designed to be executable as a standalone script to simplify testing and minimize runtime dependencies.
