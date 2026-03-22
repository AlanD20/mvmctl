#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

VM_REF="${1:-}"
SSH_USER="${2:-root}"

if [ -z "$VM_REF" ]; then
  echo "Usage: $0 <vm_name|ip_address> [user]"
  echo ""
  echo "Arguments:"
  echo " vm_name|ip_address - VM name (for multi-vm) or IP address"
  echo " user - SSH user (default: root)"
  echo ""
  echo "Examples:"
  echo " $0 vm1       # SSH to multi-vm named 'vm1' as root"
  echo " $0 10.20.0.2 # SSH to IP 10.20.0.2 as root"
  echo " $0 vm1 human # SSH to multi-vm 'vm1' as user 'human'"
  exit 1
fi

OUTPUT_DIR="env"

SSH_KEYS=""
for key in assets/keys/id_*; do
  [ -f "$key" ] || continue
  [[ "$key" == *.pub ]] && continue
  chmod 600 "$key" 2>/dev/null || true
  SSH_KEYS="$SSH_KEYS -i $key"
done

if [ -z "$SSH_KEYS" ]; then
  echo "ERROR: No SSH private keys found in assets/keys/"
  echo "Make sure keys are generated: ./assets/download-assets.sh"
  exit 1
fi

if echo "$VM_REF" | grep -qE "^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$"; then
  VM_IP="$VM_REF"
else
  VM_NAME="$VM_REF"

  CONFIG_FILE="multi-vm/${OUTPUT_DIR}/${VM_NAME}/firecracker.json"

  if [ ! -f "$CONFIG_FILE" ]; then
    echo "ERROR: VM '$VM_NAME' not found at $CONFIG_FILE"
    exit 1
  fi

  VM_IP=$(grep -oP 'ip=\K[^:]*' "$CONFIG_FILE" 2>/dev/null | head -1 || true)
  if [ -z "$VM_IP" ]; then
    echo "ERROR: Could not find IP address for VM '$VM_NAME'"
    exit 1
  fi
fi

echo "Connecting to $VM_IP as $SSH_USER..."
ssh $SSH_KEYS -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$SSH_USER@$VM_IP"
