#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

VM_NAME="${1:-}"
VM_VCPU="${2:-0.5}"
VM_MEM="${3:-0.5}"
VM_IP="${4:-}"

if [ -z "$VM_NAME" ]; then
  echo "Usage: $0 <name> [vcpu] [memory] [ip]"
  echo ""
  echo "Arguments:"
  echo "  name     - VM name (required)"
  echo "  vcpu     - Number of vCPUs (default: 0.5)"
  echo "  memory   - Memory in GB (default: 0.5)"
  echo "  ip       - Static IP (optional, auto-assigned if not provided)"
  echo ""
  echo "Examples:"
  echo "  $0 vm1                              # 0.5 vCPU, 0.5GB, auto IP"
  echo "  $0 vm2 1.5 2                        # 1.5 vCPU, 2GB, auto IP"
  echo "  $0 vm3 2 4 10.10.0.50              # 2 vCPU, 4GB, static IP"
  exit 1
fi

VM_DIR="vms/$VM_NAME"
if [ -d "$VM_DIR" ]; then
  echo "ERROR: VM '$VM_NAME' already exists at $VM_DIR"
  exit 1
fi

if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available"
  exit 1
fi

if ! ip link show "$BRIDGE_NAME" &>/dev/null; then
  echo "ERROR: Bridge $BRIDGE_NAME does not exist. Run ./setup-bridge.sh first."
  exit 1
fi

if [ ! -f "vmlinux" ]; then
  echo "Kernel not found. Running get-kernel.sh..."
  chmod +x get-kernel.sh
  ./get-kernel.sh
fi

VM_MEM_MIB=$(echo "$VM_MEM * 1024" | bc | cut -d'.' -f1)
VM_VCPU_INT=$(echo "$VM_VCPU * 1" | bc | cut -d'.' -f1)
if [ "$VM_VCPU_INT" -lt 1 ]; then
  VM_VCPU_INT=1
fi

TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
MAC_SUFFIX=$(printf "%02x%02x%02x" $((RANDOM % 256)) $((RANDOM % 256)) $((RANDOM % 256)))
GUEST_MAC="02:FC:${MAC_SUFFIX:0:2}:${MAC_SUFFIX:2:2}:${MAC_SUFFIX:4:2}"

if [ -z "$VM_IP" ]; then
  for ip in $(seq 2 254); do
    IP="10.10.0.$ip"
    if ! grep -rq "$IP" vms/*/config.json 2>/dev/null; then
      VM_IP="$IP"
      break
    fi
  done
  if [ -z "$VM_IP" ]; then
    echo "ERROR: No available IPs in pool"
    exit 1
  fi
fi

echo "=== Creating VM: $VM_NAME ==="
echo "  vCPUs: $VM_VCPU"
echo "  Memory: ${VM_MEM}GB (${VM_MEM_MIB}MB)"
echo "  IP: $VM_IP"
echo "  MAC: $GUEST_MAC"
echo "  Tap: $TAP_DEV"

mkdir -p "$VM_DIR"

if [ ! -f "base-rootfs.ext4" ]; then
  echo "ERROR: base-rootfs.ext4 not found. Run ./setup-bridge.sh first."
  exit 1
fi
cp base-rootfs.ext4 "$VM_DIR/rootfs.ext4"

cat >"$VM_DIR/config.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "../../vmlinux",
    "boot_args": "ro console=ttyS0 noapic reboot=k panic=1 pci=off ip=${VM_IP}::10.10.0.1:255.255.255.0::eth0:off",
    "initrd_path": null
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "rootfs.ext4",
      "is_root_device": true,
      "is_read_only": false,
      "partuuid": null,
      "cache_type": "Unsafe",
      "io_engine": "Sync",
      "rate_limiter": null,
      "socket": null
    }
  ],
  "network-interfaces": [
    {
      "iface_id": "eth0",
      "guest_mac": "$GUEST_MAC",
      "host_dev_name": "$TAP_DEV",
      "guest_ip": "$VM_IP",
      "netmask": "255.255.255.0"
    }
  ],
  "machine-config": {
    "vcpu_count": $VM_VCPU_INT,
    "mem_size_mib": $VM_MEM_MIB,
    "ht_enabled": false,
    "cpu_template": null
  },
  "cpu-config": null,
  "balloon": null,
  "vsock": null,
  "logger": {
    "log_path": "./firecracker.log",
    "level": "Info",
    "show_level": true,
    "show_log_origin": true
  },
  "metrics": {
    "metrics_path": "./firecracker.metrics"
  }
}
EOF

if ! ip link show "$TAP_DEV" &>/dev/null; then
  ip tuntap add dev "$TAP_DEV" mode tap
fi
ip link set "$TAP_DEV" master "$BRIDGE_NAME" 2>/dev/null || true
ip link set "$TAP_DEV" up

echo ""
echo "Starting VM..."
cd "$VM_DIR"
../../firecracker --no-api --config-file config.json &
VM_PID=$!
echo $VM_PID >firecracker.pid
cd ../..

echo ""
echo "=== VM Created Successfully ==="
echo "  Name: $VM_NAME"
echo "  PID: $VM_PID"
echo "  IP: $VM_IP"
echo "  Directory: $VM_DIR"
echo ""
echo "Connect to serial console:"
echo "  screen -r $VM_PID"
echo "  or"
echo "  sudo microcom /dev/ttyS0"
