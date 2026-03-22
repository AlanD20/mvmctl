#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker VM Setup ==="
echo "Image source : ${IMAGE_SOURCE}"
echo "Kernel       : ${KERNEL_PATH}"
echo "Rootfs       : ${ROOTFS_PATH}"
echo ""

# -----------------------------------------------------------------------------
# Step 1: Check dependencies
# -----------------------------------------------------------------------------
echo "[1/6] Checking dependencies..."

for cmd in mkisofs mount umount sudo ip iptables; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: Required command '$cmd' is not installed"
    exit 1
  fi
done

if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM not available (/dev/kvm not found)"
  exit 1
fi

echo " - Dependencies OK"

# -----------------------------------------------------------------------------
# Step 2: Check assets
# -----------------------------------------------------------------------------
echo "[2/6] Checking assets..."

if [ ! -f "$KERNEL_PATH" ]; then
  echo "ERROR: Kernel not found at $KERNEL_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

if [ ! -f "$ROOTFS_PATH" ]; then
  echo "ERROR: Rootfs not found at $ROOTFS_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

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
# Step 3: Set up VM environment
# -----------------------------------------------------------------------------
echo "[3/6] Setting up VM environment..."

mkdir -p "$OUTPUT_DIR"

# Derive the rootfs filename extension from the source path (e.g. ext4, btrfs)
ROOTFS_EXT="${ROOTFS_PATH##*.}"
ROOTFS_DEST="${OUTPUT_DIR}/rootfs.${ROOTFS_EXT}"

if [ ! -f "$ROOTFS_DEST" ]; then
  echo " - Copying rootfs..."
  cp "$ROOTFS_PATH" "$ROOTFS_DEST"
  echo " - Copying SSH key..."
  cp "$SSH_KEY_SOURCE" "${OUTPUT_DIR}/vm.id_rsa"
  chmod 600 "${OUTPUT_DIR}/vm.id_rsa"
fi

echo " - VM environment ready"

# -----------------------------------------------------------------------------
# Step 4: Create cloud-init
# -----------------------------------------------------------------------------
echo "[4/6] Creating cloud-init..."

cp -r cloud-init "${OUTPUT_DIR}"

INSTANCE_ID="i-$(head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12)"

cat > "${OUTPUT_DIR}/cloud-init/meta-data" <<EOF
instance-id: ${INSTANCE_ID}
local-hostname: ${VM_NAME}
EOF

cat > "${OUTPUT_DIR}/cloud-init/network-config" <<EOF
version: 2
ethernets:
  eth0:
    dhcp4: false
    dhcp6: false
    addresses:
      - ${GUEST_IP}/30
    routes:
      - to: default
        via: ${HOST_IP}
    nameservers:
      addresses:
        - 1.1.1.1
        - 8.8.8.8
EOF

PUB_KEY_CONTENT=""
[ -f "$SSH_PUB_KEY_SOURCE" ] && PUB_KEY_CONTENT=$(cat "$SSH_PUB_KEY_SOURCE")

if [ -f "cloud-init/user-data" ]; then
  echo " - Using custom cloud-init/user-data..."
  sed \
    -e "s|# SSH keys will be injected here by setup.sh from assets/keys/|${PUB_KEY_CONTENT}|g" \
    -e "s|# Root SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
    -e "s|# User SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
    -e "s|HOSTNAME_PLACEHOLDER|${VM_NAME}|g" \
    "cloud-init/user-data" > "${OUTPUT_DIR}/cloud-init/user-data"
fi

echo " - Embedding cloud-init into rootfs..."

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

# Inject cloud-init seed files
sudo mkdir -p "$MOUNT_DIR/var/lib/cloud/seed/nocloud"
sudo mkdir -p "$MOUNT_DIR/etc/cloud/cloud.cfg.d"
sudo cp -r "${OUTPUT_DIR}/cloud-init/"* "$MOUNT_DIR/var/lib/cloud/seed/nocloud/"
sudo chmod 644 "$MOUNT_DIR"/var/lib/cloud/seed/nocloud/*

# Comment out /boot/efi entries — present by default on some images (e.g. Debian Bookworm)
# Firecracker doesn't expose an EFI partition so leaving this uncommented causes boot failure
sudo sed -i '/boot\/efi/s/^/#/' "$MOUNT_DIR/etc/fstab"

sudo umount "$MOUNT_DIR"
sudo rmdir "$MOUNT_DIR" 2>/dev/null || true

echo " - Cloud-init embedded at /var/lib/cloud/seed/nocloud/"

# -----------------------------------------------------------------------------
# Step 5: Set up network
# -----------------------------------------------------------------------------
echo "[5/6] Setting up network..."

if [ "$(sysctl -n net.ipv4.ip_forward)" != "1" ]; then
  echo "ERROR: IP forwarding is not enabled. Run ../environment_setup.sh first."
  exit 1
fi

if ip link show "$TAP_DEV" &>/dev/null; then
  echo " - Tap device $TAP_DEV already exists, removing..."
  sudo ip link del "$TAP_DEV" 2>/dev/null || true
fi

sudo ip tuntap add dev "$TAP_DEV" mode tap
sudo ip link set dev "$TAP_DEV" up
sudo ip addr add "${HOST_IP}/30" dev "$TAP_DEV"
sudo sysctl -w net.ipv4.conf."$TAP_DEV".proxy_arp=1 >/dev/null

DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -z "$DEFAULT_IFACE" ]; then
  echo "ERROR: Could not detect default network interface."
  exit 1
fi

sudo iptables -t nat -A POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE
sudo iptables -A FORWARD -i "$TAP_DEV" -o "$DEFAULT_IFACE" -j ACCEPT
sudo iptables -A FORWARD -i "$DEFAULT_IFACE" -o "$TAP_DEV" -j ACCEPT

echo " - Network configured (${GUEST_IP}/30 via ${TAP_DEV})"

# -----------------------------------------------------------------------------
# Step 6: Generate VM configuration
# -----------------------------------------------------------------------------
echo "[6/6] Generating VM configuration..."

ROOTFS_ABS_PATH="${SCRIPT_DIR}/${ROOTFS_DEST}"
KERNEL_ABS_PATH="${SCRIPT_DIR}/${KERNEL_PATH}"

# Detect rootfs filesystem type
ROOTFS_TYPE="ext4"
if command -v file &>/dev/null; then
  FS_INFO=$(file -b "$ROOTFS_ABS_PATH" 2>/dev/null || true)
  for fs in btrfs xfs ext2 ext3; do
    echo "$FS_INFO" | grep -qi "$fs" && ROOTFS_TYPE="$fs" && break
  done
fi

echo " - Detected filesystem: $ROOTFS_TYPE"

BOOT_ARGS="console=ttyS0 reboot=k panic=1 ip=${GUEST_IP}::${HOST_IP}:${MASK}::eth0:off root=/dev/vda rw rootwait rootfstype=${ROOTFS_TYPE} ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/ lsm=${BOOT_ARG_LSM_FLAGS}"

cat > "${OUTPUT_DIR}/firecracker.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "${KERNEL_ABS_PATH}",
    "boot_args": "${BOOT_ARGS}",
    "initrd_path": null
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "${ROOTFS_ABS_PATH}",
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
      "guest_mac": "${MAC}",
      "host_dev_name": "${TAP_DEV}"
    }
  ],
  "machine-config": {
    "vcpu_count": ${VM_VCPU},
    "mem_size_mib": ${VM_MEM_MIB},
    "smt": false,
    "cpu_template": null
  },
  "cpu-config": null,
  "balloon": null,
  "vsock": null,
  "logger": {
    "log_path": "${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.log",
    "level": "debug",
    "show_level": true,
    "show_log_origin": true
  },
  "metrics": {
    "metrics_path": "${OUTPUT_DIR}/firecracker.metrics"
  }
}
EOF

echo " - Config written to ${OUTPUT_DIR}/firecracker.json"

# -----------------------------------------------------------------------------

echo ""
echo "=== Setup Complete ==="
echo ""
echo "VM configuration:"
echo " - Kernel  : ${KERNEL_PATH}"
echo " - Rootfs  : ${DISK_SIZE} ${ROOTFS_TYPE}"
echo " - SSH key : ${OUTPUT_DIR}/vm.id_rsa"
echo " - vCPUs   : ${VM_VCPU}"
echo " - Memory  : ${VM_MEM_MIB} MiB"
echo " - Network : ${GUEST_IP}/30 via ${TAP_DEV}"
echo ""
echo "Run: ./create-vm.sh"
echo "Log: cat ${OUTPUT_DIR}/firecracker.log"
echo ""
