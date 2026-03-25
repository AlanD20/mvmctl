#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source config.env

FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"
FIRECRACKER_SOCKET="${OUTPUT_DIR}/firecracker.socket"

echo "=== Deleting Firecracker VM ==="

# -----------------------------------------------------------------------------
# Stop Firecracker
# -----------------------------------------------------------------------------

if [ ! -f "$FIRECRACKER_PID_FILE" ]; then
  echo " - No PID file found, checking for stray processes..."
else
  FIRECRACKER_PID=$(cat "$FIRECRACKER_PID_FILE" 2>/dev/null)

  if [ -n "$FIRECRACKER_PID" ] && kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
    echo " - VM is running (PID: $FIRECRACKER_PID), stopping..."

    # Try graceful shutdown via API socket
    if [ "$ENABLE_SOCKET" = "true" ] && [ -S "$FIRECRACKER_SOCKET" ]; then
      echo " - Sending graceful shutdown (CtrlAltDel)..."
      if curl --unix-socket "$FIRECRACKER_SOCKET" -s -X PUT \
        "http://localhost/actions" \
        -d '{ "action_type": "SendCtrlAltDel" }' 2>/dev/null; then
        echo " - Waiting for VM to shutdown (5s timeout)..."
        for i in {1..10}; do
          sleep 0.5
          if ! kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
            echo " - VM shutdown gracefully"
            break
          fi
        done
        kill -0 "$FIRECRACKER_PID" 2>/dev/null && echo " - Graceful shutdown timeout, forcing stop..."
      fi
    fi

    # Force kill if still running
    if kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
      echo " - Force stopping Firecracker (PID: $FIRECRACKER_PID)..."
      kill "$FIRECRACKER_PID" 2>/dev/null || true
      sleep 1
      if kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
        echo " - Force killing with SIGKILL..."
        kill -9 "$FIRECRACKER_PID" 2>/dev/null || true
      fi
    fi
  fi
fi

# Kill any remaining stray processes associated with this VM
for pid in $(pgrep -f "firecracker.*${OUTPUT_DIR}" 2>/dev/null || true); do
  echo " - Stopping stray Firecracker process (PID: $pid)..."
  kill "$pid" 2>/dev/null || true
  sleep 1
  kill -9 "$pid" 2>/dev/null || true
done

rm -f "$FIRECRACKER_PID_FILE" "$FIRECRACKER_SOCKET" 2>/dev/null || true

# -----------------------------------------------------------------------------
# Remove network configuration
# -----------------------------------------------------------------------------

if ip link show "$TAP_DEV" &>/dev/null; then
  echo " - Removing tap device $TAP_DEV..."
  ip link del "$TAP_DEV" 2>/dev/null || sudo ip link del "$TAP_DEV" 2>/dev/null || true
fi

echo " - Flushing iptables NAT rules..."
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -n "$DEFAULT_IFACE" ]; then
  sudo iptables -t nat -D POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
  sudo iptables -D FORWARD -i "$TAP_DEV" -o "$DEFAULT_IFACE" -j ACCEPT 2>/dev/null || true
  sudo iptables -D FORWARD -i "$DEFAULT_IFACE" -o "$TAP_DEV" -j ACCEPT 2>/dev/null || true
fi

# -----------------------------------------------------------------------------
# Clean up remaining files
# -----------------------------------------------------------------------------

if [ -n "$GUEST_IP" ]; then
  echo " - Removing SSH fingerprint for $GUEST_IP..."
  ssh-keygen -R "$GUEST_IP" 2>/dev/null || true
fi

echo " - Removing VM files in '$OUTPUT_DIR'..."
rm -rf "$OUTPUT_DIR"

# -----------------------------------------------------------------------------

echo ""
echo "=== VM Deleted ==="
echo ""
echo "To create a new VM: ./setup.sh"
echo ""
