#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="$(cd "$SCRIPT_DIR" && pwd)"
cd "$SCRIPT_DIR"

# Source configuration
if [ -f "config.env" ]; then
  source config.env
else
  echo "ERROR: config.env not found"
  exit 1
fi

ARCH="x86_64"
KERNEL_OUTPUT="kernels/vmlinux"
ROOTFS_OUTPUT_BASE="images/ubuntu"
KEYS_DIR="keys"

# Helper functions
download() {
  echo "Downloading $2..."
  curl -s -o "$1" "$2"
}

download_if_not_present() {
  [ -f "$1" ] || download "$1" "$2"
}

extract_vmlinux() {
  local kernel_file="$1"
  local output_file="$2"
  echo "Extracting vmlinux from $kernel_file..."
  local extract_linux=/tmp/extract-vmlinux-$$
  curl -s -o "$extract_linux" https://raw.githubusercontent.com/torvalds/linux/master/scripts/extract-vmlinux
  chmod +x "$extract_linux"
  "$extract_linux" "$kernel_file" >"$output_file"
  rm -f "$extract_linux"
}

detect_latest_ci_version() {
  echo "[1/4] Detecting latest Firecracker CI version..."
  local release_url="https://github.com/firecracker-microvm/firecracker/releases"
  local latest_version=$(basename $(curl -fsSLI -o /dev/null -w %{url_effective} ${release_url}/latest))
  CI_VERSION=${latest_version%.*}
  FULL_VERSION="$latest_version"
  echo "✓ Latest CI version: $CI_VERSION (full: $FULL_VERSION)"
}

download_kernel_firecracker_ci() {
  echo "[2/4] Downloading kernel for $CI_VERSION..."

  if [ -f "$KERNEL_OUTPUT" ]; then
    echo "✓ Kernel already exists: $KERNEL_OUTPUT"
    return
  fi

  mkdir -p "$(dirname "$KERNEL_OUTPUT")"

  local kernel_list_url="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/$CI_VERSION/$ARCH/vmlinux-&list-type=2"
  local latest_kernel_key=$(curl -s "$kernel_list_url" |
    grep -oP "(?<=<Key>)(firecracker-ci/$CI_VERSION/$ARCH/vmlinux-[0-9]+\.[0-9]+\.[0-9]{1,3})(?=</Key>)" |
    sort -V | tail -1)

  if [ -z "$latest_kernel_key" ]; then
    echo "ERROR: Could not find kernel for CI version $CI_VERSION"
    exit 1
  fi

  echo " - Downloading kernel: $latest_kernel_key"
  wget -q -O "$KERNEL_OUTPUT" "https://s3.amazonaws.com/spec.ccfc.min/$latest_kernel_key"
  chmod +x "$KERNEL_OUTPUT"
  echo "✓ Kernel downloaded: $KERNEL_OUTPUT"
}

download_and_convert_rootfs_firecracker_ci() {
  echo "[3/4] Downloading and converting rootfs..."

  local rootfs_list_url="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/$CI_VERSION/$ARCH/ubuntu-&list-type=2"
  local latest_ubuntu_key=$(curl -s "$rootfs_list_url" |
    grep -oP "(?<=<Key>)(firecracker-ci/$CI_VERSION/$ARCH/ubuntu-[0-9]+\.[0-9]+\.squashfs)(?=</Key>)" |
    sort -V | tail -1)

  if [ -z "$latest_ubuntu_key" ]; then
    echo "ERROR: Could not find Ubuntu rootfs for CI version $CI_VERSION"
    exit 1
  fi

  local ubuntu_version=$(basename "$latest_ubuntu_key" .squashfs | grep -oE '[0-9]+\.[0-9]+')
  local squashfs_file="/tmp/ubuntu-${ubuntu_version}.squashfs"
  local rootfs_output="${ROOTFS_OUTPUT_BASE}-${ubuntu_version}.ext4"

  if [ -f "$rootfs_output" ]; then
    echo "✓ Rootfs already exists for Ubuntu $ubuntu_version"
    return
  fi

  mkdir -p "$(dirname "$rootfs_output")"

  echo " - Downloading Ubuntu $ubuntu_version rootfs..."
  wget -q -O "$squashfs_file" "https://s3.amazonaws.com/spec.ccfc.min/$latest_ubuntu_key"

  echo " - Extracting squashfs..."
  local temp_dir=$(mktemp -d)
  cd "$temp_dir"
  unsquashfs "$squashfs_file" >/dev/null

  mkdir -p squashfs-root/root/.ssh
  # Add all public keys from assets/keys to authorized_keys
  if [ -d "$ASSETS_DIR/${KEYS_DIR}" ]; then
    for pub_key in "$ASSETS_DIR/${KEYS_DIR}"/*.pub; do
      if [ -f "$pub_key" ]; then
        cat "$pub_key" >>squashfs-root/root/.ssh/authorized_keys
        echo " Added $(basename "$pub_key") to authorized_keys"
      fi
    done
  fi
  chmod 600 squashfs-root/root/.ssh/authorized_keys

  echo " - Creating ext4 filesystem..."
  sudo chown -R root:root squashfs-root
  truncate -s 1G "ubuntu-${ubuntu_version}.ext4"
  sudo mkfs.ext4 -d squashfs-root -F "ubuntu-${ubuntu_version}.ext4" >/dev/null

  cp "ubuntu-${ubuntu_version}.ext4" "$ASSETS_DIR/${rootfs_output}"
  # Keys are already in assets/keys/ directory

  cd "$ASSETS_DIR"
  sudo rm -rf "$temp_dir" "$squashfs_file"

  echo "✓ Rootfs created: $rootfs_output"
  echo "✓ SSH keys in: ${KEYS_DIR}/"
}

download_ubuntu_cloud() {
  echo "=== Ubuntu Cloud Images Setup ==="
  echo "Version: $UBUNTU_VERSION"
  echo "Size: $IMAGE_SIZE"
  echo ""

  local download_dir="images/${UBUNTU_VERSION}/download"
  local rootfs_output="images/${UBUNTU_VERSION}.ext4"
  local kernel_output="kernels/${UBUNTU_VERSION}-vmlinux"
  mkdir -p "$download_dir"

  # Download components
  local image_tar="${UBUNTU_VERSION}-server-cloudimg-amd64-root.tar.xz"
  local kernel="${UBUNTU_VERSION}-server-cloudimg-amd64-vmlinuz-generic"
  local initrd="${UBUNTU_VERSION}-server-cloudimg-amd64-initrd-generic"

  echo "[1/3] Downloading Ubuntu Cloud Image components..."
  download_if_not_present \
    "${download_dir}/${image_tar}" \
    "https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current/${image_tar}"

  download_if_not_present \
    "${download_dir}/${kernel}" \
    "https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current/unpacked/${kernel}"

  download_if_not_present \
    "${download_dir}/${initrd}" \
    "https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current/unpacked/${initrd}"

  # Generate image
  if [ ! -f "$rootfs_output" ]; then
    echo "[2/3] Generating ext4 rootfs..."
    truncate -s "$IMAGE_SIZE" "$rootfs_output"
    mkfs.ext4 "$rootfs_output" >/dev/null 2>&1

    local tmppath=/tmp/.rootfs-$$
    mkdir "$tmppath"
    sudo mount "$rootfs_output" -o loop "$tmppath"
    sudo tar -xf "${download_dir}/${image_tar}" --directory "$tmppath"

    #
    # Extract is done via sudo, therefore anything here requires sudo
    #

    # Ensure cloud-init is installed and enabled
    if [ -d "$tmppath/etc/cloud" ]; then
      echo " Cloud-init configuration present"
      # Ensure nocloud datasource is available
      sudo mkdir -p "$tmppath/var/lib/cloud/seed/nocloud"
    fi

    sudo umount "$tmppath"
    rmdir "$tmppath"
    echo "✓ Rootfs created: $rootfs_output"
  else
    echo "✓ Rootfs already exists: $rootfs_output"
  fi

  # Extract vmlinux
  if [ ! -f "$kernel_output" ]; then
    echo "[3/3] Extracting vmlinux from kernel..."
    extract_vmlinux "${download_dir}/${kernel}" "$kernel_output"
    echo "✓ Kernel extracted: $kernel_output"
  else
    echo "✓ Kernel already exists: $kernel_output"
  fi

  # Create symlink for initrd if needed
  if [ ! -f "images/${UBUNTU_VERSION}-initrd" ]; then
    ln -s "download/${initrd}" "images/${UBUNTU_VERSION}-initrd" 2>/dev/null || true
  fi

  echo ""
  echo "Ubuntu Cloud Image Setup Complete:"
  echo " - Rootfs: $rootfs_output"
  echo " - Kernel: $kernel_output"
}

download_firecracker() {
  echo "[4/4] Checking Firecracker binary..."
  if [ -f "bin/firecracker" ]; then
    echo "✓ Firecracker already exists"
    return
  fi

  mkdir -p bin

  # Use full version for download URL (e.g., v1.11.0 not v1.11)
  local download_url="https://github.com/firecracker-microvm/firecracker/releases/download/${FULL_VERSION}/firecracker-${FULL_VERSION}-${ARCH}.tgz"
  local temp_dir=$(mktemp -d)

  echo " - Downloading Firecracker ${FULL_VERSION}..."
  echo " URL: $download_url"
  if curl -sL "$download_url" | tar xz -C "$temp_dir" 2>/dev/null; then
    # Find the actual firecracker and jailer binaries (they have version in name)
    # Exclude YAML files, debug binaries, and spec files
    local fc_bin=$(find "$temp_dir" -name "firecracker-v*-${ARCH}" -type f ! -name "*.debug" | head -1)
    local jailer_bin=$(find "$temp_dir" -name "jailer-v*-${ARCH}" -type f ! -name "*.debug" | head -1)
    if [ -n "$fc_bin" ]; then
      cp "$fc_bin" bin/firecracker
      chmod +x bin/firecracker
      echo "✓ Firecracker downloaded"
    else
      echo "⚠️ Could not find firecracker binary in archive"
    fi
    if [ -n "$jailer_bin" ]; then
      cp "$jailer_bin" bin/jailer
      chmod +x bin/jailer
      echo "✓ Jailer downloaded"
    fi
    rm -rf "$temp_dir"
  else
    rm -rf "$temp_dir"
    echo "⚠️ Could not download Firecracker ${FULL_VERSION}, continuing without it"
    echo " You may need to download manually from:"
    echo " https://github.com/firecracker-microvm/firecracker/releases"
  fi
}

# Helper function to download and convert any cloud image
download_and_convert_image() {
  local distro_name="$1"
  local download_url="$2"
  local output_name="$3"
  local kernel_url="$4"

  echo "=== $distro_name Cloud Image Setup ==="

  local download_dir="images/${output_name}/download"
  local rootfs_output="images/${output_name}.ext4"
  local kernel_output="kernels/${output_name}-vmlinux"
  mkdir -p "$download_dir"

  # Download rootfs
  local image_file=$(basename "$download_url")
  echo "[1/3] Downloading $distro_name image..."
  download_if_not_present "${download_dir}/${image_file}" "$download_url"

  # Convert/generate image
  if [ ! -f "$rootfs_output" ]; then
    echo "[2/3] Converting to ext4..."

    # Check if it's a qcow2 or already raw
    if [[ "$image_file" == *.qcow2 ]]; then
      qemu-img convert -f qcow2 -O raw "${download_dir}/${image_file}" "$rootfs_output"
    elif [[ "$image_file" == *.raw ]] || [[ "$image_file" == *.img ]]; then
      cp "${download_dir}/${image_file}" "$rootfs_output"
    else
      # Assume tar archive (like Ubuntu cloud images)
      truncate -s "${IMAGE_SIZE:-2G}" "$rootfs_output"
      mkfs.ext4 "$rootfs_output" >/dev/null 2>&1

      local tmppath=/tmp/.rootfs-$$
      mkdir "$tmppath"
      sudo mount "$rootfs_output" -o loop "$tmppath"
      sudo tar -xf "${download_dir}/${image_file}" --directory "$tmppath"
      sudo umount "$tmppath"
      rmdir "$tmppath"
    fi

    echo "✓ Rootfs created: $rootfs_output"
  else
    echo "✓ Rootfs already exists: $rootfs_output"
  fi

  # Download or extract kernel
  if [ -n "$kernel_url" ] && [ ! -f "$kernel_output" ]; then
    echo "[3/3] Setting up kernel..."
    if [[ "$kernel_url" == *.vmlinuz* ]] || [[ "$kernel_url" == *vmlinuz* ]]; then
      # Compressed kernel, need to extract
      local kernel_file=$(basename "$kernel_url")
      download_if_not_present "${download_dir}/${kernel_file}" "$kernel_url"
      extract_vmlinux "${download_dir}/${kernel_file}" "$kernel_output"
    else
      # Already extracted vmlinux
      download_if_not_present "$kernel_output" "$kernel_url"
    fi
    echo "✓ Kernel ready: $kernel_output"
  elif [ -f "$KERNEL_OUTPUT" ] && [ ! -f "$kernel_output" ]; then
    # Fallback to generic kernel
    cp "$KERNEL_OUTPUT" "$kernel_output"
    echo "✓ Using generic kernel: $kernel_output"
  else
    echo "✓ Kernel already exists: $kernel_output"
  fi

  echo ""
  echo "$distro_name Setup Complete:"
  echo " - Rootfs: $rootfs_output"
  echo " - Kernel: $kernel_output"
}

download_arch_linux() {
  download_and_convert_image \
    "Arch Linux" \
    "https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2" \
    "arch"
}

download_debian() {
  local debian_version="${1:-bookworm}"
  download_and_convert_image \
    "Debian $debian_version" \
    "https://saimei.ftp.acc.umu.se/images/cloud/${debian_version}/latest/debian-12-generic-amd64.qcow2" \
    "debian-${debian_version}"
}

download_almalinux() {
  download_and_convert_image \
    "AlmaLinux 9" \
    "https://repo.almalinux.org/almalinux/9/BaseOS/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2" \
    "almalinux-9"
}

download_fedora() {
  local fedora_version="${1:-39}"
  download_and_convert_image \
    "Fedora $fedora_version" \
    "https://download.fedoraproject.org/pub/fedora/linux/releases/${fedora_version}/Cloud/x86_64/images/Fedora-Cloud-Base-${fedora_version}-latest.x86_64.qcow2" \
    "fedora-${fedora_version}"
}

show_custom_image_help() {
  echo ""
  echo "=== Custom Image Support ==="
  echo ""
  echo "To download other distributions, set IMAGE_SOURCE and run again:"
  echo ""
  echo "  IMAGE_SOURCE=arch-linux ./download-assets.sh"
  echo "  IMAGE_SOURCE=debian ./download-assets.sh"
  echo "  IMAGE_SOURCE=almalinux ./download-assets.sh"
  echo "  IMAGE_SOURCE=fedora ./download-assets.sh"
  echo ""
  echo "Or manually download and convert any cloud image:"
  echo "  1. Download .qcow2, .raw, or .img file"
  echo "  2. Convert: qemu-img convert -f qcow2 -O raw source.qcow2 destination.ext4"
  echo "  3. Place in assets/images/"
  echo "  4. Update config.env to point to it"
  echo ""
  echo "See custom-images.md for detailed instructions."
}

main() {
  echo "=== Firecracker Assets Setup ==="
  echo "Image Source: $IMAGE_SOURCE"
  echo ""

  case "$IMAGE_SOURCE" in
  "ubuntu-cloud")
    if [ -z "$UBUNTU_VERSION" ]; then
      echo "ERROR: UBUNTU_VERSION not set in config.env"
      exit 1
    fi
    download_ubuntu_cloud
    download_firecracker
    ;;
  "arch-linux")
    download_arch_linux
    download_firecracker
    ;;
  "debian")
    download_debian
    download_firecracker
    ;;
  "almalinux")
    download_almalinux
    download_firecracker
    ;;
  "fedora")
    download_fedora
    download_firecracker
    ;;
  *)
    # Default: Firecracker CI path
    detect_latest_ci_version
    download_firecracker
    download_kernel_firecracker_ci
    download_and_convert_rootfs_firecracker_ci
    ;;
  esac

  echo ""
  echo "=== Assets Setup Complete ==="
  echo "Location: $ASSETS_DIR"
  case "$IMAGE_SOURCE" in
  "ubuntu-cloud")
    echo " - Rootfs: images/${UBUNTU_VERSION}.ext4"
    echo " - Kernel: kernels/${UBUNTU_VERSION}-vmlinux"
    ;;
  "arch-linux")
    echo " - Rootfs: images/arch.ext4"
    echo " - Kernel: kernels/arch-vmlinux"
    ;;
  "debian")
    echo " - Rootfs: images/debian-*.ext4"
    echo " - Kernel: kernels/debian-*-vmlinux"
    ;;
  "almalinux")
    echo " - Rootfs: images/almalinux-*.ext4"
    echo " - Kernel: kernels/almalinux-*-vmlinux"
    ;;
  "fedora")
    echo " - Rootfs: images/fedora-*.ext4"
    echo " - Kernel: kernels/fedora-*-vmlinux"
    ;;
  *)
    echo " - Kernel: $KERNEL_OUTPUT"
    echo " - Rootfs: ${ROOTFS_OUTPUT_BASE}-*.ext4"
    ;;
  esac
  echo " - Firecracker: bin/firecracker"
  echo " - Jailer: bin/jailer"
  echo ""
  show_custom_image_help
}

main "$@"
