#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Setting up Bridge for Firecracker Multi-VM ==="

echo "[1/5] Checking KVM availability..."
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available. Please ensure KVM is enabled."
  exit 1
fi
echo "KVM is available"

echo "[2/5] Downloading Firecracker binary..."
if [ ! -f "firecracker" ]; then
  curl -sL "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-x86_64.tar.gz" | tar xz -C .
  mv firecracker-${FIRECRACKER_VERSION}-x86_64/firecracker .
  mv firecracker-${FIRECRACKER_VERSION}-x86_64/jailer .
  rm -rf firecracker-${FIRECRACKER_VERSION}-x86_64
  chmod +x firecracker jailer
  echo "Firecracker installed"
else
  echo "Firecracker already installed"
fi

echo "[3/5] Downloading Ubuntu 24.04 cloud image..."
if [ ! -f "ubuntu-24.04-server-cloudimg-amd64.img" ]; then
  curl -sL "https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current/${UBUNTU_VERSION}-server-cloudimg-amd64.img" -o "ubuntu-24.04-server-cloudimg-amd64.img"
fi
echo "Ubuntu cloud image ready"

echo "[4/5] Preparing base rootfs..."
if [ ! -f "base-rootfs.ext4" ]; then
  qemu-img convert -f qcow2 -O raw "ubuntu-24.04-server-cloudimg-amd64.img" "base-rootfs.ext4"
  truncate -s "$DISK_SIZE" base-rootfs.ext4
  e2fsck -f base-rootfs.ext4 || true
  resize2fs base-rootfs.ext4
fi
echo "Base rootfs ready"

echo "[5/6] Downloading vmlinux kernel..."
if [ ! -f "vmlinux" ]; then
  chmod +x get-kernel.sh
  ./get-kernel.sh
fi
echo "Kernel ready"

echo "[6/6] Creating bridge $BRIDGE_NAME..."
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
if [ -z "$DEFAULT_IFACE" ]; then
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
