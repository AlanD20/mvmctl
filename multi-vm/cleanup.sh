#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Cleaning up Firecracker Multi-VM ==="

# =============================================================================
# STEP 1: STOP ALL VMS GRACEFULLY
# =============================================================================
echo "[1/6] Stopping all VMs..."

if [ -d "${OUTPUT_DIR}" ]; then
  for vm_dir in "${OUTPUT_DIR}"/*/; do
    if [ -d "$vm_dir" ]; then
      VM_NAME=$(basename "$vm_dir")
      echo " - Stopping $VM_NAME..."

      source "${vm_dir}vm.env" 2>/dev/null || true

      if [ -f "$vm_dir/firecracker.pid" ]; then
        VM_PID=$(cat "$vm_dir/firecracker.pid")

        if [ -n "$VM_PID" ] && kill -0 "$VM_PID" 2>/dev/null; then
          echo "   - VM is running (PID: $VM_PID)"

          if [ "$ENABLE_SOCKET" = "true" ] && [ -S "$vm_dir/firecracker.socket" ]; then
            echo "   - Sending graceful shutdown (CtrlAltDel)..."
            if curl --unix-socket "$vm_dir/firecracker.socket" -s -X PUT \
              "http://localhost/actions" \
              -d '{ "action_type": "SendCtrlAltDel" }' 2>/dev/null; then
              echo "   - Waiting for VM to shutdown (5s timeout)..."
              for i in {1..10}; do
                sleep 0.5
                if ! kill -0 "$VM_PID" 2>/dev/null; then
                  echo "   - VM shutdown gracefully"
                  break
                fi
              done
            fi
          fi

          if kill -0 "$VM_PID" 2>/dev/null; then
            echo "   - Force stopping Firecracker (PID: $VM_PID)..."
            kill "$VM_PID" 2>/dev/null || true
            sleep 1
            if kill -0 "$VM_PID" 2>/dev/null; then
              echo "   - Force killing with SIGKILL..."
              kill -9 "$VM_PID" 2>/dev/null || true
            fi
          fi
        fi

        rm -f "$vm_dir/firecracker.pid"
      fi

      for pid in $(pgrep -f "firecracker.*${vm_dir}" 2>/dev/null || true); do
        echo "   - Stopping stray Firecracker process (PID: $pid)..."
        kill "$pid" 2>/dev/null || true
        sleep 1
        kill -9 "$pid" 2>/dev/null || true
      done

      rm -f "$vm_dir/firecracker.socket" 2>/dev/null || true

      TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
      if ip link show "$TAP_DEV" &>/dev/null; then
        echo "   - Removing tap device $TAP_DEV..."
        ip link del "$TAP_DEV" 2>/dev/null || true
      fi
    fi
  done

  rm -rf "${OUTPUT_DIR}"
  echo " - All VMs stopped and removed"
else
  echo " - No VMs to stop"
fi

# =============================================================================
# STEP 2: REMOVE ALL TAP DEVICES
# =============================================================================
echo "[2/6] Removing tap devices..."

for tap in $(ip link show type tap 2>/dev/null | grep -oE "${TAP_PREFIX}-[^[:space:]:]+" | sort -u); do
  echo " - Removing tap: $tap"
  ip link del "$tap" 2>/dev/null || true
done

for tap in $(ip link show 2>/dev/null | grep -oE "${TAP_PREFIX}-[a-zA-Z0-9-]+" | sort -u); do
  if ip link show "$tap" &>/dev/null; then
    echo " - Removing tap: $tap"
    ip link del "$tap" 2>/dev/null || true
  fi
done

# =============================================================================
# STEP 3: REMOVE BRIDGE
# =============================================================================
echo "[3/6] Removing bridge $BRIDGE_NAME..."

if ip link show "$BRIDGE_NAME" &>/dev/null; then
  echo " - Bringing down bridge $BRIDGE_NAME..."
  ip link set "$BRIDGE_NAME" down 2>/dev/null || true

  for iface in $(ip link show master "$BRIDGE_NAME" 2>/dev/null | grep -oE '^[0-9]+: [^@:' | awk '{print $2}' | tr -d ':' || true); do
    if [ -n "$iface" ]; then
      echo " - Removing $iface from bridge..."
      ip link set "$iface" master dummy 2>/dev/null || true
      ip link set "$iface" nomaster 2>/dev/null || true
    fi
  done

  echo " - Deleting bridge $BRIDGE_NAME..."
  ip link del "$BRIDGE_NAME" 2>/dev/null || true
  echo " - Bridge removed"
else
  echo " - Bridge does not exist"
fi

# =============================================================================
# STEP 4: FLUSH IPTABLES NAT RULES
# =============================================================================
echo "[4/6] Flushing iptables NAT rules..."

DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -n "$DEFAULT_IFACE" ]; then
  echo " - Default interface: $DEFAULT_IFACE"

  echo " - Removing NAT MASQUERADE rule..."
  iptables -t nat -D POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true

  echo " - Removing FORWARD rules..."
  for tap in $(ip link show type tap 2>/dev/null | grep -oE "${TAP_PREFIX}-[^[:space:]:]+" | sort -u); do
    iptables -D FORWARD -i "$tap" -o "$DEFAULT_IFACE" -j ACCEPT 2>/dev/null || true
    iptables -D FORWARD -i "$DEFAULT_IFACE" -o "$tap" -j ACCEPT 2>/dev/null || true
  done

  iptables -D FORWARD -i "$BRIDGE_NAME" -j ACCEPT 2>/dev/null || true
  iptables -D FORWARD -o "$BRIDGE_NAME" -j ACCEPT 2>/dev/null || true

  echo " - NAT rules flushed"
else
  echo " - No default interface found, skipping NAT cleanup"
fi

# =============================================================================
# STEP 5: CLEANUP SSH FINGERPRINTS
# =============================================================================
echo "[5/6] Cleaning up SSH fingerprints..."

if [ -n "$GUEST_IP_START" ] && [ -n "$GUEST_IP_END" ]; then
  IP_BASE="${GUEST_IP_START%.*}"

  for i in $(seq 2 254); do
    GUEST_IP="${IP_BASE}.${i}"
    if ssh-keygen -F "$GUEST_IP" &>/dev/null; then
      echo " - Removing SSH fingerprint for $GUEST_IP..."
      ssh-keygen -R "$GUEST_IP" 2>/dev/null || true
    fi
  done
fi

if [ -f "$HOME/.ssh/known_hosts" ]; then
  for vm_name in vm1 vm2 vm3 vm4 vm5 testvm; do
    if grep -q "$vm_name" "$HOME/.ssh/known_hosts" 2>/dev/null; then
      echo " - Removing SSH fingerprint for hostname $vm_name..."
      ssh-keygen -R "$vm_name" 2>/dev/null || true
    fi
  done
fi

# =============================================================================
# STEP 6: CLEANUP REMAINING ARTIFACTS
# =============================================================================
echo "[6/6] Cleaning up remaining artifacts..."

for pid in $(pgrep -f "firecracker.*${SCRIPT_DIR}" 2>/dev/null || true); do
  echo " - Stopping stray Firecracker process (PID: $pid)..."
  kill "$pid" 2>/dev/null || true
  sleep 0.5
  kill -9 "$pid" 2>/dev/null || true
done

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=========================================="
echo "✓✓✓ Cleanup Complete ✓✓✓"
echo "=========================================="
echo ""
echo "Cleaned up:"
echo " - All VMs stopped and removed"
echo " - All tap devices removed"
echo " - Bridge $BRIDGE_NAME removed"
echo " - NAT rules flushed"
echo " - SSH fingerprints cleaned"
echo ""
echo "NOTE: IP forwarding is still enabled"
echo "To disable: sysctl -w net.ipv4.ip_forward=0"
echo ""
