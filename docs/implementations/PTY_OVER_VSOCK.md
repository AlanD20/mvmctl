> **STATUS: Current — general reference guide.** This is a Firecracker vsock/PTY tutorial, not a codebase-specific document. The patterns described match how mvmctl's console relay works conceptually, but note the implementation difference below. No changes needed.
>
> **Implementation note:** While this document describes vsock-based communication between guest and host, mvmctl's actual console relay (`mvm run console relay`) uses **PTY passthrough via `--pty-fd 3`** — the host process creates a PTY pair and passes the slave file descriptor to Firecracker's serial console. It does NOT use vsock devices for console communication. The general patterns of PTY allocation, raw terminal mode, and I/O bridging are conceptually similar regardless of transport.
>
> **Last verified:** 2026-06-10

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
│ │ Host Bridge (Go) │          │  │ Kernel│◄──────┘     │      ││
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
```

**Use case:** You might use the serial console to start the agent, then switch to vsock for better terminal experience.

### Option 3: Start via Existing Serial Console

```bash
# 1. Boot VM with serial console
# 2. Log in via serial
# 3. Start agent manually:
root@guest:~# socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &

# 4. Now connect from host via vsock:
# (On host) ./fc-pty-bridge ./v.sock 1024
```

**This is the "upgrade" pattern:** Use serial to bootstrap vsock, then use vsock for the real work.

---

### Quick Setup with Socat

**How the port works:** The guest agent and host bridge must agree on the same port number. The port is chosen by **you** - it's not configured in Firecracker, it's just a runtime agreement between guest and host applications.

```
Guest Agent (inside VM)          Host Bridge (on host)
    |                                   |
    | socat VSOCK-LISTEN:1024           | ./fc-pty-bridge ./v.sock 1024
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
./fc-pty-bridge ./v.sock 1024

# Guest listens on 2222 (like SSH), host connects to 2222
socat VSOCK-LISTEN:2222,fork EXEC:/bin/bash,pty,stderr,setsid &
./fc-pty-bridge ./v.sock 2222

# Different services on different ports
socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid &      # Shell
socat VSOCK-LISTEN:1025,fork EXEC:/usr/bin/tail -f /var/log/syslog &   # Logs
socat VSOCK-LISTEN:1026,fork EXEC:/usr/bin/top,pty,stderr,setsid &    # Top

# Connect to different services
./fc-pty-bridge ./v.sock 1024  # Get shell
./fc-pty-bridge ./v.sock 1025  # View logs
./fc-pty-bridge ./v.sock 1026  # Run top
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

---

## Host Bridge Implementation

The host bridge connects your terminal to the Firecracker vsock. Choose your implementation based on needs:

> **Port Coordination Reminder:** 
> The host bridge must connect to the **same port** that the guest agent is listening on. 
> If guest listens on port 1024, host connects to port 1024. If guest uses 2222, host uses 2222.
> 
> ```
> Guest: socat VSOCK-LISTEN:1024 ←→ Host: ./fc-pty-bridge ./v.sock 1024
> Guest: socat VSOCK-LISTEN:2222 ←→ Host: ./fc-pty-bridge ./v.sock 2222
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

### Option 3: Full PTY Bridge (Recommended)

For production use, build a proper PTY bridge that handles:
- Automatic CONNECT handshake
- Window resize forwarding
- Clean disconnection handling
- Multiple sessions

**Go Implementation:**

```go
package main

import (
    "fmt"
    "net"
    "os"
    "os/signal"
    "syscall"

    "github.com/creack/pty"
    "golang.org/x/term"
)

type VsockPtyBridge struct {
    udsPath string
    port    int
    conn    net.Conn
    ptmx    *os.File
    oldState *term.State
}

func NewVsockPtyBridge(udsPath string, port int) *VsockPtyBridge {
    return &VsockPtyBridge{
        udsPath: udsPath,
        port:    port,
    }
}

func (b *VsockPtyBridge) Connect() error {
    conn, err := net.Dial("unix", b.udsPath)
    if err != nil {
        return fmt.Errorf("failed to connect to UDS: %w", err)
    }
    b.conn = conn

    // Send CONNECT command
    _, err = fmt.Fprintf(conn, "CONNECT %d\n", b.port)
    if err != nil {
        return fmt.Errorf("failed to send CONNECT: %w", err)
    }

    // Read response
    buf := make([]byte, 256)
    n, err := conn.Read(buf)
    if err != nil {
        return fmt.Errorf("failed to read response: %w", err)
    }

    response := string(buf[:n])
    if response[:2] != "OK" {
        return fmt.Errorf("connection failed: %s", response)
    }

    fmt.Fprintf(os.Stderr, "Connected: %s\n", response)
    return nil
}

func (b *VsockPtyBridge) CreatePTY() error {
    ptmx, err := pty.Start(nil)
    if err != nil {
        return fmt.Errorf("failed to create PTY: %w", err)
    }
    b.ptmx = ptmx
    return nil
}

func (b *VsockPtyBridge) SetRawMode() error {
    oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
    if err != nil {
        return fmt.Errorf("failed to set raw mode: %w", err)
    }
    b.oldState = oldState
    return nil
}

func (b *VsockPtyBridge) RestoreTerminal() {
    if b.oldState != nil {
        term.Restore(int(os.Stdin.Fd()), b.oldState)
    }
}

func (b *VsockPtyBridge) HandleResize() {
    sigwinch := make(chan os.Signal, 1)
    signal.Notify(sigwinch, syscall.SIGWINCH)

    go func() {
        for range sigwinch {
            rows, cols, err := term.GetSize(int(os.Stdout.Fd()))
            if err != nil {
                continue
            }
            pty.Setsize(b.ptmx, &pty.Winsize{
                Rows: uint16(rows),
                Cols: uint16(cols),
            })
        }
    }()
}

func (b *VsockPtyBridge) Run() error {
    if err := b.Connect(); err != nil {
        return err
    }
    defer b.conn.Close()

    if err := b.CreatePTY(); err != nil {
        return err
    }
    defer b.ptmx.Close()

    if err := b.SetRawMode(); err != nil {
        return err
    }
    defer b.RestoreTerminal()

    b.HandleResize()

    fmt.Fprintln(os.Stderr, "Bridge started. Press Ctrl-C to exit.")

    // Bridge I/O
    done := make(chan struct{})

    // stdin -> PTY -> conn
    go func() {
        buf := make([]byte, 4096)
        for {
            n, err := os.Stdin.Read(buf)
            if err != nil {
                break
            }
            b.conn.Write(buf[:n])
        }
        close(done)
    }()

    // conn -> PTY -> stdout
    go func() {
        buf := make([]byte, 4096)
        for {
            n, err := b.conn.Read(buf)
            if err != nil {
                break
            }
            os.Stdout.Write(buf[:n])
        }
        close(done)
    }()

    <-done
    return nil
}

func main() {
    if len(os.Args) != 3 {
        fmt.Fprintf(os.Stderr, "Usage: %s <uds_path> <port>\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "Example: %s ./v.sock 1024\n", os.Args[0])
        os.Exit(1)
    }

    udsPath := os.Args[1]
    port := os.Args[2]

    if _, err := os.Stat(udsPath); os.IsNotExist(err) {
        fmt.Fprintf(os.Stderr, "Error: Unix socket not found: %s\n", udsPath)
        os.Exit(1)
    }

    bridge := NewVsockPtyBridge(udsPath, port)
    if err := bridge.Run(); err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }

    fmt.Fprintln(os.Stderr, "Disconnected.")
}
```

**Features:**
- Pure Go (uses `github.com/creack/pty` and `golang.org/x/term`)
- Automatic CONNECT handshake
- Raw terminal mode with proper restoration
- Window resize forwarding (SIGWINCH)
- Clean disconnection handling

**Usage:**
```bash
go build -o fc-pty-bridge
./fc-pty-bridge ./v.sock 1024
```

---

## Integration Examples

### SSH-Compatible Connection

Create an SSH config entry that uses the Go bridge:

```bash
# ~/.ssh/config
Host fc-vm
    HostName localhost
    User root
    ProxyCommand /path/to/fc-pty-bridge ./v.sock 1024
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
```

**Connect:**
```bash
ssh fc-vm
```

### With Tmux/Screen

For persistent sessions:

```bash
# Start and attach to persistent session
tmux new-session -d -s fc-vm "./fc-pty-bridge ./v.sock 1024"
tmux attach -t fc-vm

# Detach: Ctrl+B, D

# Reconnect later
tmux attach -t fc-vm

# Clean up
tmux kill-session -t fc-vm
```

### Automated VM Management

Go script to start Firecracker VM and connect console:

```go
package main

import (
    "fmt"
    "os"
    "os/exec"
    "time"
)

const (
    FCSocket    = "/tmp/fc.sock"
    ConfigFile  = "vm-config.json"
    UDSPath     = "./v.sock"
    Port        = "1024"
    BridgePath  = "./fc-pty-bridge"
)

func waitForUDS(timeout time.Duration) bool {
    start := time.Now()
    for time.Since(start) < timeout {
        if _, err := os.Stat(UDSPath); err == nil {
            return true
        }
        time.Sleep(100 * time.Millisecond)
    }
    return false
}

func main() {
    // Start Firecracker
    fcCmd := exec.Command("firecracker", "--api-sock", FCSocket, "--config-file", ConfigFile)
    fcCmd.Stdout = os.Stdout
    fcCmd.Stderr = os.Stderr

    fmt.Println("Starting Firecracker...")
    if err := fcCmd.Start(); err != nil {
        fmt.Fprintf(os.Stderr, "Failed to start Firecracker: %v\n", err)
        os.Exit(1)
    }

    // Wait for UDS
    if !waitForUDS(30 * time.Second) {
        fmt.Fprintln(os.Stderr, "Error: Firecracker did not create UDS in time")
        fcCmd.Process.Kill()
        os.Exit(1)
    }

    fmt.Println("Firecracker started successfully")

    // Connect console
    bridgeCmd := exec.Command(BridgePath, UDSPath, Port)
    bridgeCmd.Stdin = os.Stdin
    bridgeCmd.Stdout = os.Stdout
    bridgeCmd.Stderr = os.Stderr

    fmt.Printf("Connecting to console on port %s...\n", Port)
    if err := bridgeCmd.Run(); err != nil {
        fmt.Fprintf(os.Stderr, "Console error: %v\n", err)
    }

    // Cleanup
    fmt.Println("\nShutting down Firecracker...")
    fcCmd.Process.Signal(os.Interrupt)
    fcCmd.Wait()
}
```

**Usage:**
```bash
go build -o start-vm-with-console
./start-vm-with-console

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

---

## Troubleshooting

### Connection Refused

**Symptom:** `CONNECT 1024` returns error

**Check:**
1. Is the guest agent running?
   ```bash
   # In guest (via serial console)
   ps aux | grep socat
   ```

2. Is the port correct?
   ```bash
   # In guest
   ss -l | grep vsock
   ```

3. Is the UDS path correct?
   ```bash
   # On host
   ls -la ./v.sock
   ```

### No Data Flow

**Symptom:** Connected but no output

**Check:**
1. Is the guest agent spawning a shell?
   ```bash
   # In guest
   ps aux | grep bash
   ```

2. Is the PTY allocated correctly?
   ```bash
   # In guest
   ls -la /dev/pts/
   ```

### Terminal Not Responsive

**Symptom:** Can type but no echo, no line editing

**Check:**
1. Is raw mode set correctly on the host bridge?
2. Is the guest shell expecting a PTY?
   ```bash
   # In guest agent script
   socat VSOCK-LISTEN:1024,fork EXEC:/bin/bash,pty,stderr,setsid
   #                                      ^^^  ^^^^^  ^^^^^^^
   #                                      PTY  stderr  new session
   ```

---

## mvmctl Implementation Note

**mvmctl does NOT use vsock for console.** It uses PTY passthrough:

1. **PTY creation** (`Controller.CreatePTY`): Opens `/dev/ptmx`, gets the slave PTY number via `TIOCGPTN`, unlocks via `TIOCSPTLCK`, opens the slave at `/dev/pts/<N>`. Returns the slave FD (clientFD). The master FD stays in the Controller.

2. **Firecracker wiring** (`internal/core/vm/firecracker.go`): The slave/client PTY FD is passed as both `Stdin` and `Stdout` to the Firecracker process. This means Firecracker's serial console (ttyS0) reads/writes directly through the PTY pair.

3. **Relay subprocess spawn** (`Spawn` in `internal/service/console/spawn.go`): The master PTY FD is passed as `ExtraFiles[0]` (FD 3) to a child process via `system.SpawnService`. The child runs `mvm run console relay --pty-fd 3 --vm-id ... --vm-path ...`.

4. **Relay I/O loop** (`Run` in `internal/service/console/entry.go`): The subprocess opens FD 3 as the PTY master, creates a Unix domain socket at `<vmDir>/console.sock`, and runs `runRelayIO` which:
   - Reads from PTY in a goroutine, sends data to a channel
   - Main loop: accepts one client connection on the Unix socket
   - Forwards PTY output to both the log file (`firecracker.console.log`) and the connected socket client
   - Forwards socket client input back to the PTY (writing to the guest)
   - Uses poll-like timeouts (100ms) for non-blocking select behavior

5. **Client attachment** (`InteractiveAttach` in `internal/service/console/client.go`): Connects to the Unix socket, sets the local terminal to raw mode via `term.MakeRaw`, then:
   - Goroutine reads from socket, writes to stdout
   - Goroutine reads stdin byte-by-byte
   - Main loop checks for detach sequence (Ctrl+X then D = `\x18d`)
   - Non-detach bytes are forwarded to the relay socket

The vsock patterns described in this document are conceptually similar (PTY allocation, raw terminal mode, I/O bridging), but the transport mechanism is different (Unix socket vs vsock).
