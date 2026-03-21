#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker Multi-VM Setup ==="
echo ""

# =============================================================================
# STEP 1: Check Dependencies
# =============================================================================
echo "[1/5] Checking dependencies..."

# Required commands for multi-vm setup
declare -a REQUIRED_CMDS=("mkisofs" "mount" "umount" "sudo" "ip" "iptables")
for cmd in "${REQUIRED_CMDS[@]}"; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: Required command '$cmd' is not installed"
    exit 1
  fi
done

# Check KVM is available
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM not available (/dev/kvm not found)"
  exit 1
fi

echo "✓ All dependencies and KVM OK"

# =============================================================================
# STEP 2: Check Assets
# =============================================================================
echo "[2/5] Checking assets..."

# Check firecracker binary
if [ ! -f "../assets/bin/firecracker" ]; then
  echo "ERROR: Firecracker binary not found at ../assets/bin/firecracker"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

# Check kernel exists
if [ ! -f "$KERNEL_PATH" ]; then
  echo "ERROR: Kernel not found at $KERNEL_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

# Check rootfs exists
if [ ! -f "$ROOTFS_PATH" ]; then
  echo "ERROR: Rootfs not found at $ROOTFS_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
ROOTFS_SOURCE="$ROOTFS_PATH"

echo "✓ All assets present"

# =============================================================================
# STEP 3: Prepare Base Rootfs
# =============================================================================
echo "[3/5] Preparing base rootfs..."

mkdir -p "$OUTPUT_DIR"

if [ ! -f "${OUTPUT_DIR}/base-rootfs.ext4" ]; then
  echo " - Copying source rootfs to base..."
  cp "$ROOTFS_SOURCE" "${OUTPUT_DIR}/base-rootfs.ext4"
  echo "✓ Base rootfs prepared"
else
  echo "✓ Base rootfs already exists"
fi

# =============================================================================
# STEP 4: Setup Bridge Network
# =============================================================================
echo "[4/5] Setting up bridge network..."

# Create bridge if not exists
if ip link show "$BRIDGE_NAME" &>/dev/null; then
  echo " - Bridge $BRIDGE_NAME already exists"
else
  echo " - Creating bridge $BRIDGE_NAME..."
  sudo ip link add name "$BRIDGE_NAME" type bridge
  sudo ip addr add "$BRIDGE_IP" dev "$BRIDGE_NAME"
  sudo ip link set "$BRIDGE_NAME" up
  echo " - Bridge created"
fi

# =============================================================================
# STEP 5: Configure NAT
# =============================================================================
echo "[5/5] Configuring NAT..."

# Check IP forwarding
if [ "$(sysctl -n net.ipv4.ip_forward)" != "1" ]; then
  echo "ERROR: IP forwarding is not enabled. Run ../environment_setup.sh first"
  exit 1
fi

# Detect default interface
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -z "$DEFAULT_IFACE" ]; then
  echo "ERROR: Could not detect default network interface"
  exit 1
fi
echo " - Using host interface: $DEFAULT_IFACE"

# Setup NAT rules
sudo iptables -t nat -C POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null ||
  sudo iptables -t nat -A POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE

echo " - NAT rules configured"

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=========================================="
echo "✓✓✓ Multi-VM Setup Complete! ✓✓✓"
echo "=========================================="
echo ""
echo "Configuration:"
echo " - Bridge: $BRIDGE_NAME ($BRIDGE_IP)"
echo " - Guest IP Range: $GUEST_IP_START - $GUEST_IP_END"
echo " - Base Rootfs: ${OUTPUT_DIR}/base-rootfs.ext4"
echo ""
echo "Create VMs with:"
echo " ./create-vm.sh <name> [vcpu] [memory_mib]"
echo ""
echo "Examples:"
echo " ./create-vm.sh vm1                    # default resources, auto IP"
echo " ./create-vm.sh vm2 2 2048           # 2 vCPU, 2048MiB, auto IP"
echo ""
