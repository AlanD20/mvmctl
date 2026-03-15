#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

API_SOCKET="/tmp/firecracker.socket"
FIRECRACKER_PID_FILE="/tmp/firecracker.pid"

echo "=== Cleaning up Firecracker VM ==="

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

rm -f "$API_SOCKET"

if ip link show "$TAP_DEV" &>/dev/null; then
  echo "Removing tap device $TAP_DEV..."
  ip link del "$TAP_DEV" 2>/dev/null || true
fi

echo "Flushing iptables NAT rules..."
iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE 2>/dev/null || true
iptables -D FORWARD -i "$TAP_DEV" -o eth0 -j ACCEPT 2>/dev/null || true
iptables -D FORWARD -i eth0 -o "$TAP_DEV" -j ACCEPT 2>/dev/null || true

echo "Resetting IP forwarding..."
sysctl -w net.ipv4.ip_forward=0 >/dev/null 2>&1 || true

echo ""
echo "=== Cleanup Complete ==="
echo "  - Firecracker process stopped"
echo "  - Tap device removed"
echo "  - NAT rules flushed"
