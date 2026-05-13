# Console Relay Service

Bidirectional PTY-to-socket relay for VM serial console access.

## Overview

The console relay service bridges Firecracker's serial console (Unix socket) to a host PTY, enabling interactive console access without SSH.

```
Firecracker VM ──PTY──► Relay Process ──► Unix Socket (for CLI client)
                     └──► console.log (persistent)
```

## Components

| File | Purpose |
|------|---------|
| `manager.py` | `ConsoleRelayManager` - Lifecycle management (one instance per relay) |
| `process.py` | Standalone relay subprocess (spawned by manager) |
| `client.py` | `ConsoleRelayClient` - CLI client for connecting to relay |
| `exceptions.py` | Custom exception hierarchy |
| `_defaults.py` | Service-specific constants |

## Usage

### Server Side (Starting a Relay)

```python
from pathlib import Path
from mvmctl.services.console_relay import ConsoleRelayManager

# Create manager for a specific VM/console
manager = ConsoleRelayManager(
    id="vm-abc123",
    path=Path("/var/lib/mvm/vms/abc123"),
    name="my-vm"
)

# Start the relay (requires PTY controller FD)
pty_controller_fd = 10  # From Firecracker PTY
socket_path, pid = manager.start(pty_controller_fd)

# Later: stop gracefully
manager.stop()

# Or: force terminate
manager.terminate()
```

### Client Side (Connecting to Relay)

```python
from pathlib import Path
from mvmctl.services.console_relay import ConsoleRelayClient

socket_path = Path("/var/lib/mvm/vms/abc123/console.sock")

# Context manager handles connect/disconnect
with ConsoleRelayClient(socket_path) as client:
    # Receive output from VM
    for data in client.receive():
        print(data.decode(), end="")
        
        # Send input to VM
        user_input = input(":")
        client.send((user_input + "\n").encode())
```

### With Detach Sequence Detection

```python
from mvmctl.services.console_relay import ConsoleRelayClient, CONST_CONSOLE_DETACH_SEQUENCE

input_buffer = bytearray()

with ConsoleRelayClient(socket_path) as client:
    for data in client.receive():
        print(data.decode(), end="", flush=True)
        
        # Non-blocking input check
        import sys, select
        if select.select([sys.stdin], [], [], 0)[0]:
            char = sys.stdin.read(1)
            input_buffer.extend(char.encode())
            
            # Check for detach: Ctrl+X then d
            if client.check_detach(input_buffer):
                print("\n[Detached]")
                break
            
            client.send(char.encode())
```

## Architecture

### Process Model

```
┌─────────────────┐     spawns    ┌──────────────────┐
│ ConsoleRelayMgr │ ─────────────►│  process.py      │
│   (manager.py)  │               │  (standalone)    │
└─────────────────┘               └──────────────────┘
        │                                 │
        │ manages                         │ reads/writes
        ▼                                 ▼
   PID/socket files                   PTY ◄──► Socket
```

- **Manager** (parent process): Handles lifecycle, monitors health
- **Process** (subprocess): Bidirectional relay loop
- Only ONE client can connect to socket at a time

### Data Flow

1. **Firecracker** writes VM console output to PTY
2. **Relay process** reads from PTY and:
   - Writes to `console.log` (always)
   - Forwards to Unix socket (if client connected)
3. **Client** reads from socket and displays to user
4. **User input** goes reverse: Client → Socket → Relay → PTY → VM

## Configuration

All timeouts and filenames are configurable via `_defaults.py`:

```python
# Timing
CONST_CONSOLE_KILL_TIMEOUT_S = 2.0      # Seconds before SIGKILL
CONST_CONSOLE_SELECT_TIMEOUT_S = 0.1    # Select loop timeout
CONST_CONSOLE_READ_BUFFER_SIZE = 4096   # Read buffer size

# Files (customizable per-manager)
DEFAULT_CONSOLE_PID_FILENAME = "console.pid"
DEFAULT_CONSOLE_SOCKET_FILENAME = "console.sock"
DEFAULT_CONSOLE_LOG_FILENAME = "firecracker.console.log"

# Client
CONST_CONSOLE_DETACH_SEQUENCE = b"\x18d"  # Ctrl+X, then 'd'
```

## Exceptions

| Exception | Raised When |
|-----------|-------------|
| `ConsoleRelayError` | Base class for all relay errors |
| `ConsoleRelayAlreadyRunningError` | Attempting to start already-running relay |
| `ConsoleRelayNotRunningError` | Attempting to stop or interact with a relay that is not running |
| `ConsoleRelayProcessError` | Subprocess fails to start |
| `ConsoleRelayPermissionError` | Relay lacks necessary permissions (e.g., for PTY or socket) |
| `ConsoleRelayConnectionError` | Client fails to connect to socket |

## Thread Safety

- `ConsoleRelayManager`: Thread-safe via internal lock
- `ConsoleRelayClient`: Not thread-safe (use one client per thread)

## Notes

- Console output is **always** logged to `firecracker.console.log`
- Socket is Unix domain (not TCP) for security
- Relay auto-cleans PID/socket files on exit
- Single client at a time - others will be rejected
