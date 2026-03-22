#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker Multi-VM Setup ==="
echo "Image source : ${IMAGE_SOURCE}"
echo "Kernel : ${KERNEL_PATH}"
echo "Rootfs : ${ROOTFS_PATH}"
echo ""

# -----------------------------------------------------------------------------
# Step 1: Check dependencies
# -----------------------------------------------------------------------------
echo "[1/7] Checking dependencies..."

# Required commands for multi-vm setup
for cmd in mkisofs mount umount sudo ip iptables; do
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

echo " - Dependencies OK"

# -----------------------------------------------------------------------------
# Step 2: Check assets
# -----------------------------------------------------------------------------
echo "[2/7] Checking assets..."

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

# Check SSH keys from assets/keys/
SSH_KEY_SOURCE="../assets/keys/id_rsa"
SSH_PUB_KEY_SOURCE="../assets/keys/id_rsa.pub"

if [ ! -f "$SSH_KEY_SOURCE" ]; then
  echo "ERROR: SSH key not found at $SSH_KEY_SOURCE"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

if [ ! -f "$SSH_PUB_KEY_SOURCE" ]; then
  echo "WARNING: SSH public key not found at $SSH_PUB_KEY_SOURCE"
fi

echo " - Assets OK"

# -----------------------------------------------------------------------------
# Step 3: Prepare base rootfs
# -----------------------------------------------------------------------------
echo "[3/7] Preparing base rootfs..."

mkdir -p "$OUTPUT_DIR"

# Derive the rootfs filename extension from the source path (e.g. ext4, btrfs)
ROOTFS_EXT="${ROOTFS_PATH##*.}"
ROOTFS_DEST="${OUTPUT_DIR}/base-rootfs.${ROOTFS_EXT}"

if [ ! -f "$ROOTFS_DEST" ]; then
  echo " - Copying source rootfs to base..."
  cp "$ROOTFS_PATH" "$ROOTFS_DEST"
  echo " - Copying SSH key..."
  cp "$SSH_KEY_SOURCE" "${OUTPUT_DIR}/vm.id_rsa"
  chmod 600 "${OUTPUT_DIR}/vm.id_rsa"
  echo "✓ Base rootfs prepared"
else
  echo "✓ Base rootfs already exists"
fi

# -----------------------------------------------------------------------------
# Step 4: Create cloud-init
# -----------------------------------------------------------------------------
echo "[4/7] Creating cloud-init..."

# Check if cloud-init template exists
if [ -d "cloud-init" ]; then
  cp -r cloud-init "${OUTPUT_DIR}"

  # Generate instance ID
  INSTANCE_ID="i-$(head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12)"

  # Create meta-data for each VM (will be customized per-VM in create-vm.sh)
  cat >"${OUTPUT_DIR}/cloud-init/meta-data" <<'METAEOF'
instance-id: __INSTANCE_ID__
local-hostname: __HOSTNAME__
METAEOF

  # Create network-config template
  cat >"${OUTPUT_DIR}/cloud-init/network-config" <<'NETEOF'
version: 2
ethernets:
  eth0:
    dhcp4: false
    dhcp6: false
    addresses:
    - __GUEST_IP__/24
    routes:
    - to: default
      via: __HOST_IP__
    nameservers:
      addresses:
      - 1.1.1.1
      - 8.8.8.8
NETEOF

  # Inject SSH public key into user-data if template exists
  PUB_KEY_CONTENT=""
  [ -f "$SSH_PUB_KEY_SOURCE" ] && PUB_KEY_CONTENT=$(cat "$SSH_PUB_KEY_SOURCE")

  if [ -f "cloud-init/user-data" ]; then
    echo " - Using custom cloud-init/user-data..."
    sed \
      -e "s|# SSH keys will be injected here by setup.sh from assets/keys/|${PUB_KEY_CONTENT}|g" \
      -e "s|# Root SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
      -e "s|# User SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
      -e "s|HOSTNAME_PLACEHOLDER|__HOSTNAME__|g" \
      "cloud-init/user-data" >"${OUTPUT_DIR}/cloud-init/user-data"
  fi

  echo " - Cloud-init templates prepared"
else
  echo " - WARNING: cloud-init directory not found, skipping cloud-init setup"
fi

# -----------------------------------------------------------------------------
# Step 5: Embed cloud-init into base rootfs
# -----------------------------------------------------------------------------
echo "[5/7] Embedding cloud-init into base rootfs..."

# Mount the rootfs image so we can inject cloud-init files directly into the
# guest filesystem. This places user-data, meta-data, and network-config into
# /var/lib/cloud/seed/nocloud/ inside the image, which cloud-init reads on
# first boot to configure the VM (users, SSH keys, hostname, networking, etc.)
MOUNT_DIR="${OUTPUT_DIR}/mnt-rootfs"
mkdir -p "$MOUNT_DIR"

if ! sudo mount "$ROOTFS_DEST" "$MOUNT_DIR" 2>/dev/null; then
  echo "ERROR: Could not mount rootfs image ($ROOTFS_DEST)."
  echo "Ensure the image is a raw filesystem (not a partitioned disk) and that"
  echo "you have permission to mount loop devices (try running with sudo)."
  sudo rmdir "$MOUNT_DIR" 2>/dev/null || true
  exit 1
fi

# Inject cloud-init seed files if cloud-init directory exists
if [ -d "${OUTPUT_DIR}/cloud-init" ]; then
  sudo mkdir -p "$MOUNT_DIR/var/lib/cloud/seed/nocloud"
  sudo mkdir -p "$MOUNT_DIR/etc/cloud/cloud.cfg.d"
  sudo cp -r "${OUTPUT_DIR}/cloud-init/"* "$MOUNT_DIR/var/lib/cloud/seed/nocloud/"
  sudo chmod 644 "$MOUNT_DIR"/var/lib/cloud/seed/nocloud/*
  echo " - Cloud-init embedded at /var/lib/cloud/seed/nocloud/"
else
  echo " - WARNING: No cloud-init to embed"
fi

# Comment out /boot/efi entries — present by default on some images (e.g. Debian Bookworm)
# Firecracker doesn't expose an EFI partition so leaving this uncommented causes boot failure
if [ -f "$MOUNT_DIR/etc/fstab" ]; then
  sudo sed -i '/boot\/efi/s/^/#/' "$MOUNT_DIR/etc/fstab"
  echo " - Commented out /boot/efi entries in fstab"
fi

sudo umount "$MOUNT_DIR"
sudo rmdir "$MOUNT_DIR" 2>/dev/null || true

echo " - Cloud-init embedding complete"

# -----------------------------------------------------------------------------
# Step 6: Setup Bridge Network
# -----------------------------------------------------------------------------
echo "[6/7] Setting up bridge network..."

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

# -----------------------------------------------------------------------------
# Step 7: Configure NAT
# -----------------------------------------------------------------------------
echo "[7/7] Configuring NAT..."

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

sudo iptables -C FORWARD -i "$BRIDGE_NAME" -o "$DEFAULT_IFACE" -j ACCEPT 2>/dev/null ||
  sudo iptables -A FORWARD -i "$BRIDGE_NAME" -o "$DEFAULT_IFACE" -j ACCEPT

sudo iptables -C FORWARD -i "$DEFAULT_IFACE" -o "$BRIDGE_NAME" -j ACCEPT 2>/dev/null ||
  sudo iptables -A FORWARD -i "$DEFAULT_IFACE" -o "$BRIDGE_NAME" -j ACCEPT

echo " - NAT rules configured"

# -----------------------------------------------------------------------------
# Completion Message
# -----------------------------------------------------------------------------
echo ""
echo "=== Setup Complete ==="
echo ""
echo "Configuration:"
echo " - Bridge: $BRIDGE_NAME ($BRIDGE_IP)"
echo " - Guest IP Range: $GUEST_IP_START - $GUEST_IP_END"
echo " - Base Rootfs: ${OUTPUT_DIR}/base-rootfs.${ROOTFS_EXT}"
echo " - SSH key: ${OUTPUT_DIR}/vm.id_rsa"
echo ""
echo "Create VMs with:"
echo " ./create-vm.sh <name> [vcpu] [memory_mib]"
echo ""
echo "Examples:"
echo " ./create-vm.sh vm1 # default resources, auto IP"
echo " ./create-vm.sh vm2 2 2048 # 2 vCPU, 2048MiB, auto IP"
echo ""
