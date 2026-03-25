#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker Multi-VM Status ==="
echo ""

if [ ! -d "$OUTPUT_DIR" ] || [ -z "$(ls -A $OUTPUT_DIR 2>/dev/null | grep -v base-rootfs | grep -v cloud-init)" ]; then
  echo "No VMs found."
  echo ""
  echo "Create a VM with:"
  echo " ./create-vm.sh <name> [vcpu] [memory_mib]"
  exit 0
fi

printf "%-12s %-15s %-17s %-10s %-8s\n" "NAME" "IP ADDRESS" "MAC ADDRESS" "STATUS" "PID"
printf "%-12s %-15s %-17s %-10s %-8s\n" "------------" "---------------" "-----------------" "----------" "--------"

for vm_dir in "$OUTPUT_DIR"/*/; do
  if [ -d "$vm_dir" ]; then
    VM_NAME=$(basename "$vm_dir")

    [ "$VM_NAME" = "base-rootfs.ext4" ] && continue
    [ "$VM_NAME" = "cloud-init" ] && continue

    [ ! -f "$vm_dir/firecracker.json" ] && continue

    VM_IP=$(grep -oP 'ip=\K[^:]*' "$vm_dir/firecracker.json" 2>/dev/null | head -1 || true)
    [ -z "$VM_IP" ] && continue

    VM_MAC=$(grep -oP '"guest_mac": "\K[^"]*' "$vm_dir/firecracker.json" 2>/dev/null | head -1 || true)
    [ -z "$VM_MAC" ] && continue

    VM_STATUS="stopped"
    VM_PID="N/A"
    if [ -f "$vm_dir/firecracker.pid" ]; then
      VM_PID=$(cat "$vm_dir/firecracker.pid" 2>/dev/null)
      if [ -n "$VM_PID" ] && kill -0 "$VM_PID" 2>/dev/null; then
        VM_STATUS="running"
      fi
    fi

    printf "%-12s %-15s %-17s %-10s %-8s\n" "$VM_NAME" "$VM_IP" "$VM_MAC" "$VM_STATUS" "$VM_PID"
  fi
done

echo ""
echo "Commands:"
echo " Create: ./create-vm.sh <name> [vcpu] [memory_mib]"
echo " Delete: ./delete-vm.sh <name>"
echo " Logs: ./logs-vm.sh <name>"
echo ""
