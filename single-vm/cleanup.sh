#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

API_SOCKET="${OUTPUT_DIR}/firecracker.socket"
FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"

echo "=== Cleaning up Firecracker VM ==="

# =============================================================================
# STOP FIRECRACKER PROCESSES
# =============================================================================
stop_firecracker_processes() {
  local config_pattern="${OUTPUT_DIR}"

  # Kill by PID file if exists
  if [ -f "$FIRECRACKER_PID_FILE" ]; then
    local pid=$(cat "$FIRECRACKER_PID_FILE")
    if kill -0 "$pid" 2>/dev/null; then

      echo "Stopping Firecracker (PID: $pid)..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$FIRECRACKER_PID_FILE"
  fi

  # Kill any remaining firecracker processes for this VM
  for pid in $(pgrep -f "firecracker.*${config_pattern}" 2>/dev/null || true); do
    if [ -n "$pid" ]; then
      echo "Stopping Firecracker process (PID: $pid)..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
  done
}

stop_firecracker_processes

# =============================================================================
# CLEANUP FILES AND RESOURCES
# =============================================================================
rm -f "$API_SOCKET"

echo "Removing '$OUTPUT_DIR'"
rm -rf "$OUTPUT_DIR"

# =============================================================================
# REMOVE NETWORK CONFIGURATION
# =============================================================================
if ip link show "$TAP_DEV" &>/dev/null; then
  echo "Removing tap device $TAP_DEV..."
  ip link del "$TAP_DEV" 2>/dev/null || true
fi

echo "Flushing iptables NAT rules..."
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
iptables -t nat -D POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
iptables -D FORWARD -i "$TAP_DEV" -o "$DEFAULT_IFACE" -j ACCEPT 2>/dev/null || true
iptables -D FORWARD -i "$DEFAULT_IFACE" -o "$TAP_DEV" -j ACCEPT 2>/dev/null || true

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=== Cleanup Complete ==="
echo " - Firecracker process stopped"
echo " - Removed '$OUTPUT_DIR'"
echo " - Tap device removed"
echo " - NAT rules flushed"
