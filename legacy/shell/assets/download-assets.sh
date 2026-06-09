#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="$SCRIPT_DIR"
cd "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

if [ ! -f "config.env" ]; then
  echo "ERROR: config.env not found"
  exit 1
fi
source config.env

ARCH="x86_64"
KEYS_DIR="keys"

# KERNEL_PATH must be set in config.env — it is the single shared vmlinux for
# all distros. No per-distro kernels are downloaded or managed here.
if [ -z "$KERNEL_PATH" ]; then
  echo "ERROR: KERNEL_PATH not set in config.env"
  exit 1
fi

# ---------------------------------------------------------------------------
# Utility
# ---------------------------------------------------------------------------

download() {
  local dest="$1" url="$2"
  echo " - Downloading $(basename "$url")..."
  curl -fsSL -o "$dest" "$url"
}

download_if_absent() {
  [ -f "$1" ] || download "$1" "$2"
}

fix_owner() {
  sudo chown "$USER:$USER" "$1"
}

# ---------------------------------------------------------------------------
# Firecracker binary
# ---------------------------------------------------------------------------

download_firecracker() {
  echo "[firecracker] Checking binary..."

  if [ -f "$FIRECRACKER_PATH" ]; then
    echo "✓ Already present: $FIRECRACKER_PATH"
    return
  fi

  mkdir -p "$(dirname "$FIRECRACKER_PATH")"

  local url="https://github.com/firecracker-microvm/firecracker/releases/download/${FULL_VERSION}/firecracker-${FULL_VERSION}-${ARCH}.tgz"
  local tmp_dir
  tmp_dir=$(mktemp -d)

  echo " - Downloading Firecracker ${FULL_VERSION}..."
  if curl -fsSL "$url" | tar xz -C "$tmp_dir" 2>/dev/null; then
    local fc_bin jailer_bin
    fc_bin=$(find    "$tmp_dir" -name "firecracker-v*-${ARCH}" -type f ! -name "*.debug" | head -1)
    jailer_bin=$(find "$tmp_dir" -name "jailer-v*-${ARCH}"     -type f ! -name "*.debug" | head -1)

    [ -n "$fc_bin"     ] && cp "$fc_bin"     "$FIRECRACKER_PATH" && chmod +x "$FIRECRACKER_PATH" && echo "✓ $FIRECRACKER_PATH"
    [ -n "$jailer_bin" ] && cp "$jailer_bin" "$JAILER_PATH"      && chmod +x "$JAILER_PATH"      && echo "✓ $JAILER_PATH"
    [ -z "$fc_bin"     ] && echo "⚠ Could not find firecracker binary in archive"
  else
    echo "⚠ Download failed. Get it manually: https://github.com/firecracker-microvm/firecracker/releases"
  fi

  rm -rf "$tmp_dir"
}

# ---------------------------------------------------------------------------
# Firecracker CI kernel (vmlinux) — only used by the default CI image path
# ---------------------------------------------------------------------------

download_kernel_firecracker_ci() {
  echo "[kernel] Checking Firecracker CI vmlinux..."

  if [ -f "$KERNEL_PATH" ]; then
    echo "✓ Already present: $KERNEL_PATH"
    return
  fi

  mkdir -p "$(dirname "$KERNEL_PATH")"

  local list_url="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/${CI_VERSION}/${ARCH}/vmlinux-&list-type=2"
  local key
  key=$(curl -s "$list_url" \
    | grep -oP "(?<=<Key>)(firecracker-ci/${CI_VERSION}/${ARCH}/vmlinux-[0-9]+\.[0-9]+\.[0-9]{1,3})(?=</Key>)" \
    | sort -V | tail -1)

  if [ -z "$key" ]; then
    echo "ERROR: Could not find a vmlinux for CI version $CI_VERSION"
    exit 1
  fi

  echo " - Downloading: $key"
  wget -q -O "$KERNEL_PATH" "https://s3.amazonaws.com/spec.ccfc.min/$key"
  chmod +x "$KERNEL_PATH"
  echo "✓ Kernel saved: $KERNEL_PATH"
}

# ---------------------------------------------------------------------------
# Firecracker CI Ubuntu rootfs (squashfs -> ext4)
# ---------------------------------------------------------------------------

download_rootfs_firecracker_ci() {
  echo "[rootfs] Downloading Firecracker CI Ubuntu rootfs..."

  local list_url="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/${CI_VERSION}/${ARCH}/ubuntu-&list-type=2"
  local key
  key=$(curl -s "$list_url" \
    | grep -oP "(?<=<Key>)(firecracker-ci/${CI_VERSION}/${ARCH}/ubuntu-[0-9]+\.[0-9]+\.squashfs)(?=</Key>)" \
    | sort -V | tail -1)

  if [ -z "$key" ]; then
    echo "ERROR: Could not find Ubuntu rootfs for CI version $CI_VERSION"
    exit 1
  fi

  local ubuntu_ver
  ubuntu_ver=$(basename "$key" .squashfs | grep -oE '[0-9]+\.[0-9]+')
  local squashfs_file="/tmp/ubuntu-${ubuntu_ver}.squashfs"
  local rootfs_out="images/ubuntu-${ubuntu_ver}.ext4"

  if [ -f "$rootfs_out" ]; then
    echo "✓ Already present: $rootfs_out"
    return
  fi

  mkdir -p images

  echo " - Downloading Ubuntu ${ubuntu_ver} squashfs..."
  wget -q -O "$squashfs_file" "https://s3.amazonaws.com/spec.ccfc.min/$key"

  local tmp_dir
  tmp_dir=$(mktemp -d)
  echo " - Extracting squashfs..."
  cd "$tmp_dir"
  unsquashfs "$squashfs_file" >/dev/null

  # Inject SSH public keys
  mkdir -p squashfs-root/root/.ssh
  if [ -d "$ASSETS_DIR/$KEYS_DIR" ]; then
    for pub_key in "$ASSETS_DIR/$KEYS_DIR"/*.pub; do
      [ -f "$pub_key" ] || continue
      cat "$pub_key" >> squashfs-root/root/.ssh/authorized_keys
      echo " - Added key: $(basename "$pub_key")"
    done
    chmod 600 squashfs-root/root/.ssh/authorized_keys
  fi

  echo " - Building ext4 image..."
  sudo chown -R root:root squashfs-root
  truncate -s 1G "ubuntu-${ubuntu_ver}.ext4"
  sudo mkfs.ext4 -d squashfs-root -F "ubuntu-${ubuntu_ver}.ext4" >/dev/null

  cp "ubuntu-${ubuntu_ver}.ext4" "$ASSETS_DIR/$rootfs_out"
  cd "$ASSETS_DIR"
  sudo rm -rf "$tmp_dir" "$squashfs_file"
  fix_owner "$rootfs_out"

  echo "✓ Rootfs saved: $rootfs_out"
}

# ---------------------------------------------------------------------------
# Ubuntu Cloud image (tar archive -> ext4)
# ---------------------------------------------------------------------------

download_ubuntu() {
  echo "=== Ubuntu Cloud Image ==="
  echo "    Version : $UBUNTU_VERSION"
  echo "    Size    : $IMAGE_SIZE"
  echo ""

  if [ -z "$UBUNTU_VERSION" ]; then
    echo "ERROR: UBUNTU_VERSION not set in config.env"
    exit 1
  fi

  local dl_dir="images/${UBUNTU_VERSION}/download"
  local rootfs_out="images/${UBUNTU_VERSION}.ext4"
  local base_url="https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current"
  local image_tar="${UBUNTU_VERSION}-server-cloudimg-amd64-root.tar.xz"
  mkdir -p "$dl_dir"

  echo "[1/2] Downloading Ubuntu Cloud image..."
  download_if_absent "${dl_dir}/${image_tar}" "${base_url}/${image_tar}"

  if [ -f "$rootfs_out" ]; then
    echo "✓ Rootfs already present: $rootfs_out"
    return
  fi

  echo "[2/2] Building ext4 rootfs..."
  truncate -s "$IMAGE_SIZE" "$rootfs_out"
  mkfs.ext4 "$rootfs_out" >/dev/null 2>&1

  local mnt=/tmp/.rootfs-$$
  mkdir "$mnt"
  sudo mount "$rootfs_out" -o loop "$mnt"
  sudo tar -xf "${dl_dir}/${image_tar}" --directory "$mnt"
  [ -d "$mnt/etc/cloud" ] && sudo mkdir -p "$mnt/var/lib/cloud/seed/nocloud"
  sudo umount "$mnt"
  rmdir "$mnt"
  fix_owner "$rootfs_out"

  echo "✓ Rootfs saved: $rootfs_out"
}

# ---------------------------------------------------------------------------
# Generic cloud image helper (qcow2 / raw / tar -> plain filesystem image)
#
# For images with a partition table the user is shown fdisk output and asked
# to pick the root partition. The extracted partition is renamed to match its
# detected filesystem type (e.g. arch.btrfs, debian-bookworm.ext4).
#
# Sets global EXTRACTED_ROOTFS to the final image path.
# ---------------------------------------------------------------------------

extract_partition_from_raw() {
  local raw_file="$1"
  local out_file="$2"

  echo " - Inspecting partition layout..."
  local fdisk_out
  fdisk_out=$(sudo fdisk -l "$raw_file" 2>/dev/null)

  local part_lines part_count
  part_lines=$(echo "$fdisk_out" | grep -E "^${raw_file}p?[0-9]" || true)
  part_count=$(echo "$part_lines" | grep -c . || true)

  if [ "$part_count" -eq 0 ]; then
    echo " - No partition table found; using image as-is."
    cp "$raw_file" "$out_file"
    EXTRACTED_ROOTFS="$out_file"
    return
  fi

  # Show layout and prompt for selection
  echo ""
  echo "  -------------------------------------------------------"
  echo "  $(basename "$raw_file") partition layout:"
  echo "  -------------------------------------------------------"
  echo "$fdisk_out"
  echo "  -------------------------------------------------------"
  echo ""
  local i=1
  while IFS= read -r line; do
    printf "  %d) %s\n" "$i" "$line"
    i=$(( i + 1 ))
  done <<< "$part_lines"
  echo ""

  local choice
  while true; do
    read -rp "  Select root partition [1-${part_count}]: " choice
    [[ "$choice" =~ ^[0-9]+$ ]] && [ "$choice" -ge 1 ] && [ "$choice" -le "$part_count" ] && break
    echo "  Please enter a number between 1 and ${part_count}."
  done

  local chosen_line start_sector sector_count
  chosen_line=$(echo "$part_lines" | sed -n "${choice}p")
  start_sector=$(echo "$chosen_line" | awk '{print $2}')
  sector_count=$(echo  "$chosen_line" | awk '{print $4}')
  echo " - Selected: $chosen_line"

  echo " - Extracting partition (start=${start_sector}, sectors=${sector_count})..."
  sudo dd if="$raw_file" of="$out_file" \
    bs=512 skip="$start_sector" count="$sector_count" \
    status=progress 2>&1 | tail -1

  # Detect filesystem type and rename to match (e.g. .btrfs, .ext4)
  local fs_type
  fs_type=$(sudo blkid -o value -s TYPE "$out_file" 2>/dev/null || true)

  if [ -n "$fs_type" ]; then
    echo " - Filesystem detected: $fs_type"
    local final
    final="$(dirname "$out_file")/$(basename "$out_file" .img).${fs_type}"
    [ "$final" != "$out_file" ] && mv "$out_file" "$final"
    EXTRACTED_ROOTFS="$final"
  else
    echo " - Could not detect filesystem type; keeping as .img"
    EXTRACTED_ROOTFS="$out_file"
  fi
  fix_owner "$EXTRACTED_ROOTFS"
}

download_and_convert_image() {
  local distro_name="$1"
  local download_url="$2"
  local output_name="$3"

  echo "=== $distro_name ==="

  local dl_dir="images/${output_name}/download"
  local image_file
  image_file=$(basename "$download_url")
  mkdir -p "$dl_dir"

  # Skip if a converted image already exists for this distro
  local existing
  existing=$(find images/ -maxdepth 1 -name "${output_name}.*" ! -name "*.img" 2>/dev/null | head -1)
  if [ -n "$existing" ]; then
    echo "✓ Rootfs already present: $existing"
    return
  fi

  echo "[1/2] Downloading $distro_name image..."
  download_if_absent "${dl_dir}/${image_file}" "$download_url"

  echo "[2/2] Converting to filesystem image..."
  local tmp_out="images/${output_name}.img"
  EXTRACTED_ROOTFS="$tmp_out"

  if [[ "$image_file" == *.qcow2 ]]; then
    local raw_file="${dl_dir}/${image_file%.qcow2}.raw"
    echo " - Converting qcow2 -> raw..."
    qemu-img convert -f qcow2 -O raw "${dl_dir}/${image_file}" "$raw_file"
    extract_partition_from_raw "$raw_file" "$tmp_out"
  elif [[ "$image_file" == *.raw || "$image_file" == *.img ]]; then
    extract_partition_from_raw "${dl_dir}/${image_file}" "$tmp_out"
  else
    # Tar archive fallback
    truncate -s "${IMAGE_SIZE:-2G}" "$tmp_out"
    mkfs.ext4 "$tmp_out" >/dev/null 2>&1
    local mnt=/tmp/.rootfs-$$
    mkdir "$mnt"
    sudo mount "$tmp_out" -o loop "$mnt"
    sudo tar -xf "${dl_dir}/${image_file}" --directory "$mnt"
    sudo umount "$mnt"
    rmdir "$mnt"
  fi

  echo "✓ Rootfs saved: $EXTRACTED_ROOTFS"
}

# ---------------------------------------------------------------------------
# Per-distro wrappers
# ---------------------------------------------------------------------------

download_arch_linux() {
  download_and_convert_image \
    "Arch Linux" \
    "https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2" \
    "archlinux"
}

download_debian() {
  local ver="${1:-bookworm}"
  download_and_convert_image \
    "Debian $ver" \
    "https://saimei.ftp.acc.umu.se/images/cloud/${ver}/latest/debian-12-generic-amd64.qcow2" \
    "debian-${ver}"
}

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

show_help() {
  cat <<'EOF'

=== Custom Image Support ===

Set IMAGE_SOURCE in config.env (or inline) and re-run:

  IMAGE_SOURCE=ubuntu       ./download-assets.sh
  IMAGE_SOURCE=archlinux    ./download-assets.sh
  IMAGE_SOURCE=debian       ./download-assets.sh

To add a distro manually:
  1. Download a .qcow2 / .raw / .img cloud image
  2. qemu-img convert -f qcow2 -O raw source.qcow2 dest.raw
  3. Place the result in assets/images/
  4. Set KERNEL_PATH in config.env to your vmlinux

See custom-images.md for details.
EOF
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

detect_latest_ci_version() {
  echo "[version] Detecting latest Firecracker CI release..."
  local latest
  latest=$(basename "$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    https://github.com/firecracker-microvm/firecracker/releases/latest)")
  FULL_VERSION="$latest"
  CI_VERSION="${latest%.*}"
  echo "✓ Version: $FULL_VERSION  (CI series: $CI_VERSION)"
}

main() {
  echo "========================================"
  echo " Firecracker Assets Setup"
  echo " Image source : ${IMAGE_SOURCE:-firecracker-ci}"
  echo " Kernel       : $KERNEL_PATH"
  echo "========================================"
  echo ""

  case "$IMAGE_SOURCE" in
    ubuntu)
      download_ubuntu
      ;;
    archlinux)
      download_arch_linux
      ;;
    debian)
      download_debian
      ;;
    *)
      # Default: official Firecracker CI images (Ubuntu squashfs + vmlinux)
      detect_latest_ci_version
      download_kernel_firecracker_ci
      download_rootfs_firecracker_ci
      ;;
  esac

  download_firecracker

  echo ""
  echo "========================================"
  echo " Done — $ASSETS_DIR"
  echo "   Kernel     : $KERNEL_PATH"
  echo "   Firecracker: $FIRECRACKER_PATH"
  echo "   Jailer     : $JAILER_PATH"
  echo "========================================"
  echo ""
  show_help
}

main "$@"
