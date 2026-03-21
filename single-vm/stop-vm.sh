#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"
FIRECRACKER_CONFIG="${OUTPUT_DIR}/firecracker.json"

echo "=== Stopping Firecracker VM ==="

# First try to stop via screen session (most reliable)
echo "Stopping screen session 'fc-single'..."
screen -S fc-single -X quit 2>/dev/null || true
sleep 1

# Then kill any remaining firecracker processes for this config
if [ "$ENABLE_SOCKET" = "true" ]; then
  for pid in $(pgrep -f "firecracker.*--api-sock.*${OUTPUT_DIR}"); do
    if [ -n "$pid" ]; then
      echo "Stopping Firecracker process (PID: $pid)..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
  done
else
  for pid in $(pgrep -f "firecracker.*$FIRECRACKER_CONFIG"); do
    if [ -n "$pid" ]; then
      echo "Stopping Firecracker process (PID: $pid)..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
  done
fi

# Also check via PID file
if [ -f "$FIRECRACKER_PID_FILE" ]; then
  FIRECRACKER_PID=$(cat "$FIRECRACKER_PID_FILE")
  if kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
    echo "Stopping Firecracker (PID: $FIRECRACKER_PID)..."
    kill "$FIRECRACKER_PID" 2>/dev/null || true
    sleep 1
    kill -9 "$FIRECRACKER_PID" 2>/dev/null || true
  fi
  rm -f "$FIRECRACKER_PID_FILE"
fi


echo ""
echo "=== VM Stopped ==="
echo "Firecracker process terminated"
echo ""
echo "Note: Network configuration is still active."
echo "Run ./cleanup.sh to fully clean up network and resources."
