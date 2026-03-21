#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Setting up Bridge for Firecracker Multi-VM ==="

echo "[1/6] Checking shared assets..."
if [ ! -f "../assets/bin/firecracker" ]; then
  echo "Shared Firecracker not found. Run ../assets/download-assets.sh first"
  exit 1
fi
if [ ! -f "../assets/kernels/vmlinux" ]; then
  echo "Shared kernel not found. Run ../assets/download-assets.sh first"
  exit 1
fi

# Link shared assets locally
ln -sf "../assets/bin/firecracker" firecracker
ln -sf "../assets/bin/jailer" jailer 2>/dev/null || true
ln -sf "../assets/kernels/vmlinux" vmlinux
echo "Shared assets linked"

echo "[2/6] Checking KVM availability..."
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available. Please ensure KVM is enabled."
  exit 1
fi
echo "KVM is available"

echo "[3/6] Preparing base rootfs from assets..."
IMAGE_PATH="../assets/images/${IMAGE_OS}-${IMAGE_VERSION}-server-cloudimg-${IMAGE_ARCH}.img"
if [ ! -f "$IMAGE_PATH" ]; then
  echo "ERROR: OS image not found at $IMAGE_PATH. Run ../assets/download-assets.sh first."
  exit 1
fi

if [ ! -f "base-rootfs.ext4" ]; then
  echo "Converting image to base rootfs..."
  qemu-img convert -f qcow2 -O raw "$IMAGE_PATH" "base-rootfs.ext4"
  truncate -s "$DISK_SIZE" base-rootfs.ext4
  e2fsck -f base-rootfs.ext4 || true
  resize2fs base-rootfs.ext4
  echo "Base rootfs prepared"
else
  echo "Base rootfs already exists"
fi

echo "[5/5] Creating bridge $BRIDGE_NAME..."
if ip link show "$BRIDGE_NAME" &>/dev/null; then
  echo "Bridge $BRIDGE_NAME already exists"
else
  ip link add name "$BRIDGE_NAME" type bridge
  ip addr add "$BRIDGE_IP" dev "$BRIDGE_NAME"
  ip link set "$BRIDGE_NAME" up
  echo "Bridge $BRIDGE_NAME created"
fi

echo "Setting up NAT..."
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ "$DEFAULT_IFACE" = "" ]; then
  echo "ERROR: Could not detect default network interface."
  exit 1
fi
echo "Using host interface: $DEFAULT_IFACE"
sysctl -w net.ipv4.ip_forward=1 >/dev/null
iptables -t nat -C POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null ||
  iptables -t nat -A POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE

echo ""
echo "=== Bridge Setup Complete ==="
echo "  Bridge: $BRIDGE_NAME ($BRIDGE_IP)"
echo "  Guest IP range: $GUEST_IP_START - $GUEST_IP_END"
echo ""
echo "Now you can create VMs with: ./create-vm.sh <name> [vcpu] [memory] [ip]"
