# mvmctl/services/console_relay/ — VM Serial Console Relay Service

**Scope:** PTY-to-socket relay for bidirectional VM serial console access
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Manager handles lifecycle in core/; process.py runs standalone with minimal deps

## OVERVIEW

Bidirectional PTY relay service that bridges Firecracker's serial console (Unix socket) to a host PTY, enabling interactive console access without SSH via `mvm console`.

## STRUCTURE

```
src/mvmctl/services/console_relay/
├── __init__.py          # Package exports: ConsoleRelayManager
├── manager.py           # ConsoleRelayManager lifecycle (start/stop/kill/monitor)
└── process.py           # Standalone PTY relay subprocess (main entry point)
```

## WHERE TO LOOK

### manager.py — ConsoleRelayManager
- `ConsoleRelayManager`: Lifecycle manager for console relay subprocesses.
- `start_relay()`: Spawns `process.py` subprocess using `subprocess.Popen` with `pass_fds`.
- `stop_relay()` / `kill_relay()`: Graceful and forced termination of relay processes.
- `cleanup_orphans()`: Scans cache for stale PID files and cleans up orphaned processes at init.

### process.py — Standalone Relay Process
- `main()`: Entry point with `argparse` for standalone subprocess execution.
- `select.select()` loop: Multiplexes between PTY master, Unix socket, and log file.
- Signal handlers: Handles `SIGTERM/SIGINT` for graceful cleanup of PID and socket files.

## CONVENTIONS

- **Isolated Process**: `process.py` MUST remain standalone with zero external `mvmctl` dependencies (stdlib only) to ensure reliability as a subprocess.
- **Thread Safety**: `ConsoleRelayManager` uses a lazy-initialized `_thread_lock` for all registry operations.
- **File Ownership**: Relays manage `console.pid`, `console.sock`, and `firecracker.console.log` in the VM's state directory.
- **Data Flow**: Firecracker VM ──PTY──► `process.py` ──► `console.log` + Unix socket (for CLI client).

## NOTES

- **FD Passing**: Uses `pass_fds=[pty_master_fd]` to share the PTY with the child process.
- **Single Client**: Only one CLI client can attach to the Unix socket at a time.
- **Log Persistence**: Console output is always written to `firecracker.console.log` regardless of whether a client is attached.
- **Graceful Shutdown**: SIGTERM triggers a clean exit via a flag in the multiplexing loop.
