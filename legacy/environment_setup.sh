#!/bin/bash
# ======================================================================
# ARCHIVED — Historical script from an earlier project phase.
# This script modifies /etc/sysctl.conf and references scripts/tools
# that no longer exist. Do NOT run this script.
# Use `mvm host init` for current host setup.
# ======================================================================
set -e

# Global Environment Setup for Firecracker VMs
# This script sets up system-wide configurations required for both single and multi-VM setups
# Run this ONCE before using single-vm or multi-vm

echo "=== Firecracker Environment Setup ==="

# [1/3] Enable IP forwarding
echo -n "Checking IPv4 forwarding... "
IP_FORWARD_STATUS=$(sysctl -n net.ipv4.ip_forward)
if [ "$IP_FORWARD_STATUS" = "1" ]; then
  echo "already enabled"
else
  echo "enabling..."
  echo "Setting net.ipv4.ip_forward=1"
  sudo sysctl -w net.ipv4.ip_forward=1

  # Make persistent across reboots
  if grep -q "^net.ipv4.ip_forward=" /etc/sysctl.conf 2>/dev/null; then
    sudo sed -i 's/^net.ipv4.ip_forward=.*/net.ipv4.ip_forward=1/' /etc/sysctl.conf
  else
    echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf >/dev/null
  fi
fi

# [2/3] Check KVM availability
echo -n "Checking KVM... "
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available. Please ensure KVM is enabled in BIOS/UEFI and kvm module is loaded."
  echo "You may need to:"
  echo "  1. Enable hardware virtualization in BIOS/UEFI"
  echo "  2. Install kvm packages: sudo apt install qemu-kvm"
  echo "  3. Add user to kvm group: sudo usermod -a -G kvm $USER"
  exit 1
fi
echo "available"

# [3/3] Check dependencies
echo -n "Checking dependencies... "
MISSING_DEPS=""
for cmd in qemu-img mkisofs curl bc screen ip; do
  if ! command -v "$cmd" &>/dev/null; then
    MISSING_DEPS="$MISSING_DEPS $cmd"
  fi
done

if [ "$MISSING_DEPS" != "" ]; then
  echo "ERROR: Missing dependencies:$MISSING_DEPS"
  echo ""
  echo "Install on Ubuntu/Debian:"
  echo "  sudo apt update"
  echo "  sudo apt install genisoimage curl bc screen iproute2"
  echo "  # genisoimage provides mkisofs"
  echo ""
  echo "Install on Arch Linux:"
  echo "  sudo pacman -S cdrtools bc curl iproute2 qemu-base screen"
  echo "  # cdrtools provides mkisofs"
  exit 1
fi
echo "all present"

# [4/4] Generate SSH keys
echo -n "Checking SSH keys... "
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_DIR="$SCRIPT_DIR/assets/keys"
mkdir -p "$KEYS_DIR"

# Generate default SSH key if it doesn't exist
if [ ! -f "$KEYS_DIR/id_rsa" ]; then
  echo "generating..."
  ssh-keygen -f "$KEYS_DIR/id_rsa" -N "" -q
  echo "✓ SSH key generated at $KEYS_DIR/id_rsa"
else
  echo "already exists"
fi

echo ""
echo "=== Environment Setup Complete ==="
echo ""
echo "Global system configuration:"
echo "  ✓ IPv4 forwarding enabled"
echo "  ✓ KVM available"
echo "  ✓ All dependencies present"
echo ""
echo "This setup is PERSISTENT and you should NOT need to run this again."
echo ""
echo "Next steps:"
echo "  cd assets && ./download-assets.sh"
echo "  cd ../single-vm && ./setup.sh && ./start-vm.sh"
echo "or"
echo " cd ../multi-vm && ./setup.sh && ./create-vm.sh vm1"
