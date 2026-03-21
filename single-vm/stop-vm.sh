#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"

echo "=== Stopping Firecracker VM ==="

# =============================================================================
# STOP FIRECRACKER
# =============================================================================
stop_firecracker() {
  local FIRECRACKER_PID=""

  # Try to get PID from file
  if [ -f "$FIRECRACKER_PID_FILE" ]; then
    FIRECRACKER_PID=$(cat "$FIRECRACKER_PID_FILE")
  fi

  # Stop by PID file
  if [ -n "$FIRECRACKER_PID" ] && kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
    echo "Stopping Firecracker (PID: $FIRECRACKER_PID)..."
    kill "$FIRECRACKER_PID" 2>/dev/null || true
    sleep 1

    # Force kill if still running
    if kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
      echo "Force killing..."
      kill -9 "$FIRECRACKER_PID" 2>/dev/null || true
    fi
  fi

  rm -f "$FIRECRACKER_PID_FILE"

  # Kill any remaining firecracker processes for this VM
  for pid in $(pgrep -f "firecracker.*${OUTPUT_DIR}/firecracker.json" 2>/dev/null || true); do
    if [ -n "$pid" ]; then
      echo "Stopping Firecracker process (PID: $pid)..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
  done
}

stop_firecracker

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=== VM Stopped ==="
echo "Firecracker process terminated"
echo ""
echo "Note: Network configuration is still active."
echo "Run ./cleanup.sh to fully clean up network and resources."
