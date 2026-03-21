#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

# =============================================================================
# PARSE ARGUMENTS
# =============================================================================
VM_NAME="${1:-}"
VM_VCPU="${2:-2}"
VM_MEM_MIB="${3:-2048}"

if [ -z "$VM_NAME" ]; then
  echo "Usage: $0 <name> [vcpu] [memory_mib]"
  echo ""
  echo "Arguments:"
  echo "  name         - VM name (required)"
  echo "  vcpu         - Number of vCPUs (default: 2)"
  echo "  memory_mib   - Memory in MiB (default: 2048)"
  echo ""
  echo "Examples:"
  echo "  $0 vm1                    # 2 vCPU, 2048MiB, auto IP"
  echo "  $0 vm2 1 1024           # 1 vCPU, 1024MiB, auto IP"
  exit 1
fi

VM_DIR="${OUTPUT_DIR}/${VM_NAME}"

# =============================================================================
# VALIDATION
# =============================================================================

# Check VM doesn't already exist
if [ -d "$VM_DIR" ]; then
  echo "ERROR: VM '$VM_NAME' already exists at $VM_DIR"
  exit 1
fi

# Check KVM available
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available"
  exit 1
fi

# Check bridge exists
if ! ip link show "$BRIDGE_NAME" &>/dev/null; then
  echo "ERROR: Bridge $BRIDGE_NAME does not exist. Run ./setup.sh first"
  exit 1
fi

# Check base rootfs exists
if [ ! -f "${OUTPUT_DIR}/base-rootfs.ext4" ]; then
  echo "ERROR: base-rootfs.ext4 not found. Run ./setup.sh first"
  exit 1
fi

# =============================================================================
# ASSIGN IP ADDRESS
# =============================================================================

# Extract network prefix (e.g., 10.20.0 from 10.20.0.1/24)
NETWORK_PREFIX=$(echo "$BRIDGE_IP" | cut -d. -f1-3)

# Auto-assign IP
VM_IP=""
for i in $(seq 2 254); do
  IP="${NETWORK_PREFIX}.${i}"
  # Check if IP is already used in existing VMs
  IP_IN_USE=false
  while IFS= read -r config_file; do
    if [ -f "$config_file" ] && grep -q "ip=${IP}::" "$config_file" 2>/dev/null; then
      IP_IN_USE=true
      break
    fi
  done < <(find ${OUTPUT_DIR} -name "firecracker.json" 2>/dev/null)
  if [ "$IP_IN_USE" = "false" ]; then
    VM_IP="$IP"
    break
  fi
done

if [ -z "$VM_IP" ]; then
  echo "ERROR: No available IPs in pool"
  exit 1
fi

# Generate MAC address (6 bytes total: 02:FC + 4 random bytes)
MAC_BYTES=$(printf "%02x%02x%02x%02x" $((RANDOM % 256)) $((RANDOM % 256)) $((RANDOM % 256)) $((RANDOM % 256)))
GUEST_MAC="02:FC:${MAC_BYTES:0:2}:${MAC_BYTES:2:2}:${MAC_BYTES:4:2}:${MAC_BYTES:6:2}"
TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"

echo "=== Creating VM: $VM_NAME ==="
echo " - vCPUs: $VM_VCPU"
echo " - Memory: ${VM_MEM_MIB} MiB"
echo " - IP: $VM_IP"
echo " - MAC: $GUEST_MAC"
echo " - Tap: $TAP_DEV"

# =============================================================================
# CREATE VM DIRECTORY AND COPY ROOTFS
# =============================================================================

echo " - Setting up VM directory..."
mkdir -p "$VM_DIR"

echo " - Copying rootfs..."
cp "${OUTPUT_DIR}/base-rootfs.ext4" "$VM_DIR/rootfs.ext4"

# =============================================================================
# CREATE CLOUD-INIT
# =============================================================================

echo " - Creating cloud-init..."
mkdir -p "$VM_DIR/cloud-init"

# Create meta-data
INSTANCE_ID="i-$(head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12)"
cat >"$VM_DIR/cloud-init/meta-data" <<EOF
instance-id: ${INSTANCE_ID}
local-hostname: ${VM_NAME}
EOF

# Create network-config
cat >"$VM_DIR/cloud-init/network-config" <<EOF
version: 2
ethernets:
  eth0:
    dhcp4: false
    dhcp6: false
    addresses:
      - ${VM_IP}/24
    routes:
      - to: default
        via: ${NETWORK_PREFIX}.1
    nameservers:
      addresses:
        - 1.1.1.1
        - 8.8.8.8
EOF

# Copy and customize user-data with SSH keys
echo " - Injecting SSH keys..."
SSH_PUB_KEY_SOURCE="../assets/keys/id_rsa.pub"

if [ -f "$SSH_PUB_KEY_SOURCE" ]; then
  PUB_KEY_CONTENT=$(cat "$SSH_PUB_KEY_SOURCE")
  if [ -f "cloud-init/user-data" ]; then
    # Inject SSH keys into user-data
    sed -e "s|# SSH keys will be injected here by setup.sh from assets/keys/|${PUB_KEY_CONTENT}|g" \
      -e "s|# Root SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
      -e "s|# Ubuntu user SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
      "cloud-init/user-data" >"$VM_DIR/cloud-init/user-data"
  fi
else
  echo " - Warning: SSH public key not found at $SSH_PUB_KEY_SOURCE"
  if [ -f "cloud-init/user-data" ]; then
    cp "cloud-init/user-data" "$VM_DIR/cloud-init/user-data"
  fi
fi

# =============================================================================
# EMBED CLOUD-INIT INTO ROOTFS
# =============================================================================

echo " - Embedding cloud-init into rootfs..."

MOUNT_DIR="$VM_DIR/mnt-rootfs"
mkdir -p "$MOUNT_DIR"

# Try to mount
if mount "$VM_DIR/rootfs.ext4" "$MOUNT_DIR" 2>/dev/null || sudo mount "$VM_DIR/rootfs.ext4" "$MOUNT_DIR"; then
  # Create cloud-init seed directory
  sudo mkdir -p "$MOUNT_DIR/var/lib/cloud/seed/nocloud"
  sudo mkdir -p "$MOUNT_DIR/etc/cloud/cloud.cfg.d"

  # Copy cloud-init files
  sudo cp -r "$VM_DIR/cloud-init/"* "$MOUNT_DIR/var/lib/cloud/seed/nocloud/"

  # Set permissions
  sudo chmod 644 "$MOUNT_DIR"/var/lib/cloud/seed/nocloud/*

  # Unmount
  umount "$MOUNT_DIR" 2>/dev/null || sudo umount "$MOUNT_DIR"
  sudo rmdir "$MOUNT_DIR" 2>/dev/null || true

  echo " - Cloud-init embedded"
else
  echo " - Warning: Could not mount rootfs, cloud-init will not be available"
fi

# =============================================================================
# CREATE FIRECRACKER CONFIG
# =============================================================================

echo " - Generating Firecracker configuration..."

ROOTFS_ABS_PATH="${SCRIPT_DIR}/${VM_DIR}/rootfs.ext4"
KERNEL_ABS_PATH="${SCRIPT_DIR}/${KERNEL_PATH}"

cat >"$VM_DIR/firecracker.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "${KERNEL_ABS_PATH}",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off ip=${VM_IP}::${NETWORK_PREFIX}.1:255.255.255.0::eth0:off rw rootwait ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/",
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
      "guest_mac": "${GUEST_MAC}",
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
    "log_path": "${SCRIPT_DIR}/${VM_DIR}/firecracker.log",
    "level": "Info",
    "show_level": true,
    "show_log_origin": true
  },
  "metrics": {
    "metrics_path": "${SCRIPT_DIR}/${VM_DIR}/firecracker.metrics"
  }
}
EOF

# =============================================================================
# SETUP NETWORK
# =============================================================================

echo " - Setting up network..."

# Create tap device
if ! ip link show "$TAP_DEV" &>/dev/null; then
  sudo ip tuntap add dev "$TAP_DEV" mode tap
fi

# Attach to bridge
sudo ip link set "$TAP_DEV" master "$BRIDGE_NAME" 2>/dev/null || true
sudo ip link set "$TAP_DEV" up

# =============================================================================
# START VM
# =============================================================================

echo " - Starting Firecracker VM..."

cd "$VM_DIR"

FIRECRACKER_BIN="../../../assets/bin/firecracker"
PID_FILE="firecracker.pid"
SOCKET_FILE="${VM_NAME}.socket"
CONSOLE_LOG="firecracker.console.log"

# Check if already running
if [ -f "$PID_FILE" ]; then
  EXISTING_PID=$(cat "$PID_FILE")
  if kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo "VM '$VM_NAME' is already running (PID: $EXISTING_PID)"
    cd ../..
    exit 0
  fi
fi

# Start Firecracker
if [ "$ENABLE_SOCKET" = "true" ]; then
  nohup "$FIRECRACKER_BIN" --api-sock "$SOCKET_FILE" --config-file firecracker.json >"$CONSOLE_LOG" 2>&1 &
else
  nohup "$FIRECRACKER_BIN" --no-api --config-file firecracker.json >"$CONSOLE_LOG" 2>&1 &
fi

VM_PID=$!
echo "$VM_PID" >"$PID_FILE"

sleep 2

# Verify process started
if ! kill -0 "$VM_PID" 2>/dev/null; then
  echo "ERROR: Firecracker failed to start. Check $VM_DIR/firecracker.console.log"
  cd ../..
  exit 1
fi

cd ../..

# =============================================================================
# SUCCESS MESSAGE
# =============================================================================

echo ""
echo "=========================================="
echo "✓✓✓ VM Created Successfully ✓✓✓"
echo "=========================================="
echo ""
echo "VM Details:"
echo " - Name: $VM_NAME"
echo " - PID: $VM_PID"
echo " - IP: $VM_IP"
echo " - MAC: $GUEST_MAC"
echo " - Tap: $TAP_DEV"
echo " - Directory: $VM_DIR"
echo ""
echo "Commands:"
echo " - Logs: tail -f $VM_DIR/firecracker.console.log"
echo " - Stop: ./stop-vm.sh $VM_NAME"
echo ""
