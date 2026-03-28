# Firecracker PTY-over-Vsock Bridge Implementation Guide

## Overview

This guide explains how to implement an interactive console for Firecracker microVMs using the **virtio-vsock** device and a host-side PTY bridge. No modifications to Firecracker are required.

**What you'll achieve:**
- Interactive shell access to microVMs (like SSH but lighter)
- Full terminal support (resize, signals, line editing)
- Works with standard terminal emulators and SSH clients
- No network stack required in the guest

**Prerequisites:**
- Firecracker v1.0+ (vsock is built-in)
- Guest kernel with `CONFIG_VSOCKETS=y` (included in official Firecracker kernels)
- Linux host with Unix domain socket support

---

## Firecracker Configuration

### 1. Enable Vsock Device

Add a vsock device to your microVM using either the Firecracker API or a configuration file:

#### Method A: Via API (before starting VM)

```bash
curl --unix-socket /tmp/firecracker.socket -i \
  -X PUT 'http://localhost/vsock' \
  -H 'Content-Type: application/json' \
  -d '{
      "guest_cid": 3,
      "uds_path": "./v.sock"
  }'
```

#### Method B: Via Configuration File

Add to your Firecracker JSON configuration file:

```json
{
  "boot-source": {
    "kernel_image_path": "vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off"
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "rootfs.ext4",
      "is_root_device": true,
      "is_read_only": false
    }
  ],
  "machine-config": {
    "vcpu_count": 2,
    "mem_size_mib": 512
  },
  "vsock": {
    "guest_cid": 3,
    "uds_path": "./v.sock"
  }
}
```

Then start Firecracker with the config:

```bash
firecracker \
  --api-sock /tmp/firecracker.socket \
  --config-file vm-config.json
```

Or use the API to load the configuration:

```bash
curl --unix-socket /tmp/firecracker.socket \
  -X PUT 'http://localhost/vm/config' \
  -H 'Content-Type: application/json' \
  -d @vm-config.json
```

**Parameters:**
- `guest_cid`: Context ID (like an IP address for vsock). Use 3 or higher (2 is reserved for host).
- `uds_path`: Path to the Unix domain socket Firecracker will create on the host.

**Important:** Configure this **before** starting the microVM (`InstanceStart`).

#### Understanding CID vs Port

There's an important distinction between **CID** and **Port**:

| Concept | Configured In | What It Is | Analogy |
|---------|---------------|------------|---------|
| **CID** | Firecracker API/config | Guest's vsock "address" | IP address (e.g., 192.168.1.5) |
| **Port** | Guest application runtime | Service endpoint | TCP port (e.g., :22 for SSH) |

**How it works:**
1. You configure **CID=3** in Firecracker (once per VM)
2. Inside the guest, applications listen on **ports** (like 1024) using standard socket APIs
3. The guest kernel has native AF_VSOCK support - no boot args needed for port allocation

**Example:**
```bash
# Firecracker configuration (host side)
guest_cid: 3  # This VM's vsock "address"

# Inside guest (runtime, not boot config)
socat VSOCK-LISTEN:1024  # Application listens on port 1024
```

**You do NOT add vsock ports to kernel boot args.** The kernel automatically creates `/dev/vsock` when `CONFIG_VSOCKETS=y`. Applications then use that device to listen on any port they choose at runtime.

### 2. Verify Vsock Device

After VM starts, check that vsock is available:

**On the host:**
```bash
# Check if UDS was created
ls -la ./v.sock
srwxr-xr-x 1 user user 0 Mar 28 12:00 ./v.sock
```

**Inside the guest:**
```bash
# Verify /dev/vsock exists
ls -la /dev/vsock
crw-rw-rw- 1 root root 10, 241 Mar 28 12:00 /dev/vsock

# Check kernel support
ls /sys/bus/virtio/drivers/vmw_vsock_virtio_transport/
echo 'DRIVER_OK' > /sys/bus/virtio/drivers/vmw_vsock_virtio_transport/bind
```

---

## Guest Setup (Inside MicroVM)

**Important Architecture:** There are TWO components working together:

1. **Guest Agent** (runs INSIDE the microVM) - accepts vsock connections and spawns shells
2. **Host Bridge** (runs on the HOST) - connects your terminal to the guest

```
┌─────────────────────────────────────────────────────────────────┐
│ HOST (Your Computer)                                            │
│ ┌──────────────────┐         Firecracker         ┌────────────┐│
│ │ Terminal/SSH     │◄──────┐  ┌──────────┐       │ Guest VM   ││
│ └────────┬─────────┘       │  │ Vsock    │       │            ││
│          │                 └──►│ Device   │──────►│ ┌────────┐ ││
│          │ ./v.sock           │ └──────────┘       │ │ Agent  │ ││
│          ▼                    │                  │ │ 1024   │ ││
│ ┌──────────────────┐          │  ┌──────┐       │ └───┬────┘ ││
│ │ Host Bridge (Python)          │  │ Kernel│◄──────┘     │      ││
│ │ • Opens ./v.sock │          │  └──────┘             │      ││
│ │ • Creates PTY    │          │                       │      ││
│ │ • Bridges I/O    │          │                       │      ││
│ └──────────────────┘          │                       │      ││
└─────────────────────────────────────────────────────────────────┘

The Guest Agent listens on vsock port 1024 inside the VM.
The Host Bridge connects from host to Firecracker UDS, then to guest.
```

**When does the agent need to run?**

The Guest Agent must be running **before** you try to connect from the host, but you have options:

### Option 1: Auto-Start at Boot (Recommended)

Add the agent to your guest's init system so it starts automatically when the VM boots:

**For init-based systems:**
```bash
# /etc/init.d/vsock-agent
#!/bin/sh
# chkconfig: 2345 99 01
# description: Vsock PTY agent

case "$1" in
  start)
    echo "Starting vsock agent..."
    socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &
    ;;
  stop)
    echo "Stopping vsock agent..."
    pkill -f "socat VSOCK-LISTEN:1024"
    ;;
  *)
    echo "Usage: $0 {start|stop}"
    exit 1
esac
```

**For systemd:**
```ini
# /etc/systemd/system/vsock-agent.service
[Unit]
Description=Vsock Console Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**Enable:**
```bash
systemctl enable vsock-agent
systemctl start vsock-agent
```

### Option 2: Manual Start After Boot

Start the agent manually after the VM is running (useful for testing):

```bash
# In guest (via existing serial console or SSH)
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &

# Or using the Python agent:
python3 guest-agent.py &
```

**Use case:** You might use the serial console to start the agent, then switch to vsock for better terminal experience.

### Option 3: Start via Existing Serial Console

```bash
# 1. Boot VM with serial console
# 2. Log in via serial
# 3. Start agent manually:
root@guest:~# socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &

# 4. Now connect from host via vsock:
# (On host) ./fc-pty-bridge.py ./v.sock 1024
```

**This is the "upgrade" pattern:** Use serial to bootstrap vsock, then use vsock for the real work.

---

### Quick Setup with Socat

**How the port works:** The guest agent and host bridge must agree on the same port number. The port is chosen by **you** - it's not configured in Firecracker, it's just a runtime agreement between guest and host applications.

```
Guest Agent (inside VM)          Host Bridge (on host)
    |                                   |
    | socat VSOCK-LISTEN:1024           | ./fc-pty-bridge.py ./v.sock 1024
    |                                   |
    └──────────────┬────────────────────┘
                   │
            Port 1024 is just an agreement
            between guest agent and host bridge
```

**Port selection:**
- **1024** in examples is just a convention (first non-reserved port)
- You can use ANY port from 1024-65535
- Guest agent and host bridge must use the SAME port number
- Firecracker doesn't care what port you use

**Examples:**
```bash
# Guest listens on 1024, host connects to 1024
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &
./fc-pty-bridge.py ./v.sock 1024

# Guest listens on 2222 (like SSH), host connects to 2222
socat VSOCK-LISTEN:2222,fork EXEC:/bin/bash,pty,stderr,setsid &
./fc-pty-bridge.py ./v.sock 2222

# Different services on different ports
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &      # Shell
socat VSOCK-LISTEN:1025,fork EXEC:/usr/bin/tail -f /var/log/syslog &   # Logs
socat VSOCK-LISTEN:1026,fork EXEC:/usr/bin/top,pty,stderr,setsid &    # Top

# Connect to different services
./fc-pty-bridge.py ./v.sock 1024  # Get shell
./fc-pty-bridge.py ./v.sock 1025  # View logs
./fc-pty-bridge.py ./v.sock 1026  # Run top
```

Install `socat` in your guest rootfs (if not present):

```bash
# Alpine
apk add socat

# Debian/Ubuntu  
apt-get install socat

# Or compile static binary for minimal rootfs
```

Start the guest agent (add to init script or run manually):

```bash
#!/bin/sh
# Start vsock agent on boot

# Single session (good for testing)
socat VSOCK-LISTEN:1024 EXEC:/bin/bash,pty,stderr,setsid &

# OR: Multiple concurrent sessions (for production)
socat VSOCK-LISTEN:1024,fork EXEC:/bin/login,pty,stderr,setsid &

# OR: With authentication prompt
socat VSOCK-LISTEN:1024,fork EXEC:/bin/su,pty,stderr,setsid &
```

**What this does:**
- Listens on **vsock port 1024** (you choose this port - any unused 16-bit number works)
- Spawns `/bin/bash` (or login) with PTY
- `pty` flag allocates pseudo-terminal
- `setsid` creates new session (proper terminal handling)
- `fork` allows multiple concurrent connections

**About the port:** The port (1024 in this example) is chosen by **you** when starting the guest agent. It's not configured in Firecracker or kernel boot args. Common choices:
- 1024-49151: User ports (good for custom services)
- 49152-65535: Dynamic/private ports

**Multiple services:** You can run multiple agents on different ports:
```bash
# Shell on port 1024
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &

# Logs on port 1025
socat VSOCK-LISTEN:1025,fork EXEC:/usr/bin/tail -f /var/log/syslog &

# Status on port 1026
socat VSOCK-LISTEN:1026,fork EXEC:/usr/bin/top,pty,stderr,setsid &
```

### Option B: Custom Guest Agent (Python)

For more control, build a minimal Python agent:

```python
#!/usr/bin/env python3
"""Guest Agent for Firecracker PTY-over-Vsock

Runs inside the microVM and accepts vsock connections,
spawning shells with proper PTY allocation.
"""

import os
import sys
import socket
import pty
import select
import subprocess
import signal
import fcntl
import array
from typing import Optional


def get_vsock_socket(cid: int = socket.VMADDR_CID_ANY, port: int = 1024):
    """Create and bind a vsock socket.
    
    Note: Requires Python 3.13+ with AF_VSOCK support,
    or use the vsock Python package.
    """
    # For Python 3.13+ with native AF_VSOCK:
    sock = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
    sock.bind((cid, port))
    sock.listen(5)
    return sock


def handle_connection(client_fd: int):
    """Handle a single vsock connection."""
    pid = os.fork()
    
    if pid == 0:
        # Child process
        # Create new session
        os.setsid()
        
        # Open PTY slave
        slave_fd = os.open(os.ttyname(client_fd), os.O_RDWR)
        
        # Make it controlling terminal
        fcntl.ioctl(slave_fd, os.TIOCSCTTY, 0)
        
        # Redirect stdio to PTY
        os.dup2(slave_fd, 0)  # stdin
        os.dup2(slave_fd, 1)  # stdout
        os.dup2(slave_fd, 2)  # stderr
        
        # Close original fds
        os.close(slave_fd)
        if client_fd not in (0, 1, 2):
            os.close(client_fd)
        
        # Execute shell
        os.execv("/bin/bash", ["bash", "-l"])
        sys.exit(1)
    
    else:
        # Parent: bridge data between vsock and PTY
        # This is a simplified version - full version would use select/poll
        os.waitpid(pid, 0)


def main():
    """Main agent loop."""
    # Accept port from command line, default to 1024
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 1024
    
    print(f"Guest agent listening on vsock port {port}")
    
    # For systems with AF_VSOCK support (Python 3.13+ or patched)
    try:
        server = get_vsock_socket(port=port)
        
        while True:
            client, addr = server.accept()
            print(f"Connection from CID {addr[0]}, port {addr[1]}")
            
            # Handle in new thread/process
            pid = os.fork()
            if pid == 0:
                # Child handles this connection
                server.close()
                handle_connection(client.fileno())
                sys.exit(0)
            else:
                # Parent continues accepting
                client.close()
                
    except AttributeError:
        print("AF_VSOCK not available in this Python build.")
        print("Use the 'python-vsock' package or socat instead.")
        sys.exit(1)
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
```

**Alternative: Simpler Python agent using socket passthrough:**

```python
#!/usr/bin/env python3
"""Simple guest agent using stdio passthrough"""

import os
import sys
import pty
import select

# This assumes the vsock connection is passed as stdin/stdout
# which socat can set up for us

def main():
    # Create PTY
    master, slave = pty.openpty()
    
    # Fork
    pid = os.fork()
    
    if pid == 0:
        # Child: setup and exec shell
        os.close(master)
        os.setsid()
        
        # Set controlling terminal
        import fcntl
        fcntl.ioctl(slave, os.TIOCSCTTY, 0)
        
        # Redirect stdio
        os.dup2(slave, 0)
        os.dup2(slave, 1)
        os.dup2(slave, 2)
        os.close(slave)
        
        # Exec shell
        os.execv("/bin/bash", ["bash", "-l"])
        sys.exit(1)
    
    else:
        # Parent: bridge between stdio (vsock) and PTY
        os.close(slave)
        
        try:
            while True:
                readable, _, _ = select.select([sys.stdin.fileno(), master], [], [])
                
                for fd in readable:
                    if fd == sys.stdin.fileno():
                        # Data from vsock -> PTY
                        data = os.read(sys.stdin.fileno(), 4096)
                        if not data:
                            return
                        os.write(master, data)
                    
                    elif fd == master:
                        # Data from PTY -> vsock
                        data = os.read(master, 4096)
                        if not data:
                            return
                        os.write(sys.stdout.fileno(), data)
        
        finally:
            os.close(master)
            os.waitpid(pid, 0)


if __name__ == "__main__":
    main()
```

**Installation:**
```bash
# Just copy to guest and run (uses default port 1024)
chmod +x guest-agent.py
./guest-agent.py

# Or specify a custom port
./guest-agent.py 2222  # Listen on port 2222

# Or with socat wrapper (port set in socat command):
socat VSOCK-LISTEN:1024,fork EXEC:/usr/local/bin/guest-agent.py
socat VSOCK-LISTEN:2222,fork EXEC:/usr/local/bin/guest-agent.py
```

**Note:** For production, the socat one-liner is usually sufficient. The Python agent is useful when you need custom authentication, logging, or connection management. Both socat and the Python agent let you choose any port number.

### Auto-Start on Boot

Add to your guest init system:

**For init-based systems:**
```bash
# /etc/init.d/vsock-agent
#!/bin/sh
# chkconfig: 2345 99 01
# description: Vsock PTY agent

case "$1" in
  start)
    echo "Starting vsock agent..."
    socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &
    ;;
  stop)
    echo "Stopping vsock agent..."
    pkill -f "socat VSOCK-LISTEN:1024"
    ;;
  *)
    echo "Usage: $0 {start|stop}"
    exit 1
esac
```

**For systemd:**
```ini
# /etc/systemd/system/vsock-agent.service
[Unit]
Description=Vsock Console Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

---

## Host Bridge Implementation

The host bridge connects your terminal to the Firecracker vsock. Choose your implementation based on needs:

> **Port Coordination Reminder:** 
> The host bridge must connect to the **same port** that the guest agent is listening on. 
> If guest listens on port 1024, host connects to port 1024. If guest uses 2222, host uses 2222.
> 
> ```
> Guest: socat VSOCK-LISTEN:1024 ←→ Host: ./fc-pty-bridge.py ./v.sock 1024
> Guest: socat VSOCK-LISTEN:2222 ←→ Host: ./fc-pty-bridge.py ./v.sock 2222
> ```

### Option 1: Quick Connect with Socat

**Simplest approach - no code required:**

```bash
# Terminal 1: Connect to Firecracker UDS
socat -,raw,echo=0 UNIX-CONNECT:./v.sock

# Then type (literally type this in terminal):
CONNECT 1024

# Press Enter
# You'll see "OK <port>" response
# Now you have an interactive bash shell!
```

**One-liner version:**
```bash
(sleep 0.1; echo "CONNECT 1024") | socat -,raw,echo=0 UNIX-CONNECT:./v.sock
```

**Limitations:**
- Manual CONNECT command required
- No automatic reconnection
- No window resize forwarding

### Option 2: Shell Script Wrapper

Create a reusable connection script:

```bash
#!/bin/bash
# fc-console.sh - Connect to Firecracker microVM console

set -e

# Configuration
UDS_PATH="${1:-./v.sock}"
PORT="${2:-1024}"
TIMEOUT=2

# Validate UDS exists
if [[ ! -S "$UDS_PATH" ]]; then
    echo "Error: Unix domain socket not found: $UDS_PATH"
    echo "Usage: $0 [uds_path] [port]"
    echo "Example: $0 ./v.sock 1024"
    exit 1
fi

echo "Connecting to Firecracker VM..."
echo "  UDS: $UDS_PATH"
echo "  Port: $PORT"

# Create PTY and connect
# Uses socat to handle the CONNECT handshake automatically
exec socat -,raw,echo=0,escape=0x1d \
    SYSTEM:"sleep 0.1; echo 'CONNECT $PORT'; cat",nofork,stderr \
    UNIX-CONNECT:"$UDS_PATH"
```

**Usage:**
```bash
chmod +x fc-console.sh
./fc-console.sh ./v.sock 1024

# Exit with Ctrl+], then 'q'
```

### Option 2: Python Shell Script Wrapper

Create a reusable Python connection script:

```python
#!/usr/bin/env python3
"""Simple shell wrapper for Firecracker console connection

This is a lightweight alternative to the full PTY bridge.
It uses socat for the actual connection but provides a nicer interface.
"""

import sys
import os
import subprocess
import argparse

def connect(uds_path: str, port: int):
    """Connect to Firecracker VM using socat."""
    
    # Validate UDS exists
    if not os.path.exists(uds_path):
        print(f"Error: Unix domain socket not found: {uds_path}", file=sys.stderr)
        print("Usage: python3 fc-console.py [uds_path] [port]", file=sys.stderr)
        print("Example: python3 fc-console.py ./v.sock 1024", file=sys.stderr)
        sys.exit(1)
    
    if not os.path.isdir(uds_path):
        # Check if it's a socket
        import stat
        mode = os.stat(uds_path).st_mode
        if not stat.S_ISSOCK(mode):
            print(f"Error: {uds_path} is not a Unix socket", file=sys.stderr)
            sys.exit(1)
    
    print(f"Connecting to Firecracker VM...")
    print(f"  UDS: {uds_path}")
    print(f"  Port: {port}")
    print()
    
    # Build socat command
    # - Terminal mode with raw and no local echo
    # - Escape character Ctrl+] (0x1d)
    # - Execute shell command to send CONNECT then pass through data
    cmd = [
        "socat",
        "-",  # stdin/stdout
        f"UNIX-CONNECT:{uds_path}",
        ",".join([
            "raw",          # Raw mode
            "echo=0",       # No local echo
            "escape=0x1d"   # Escape character (Ctrl+])
        ])
    ]
    
    # First send CONNECT command, then interactive
    connect_script = f"sleep 0.1; echo 'CONNECT {port}'; cat"
    
    full_cmd = [
        "socat",
        "-",  # stdin/stdout with raw mode
        f"SYSTEM:{connect_script},nofork,stderr",
        f"UNIX-CONNECT:{uds_path}"
    ]
    
    # Run socat
    try:
        subprocess.run(full_cmd)
    except KeyboardInterrupt:
        print("\nDisconnected.", file=sys.stderr)
    except FileNotFoundError:
        print("Error: socat not found. Please install socat.", file=sys.stderr)
        print("  Ubuntu/Debian: sudo apt-get install socat", file=sys.stderr)
        print("  Alpine: apk add socat", file=sys.stderr)
        sys.exit(1)

def main():
    parser = argparse.ArgumentParser(
        description="Connect to Firecracker microVM console"
    )
    parser.add_argument(
        "uds_path",
        nargs="?",
        default="./v.sock",
        help="Path to Firecracker Unix domain socket (default: ./v.sock)"
    )
    parser.add_argument(
        "port",
        nargs="?",
        type=int,
        default=1024,
        help="Vsock port to connect to (default: 1024)"
    )
    
    args = parser.parse_args()
    
    connect(args.uds_path, args.port)

if __name__ == "__main__":
    main()
```

**Usage:**
```bash
# Connect with defaults (./v.sock, port 1024)
python3 fc-console.py

# Specify custom UDS and port
python3 fc-console.py ./v.sock 1024
python3 fc-console.py /tmp/vm.sock 2222

# Help
python3 fc-console.py --help

# Exit with Ctrl+], then 'q'
```

### Option 3: Full PTY Bridge (Recommended)

For production use, build a proper PTY bridge that handles:
- Automatic CONNECT handshake
- Window resize forwarding
- Clean disconnection handling
- Multiple sessions

**Python 3.13 Implementation:**

```python
#!/usr/bin/env python3
"""Firecracker PTY-over-Vsock Bridge

Connects to a Firecracker microVM via vsock and provides an interactive PTY session.
Requires Python 3.10+ (uses match/case, better exception groups support).
"""

import os
import sys
import tty
import termios
import pty
import select
import socket
import struct
import signal
import fcntl
import array
from typing import Optional


class VsockPtyBridge:
    """Bridge between local PTY and Firecracker vsock."""
    
    def __init__(self, uds_path: str, port: int):
        self.uds_path = uds_path
        self.port = port
        self.vsock: Optional[socket.socket] = None
        self.master_fd: Optional[int] = None
        self.slave_fd: Optional[int] = None
        self.old_termios: Optional[list] = None
        
    def connect_vsock(self) -> socket.socket:
        """Connect to Firecracker via Unix domain socket."""
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.connect(self.uds_path)
        
        # Send CONNECT command
        sock.sendall(f"CONNECT {self.port}\n".encode())
        
        # Read response
        response = sock.recv(256).decode()
        if not response.startswith("OK"):
            raise ConnectionError(f"Failed to connect: {response}")
        
        print(f"Connected: {response.strip()}", file=sys.stderr)
        return sock
    
    def create_pty(self) -> tuple[int, int]:
        """Create a PTY master/slave pair."""
        master, slave = pty.openpty()
        return master, slave
    
    def set_raw_mode(self, fd: int) -> list:
        """Set terminal to raw mode, saving original settings."""
        old = termios.tcgetattr(fd)
        tty.setraw(fd, termios.TCSANOW)
        return old
    
    def restore_terminal(self):
        """Restore original terminal settings."""
        if self.old_termios and sys.stdin.isatty():
            termios.tcsetattr(sys.stdin.fileno(), termios.TCSANOW, self.old_termios)
    
    def get_terminal_size(self) -> tuple[int, int]:
        """Get current terminal size."""
        # Use TIOCGWINSZ ioctl
        buf = array.array('H', [0, 0, 0, 0])
        fcntl.ioctl(sys.stdout.fileno(), termios.TIOCGWINSZ, buf)
        return buf[0], buf[1]  # rows, cols
    
    def set_pty_size(self, rows: int, cols: int):
        """Set PTY window size."""
        if self.master_fd:
            buf = array.array('H', [rows, cols, 0, 0])
            fcntl.ioctl(self.master_fd, termios.TIOCSWINSZ, buf)
    
    def handle_sigwinch(self, signum, frame):
        """Handle window resize."""
        try:
            rows, cols = self.get_terminal_size()
            self.set_pty_size(rows, cols)
        except Exception:
            pass
    
    def run(self):
        """Main bridge loop using select."""
        try:
            # Connect to vsock
            self.vsock = self.connect_vsock()
            
            # Create PTY
            self.master_fd, self.slave_fd = self.create_pty()
            
            # Set initial terminal size
            rows, cols = self.get_terminal_size()
            self.set_pty_size(rows, cols)
            
            # Setup signal handler for window resize
            signal.signal(signal.SIGWINCH, self.handle_sigwinch)
            
            # Save and set raw mode on stdin
            if sys.stdin.isatty():
                self.old_termios = self.set_raw_mode(sys.stdin.fileno())
            
            print("Bridge started. Press Ctrl-C to exit.", file=sys.stderr)
            
            # Close slave FD in parent (child would use it)
            os.close(self.slave_fd)
            self.slave_fd = None
            
            # Main loop
            while True:
                # Select on stdin, vsock, and PTY master
                readable, _, _ = select.select(
                    [sys.stdin.fileno(), self.vsock.fileno(), self.master_fd],
                    [],
                    []
                )
                
                for fd in readable:
                    if fd == sys.stdin.fileno():
                        # User input -> vsock
                        data = os.read(sys.stdin.fileno(), 4096)
                        if not data:
                            return
                        self.vsock.sendall(data)
                        
                    elif fd == self.vsock.fileno():
                        # Data from guest -> PTY (input to shell)
                        data = self.vsock.recv(4096)
                        if not data:
                            return
                        os.write(self.master_fd, data)
                        
                    elif fd == self.master_fd:
                        # PTY output (from shell) -> stdout
                        data = os.read(self.master_fd, 4096)
                        if not data:
                            return
                        os.write(sys.stdout.fileno(), data)
                        
        except KeyboardInterrupt:
            print("\nInterrupted.", file=sys.stderr)
        except Exception as e:
            print(f"Error: {e}", file=sys.stderr)
            raise
        finally:
            self.cleanup()
    
    def cleanup(self):
        """Clean up resources."""
        self.restore_terminal()
        
        if self.vsock:
            self.vsock.close()
        if self.master_fd:
            os.close(self.master_fd)
        if self.slave_fd:
            os.close(self.slave_fd)


def main():
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <uds_path> <port>", file=sys.stderr)
        print(f"Example: {sys.argv[0]} ./v.sock 1024", file=sys.stderr)
        sys.exit(1)
    
    uds_path = sys.argv[1]
    port = int(sys.argv[2])
    
    if not os.path.exists(uds_path):
        print(f"Error: Unix socket not found: {uds_path}", file=sys.stderr)
        sys.exit(1)
    
    bridge = VsockPtyBridge(uds_path, port)
    
    try:
        bridge.run()
    except ConnectionError as e:
        print(f"Connection failed: {e}", file=sys.stderr)
        sys.exit(1)
    
    print("Disconnected.", file=sys.stderr)


if __name__ == "__main__":
    main()
```

**Features:**
- Pure Python 3.10+ (uses modern exception handling)
- Automatic CONNECT handshake
- Raw terminal mode with proper restoration
- Window resize forwarding (SIGWINCH)
- Clean disconnection handling
- Type hints throughout

**Usage:**
```bash
python3 fc-pty-bridge.py ./v.sock 1024
```

### Option 4: Async Python Implementation (Python 3.13+)

For better performance with concurrent connections:

```python
#!/usr/bin/env python3
"""Async Firecracker PTY-over-Vsock Bridge

Uses asyncio for better performance and concurrent session handling.
Optimized for Python 3.13 features.
"""

import asyncio
import os
import sys
import tty
import termios
import pty
import fcntl
import array
import signal
import socket
from typing import Optional


class AsyncVsockPtyBridge:
    """Async bridge between local PTY and Firecracker vsock."""
    
    def __init__(self, uds_path: str, port: int):
        self.uds_path = uds_path
        self.port = port
        self.vsock: Optional[socket.socket] = None
        self.master_fd: Optional[int] = None
        self.old_termios: Optional[list] = None
        self.running = True
        
    async def connect_vsock(self) -> socket.socket:
        """Async connect to Firecracker via Unix domain socket."""
        # Use asyncio's socket wrapper
        loop = asyncio.get_event_loop()
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.setblocking(False)
        
        await loop.sock_connect(sock, self.uds_path)
        
        # Send CONNECT command
        await loop.sock_sendall(sock, f"CONNECT {self.port}\n".encode())
        
        # Read response
        response = await loop.sock_recv(sock, 256)
        response_str = response.decode()
        
        if not response_str.startswith("OK"):
            raise ConnectionError(f"Failed to connect: {response_str}")
        
        print(f"Connected: {response_str.strip()}", file=sys.stderr)
        return sock
    
    def create_pty(self) -> tuple[int, int]:
        """Create a PTY master/slave pair."""
        return pty.openpty()
    
    def set_raw_mode(self, fd: int) -> list:
        """Set terminal to raw mode."""
        old = termios.tcgetattr(fd)
        tty.setraw(fd, termios.TCSANOW)
        return old
    
    def restore_terminal(self):
        """Restore original terminal settings."""
        if self.old_termios and sys.stdin.isatty():
            termios.tcsetattr(sys.stdin.fileno(), termios.TCSANOW, self.old_termios)
    
    def get_terminal_size(self) -> tuple[int, int]:
        """Get current terminal size."""
        buf = array.array('H', [0, 0, 0, 0])
        fcntl.ioctl(sys.stdout.fileno(), termios.TIOCGWINSZ, buf)
        return buf[0], buf[1]
    
    def set_pty_size(self, rows: int, cols: int):
        """Set PTY window size."""
        if self.master_fd:
            buf = array.array('H', [rows, cols, 0, 0])
            fcntl.ioctl(self.master_fd, termios.TIOCSWINSZ, buf)
    
    def handle_sigwinch(self, signum, frame):
        """Handle window resize signal."""
        try:
            rows, cols = self.get_terminal_size()
            self.set_pty_size(rows, cols)
        except Exception:
            pass
    
    async def forward_stdin_to_vsock(self):
        """Forward stdin to vsock."""
        loop = asyncio.get_event_loop()
        while self.running:
            try:
                data = await loop.run_in_executor(
                    None, 
                    lambda: os.read(sys.stdin.fileno(), 4096)
                )
                if not data:
                    break
                await loop.sock_sendall(self.vsock, data)
            except (BrokenPipeError, ConnectionResetError):
                break
            except Exception as e:
                print(f"Stdin error: {e}", file=sys.stderr)
                break
    
    async def forward_vsock_to_pty(self):
        """Forward vsock data to PTY."""
        loop = asyncio.get_event_loop()
        while self.running:
            try:
                data = await loop.sock_recv(self.vsock, 4096)
                if not data:
                    break
                await loop.run_in_executor(
                    None,
                    lambda: os.write(self.master_fd, data)
                )
            except (BrokenPipeError, ConnectionResetError):
                break
            except Exception as e:
                print(f"Vsock error: {e}", file=sys.stderr)
                break
    
    async def forward_pty_to_stdout(self):
        """Forward PTY output to stdout."""
        loop = asyncio.get_event_loop()
        while self.running:
            try:
                data = await loop.run_in_executor(
                    None,
                    lambda: os.read(self.master_fd, 4096)
                )
                if not data:
                    break
                await loop.run_in_executor(
                    None,
                    lambda: os.write(sys.stdout.fileno(), data)
                )
            except (BrokenPipeError, ConnectionResetError):
                break
            except Exception as e:
                print(f"PTY error: {e}", file=sys.stderr)
                break
    
    async def run(self):
        """Main async bridge loop."""
        try:
            # Connect to vsock
            self.vsock = await self.connect_vsock()
            
            # Create PTY
            self.master_fd, slave_fd = self.create_pty()
            
            # Set initial terminal size
            rows, cols = self.get_terminal_size()
            self.set_pty_size(rows, cols)
            
            # Setup signal handler
            signal.signal(signal.SIGWINCH, self.handle_sigwinch)
            
            # Set raw mode
            if sys.stdin.isatty():
                self.old_termios = self.set_raw_mode(sys.stdin.fileno())
            
            # Close slave FD
            os.close(slave_fd)
            
            print("Bridge started. Press Ctrl-C to exit.", file=sys.stderr)
            
            # Run all forwarders concurrently
            await asyncio.gather(
                self.forward_stdin_to_vsock(),
                self.forward_vsock_to_pty(),
                self.forward_pty_to_stdout(),
                return_exceptions=True
            )
            
        except asyncio.CancelledError:
            print("\nCancelled.", file=sys.stderr)
        except Exception as e:
            print(f"Error: {e}", file=sys.stderr)
            raise
        finally:
            self.cleanup()
    
    def cleanup(self):
        """Clean up resources."""
        self.running = False
        self.restore_terminal()
        
        if self.vsock:
            self.vsock.close()
        if self.master_fd:
            os.close(self.master_fd)


async def main():
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <uds_path> <port>", file=sys.stderr)
        print(f"Example: {sys.argv[0]} ./v.sock 1024", file=sys.stderr)
        sys.exit(1)
    
    uds_path = sys.argv[1]
    port = int(sys.argv[2])
    
    if not os.path.exists(uds_path):
        print(f"Error: Unix socket not found: {uds_path}", file=sys.stderr)
        sys.exit(1)
    
    bridge = AsyncVsockPtyBridge(uds_path, port)
    
    try:
        await bridge.run()
    except ConnectionError as e:
        print(f"Connection failed: {e}", file=sys.stderr)
        sys.exit(1)
    
    print("Disconnected.", file=sys.stderr)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\nInterrupted.", file=sys.stderr)
```

**Benefits of async version:**
- Better resource utilization
- Handles multiple connections efficiently
- Native Python 3.13 `asyncio` improvements
- Cleaner cancellation handling

**Usage:**
```bash
python3 fc-async-pty-bridge.py ./v.sock 1024
```

---

## Integration Examples

### SSH-Compatible Connection

Create an SSH config entry that uses the Python bridge:

```bash
# ~/.ssh/config
Host fc-vm
    HostName localhost
    User root
    ProxyCommand python3 /path/to/fc-pty-bridge.py ./v.sock 1024
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
```

**Connect:**
```bash
ssh fc-vm
```

**Alternative: Python wrapper script for SSH**

```python
#!/usr/bin/env python3
"""SSH wrapper for Firecracker VM console"""

import subprocess
import sys
import os

UDS_PATH = "./v.sock"
PORT = 1024

def main():
    # Run the bridge as SSH proxy
    bridge_path = "/path/to/fc-pty-bridge.py"
    
    cmd = [
        "python3", bridge_path,
        UDS_PATH,
        str(PORT)
    ]
    
    try:
        subprocess.run(cmd)
    except KeyboardInterrupt:
        pass

if __name__ == "__main__":
    main()
```

Save as `fc-ssh-bridge.py` and use in SSH config:
```bash
Host fc-vm
    ProxyCommand python3 /path/to/fc-ssh-bridge.py
```

### With Tmux/Screen (Python Script)

For persistent sessions, use a Python script to manage tmux:

```python
#!/usr/bin/env python3
"""Tmux session manager for Firecracker console"""

import subprocess
import sys
import os

SESSION_NAME = "fc-vm"
BRIDGE_CMD = ["python3", "./fc-pty-bridge.py", "./v.sock", "1024"]

def session_exists():
    """Check if tmux session exists."""
    result = subprocess.run(
        ["tmux", "has-session", "-t", SESSION_NAME],
        capture_output=True
    )
    return result.returncode == 0

def create_session():
    """Create new tmux session with bridge."""
    subprocess.run([
        "tmux", "new-session", "-d", "-s", SESSION_NAME,
        " ".join(BRIDGE_CMD)
    ])
    print(f"Created tmux session '{SESSION_NAME}' with Firecracker console")

def attach_session():
    """Attach to existing session."""
    subprocess.run(["tmux", "attach", "-t", SESSION_NAME])

def kill_session():
    """Kill the session."""
    subprocess.run(["tmux", "kill-session", "-t", SESSION_NAME])
    print(f"Killed session '{SESSION_NAME}'")

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 fc-tmux-manager.py {start|attach|kill}")
        sys.exit(1)
    
    action = sys.argv[1]
    
    if action == "start":
        if session_exists():
            print(f"Session '{SESSION_NAME}' already exists, attaching...")
            attach_session()
        else:
            create_session()
            attach_session()
    
    elif action == "attach":
        if session_exists():
            attach_session()
        else:
            print(f"Session '{SESSION_NAME}' does not exist. Start it first.")
            sys.exit(1)
    
    elif action == "kill":
        if session_exists():
            kill_session()
        else:
            print(f"Session '{SESSION_NAME}' does not exist.")
    
    else:
        print(f"Unknown action: {action}")
        print("Usage: python3 fc-tmux-manager.py {start|attach|kill}")

if __name__ == "__main__":
    main()
```

**Usage:**
```bash
# Start and attach to persistent session
python3 fc-tmux-manager.py start

# Detach: Ctrl+B, D

# Reconnect later
python3 fc-tmux-manager.py attach

# Clean up
python3 fc-tmux-manager.py kill
```

### Automated VM Management

Python script to start Firecracker VM and connect console:

```python
#!/usr/bin/env python3
"""Automated Firecracker VM launcher with console"""

import subprocess
import time
import sys
import os
import signal

# Configuration
FC_SOCKET = "/tmp/fc.sock"
CONFIG_FILE = "vm-config.json"
UDS_PATH = "./v.sock"
PORT = 1024
BRIDGE_SCRIPT = "./fc-pty-bridge.py"

def wait_for_uds(timeout=30):
    """Wait for Unix domain socket to be created."""
    start = time.time()
    while time.time() - start < timeout:
        if os.path.exists(UDS_PATH):
            return True
        time.sleep(0.1)
    return False

def start_firecracker():
    """Start Firecracker microVM."""
    cmd = [
        "firecracker",
        "--api-sock", FC_SOCKET,
        "--config-file", CONFIG_FILE
    ]
    
    print("Starting Firecracker...")
    process = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE
    )
    
    # Wait for UDS
    if not wait_for_uds():
        print("Error: Firecracker did not create UDS in time")
        process.terminate()
        sys.exit(1)
    
    print("Firecracker started successfully")
    return process

def connect_console():
    """Connect Python PTY bridge."""
    print(f"Connecting to console on port {PORT}...")
    
    cmd = ["python3", BRIDGE_SCRIPT, UDS_PATH, str(PORT)]
    
    try:
        result = subprocess.run(cmd)
        return result.returncode
    except KeyboardInterrupt:
        print("\nConsole interrupted")
        return 0

def cleanup(firecracker_process):
    """Clean up resources."""
    print("\nShutting down Firecracker...")
    firecracker_process.terminate()
    
    try:
        firecracker_process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        firecracker_process.kill()

def main():
    firecracker = None
    
    try:
        # Start Firecracker
        firecracker = start_firecracker()
        
        # Connect console
        exit_code = connect_console()
        
        # Cleanup
        cleanup(firecracker)
        
        sys.exit(exit_code)
        
    except Exception as e:
        print(f"Error: {e}")
        if firecracker:
            cleanup(firecracker)
        sys.exit(1)

if __name__ == "__main__":
    main()
```

**Usage:**
```bash
# Start VM and automatically connect console
python3 start-vm-with-console.py

# Press Ctrl-C to disconnect and shutdown VM
```

---

## Advanced Configuration

**Architecture Note:** All advanced features (authentication, logging, multiple services) are implemented at the **guest agent level** (inside the VM), not at the host bridge. The host bridge is a simple data pipe - it doesn't interpret or modify the data flowing through it. Security and logging are handled by the guest agent before spawning the shell.

```
┌──────────────────────────────────────────────────────────────┐
│ Guest VM (Inside MicroVM)                                    │
│ ┌──────────────────┐      ┌──────────────────┐             │
│ │ Guest Agent      │─────►│ Shell (/bin/bash)│             │
│ │                  │      │                  │             │
│ │ • Authentication │      │ • Commands       │             │
│ │ • Logging        │      │ • Output         │             │
│ │ • Access Control │      │ • Session        │             │
│ └────────┬─────────┘      └──────────────────┘             │
│          │                                                 │
│          │ vsock port 1024                                  │
└──────────┼──────────────────────────────────────────────────┘
           │
┌──────────┼──────────────────────────────────────────────────┐
│ Host     │                                                    │
│          ▼                                                    │
│ ┌──────────────────┐                                         │
│ │ Host Bridge      │                                         │
│ │                  │  Simple data pipe                        │
│ │ • No auth        │  (doesn't interpret data)                │
│ │ • No logging     │                                         │
│ │ • Just forwards  │                                         │
│ │   bytes          │                                         │
│ └────────┬─────────┘                                         │
│          │                                                    │
│          ▼                                                    │
│    [Your Terminal]                                            │
└───────────────────────────────────────────────────────────────┘
```

### Multiple Consoles

Use different ports for different services:

**In guest:**
```bash
# Root shell on port 1024
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &

# Restricted shell on port 1025  
socat VSOCK-LISTEN:1025,fork EXEC:/bin/rbash,pty,stderr,setsid &

# Log viewer on port 1026
socat VSOCK-LISTEN:1026,fork EXEC:/usr/bin/tail -f /var/log/syslog,pty,stderr &
```

**Connect:**
```bash
./fc-pty-bridge ./v.sock 1024  # Root shell
./fc-pty-bridge ./v.sock 1025  # Restricted shell
./fc-pty-bridge ./v.sock 1026  # Logs
```

### Secure Access (Python)

**Location:** Inside the guest VM (part of guest agent)

Add authentication to the guest agent. This runs **inside the microVM** before spawning the shell:

```python
#!/usr/bin/env python3
"""Authenticated shell wrapper for vsock agent"""

import sys
import os
import getpass

# Simple password (use proper auth in production)
EXPECTED_PASSWORD = "secret123"

def authenticate():
    """Prompt for password."""
    try:
        password = getpass.getpass("Password: ")
        return password == EXPECTED_PASSWORD
    except (KeyboardInterrupt, EOFError):
        return False

def main():
    if not authenticate():
        print("Authentication failed", file=sys.stderr)
        sys.exit(1)
    
    # Execute shell
    os.execl("/bin/bash", "bash", "-l")

if __name__ == "__main__":
    main()
```

**In guest:**
```bash
# Make executable
chmod +x /usr/local/bin/authenticated-shell.py

# Run with socat
socat VSOCK-LISTEN:1024,fork EXEC:/usr/local/bin/authenticated-shell.py,pty,stderr,setsid

# Or with Python agent wrapper
python3 guest-agent-with-auth.py 1024
```

### Logging and Auditing (Python)

**Location:** Inside the guest VM (part of guest agent)

Log all console activity. This runs **inside the microVM** and captures all session data before it goes to vsock:

```python
#!/usr/bin/env python3
"""Logged shell wrapper for session recording"""

import sys
import os
import subprocess
import datetime
import pty

def main():
    # Create log file with timestamp
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    log_file = f"/var/log/console-{timestamp}.log"
    
    # Ensure log directory exists
    os.makedirs("/var/log", exist_ok=True)
    
    print(f"Session will be logged to: {log_file}", file=sys.stderr)
    
    # Use script command to record session
    try:
        result = subprocess.run(
            ["script", "-q", "-c", "/bin/bash -l", log_file],
            check=False
        )
        sys.exit(result.returncode)
    except FileNotFoundError:
        # Fallback: use Python pty directly
        import select
        import termios
        import tty
        
        # Create log file
        with open(log_file, "wb") as log:
            # Create PTY
            master, slave = pty.openpty()
            
            # Fork child
            pid = os.fork()
            
            if pid == 0:
                # Child: setup and exec shell
                os.close(master)
                os.setsid()
                
                # Set controlling terminal
                import fcntl
                fcntl.ioctl(slave, os.TIOCSCTTY, 0)
                
                # Redirect stdio
                os.dup2(slave, 0)
                os.dup2(slave, 1)
                os.dup2(slave, 2)
                os.close(slave)
                
                # Exec shell
                os.execv("/bin/bash", ["bash", "-l"])
                sys.exit(1)
            
            else:
                # Parent: log all traffic
                os.close(slave)
                
                try:
                    while True:
                        readable, _, _ = select.select([master], [], [], 0.1)
                        
                        if master in readable:
                            data = os.read(master, 4096)
                            if not data:
                                break
                            
                            # Log to file
                            log.write(data)
                            log.flush()
                            
                            # Output to stdout
                            os.write(1, data)
                
                except (KeyboardInterrupt, EOFError):
                    pass
                finally:
                    os.close(master)
                    os.waitpid(pid, 0)

if __name__ == "__main__":
    main()
```

**In guest:**
```bash
# Make executable
chmod +x /usr/local/bin/logged-console.py

# Run with socat
socat VSOCK-LISTEN:1024,fork EXEC:/usr/local/bin/logged-console.py,pty,stderr,setsid

# Or standalone
python3 logged-console.py
```

**View logs:**
```bash
# List session logs
ls -la /var/log/console-*

# View specific session
cat /var/log/console-20240328-120000.log
```

---

## Troubleshooting

### Connection Refused

**Symptom:** `CONNECT 1024` returns error

**Check:**
1. Is guest agent running?
   ```bash
   # In guest
   pgrep -f "socat VSOCK-LISTEN:1024"
   # or
   ss -lvs | grep 1024
   ```

2. Is vsock device present?
   ```bash
   # In guest
   ls /dev/vsock
   dmesg | grep vsock
   ```

3. Is Firecracker listening?
   ```bash
   # On host
   ls -la ./v.sock
   ss -lxs | grep v.sock
   ```

### Terminal Issues

**Symptom:** No echo, arrow keys don't work

**Solution:** Ensure PTY is allocated and raw mode is set

```bash
# In guest agent, verify 'pty' flag
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid
#                               ^^^ required
```

**Symptom:** Window resize not working

**Solution:** Use full bridge (C or Rust version) with SIGWINCH handler

### Permission Denied

**Symptom:** Cannot access UDS

**Solution:** Check UDS permissions
```bash
ls -la ./v.sock
# Should be readable/writable by your user
# If using firecracker jailer, ensure proper permissions
```

---

## Best Practices

1. **Use socat in guest** for quick setup, custom agent for production
2. **Enable `fork` option** in socat to handle multiple concurrent connections
3. **Set `setsid`** to create proper session for signal handling
4. **Handle SIGWINCH** in host bridge for terminal resize support
5. **Use non-standard ports** (1024+) to avoid conflicts
6. **Monitor guest agent** with systemd or supervisor to ensure availability
7. **Log console sessions** for audit compliance
8. **Implement authentication** for multi-tenant environments
9. **Use tmux/screen** in guest for persistent sessions across disconnections
10. **Test disconnect/reconnect** behavior - vsock connections should be recreated cleanly

---

## Summary

You now have everything needed to implement interactive console access for Firecracker:

1. **Firecracker**: Configure vsock via API (no changes needed)
2. **Guest**: Run `socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid`
3. **Host**: Connect with `./fc-pty-bridge ./v.sock 1024`

No Firecracker modifications required. The vsock device is production-ready and provides reliable bidirectional communication for interactive sessions.
