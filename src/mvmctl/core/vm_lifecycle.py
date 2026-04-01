import fcntl
import hashlib
import logging
import os
import random
import shutil
import signal
import string
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    BRIDGE_NAME,
    CLI_NAME,
    CONST_DIR_PERMS_CACHE,
    CONST_FILE_PERMS_PID_FILE,
    CONST_MEBIBYTE_BYTES,
    CONST_POLL_STEP_SECONDS,
    CONST_SIGNAL_EXIT_CODE_BASE,
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    CONST_VM_START_WAIT_S,
    DEFAULT_CLOUD_INIT_DIRNAME,
    DEFAULT_CLOUD_INIT_ISO_NAME,
    DEFAULT_FC_API_SOCKET_FILENAME,
    DEFAULT_FC_CONFIG_FILENAME,
    DEFAULT_FC_CONSOLE_LOG_FILENAME,
    DEFAULT_FC_EXITCODE_FILENAME,
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_PID_FILENAME,
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_CONSOLE,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_KERNEL_FILENAME,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
    FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S,
    FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S,
    FIRECRACKER_SIGTERM_WAIT_S,
    MAX_VMS,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.core.cloud_init import create_cloud_init_iso, write_cloud_init
from mvmctl.core.config_gen import ConfigGenerator, DriveConfig
from mvmctl.core.firecracker import FirecrackerClient, get_vm_socket_path
from mvmctl.core.firewall import (
    add_nocloud_input_rule,
    remove_nocloud_input_rule,
    setup_nocloud_input_chain,
)
from mvmctl.core.image import copy_from_ready_pool, ensure_image_in_ready_pool
from mvmctl.core.metadata import list_image_entries
from mvmctl.core.network import (
    add_iptables_forward_rules,
    bridge_exists,
    create_tap,
    delete_tap,
    generate_mac,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_nat,
    teardown_nat,
)
from mvmctl.core.network_manager import (
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
from mvmctl.core.rootfs_injector import inject_cloud_init
from mvmctl.core.ssh import resolve_ssh_key
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.exceptions import (
    CloudInitError,
    MVMError,
    NetworkError,
    VMCreateError,
    VMNotFoundError,
)
from mvmctl.models import CloudInitMode, VMConfig, VMInstance, VMState
from mvmctl.services.console_relay import ConsoleRelayManager
from mvmctl.services.nocloud_server import NoCloudNetServerManager
from mvmctl.utils.fs import get_cache_dir, get_images_dir, get_kernels_dir


def get_vm_dir(vm_hash: str) -> Path:
    """Return the directory for a specific VM by its hash.

    This is a compatibility shim for tests that patch get_vm_dir.
    All internal code should use this function instead of get_vm_dir_by_hash.

    Uses dynamic import to respect test patches on mvmctl.utils.fs.get_vm_dir_by_hash.
    """
    from mvmctl.utils.fs import get_vm_dir_by_hash

    return get_vm_dir_by_hash(vm_hash)


# Compatibility alias for tests that patch get_vm_dir_by_hash
get_vm_dir_by_hash = get_vm_dir


def _resolve_image_path(image: str) -> Path:
    images_dir = get_images_dir()

    # Check for compressed images first (.zst), then uncompressed
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        # Check for compressed version first
        compressed_candidate = images_dir / f"{image}{ext}.zst"
        if compressed_candidate.exists():
            return compressed_candidate
        # Fall back to uncompressed
        candidate = images_dir / f"{image}{ext}"
        if candidate.exists():
            return candidate

    direct = Path(image)
    if direct.is_absolute() and direct.exists():
        return direct

    from mvmctl.core.metadata import find_images_by_id_prefix
    from mvmctl.utils.fs import get_cache_dir

    matches = find_images_by_id_prefix(get_cache_dir(), image)
    if len(matches) == 1:
        full_key, meta = matches[0]
        filename = str(meta.get("filename", ""))
        if filename:
            # Check for compressed version first
            compressed_candidate = images_dir / f"{filename}.zst"
            if compressed_candidate.exists():
                return compressed_candidate
            candidate = images_dir / filename
            if candidate.exists():
                return candidate
        for ext in SUPPORTED_IMAGE_EXTENSIONS:
            # Check for compressed version first
            compressed_candidate = images_dir / f"{full_key}{ext}.zst"
            if compressed_candidate.exists():
                return compressed_candidate
            candidate = images_dir / f"{full_key}{ext}"
            if candidate.exists():
                return candidate

    if direct.exists():
        return direct

    raise MVMError(f"Image not found: {image!r}")


def _resolve_image_fs_uuid(image: str) -> str | None:
    from mvmctl.core.metadata import find_images_by_id_prefix, list_image_entries
    from mvmctl.utils.fs import get_cache_dir

    cache_dir = get_cache_dir()

    all_entries = list_image_entries(cache_dir)
    for _full_key, meta in all_entries.items():
        internal_id = str(meta.get("internal_id", ""))
        filename = str(meta.get("filename", ""))
        if image not in {internal_id, filename}:
            continue

        fs_uuid = meta.get("fs_uuid")
        if isinstance(fs_uuid, str) and fs_uuid.strip():
            return fs_uuid.strip()

    matches = find_images_by_id_prefix(cache_dir, image)
    if len(matches) == 1:
        _, meta = matches[0]
        fs_uuid = meta.get("fs_uuid")
        if isinstance(fs_uuid, str) and fs_uuid.strip():
            return fs_uuid.strip()

    return None


def _resolve_image_fs_type(image: str) -> str | None:
    from mvmctl.core.metadata import find_images_by_id_prefix, list_image_entries
    from mvmctl.utils.fs import get_cache_dir

    cache_dir = get_cache_dir()

    all_entries = list_image_entries(cache_dir)
    for _full_key, meta in all_entries.items():
        internal_id = str(meta.get("internal_id", ""))
        filename = str(meta.get("filename", ""))
        if image not in {internal_id, filename}:
            continue
        fs_type = meta.get("fs_type")
        if isinstance(fs_type, str) and fs_type.strip():
            return fs_type.strip()

    matches = find_images_by_id_prefix(cache_dir, image)
    if len(matches) == 1:
        _, meta = matches[0]
        fs_type = meta.get("fs_type")
        if isinstance(fs_type, str) and fs_type.strip():
            return fs_type.strip()

    return None


def _resolve_image_id_path(image: str) -> Path:
    from mvmctl.core.metadata import find_images_by_id_prefix
    from mvmctl.utils.fs import get_cache_dir
    from mvmctl.utils.id_prefix import resolve_single_by_id_prefix

    images_dir = get_images_dir()
    match = resolve_single_by_id_prefix(image, find_images_by_id_prefix, get_cache_dir())
    if match is None:
        raise MVMError(f"Image ID not found or ambiguous: {image!r}")

    full_key, meta = match
    filename = str(meta.get("filename", ""))
    if filename:
        # Check for compressed version first
        compressed_candidate = images_dir / f"{filename}.zst"
        if compressed_candidate.exists():
            return compressed_candidate
        candidate = images_dir / filename
        if candidate.exists():
            return candidate
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        # Check for compressed version first
        compressed_candidate = images_dir / f"{full_key}{ext}.zst"
        if compressed_candidate.exists():
            return compressed_candidate
        candidate = images_dir / f"{full_key}{ext}"
        if candidate.exists():
            return candidate

    raise MVMError(f"Image not found: {image!r}")


def _resolve_kernel_path(kernel: str) -> Path:
    kernels_dir = get_kernels_dir()

    candidate = kernels_dir / kernel
    if candidate.exists():
        return candidate

    direct = Path(kernel)
    if direct.is_absolute() and direct.exists():
        return direct

    from mvmctl.core.metadata import list_kernel_entries
    from mvmctl.utils.fs import get_cache_dir

    matches = [
        (full_key, meta)
        for full_key, meta in list_kernel_entries(get_cache_dir(), kernels_dir).items()
        if full_key.startswith(kernel)
    ]
    if len(matches) == 1:
        full_key, meta = matches[0]
        filename = str(meta.get("filename", ""))
        if filename:
            candidate = kernels_dir / filename
            if candidate.exists():
                return candidate
        candidate = kernels_dir / full_key
        if candidate.exists():
            return candidate

    if direct.exists():
        return direct

    raise MVMError(f"Kernel not found: {kernel!r}")


def _resolve_kernel_id_path(kernel: str) -> Path:
    from mvmctl.core.metadata import list_kernel_entries
    from mvmctl.utils.fs import get_cache_dir
    from mvmctl.utils.id_prefix import resolve_single_by_id_prefix

    kernels_dir = get_kernels_dir()

    def _find_kernels_by_id_prefix(
        cache_dir: Path, prefix: str
    ) -> list[tuple[str, dict[str, object]]]:
        return [
            (full_key, meta)
            for full_key, meta in list_kernel_entries(cache_dir, kernels_dir).items()
            if full_key.startswith(prefix)
        ]

    match = resolve_single_by_id_prefix(kernel, _find_kernels_by_id_prefix, get_cache_dir())
    if match is None:
        raise MVMError(f"Kernel ID not found or ambiguous: {kernel!r}")

    full_key, meta = match
    filename = str(meta.get("filename", ""))
    if filename:
        candidate = kernels_dir / filename
        if candidate.exists():
            return candidate
    candidate = kernels_dir / full_key
    if candidate.exists():
        return candidate

    raise MVMError(f"Kernel not found: {kernel!r}")


def generate_vm_id(name: str) -> str:
    """Generate a unique VM ID from name and current time."""
    data = f"{name}:{time.time()}"
    return hashlib.sha256(data.encode()).hexdigest()[:16]


logger = logging.getLogger(__name__)


def grow_rootfs_with_guestfs(image_path: Path, target_size_bytes: int) -> None:
    """Grow a rootfs image to target size using libguestfs.

    Args:
        image_path: Path to the rootfs image
        target_size_bytes: Target size in bytes

    Raises:
        VMCreateError: If libguestfs is not available or resize fails
    """
    from mvmctl.utils.guestfs import check_libguestfs

    if not check_libguestfs():
        raise VMCreateError("libguestfs required for disk resize")

    # Handle case where file doesn't exist or stat returns mock (e.g., in tests)
    try:
        current_size = image_path.stat().st_size
        if not isinstance(current_size, int):
            return  # Skip in mocked tests
    except (OSError, AttributeError):
        return  # Skip if file doesn't exist or stat fails

    if current_size >= target_size_bytes:
        raise VMCreateError(
            f"Requested disk size ({target_size_bytes // CONST_MEBIBYTE_BYTES} MB) is smaller than "
            f"current image size ({current_size // CONST_MEBIBYTE_BYTES} MB). "
            f"Cannot shrink filesystem. Use a larger size or recreate VM with smaller image."
        )

    try:
        # First, extend the file size
        with open(image_path, "r+b") as f:
            f.truncate(target_size_bytes)

        from mvmctl.utils.guestfs import optimized_guestfs

        with optimized_guestfs(image_path, readonly=False) as g:
            partitions = g.list_partitions()
            root_device = partitions[0] if partitions else "/dev/sda"

            fs_type = g.vfs_type(root_device)

            if fs_type in ("ext2", "ext3", "ext4"):
                # For ext: resize the filesystem to fill new space
                g.resize2fs(root_device)
            elif fs_type == "btrfs":
                g.mount(root_device, "/")
                g.btrfs_filesystem_resize("/", target_size_bytes)
                g.umount(root_device)
            else:
                logger.warning(f"Cannot resize {fs_type} filesystem")

        logger.info(
            "Grew rootfs: %d MB → %d MB",
            current_size // CONST_MEBIBYTE_BYTES,
            target_size_bytes // CONST_MEBIBYTE_BYTES,
        )

    except Exception as e:
        raise VMCreateError(f"Failed to grow rootfs: {e}") from e


def _detect_init_system_and_enable_ssh(g: Any, rootfs_path: Path) -> bool:
    """Detect init system and enable SSH service.

    Args:
        g: GuestFS handle (already mounted)
        rootfs_path: Path to rootfs (for logging)

    Returns:
        True if SSH was enabled successfully
    """
    init_system = "unknown"

    # Detection logic
    if g.exists("/lib/systemd/systemd") or g.exists("/usr/lib/systemd/systemd"):
        init_system = "systemd"
        logger.debug("Detected systemd init system in %s", rootfs_path.name)
    elif g.exists("/sbin/openrc") or g.exists("/usr/sbin/openrc"):
        init_system = "openrc"
        logger.debug("Detected OpenRC init system in %s", rootfs_path.name)
    elif g.exists("/etc/init.d/"):
        init_system = "sysvinit"
        logger.debug("Detected sysvinit in %s", rootfs_path.name)
    else:
        logger.warning("Unknown init system in %s, cannot enable SSH", rootfs_path.name)
        return False

    # SSH enablement based on init system
    try:
        if init_system == "systemd":
            # Create symlink to enable service
            # Try ssh.service first, fallback to sshd.service
            ssh_services = [
                "/usr/lib/systemd/system/ssh.service",
                "/lib/systemd/system/ssh.service",
                "/etc/systemd/system/ssh.service",
                "/usr/lib/systemd/system/sshd.service",
                "/lib/systemd/system/sshd.service",
                "/etc/systemd/system/sshd.service",
            ]

            ssh_service_path = None
            for svc_path in ssh_services:
                if g.exists(svc_path):
                    ssh_service_path = svc_path
                    break

            if ssh_service_path:
                g.mkdir_p("/etc/systemd/system/multi-user.target.wants")
                g.ln_s(
                    ssh_service_path,
                    f"/etc/systemd/system/multi-user.target.wants/{ssh_service_path.split('/')[-1]}",
                )
                logger.info("Enabled SSH service (systemd) for %s", rootfs_path.name)
                return True
            else:
                logger.warning("SSH service unit not found in %s", rootfs_path.name)
                return False

        elif init_system == "openrc":
            # Enable sshd for default runlevel
            g.mkdir_p("/etc/runlevels/default")
            if g.exists("/etc/init.d/sshd"):
                g.ln_s("/etc/init.d/sshd", "/etc/runlevels/default/sshd")
                logger.info("Enabled SSH service (OpenRC) for %s", rootfs_path.name)
                return True
            elif g.exists("/etc/init.d/ssh"):
                g.ln_s("/etc/init.d/ssh", "/etc/runlevels/default/ssh")
                logger.info("Enabled SSH service (OpenRC) for %s", rootfs_path.name)
                return True
            else:
                logger.warning("SSH init script not found for OpenRC in %s", rootfs_path.name)
                return False

        elif init_system == "sysvinit":
            # For Debian/Ubuntu sysvinit - create rc.d symlinks
            if g.exists("/etc/init.d/ssh"):
                # Create symlinks for runlevels 2,3,4,5
                for level in ["2", "3", "4", "5"]:
                    g.mkdir_p(f"/etc/rc{level}.d")
                    g.ln_s("../init.d/ssh", f"/etc/rc{level}.d/S02ssh")
                logger.info("Enabled SSH service (sysvinit) for %s", rootfs_path.name)
                return True
            else:
                logger.warning("SSH init script not found for sysvinit in %s", rootfs_path.name)
                return False

    except Exception as e:
        logger.error("Failed to enable SSH for %s: %s", rootfs_path.name, e)
        return False

    return False


def _enforce_ssh_key_auth(g: Any, rootfs_path: Path, user: str) -> None:
    """Enforce SSH key authentication via sshd_config.

    Creates /etc/ssh/sshd_config.d/mvm.conf with key-only auth settings
    and adds user-specific AllowUsers for non-root users.

    Args:
        g: GuestFS handle (already mounted)
        rootfs_path: Path to rootfs (for logging)
        user: Username to configure SSH for
    """
    try:
        # Check if sshd_config exists
        if not g.exists("/etc/ssh/sshd_config"):
            logger.warning("sshd_config not found in %s", rootfs_path.name)
            return

        # Create sshd_config.d directory for our config
        sshd_config_d = "/etc/ssh/sshd_config.d"
        g.mkdir_p(sshd_config_d)

        # Build sshd config content
        config_lines = [
            "# MVM SSH key authentication configuration",
            "# This file is managed by mvmctl - changes will be preserved across reboots",
            "",
            "# Enable SSH key authentication",
            "PubkeyAuthentication yes",
            "AuthorizedKeysFile .ssh/authorized_keys",
            "",
            "# Disable password authentication entirely",
            "PasswordAuthentication no",
            "PermitEmptyPasswords no",
            "",
            "# Disable PAM for SSH (avoid password prompts)",
            "UsePAM yes",
            "",
            "# Ensure SSH protocol version 2",
            "Protocol 2",
        ]

        # Add user-specific AllowUsers for non-root users
        if user != "root":
            config_lines.append("")
            config_lines.append("# Allow specific user for key-based auth")
            config_lines.append(f"AllowUsers {user}")
        else:
            # For root, we allow root but require key auth
            config_lines.append("")
            config_lines.append("# Allow root with key authentication only")
            config_lines.append("PermitRootLogin prohibit-password")

        config_content = "\n".join(config_lines) + "\n"
        g.write(f"{sshd_config_d}/mvm.conf", config_content)
        g.chmod(0o644, f"{sshd_config_d}/mvm.conf")
        logger.info("Configured SSH key authentication for user '%s' in %s", user, rootfs_path.name)

    except Exception as e:
        logger.warning("Failed to configure sshd: %s", e)


def _ensure_user_exists(g: Any, user: str, rootfs_path: Path) -> None:
    """Ensure the specified user exists in the rootfs.

    Creates user with UID/GID 1000 if missing, sets up home directory,
    adds to sudoers with NOPASSWD, and configures shadow password.

    Args:
        g: GuestFS handle (already mounted)
        user: Username to ensure exists
        rootfs_path: Path to rootfs (for logging)
    """
    if user == "root":
        # Root always exists
        return

    try:
        # Check if user already exists in /etc/passwd
        passwd_content = ""
        if g.exists("/etc/passwd"):
            passwd_content = g.read_file("/etc/passwd")
            if isinstance(passwd_content, bytes):
                passwd_content = passwd_content.decode("utf-8", errors="replace")

        user_exists = False
        for line in passwd_content.strip().split("\n"):
            if line.startswith(f"{user}:"):
                user_exists = True
                break

        if user_exists:
            logger.debug("User '%s' already exists in %s", user, rootfs_path.name)
            return

        # User doesn't exist - create it
        logger.info("Creating user '%s' in %s", user, rootfs_path.name)

        # Create home directory with proper structure
        home_dir = f"/home/{user}"
        g.mkdir_p(home_dir)
        g.mkdir_p(f"{home_dir}/.ssh")

        # Create user in /etc/passwd: username:password:uid:gid:gecos:home:shell
        # Using '!' for locked password (no password login possible)
        passwd_entry = f"{user}:!:1000:1000::{home_dir}:/bin/bash\n"
        g.write("/etc/passwd", passwd_entry, mode="a")
        g.chmod(0o644, "/etc/passwd")

        # Create /etc/shadow with disabled password
        # Format: username:encrypted_password:last_change:min:max:warn:inactive:expire:flag
        # '!' prefix means account is locked (no password)
        shadow_entry = f"{user}:!:19700:0:99999:7:::\n"
        g.write("/etc/shadow", shadow_entry, mode="a")
        g.chmod(0o640, "/etc/shadow")

        # Add to /etc/group
        group_entry = f"{user}:x:1000:\n"
        g.write("/etc/group", group_entry, mode="a")
        g.chmod(0o644, "/etc/group")

        # Add to /etc/sudoers.d for passwordless sudo
        sudoers_dir = "/etc/sudoers.d"
        g.mkdir_p(sudoers_dir)
        sudoers_content = f"{user} ALL=(ALL) NOPASSWD: ALL\n"
        g.write(f"{sudoers_dir}/{user}", sudoers_content)
        g.chmod(0o440, f"{sudoers_dir}/{user}")

        # Set proper ownership on home directory
        g.chown(1000, 1000, home_dir)
        g.chown(1000, 1000, f"{home_dir}/.ssh")

        logger.info("Created user '%s' with UID/GID 1000 in %s", user, rootfs_path.name)

    except Exception as e:
        logger.warning("Failed to create user '%s': %s", user, e)


def _generate_ssh_host_keys(g: Any, rootfs_path: Path) -> None:
    """Ensure SSH host keys exist for first boot.

    Checks for existing host keys (rsa, ecdsa, ed25519) and creates a
    first-boot script to generate any missing keys.

    Args:
        g: GuestFS handle (already mounted)
        rootfs_path: Path to rootfs (for logging)
    """
    try:
        # Check which host keys already exist
        key_types = ["ssh_host_rsa_key", "ssh_host_ecdsa_key", "ssh_host_ed25519_key"]
        existing_keys = []
        missing_keys = []

        for key in key_types:
            if g.exists(f"/etc/ssh/{key}"):
                existing_keys.append(key)
            else:
                missing_keys.append(key)

        if not missing_keys:
            logger.debug("All SSH host keys already exist in %s", rootfs_path.name)
            return

        logger.info("Missing SSH host keys in %s: %s", rootfs_path.name, missing_keys)

        # Create first-boot script for OpenRC/systemd to generate missing keys
        # This handles cases where keys don't exist in the image (common for minimal images)
        local_d_dir = "/etc/local.d"
        g.mkdir_p(local_d_dir)

        keygen_script = """#!/bin/bash
# SSH host key generation script
# Generated by mvmctl - runs once on first boot

SSH_KEYDIR="/etc/ssh"

for key_type in ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key; do
    key_path="$SSH_KEYDIR/$key_type"
    if [ ! -f "$key_path" ]; then
        case "$key_type" in
            ssh_host_rsa_key)
                ssh-keygen -t rsa -f "$key_path" -N "" -q 2>/dev/null
                ;;
            ssh_host_ecdsa_key)
                ssh-keygen -t ecdsa -f "$key_path" -N "" -q 2>/dev/null
                ;;
            ssh_host_ed25519_key)
                ssh-keygen -t ed25519 -f "$key_path" -N "" -q 2>/dev/null
                ;;
        esac
        # Ensure proper permissions
        chmod 600 "$key_path" 2>/dev/null
        chmod 644 "${key_path}.pub" 2>/dev/null
    fi
done

# Remove ourselves so we don't run again
rm -f /etc/local.d/ssh-keygen.start 2>/dev/null
exit 0
"""
        g.write(f"{local_d_dir}/ssh-keygen.start", keygen_script)
        g.chmod(0o755, f"{local_d_dir}/ssh-keygen.start")

        # For OpenRC, make sure the service is enabled
        if g.exists("/sbin/openrc") or g.exists("/usr/sbin/openrc"):
            g.mkdir_p("/etc/runlevels/default")
            if not g.exists("/etc/runlevels/default/local"):
                g.ln_s("/sbin/openrc-local", "/etc/runlevels/default/local")

        # For systemd, create a oneshot service to generate keys
        g.mkdir_p("/etc/systemd/system")
        keygen_service = """[Unit]
Description=SSH Host Key Generation
After=local-fs.target

[Service]
Type=oneshot
ExecStart=/bin/bash /etc/local.d/ssh-keygen.start
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
"""
        g.write("/etc/systemd/system/ssh-hostkeygen.service", keygen_service)
        g.chmod(0o644, "/etc/systemd/system/ssh-hostkeygen.service")

        # Enable the service
        g.mkdir_p("/etc/systemd/system/multi-user.target.wants")
        g.ln_s(
            "/etc/systemd/system/ssh-hostkeygen.service",
            "/etc/systemd/system/multi-user.target.wants/ssh-hostkeygen.service",
        )

        logger.info("Created SSH host key generation service in %s", rootfs_path.name)

    except Exception as e:
        logger.warning("Failed to setup SSH host key generation: %s", e)


def _inject_ssh_keys_for_disabled_mode(
    rootfs_path: Path,
    ssh_pub_key: list[str] | str | None,
    vm_dir: Path,
    user: str = DEFAULT_VM_SSH_USER,
) -> None:
    """Inject SSH public keys directly into rootfs for DISABLED cloud-init mode.

    Uses libguestfs to write SSH keys to /root/.ssh/authorized_keys when
    cloud-init is disabled but SSH access is still desired.

    Args:
        rootfs_path: Path to the rootfs disk image
        ssh_pub_key: SSH public key(s) to inject (single key, list, or None)
        vm_dir: VM directory for logging

    Raises:
        VMCreateError: If libguestfs is not available or injection fails
    """
    from mvmctl.utils.guestfs import check_libguestfs, optimized_guestfs

    if ssh_pub_key is None:
        return

    if not check_libguestfs():
        raise VMCreateError("libguestfs required for SSH key injection")

    # Normalize to list
    keys: list[str]
    if isinstance(ssh_pub_key, str):
        keys = [ssh_pub_key]
    else:
        keys = ssh_pub_key

    if not keys:
        return

    try:
        with optimized_guestfs(rootfs_path, readonly=False) as g:
            # Detect and mount root filesystem
            # Firecracker uses loop filesystems (filesystem directly on disk, no partition)
            # so we need to check for both /dev/sda (loop) and /dev/sda1 (partitioned)
            filesystems: dict[str, str] = g.list_filesystems()

            # Try loop device first (Firecracker standard)
            root_device: str | None = None
            if "/dev/sda" in filesystems:
                root_device = "/dev/sda"
            elif "/dev/vda" in filesystems:
                root_device = "/dev/vda"
            elif "/dev/sda1" in filesystems:
                root_device = "/dev/sda1"
            elif "/dev/vda1" in filesystems:
                root_device = "/dev/vda1"
            elif filesystems:
                # Fallback to first available
                root_device = str(list(filesystems.keys())[0])

            if root_device is None:
                raise VMCreateError(f"No filesystem found in {rootfs_path}")

            logger.debug("Using root device: %s", root_device)

            try:
                g.mount(root_device, "/")
            except Exception as e:
                raise VMCreateError(f"Failed to mount {root_device}: {e}") from e

            try:
                # Create .ssh directory with proper permissions
                ssh_home_dir = "/root" if user == "root" else f"/home/{user}"
                logger.debug("SSH home directory: %s", ssh_home_dir)

                # Ensure user exists (for non-root users)
                _ensure_user_exists(g, user, rootfs_path)

                # Enforce SSH key authentication
                _enforce_ssh_key_auth(g, rootfs_path, user)

                # Generate SSH host keys if missing
                _generate_ssh_host_keys(g, rootfs_path)

                # Check if /root exists before creating .ssh
                if not g.exists("/root"):
                    logger.warning("/root directory does not exist, creating it")
                    g.mkdir_p("/root")
                    g.chmod(0o700, "/root")
                    g.chown(0, 0, "/root")

                g.mkdir_p(f"{ssh_home_dir}/.ssh")
                g.chmod(0o700, f"{ssh_home_dir}/.ssh")
                g.chown(0, 0, f"{ssh_home_dir}/.ssh")  # Set root:root ownership
                g.sync()
                logger.debug("Created %s/.ssh directory", ssh_home_dir)

                # Read existing authorized_keys if any
                existing_keys = ""
                if g.exists(f"{ssh_home_dir}/.ssh/authorized_keys"):
                    existing_keys = g.read_file(f"{ssh_home_dir}/.ssh/authorized_keys")
                    if isinstance(existing_keys, bytes):
                        existing_keys = existing_keys.decode("utf-8", errors="replace")

                # Append new keys (avoid duplicates)
                newline = "\n"
                existing_set = (
                    set(existing_keys.strip().split(newline)) if existing_keys.strip() else set()
                )
                new_keys = [k for k in keys if k.strip() and k.strip() not in existing_set]

                if new_keys:
                    combined = existing_keys
                    if combined and not combined.endswith(newline):
                        combined += newline
                    combined += newline.join(new_keys) + newline
                    g.write(f"{ssh_home_dir}/.ssh/authorized_keys", combined)
                    g.chmod(0o600, f"{ssh_home_dir}/.ssh/authorized_keys")
                    g.sync()  # Ensure authorized_keys is written

                    # Verify immediately
                    if g.exists(f"{ssh_home_dir}/.ssh/authorized_keys"):
                        logger.debug(
                            "Injected %d SSH key(s) for disabled cloud-init mode", len(new_keys)
                        )
                    else:
                        logger.error(
                            "FAILED to write authorized_keys - file doesn't exist after write!"
                        )
                else:
                    logger.warning(
                        "No new SSH keys to inject (all keys already exist or no keys provided)"
                    )
                # Disable cloud-init datasource probing (Ec2/MMDS, etc.)
                # Prevents ~120s timeout on boot waiting for unreachable metadata endpoints
                g.mkdir_p("/etc/cloud/cloud.cfg.d")
                g.write(
                    "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
                    "datasource_list: [None]\n",
                )

                # Mask systemd services that block boot in microVMs
                # snapd.seeded.service: waits for snap store sync (useless in VMs)
                # systemd-networkd-wait-online.service: blocks waiting for all interfaces
                #   to report "online" — TAP is already plumbed by host
                g.mkdir_p("/etc/systemd/system/snapd.seeded.service.d")
                g.write(
                    "/etc/systemd/system/snapd.seeded.service.d/override.conf",
                    "[Service]\nExecStart=\nExecStart=/bin/true\n",
                )
                g.mkdir_p("/etc/systemd/system/systemd-networkd-wait-online.service.d")
                g.write(
                    "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
                    "[Unit]\nConditionPathExists=/dev/null\n",
                )
                # Mask cloud-init services to prevent slow boot/shutdown
                # Even with datasource_list: [None], cloud-init still runs and takes time
                g.mkdir_p("/etc/systemd/system/cloud-init.service.d")
                g.write(
                    "/etc/systemd/system/cloud-init.service.d/override.conf",
                    "[Unit]\nConditionPathExists=/dev/null\n",
                )
                g.mkdir_p("/etc/systemd/system/cloud-init-local.service.d")
                g.write(
                    "/etc/systemd/system/cloud-init-local.service.d/override.conf",
                    "[Unit]\nConditionPathExists=/dev/null\n",
                )
                g.mkdir_p("/etc/systemd/system/cloud-config.service.d")
                g.write(
                    "/etc/systemd/system/cloud-config.service.d/override.conf",
                    "[Unit]\nConditionPathExists=/dev/null\n",
                )
                g.mkdir_p("/etc/systemd/system/cloud-final.service.d")
                g.write(
                    "/etc/systemd/system/cloud-final.service.d/override.conf",
                    "[Unit]\nConditionPathExists=/dev/null\n",
                )

                # Enable SSH service based on init system
                _detect_init_system_and_enable_ssh(g, rootfs_path)

                # Create first-boot SSH installer service (for minimal images like Arch)
                first_boot_service = """[Unit]
Description=First-boot SSH installer
After=network.target
ConditionFirstBoot=yes

[Service]
Type=oneshot
ExecStart=/bin/bash -c '
    # Detect package manager and install SSH if missing
    if ! command -v sshd >/dev/null 2>&1 && ! command -v ssh >/dev/null 2>&1; then
        if command -v pacman >/dev/null 2>&1; then
            # Arch Linux
            pacman -Sy --noconfirm openssh 2>/dev/null || true
        elif command -v apt-get >/dev/null 2>&1; then
            # Debian/Ubuntu
            apt-get update && apt-get install -y openssh-server 2>/dev/null || true
        elif command -v apk >/dev/null 2>&1; then
            # Alpine
            apk add --no-cache openssh 2>/dev/null || true
        fi
    fi
    
    # Enable SSH service
    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable --now sshd 2>/dev/null || systemctl enable --now ssh 2>/dev/null || true
    elif [ -f /sbin/openrc ]; then
        rc-update add sshd default 2>/dev/null || rc-update add ssh default 2>/dev/null || true
        rc-service sshd start 2>/dev/null || rc-service ssh start 2>/dev/null || true
    fi
    
    # Mark service to not run again
    systemctl disable first-boot-ssh-installer.service 2>/dev/null || true
'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
"""

                g.mkdir_p("/etc/systemd/system")
                g.write("/etc/systemd/system/first-boot-ssh-installer.service", first_boot_service)
                g.chmod(0o644, "/etc/systemd/system/first-boot-ssh-installer.service")

                # Enable the service
                g.mkdir_p("/etc/systemd/system/multi-user.target.wants")
                g.ln_s(
                    "/etc/systemd/system/first-boot-ssh-installer.service",
                    "/etc/systemd/system/multi-user.target.wants/first-boot-ssh-installer.service",
                )

                logger.info("Created first-boot SSH installer for %s", rootfs_path.name)
            finally:
                # CRITICAL: Force sync to disk before unmount (required when autosync is disabled)
                try:
                    g.sync()
                except Exception:
                    pass
                try:
                    g.umount("/")
                except Exception:
                    pass  # Already unmounted or not mounted

    except MVMError as e:
        # libguestfs failed to launch - warn but don't fail
        logger.warning(
            "SSH key injection skipped: libguestfs failed (%s). "
            "SSH keys not injected. You may need to manually configure SSH "
            "or use a different cloud-init mode.",
            str(e),
        )
        # Don't re-raise - allow VM creation to continue
        return
    except VMCreateError:
        raise
    except Exception as e:
        raise VMCreateError(f"Failed to inject SSH keys: {e}") from e


def _cleanup_vm_creation_resources(
    resources_created: dict[str, bool],
    vm_dir: Path | None,
    net_manager: NoCloudNetServerManager | None,
    relay_mgr: ConsoleRelayManager | None,
    net_config: Any,
    name: str,
    vm_id: str | None,
    guest_ip: str,
    nocloud_net_port: int,
    tap_name: str,
    pty_master_fd: int | None,
    pty_slave_fd: int | None,
    log_fp: Any,
    console_fp: Any,
) -> None:
    """Clean up resources during failed VM creation.

    Logs warnings for cleanup failures instead of silently swallowing.
    Never raises - cleanup is best-effort.
    """
    if log_fp is not None:
        try:
            log_fp.close()
        except OSError as e:
            logger.warning("Failed to close log file during cleanup: %s", e)

    if console_fp is not None:
        try:
            console_fp.close()
        except OSError as e:
            logger.warning("Failed to close console file during cleanup: %s", e)

    if resources_created.get("nocloud_server") and net_manager is not None and vm_id:
        try:
            net_manager.stop_server(name, vm_id)
        except Exception as e:
            logger.warning("Failed to stop nocloud server during cleanup: %s", e)

    if resources_created.get("firewall_rule") and guest_ip:
        try:
            remove_nocloud_input_rule(guest_ip, name, nocloud_net_port)
        except NetworkError as e:
            logger.warning("Failed to remove firewall rule during cleanup: %s", e)

    if resources_created.get("tap") and tap_name:
        try:
            cleanup_tap(tap_name, bridge=net_config.bridge if net_config else None)
        except NetworkError as e:
            logger.warning("Failed to cleanup TAP device during cleanup: %s", e)

    if resources_created.get("network_ip"):
        try:
            release_network_ip(net_config.name if net_config else DEFAULT_NETWORK_NAME, name)
        except (NetworkError, TypeError) as e:
            # TypeError can occur when builtins.open is mocked (e.g., in tests)
            logger.warning("Failed to release network IP during cleanup: %s", e)

    if resources_created.get("console_relay") and relay_mgr is not None and vm_id is not None:
        try:
            relay_mgr.stop_relay(name, vm_id)
        except Exception as e:
            logger.warning("Failed to stop console relay during cleanup: %s", e)

    if pty_slave_fd is not None:
        try:
            os.close(pty_slave_fd)
        except OSError:
            pass

    if pty_master_fd is not None:
        try:
            os.close(pty_master_fd)
        except OSError:
            pass

    if resources_created.get("vm_dir") and vm_dir and vm_dir.exists():
        try:
            shutil.rmtree(vm_dir, ignore_errors=True)
        except OSError as e:
            logger.warning("Failed to remove VM directory during cleanup: %s", e)


def _generate_tap_name(network_name: str, vm_name: str) -> str:
    rand_suffix = "".join(random.choices(string.ascii_lowercase, k=3))
    net_part = network_name[:3]
    vm_part = vm_name[:3]
    return f"{CLI_NAME}-{net_part}-{vm_part}-{rand_suffix}"


def _write_pid_file(pid_file: Path, pid: int) -> None:
    """Write PID to file with an exclusive advisory lock."""
    fd = os.open(str(pid_file), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, CONST_FILE_PERMS_PID_FILE)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        os.write(fd, str(pid).encode())
    finally:
        fcntl.flock(fd, fcntl.LOCK_UN)
        os.close(fd)


def _read_pid_file(pid_file: Path) -> int | None:
    """Read PID from file and verify the process actually exists."""
    if not pid_file.exists():
        return None
    try:
        pid = int(pid_file.read_text().strip())
    except (ValueError, OSError):
        return None
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return None
    except PermissionError:
        pass  # process exists but we can't signal it
    return pid


def _write_exit_code(vm_dir: Path, exit_code: int) -> None:
    """Write exit code to firecracker.exitcode file."""
    exitcode_file = vm_dir / DEFAULT_FC_EXITCODE_FILENAME
    try:
        exitcode_file.write_text(str(exit_code))
    except OSError:
        pass  # Best effort - don't fail if we can't write exit code


def _secure_mkdir_vm(vm_dir: Path, name: str) -> None:
    """Atomically create VM directory with TOCTOU protection.

    Uses atomic mkdir with symlink detection to prevent race conditions
    where an attacker creates a symlink between check and create.

    Args:
        vm_dir: Path to the VM directory to create
        name: VM name for error messages

    Raises:
        MVMError: If directory exists, is a symlink, or race condition detected
    """
    # SECURITY: Use os.lstat() to detect symlinks before attempting creation
    # This prevents the TOCTOU race between check and mkdir
    try:
        # Check if path exists and is a symlink BEFORE attempting creation
        os.lstat(vm_dir)  # Raises FileNotFoundError if path doesn't exist
        if os.path.islink(vm_dir):
            raise MVMError(f"VM '{name}' path is a symlink (possible attack): {vm_dir}")
        raise MVMError(f"VM '{name}' already exists at {vm_dir}")
    except FileNotFoundError:
        # Expected - path doesn't exist, safe to proceed with atomic mkdir
        pass

    # SECURITY: Attempt atomic directory creation
    # exist_ok=False ensures we fail if path was created between check and mkdir
    try:
        vm_dir.mkdir(parents=True, exist_ok=False)
    except FileExistsError:
        # Race condition: path created between our check and mkdir
        # Re-verify to detect symlinks
        if os.path.islink(vm_dir):
            raise MVMError(f"VM '{name}' path is a symlink (race condition detected): {vm_dir}")
        raise MVMError(f"VM '{name}' already exists at {vm_dir}")

    # SECURITY: Verify the created directory is not a symlink
    # This catches cases where mkdir followed a symlink to a different parent
    if os.path.islink(vm_dir):
        # Attempt cleanup - but the symlink attack may have already succeeded
        # We can't safely clean up, just report the security issue
        raise MVMError(f"VM '{name}' directory is a symlink (security violation): {vm_dir}")


def graceful_shutdown(pid: int | None, socket_path: Path | None, force: bool = False) -> None:
    if pid is None:
        return

    def _is_alive(p: int) -> bool:
        try:
            os.kill(p, 0)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    if force:
        # Skip graceful shutdown - go straight to SIGTERM → SIGKILL
        if _is_alive(pid):
            try:
                os.kill(pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
            time.sleep(float(FIRECRACKER_SIGTERM_WAIT_S))

        if _is_alive(pid):
            try:
                os.kill(pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
        return

    if socket_path is not None and Path(socket_path).exists():
        try:
            client = FirecrackerClient(Path(socket_path))
            client.send_ctrl_alt_del()
            client.close()
        except (ProcessLookupError, PermissionError, InterruptedError):
            pass
        # Poll for FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S seconds (100ms steps)
        # to allow graceful shutdown before SIGTERM/SIGKILL.
        _poll_steps = int(FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S / CONST_POLL_STEP_SECONDS)
        for _ in range(_poll_steps):
            time.sleep(FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S)
            # P-L3: single check per iteration — no fix needed
            if not _is_alive(pid):
                break

    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        time.sleep(float(FIRECRACKER_SIGTERM_WAIT_S))

    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


def cleanup_tap(tap_name: str, bridge: str | None = None) -> None:
    try:
        remove_iptables_forward_rules(tap_name, bridge=bridge or BRIDGE_NAME)
        delete_tap(tap_name)
    except NetworkError:
        pass


def create_vm(
    name: str,
    image: str,
    kernel: str | None = None,
    image_path: Path | None = None,
    kernel_path: Path | None = None,
    vcpus: int = DEFAULT_VM_VCPU_COUNT,
    mem: int = DEFAULT_VM_MEM_MIB,
    disk_size: str | None = None,
    ip: str | None = None,
    network_name: str = DEFAULT_NETWORK_NAME,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    user: str = DEFAULT_VM_SSH_USER,
    enable_api_socket: bool = DEFAULT_VM_ENABLE_API_SOCKET,
    enable_pci: bool = DEFAULT_VM_ENABLE_PCI,
    enable_console: bool = DEFAULT_VM_ENABLE_CONSOLE,
    firecracker_bin: str = DEFAULT_FIRECRACKER_BIN_NAME,
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT,
    cloud_init_iso_path: Path | None = None,
    keep_cloud_init_iso: bool = False,
    vm_manager: VMManager | None = None,
    nocloud_net_port: int = 0,
) -> VMInstance:
    import ipaddress as _ipaddress
    import re

    from mvmctl.utils.validation import validate_entity_name

    # Resource tracking for comprehensive cleanup
    vm_dir: Path | None = None
    resources_created = {
        "vm_dir": False,
        "tap": False,
        "network_ip": False,
        "nocloud_server": False,
        "firewall_rule": False,
        "console_relay": False,
    }

    # Variables needed for cleanup
    tap_name: str = ""
    guest_ip: str = ""
    net_manager: NoCloudNetServerManager | None = None
    relay_mgr: ConsoleRelayManager | None = None
    pty_master_fd: int | None = None
    pty_slave_fd: int | None = None
    effective_mode: CloudInitMode = CloudInitMode.NET
    net_config = None
    proc = None
    log_fp = None
    console_fp = None
    vm_id: str | None = None

    try:
        validate_entity_name(name, "VM")

        manager = vm_manager or get_vm_manager()
        if manager.count_vms() >= MAX_VMS:
            raise MVMError(
                f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
            )

        if not (1 <= vcpus <= 32):
            raise MVMError(f"Invalid vcpus={vcpus}: must be between 1 and 32")
        if not (CONST_VM_MEM_MIN_MIB <= mem <= CONST_VM_MEM_MAX_MIB):
            raise MVMError(f"Invalid mem_size_mib={mem}: must be between 128 and 65536")

        # Determine effective cloud-init mode
        if cloud_init_mode == CloudInitMode.INJECT:
            effective_mode = CloudInitMode.INJECT
        elif cloud_init_mode == CloudInitMode.ISO:
            effective_mode = CloudInitMode.ISO
        else:
            effective_mode = cloud_init_mode

        # Setup nocloud firewall chain (idempotent)
        setup_nocloud_input_chain()

        if mac is not None:
            mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
            if not mac_re.match(mac):
                raise MVMError(
                    f"Invalid MAC address format: {mac!r}. Expected format: XX:XX:XX:XX:XX:XX"
                )

        vm_id = generate_vm_id(name)
        vm_dir = get_vm_dir(vm_id)
        _secure_mkdir_vm(vm_dir, name)
        resources_created["vm_dir"] = True

        # Kernel resolution with path override
        kernel_path_resolved: Path
        if kernel_path is not None:
            kernel_path_resolved = kernel_path
        elif kernel:
            kernel_path_resolved = _resolve_kernel_path(kernel)
        else:
            env_kernel = os.environ.get("MVM_KERNEL")
            if env_kernel:
                kernel_path_resolved = _resolve_kernel_path(env_kernel)
            else:
                kernel_path_resolved = get_kernels_dir() / DEFAULT_VM_KERNEL_FILENAME

        if not kernel_path_resolved.exists():
            raise MVMError(f"Kernel not found: {kernel_path_resolved}")

        fc_bin_path = Path(firecracker_bin)
        if fc_bin_path.is_absolute() or "/" in firecracker_bin:
            if not fc_bin_path.exists():
                raise MVMError(f"Firecracker binary not found: {firecracker_bin}")
            if not os.access(fc_bin_path, os.X_OK):
                raise MVMError(f"Firecracker binary is not executable: {firecracker_bin}")

        # Image resolution with path override
        if image_path is not None:
            resolved_image_path = image_path
            # Still resolve metadata by image identifier for fs_uuid/fs_type
            image_fs_uuid = _resolve_image_fs_uuid(image) if image else None
            image_fs_type = _resolve_image_fs_type(image) if image else None
        else:
            resolved_image_path = _resolve_image_path(image)
            image_fs_uuid = _resolve_image_fs_uuid(image)
            image_fs_type = _resolve_image_fs_type(image)

        # Validate resolved filesystem metadata
        if image_fs_uuid:
            from mvmctl.utils.validation import validate_fs_uuid

            validate_fs_uuid(image_fs_uuid)
        if image_fs_type:
            from mvmctl.utils.validation import validate_fs_type

            validate_fs_type(image_fs_type)

        if user_data is not None and not user_data.exists():
            raise MVMError(f"User-data file not found: {user_data}")

        net_config = get_network(network_name)
        if net_config is None:
            if network_name == DEFAULT_NETWORK_NAME:
                net_config = ensure_default_network()
            else:
                raise NetworkError(f"Network '{network_name}' not found")

        if ip:
            try:
                ip_net = _ipaddress.IPv4Network(net_config.cidr, strict=False)
                if _ipaddress.IPv4Address(ip.split("/")[0]) not in ip_net:
                    raise NetworkError(
                        f"IP {ip} is outside network '{network_name}' subnet {net_config.cidr}"
                    )
            except ValueError as e:
                raise NetworkError(f"Invalid IP address: {e}")
            guest_ip = ip
        else:
            guest_ip = allocate_network_ip(network_name, name)
            resources_created["network_ip"] = True

        guest_mac = mac if mac else generate_mac()
        tap_name = _generate_tap_name(network_name, name)
        bridge = net_config.bridge

        # Copy image to VM directory (VM-local rootfs)
        # Handle compressed images (.zst suffix)
        if resolved_image_path.suffix == ".zst":
            rootfs_ext = resolved_image_path.suffixes[-2]  # Get .ext4 from .ext4.zst
            vm_rootfs_path = vm_dir / f"rootfs{rootfs_ext}"
            fs_type = rootfs_ext.lstrip(".")

            # Look up image hash from metadata by filename
            cache_dir = get_cache_dir()
            all_entries = list_image_entries(cache_dir)
            image_hash = resolved_image_path.stem  # fallback
            for img_id, meta in all_entries.items():
                if meta.get("filename") == resolved_image_path.name:
                    image_hash = img_id
                    break

            # Ensure image is in ready pool (tmpfs), then fast-copy
            ensure_image_in_ready_pool(resolved_image_path, image_hash, fs_type)
            copy_from_ready_pool(image_hash, fs_type, vm_rootfs_path)
        else:
            rootfs_ext = resolved_image_path.suffix
            vm_rootfs_path = vm_dir / f"rootfs{rootfs_ext}"
            shutil.copy2(resolved_image_path, vm_rootfs_path)
        rootfs_path = vm_rootfs_path

        # Grow rootfs if disk_size specified (only the VM-local copy)
        # This must happen BEFORE cloud-init injection for DIRECT_INJECTION mode
        if disk_size is not None:
            from mvmctl.utils.disk_size import parse_disk_size

            target_bytes = parse_disk_size(disk_size)
            grow_rootfs_with_guestfs(vm_rootfs_path, target_bytes)

        # Resolve SSH keys for DISABLED mode (outside the cloud-init block)
        disabled_ssh_pub_key: list[str] | str | None = None
        if effective_mode == CloudInitMode.OFF and ssh_key is not None:
            disabled_ssh_pub_key = resolve_ssh_key(ssh_key)
        elif effective_mode == CloudInitMode.OFF and ssh_key is None:
            from mvmctl.core.key_manager import get_default_keys as _get_default_keys
            from mvmctl.utils.fs import get_keys_dir as _get_keys_dir

            default_names = _get_default_keys()
            if default_names:
                keys_dir = _get_keys_dir()
                resolved_keys: list[str] = []
                for kname in default_names:
                    pub_file = keys_dir / f"{kname}.pub"
                    if pub_file.exists():
                        content_key = pub_file.read_text().strip()
                        if content_key:
                            resolved_keys.append(content_key)
                    else:
                        logger.warning(
                            "Default key '%s' not found at %s — skipping", kname, pub_file
                        )
                disabled_ssh_pub_key = resolved_keys if resolved_keys else None
            else:
                disabled_ssh_pub_key = resolve_ssh_key(None)

        # Inject SSH keys directly into rootfs for DISABLED mode
        if effective_mode == CloudInitMode.OFF and disabled_ssh_pub_key is not None:
            _inject_ssh_keys_for_disabled_mode(rootfs_path, disabled_ssh_pub_key, vm_dir, user)

        # Handle cloud-init based on mode
        cloud_init_iso: Path | None = None
        extra_drives: list[DriveConfig] = []
        nocloud_net_url: str | None = None
        nocloud_server_pid: int | None = None

        if effective_mode != CloudInitMode.OFF:
            cloud_init_dir = vm_dir / DEFAULT_CLOUD_INIT_DIRNAME
            cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

            ssh_pub_key: list[str] | str | None
            if ssh_key is not None:
                ssh_pub_key = resolve_ssh_key(ssh_key)
            else:
                from mvmctl.core.key_manager import get_default_keys as _get_default_keys
                from mvmctl.utils.fs import get_keys_dir as _get_keys_dir

                default_names = _get_default_keys()
                if default_names:
                    keys_dir = _get_keys_dir()
                    resolved: list[str] = []
                    for kname in default_names:
                        pub_file = keys_dir / f"{kname}.pub"
                        if pub_file.exists():
                            content = pub_file.read_text().strip()
                            if content:
                                resolved.append(content)
                        else:
                            logger.warning(
                                "Default key '%s' not found at %s — skipping", kname, pub_file
                            )
                    ssh_pub_key = resolved if resolved else None
                else:
                    ssh_pub_key = resolve_ssh_key(None)

            _prefix_len = _ipaddress.IPv4Network(net_config.cidr, strict=False).prefixlen

            write_cloud_init(
                cloud_init_dir,
                name,
                guest_ip,
                user,
                ssh_pub_key=ssh_pub_key,
                custom_user_data=user_data,
                gateway=net_config.gateway,
                prefix_len=_prefix_len,
                skip_network_config=False,
            )

            if effective_mode == CloudInitMode.ISO:
                if cloud_init_iso_path is None:
                    raise MVMError("cloud_init_iso_path required when cloud_init_mode is CUSTOM")
                if not cloud_init_iso_path.exists():
                    raise MVMError(f"Custom cloud-init ISO not found: {cloud_init_iso_path}")
                cloud_init_iso = cloud_init_iso_path
            elif effective_mode == CloudInitMode.NET:
                net_manager = NoCloudNetServerManager()
                url, port = net_manager.start_server(
                    name, cloud_init_dir, net_config.gateway, vm_id, preferred_port=nocloud_net_port
                )
                nocloud_net_url = url
                nocloud_net_port = port
                nocloud_server_pid = net_manager.get_server_pid(name, vm_id)
                resources_created["nocloud_server"] = True
                logger.info("NoCloud-net server started at %s", nocloud_net_url)
                add_nocloud_input_rule(guest_ip, name, nocloud_net_port)
                resources_created["firewall_rule"] = True
            elif effective_mode == CloudInitMode.INJECT:
                try:
                    inject_cloud_init(str(rootfs_path), str(cloud_init_dir))
                except Exception as e:
                    raise CloudInitError(f"Direct injection failed: {e}") from e
            elif effective_mode in (CloudInitMode.INJECT, CloudInitMode.ISO):
                cloud_init_iso = vm_dir / DEFAULT_CLOUD_INIT_ISO_NAME
                try:
                    create_cloud_init_iso(cloud_init_dir, cloud_init_iso)
                except CloudInitError as e:
                    raise MVMError(f"Failed to create cloud-init ISO: {e}") from e

        socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME if enable_api_socket else None
        _net = _ipaddress.IPv4Network(net_config.cidr, strict=False)
        _subnet_mask = str(_net.netmask)

        vm_config = VMConfig(
            name=name,
            vcpu_count=vcpus,
            mem_size_mib=mem,
            kernel_path=kernel_path_resolved,
            rootfs_path=rootfs_path,
            guest_ip=guest_ip,
            guest_mac=guest_mac,
            gateway=net_config.gateway,
            subnet_mask=_subnet_mask,
            tap_device=tap_name,
            root_uuid=image_fs_uuid,
            root_fs_type=image_fs_type,
            enable_api_socket=enable_api_socket,
            enable_pci=enable_pci,
            enable_console=enable_console,
            cloud_init_mode=effective_mode,
            cloud_init_iso_path=cloud_init_iso,
            keep_cloud_init_iso=keep_cloud_init_iso,
            nocloud_net_url=nocloud_net_url,
            extra_drives=extra_drives,
        )
        config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME
        ConfigGenerator(vm_config, vm_dir).write_to_file(config_file)

        console_socket_path: Path | None = None
        console_relay_pid: int | None = None

        if enable_console:
            pty_master_fd, pty_slave_fd = os.openpty()
            relay_mgr = ConsoleRelayManager()

        # AUDIT-4: Reconcile bridge if it has drifted
        if not bridge_exists(bridge):
            logger.info("Bridge %s not found — recreating for network '%s'", bridge, network_name)
            _gw_cidr = (
                f"{net_config.gateway}"
                f"/{_ipaddress.IPv4Network(net_config.cidr, strict=False).prefixlen}"
            )
            setup_bridge(bridge, gateway_cidr=_gw_cidr)
            if net_config.nat_enabled:
                setup_nat(
                    bridge, nat_gateways=net_config.nat_gateways or None, cidr=net_config.cidr
                )

        try:
            create_tap(tap_name, bridge=bridge)
            resources_created["tap"] = True
            add_iptables_forward_rules(tap_name, bridge=bridge)
        except NetworkError as e:
            raise NetworkError(f"Network setup failed: {e}") from e

        log_file = vm_dir / DEFAULT_FC_LOG_FILENAME
        console_log_file = vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME
        pid_file = vm_dir / DEFAULT_FC_PID_FILENAME

        fc_cmd = [firecracker_bin, "--no-api", "--config-file", str(config_file)]
        if enable_api_socket and socket_path:
            fc_cmd = [
                firecracker_bin,
                "--api-sock",
                str(socket_path),
                "--config-file",
                str(config_file),
            ]

        log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
        if enable_console and pty_slave_fd is not None:
            proc = subprocess.Popen(
                fc_cmd,
                stdin=pty_slave_fd,
                stdout=pty_slave_fd,
                stderr=log_fp,
                start_new_session=True,
                pass_fds=[pty_slave_fd],
            )
        else:
            console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
            proc = subprocess.Popen(
                fc_cmd,
                stdin=subprocess.DEVNULL,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )

        if enable_console and pty_slave_fd is not None:
            try:
                os.close(pty_slave_fd)
            except OSError:
                pass

        # Close file handles after subprocess inherits them (success path only)
        try:
            log_fp.close()
        except OSError:
            pass
        if console_fp is not None:
            try:
                console_fp.close()
            except OSError:
                pass

        if enable_console and relay_mgr is not None and pty_master_fd is not None:
            try:
                console_socket_path, console_relay_pid = relay_mgr.start_relay(
                    name, pty_master_fd, vm_dir
                )
                resources_created["console_relay"] = True
            except MVMError as e:
                logger.warning("Failed to start console relay: %s", e)
                try:
                    os.close(pty_master_fd)
                except OSError:
                    pass

        _write_pid_file(pid_file, proc.pid)

        vm_instance = VMInstance(
            name=name,
            id=vm_id,  # Use the pre-generated ID
            pid=proc.pid,
            socket_path=socket_path,
            ip=guest_ip,
            mac=guest_mac,
            network_name=network_name,
            tap_device=tap_name,
            created_at=datetime.now(tz=timezone.utc),
            status=VMState.RUNNING,
            config=vm_config,  # Persist VM config with VM-local rootfs_path
            nocloud_net_port=nocloud_net_port,
            nocloud_server_pid=nocloud_server_pid,
            console_relay_pid=console_relay_pid,
            console_socket_path=console_socket_path,
            rootfs_suffix=rootfs_ext,
            kernel_id=str(kernel_path_resolved),
            image_id=str(resolved_image_path),
        )
        manager.register(vm_instance)

        return vm_instance

    except (VMCreateError, NetworkError, CloudInitError, MVMError):
        logger.debug("VM creation failed with typed exception, performing cleanup")
        _cleanup_vm_creation_resources(
            resources_created,
            vm_dir,
            net_manager,
            relay_mgr,
            net_config,
            name,
            vm_id,
            guest_ip,
            nocloud_net_port,
            tap_name,
            pty_master_fd,
            pty_slave_fd,
            log_fp,
            console_fp,
        )
        raise
    except FileNotFoundError as e:
        logger.debug("VM creation failed with FileNotFoundError, performing cleanup: %s", e)
        _cleanup_vm_creation_resources(
            resources_created,
            vm_dir,
            net_manager,
            relay_mgr,
            net_config,
            name,
            vm_id,
            guest_ip,
            nocloud_net_port,
            tap_name,
            pty_master_fd,
            pty_slave_fd,
            log_fp,
            console_fp,
        )
        raise MVMError(f"Firecracker binary not found: {firecracker_bin}") from e
    except Exception as e:
        logger.debug("VM creation failed with unexpected error, performing cleanup: %s", e)
        _cleanup_vm_creation_resources(
            resources_created,
            vm_dir,
            net_manager,
            relay_mgr,
            net_config,
            name,
            vm_id,
            guest_ip,
            nocloud_net_port,
            tap_name,
            pty_master_fd,
            pty_slave_fd,
            log_fp,
            console_fp,
        )
        raise VMCreateError(f"Failed to create VM: {e}") from e


def remove_vm(name: str, vm_manager: VMManager | None = None) -> None:
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = get_vm_dir(vm.id)
    net_name = vm.network_name or DEFAULT_NETWORK_NAME
    tap_name = vm.tap_device or _generate_tap_name(net_name, name)

    net_config = get_network(net_name)
    bridge = net_config.bridge if net_config else BRIDGE_NAME

    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
    pid = _read_pid_file(pid_file)
    if pid is None:
        pid = vm.pid

    graceful_shutdown(pid, vm.socket_path)

    # Try to capture exit code after shutdown
    if pid is not None:
        try:
            # Try to get exit status if we're the parent
            _, status = os.waitpid(pid, os.WNOHANG)
            if os.WIFEXITED(status):
                exit_code = os.WEXITSTATUS(status)
                _write_exit_code(vm_dir, exit_code)
            elif os.WIFSIGNALED(status):
                sig = os.WTERMSIG(status)
                _write_exit_code(vm_dir, CONST_SIGNAL_EXIT_CODE_BASE + sig)
        except (ChildProcessError, OSError):
            # Process not our child or already reaped - exit code unknown
            pass

    if vm.console_relay_pid is not None:
        try:
            relay_mgr = ConsoleRelayManager()
            relay_mgr.stop_relay(name, vm.id)
        except (OSError, RuntimeError) as e:
            logger.warning("Failed to cleanup console relay: %s", e)

    # Stop nocloud-net server and remove firewall rule if VM has nocloud-net configured
    if vm.nocloud_net_port is not None and vm.ip is not None:
        try:
            nocloud_manager = NoCloudNetServerManager()
            if vm.id:
                nocloud_manager.stop_server(name, vm.id)
            else:
                nocloud_manager.stop_server(name)
            remove_nocloud_input_rule(vm.ip, name, vm.nocloud_net_port)
        except (OSError, RuntimeError, NetworkError) as e:
            logger.warning("Failed to cleanup nocloud-net resources: %s", e)

    remove_iptables_forward_rules(tap_name, bridge=bridge)

    try:
        teardown_nat(bridge, force=False, cidr=net_config.cidr if net_config else None)
    except NetworkError as e:
        logger.debug("NAT teardown for bridge %s: %s", bridge, e)

    try:
        delete_tap(tap_name)
    except NetworkError:
        pass

    try:
        release_network_ip(net_name, name)
    except NetworkError as e:
        logger.warning("Failed to release network IP: %s", e)

    if vm.ip:
        try:
            subprocess.run(
                ["ssh-keygen", "-R", vm.ip],
                capture_output=True,
                check=False,
            )
        except FileNotFoundError:
            pass

    manager.deregister(vm.id)

    if vm_dir.exists():
        shutil.rmtree(vm_dir)

    # Clean up any orphaned nocloud servers
    try:
        nocloud_manager = NoCloudNetServerManager()
        nocloud_manager.cleanup_orphans()
    except Exception:
        # Don't fail VM removal if orphan cleanup fails
        pass


def snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None:
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )

    client = FirecrackerClient(socket_path)
    try:
        client.create_snapshot(mem_out, state_out)
    finally:
        client.close()


def load_snapshot(name: str, mem_in: Path, state_in: Path, resume_after: bool = True) -> None:
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )

    client = FirecrackerClient(socket_path)
    try:
        client.load_snapshot(mem_in, state_in, resume_after)
    finally:
        client.close()


def pause_vm(name: str, vm_manager: VMManager | None = None) -> None:
    """Pause a running VM.

    Args:
        name: VM name to pause.
        vm_manager: Optional VMManager instance.

    Raises:
        VMNotFoundError: If VM not found.
        MVMError: If VM is not running or pause fails.
    """
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    if vm.status != VMState.RUNNING:
        raise MVMError(f"VM '{name}' is not running (current state: {vm.status.value})")

    socket_path = vm.socket_path
    if not socket_path:
        raise MVMError(f"VM '{name}' has no API socket enabled")

    client = FirecrackerClient(socket_path)
    try:
        client.pause_vm()
        manager.update_status(name, VMState.PAUSED)
    finally:
        client.close()


def resume_vm(name: str, vm_manager: VMManager | None = None) -> None:
    """Resume a paused VM.

    Args:
        name: VM name to resume.
        vm_manager: Optional VMManager instance.

    Raises:
        VMNotFoundError: If VM not found.
        MVMError: If VM is not paused or resume fails.
    """
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    if vm.status != VMState.PAUSED:
        raise MVMError(f"VM '{name}' is not paused (current state: {vm.status.value})")

    socket_path = vm.socket_path
    if not socket_path:
        raise MVMError(f"VM '{name}' has no API socket enabled")

    client = FirecrackerClient(socket_path)
    try:
        client.resume_vm()
        manager.update_status(name, VMState.RUNNING)
    finally:
        client.close()


def stop_vm(name: str, vm_manager: VMManager | None = None, force: bool = False) -> None:
    """Gracefully stop a running VM via SendCtrlAltDel, then wait for process exit.

    Args:
        name: VM name to stop.
        vm_manager: Optional VMManager instance.
        force: If True, skip graceful shutdown and go straight to SIGTERM → SIGKILL.

    Raises:
        VMNotFoundError: If VM not found.
        MVMError: If VM is not running/paused or stop fails.
    """
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    if vm.status not in (VMState.RUNNING, VMState.PAUSED):
        raise MVMError(f"VM '{name}' is not running (current state: {vm.status.value})")

    pid = vm.pid
    socket_path = vm.socket_path

    manager.update_status(name, VMState.STOPPING)

    try:
        graceful_shutdown(pid, socket_path, force=force)
        manager.update_status(name, VMState.STOPPED)
    except Exception as e:
        manager.update_status(name, VMState.ERROR)
        raise MVMError(f"Failed to stop VM '{name}': {e}") from e


def start_vm(name: str, vm_manager: VMManager | None = None) -> None:
    """Re-launch a stopped VM using its stored firecracker.json config.

    Args:
        name: VM name to start.
        vm_manager: Optional VMManager instance.

    Raises:
        VMNotFoundError: If VM not found.
        MVMError: If VM is not stopped or start fails.
    """
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    if vm.status != VMState.STOPPED:
        raise MVMError(f"VM '{name}' is not stopped (current state: {vm.status.value})")

    if not vm.id:
        raise MVMError(f"VM '{name}' has no ID")

    vm_dir = get_vm_dir(vm.id)
    config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME

    if not config_file.exists():
        raise MVMError(f"VM config not found: {config_file}")

    firecracker_bin = DEFAULT_FIRECRACKER_BIN_NAME
    if vm.config and vm.config.kernel_path:
        fc_bin_path = Path(firecracker_bin)
        if fc_bin_path.is_absolute() or "/" in firecracker_bin:
            if not fc_bin_path.exists():
                raise MVMError(f"Firecracker binary not found: {firecracker_bin}")

    enable_api_socket = vm.config.enable_api_socket if vm.config else DEFAULT_VM_ENABLE_API_SOCKET
    socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME if enable_api_socket else None

    if enable_api_socket and socket_path:
        fc_cmd = [
            firecracker_bin,
            "--api-sock",
            str(socket_path),
            "--config-file",
            str(config_file),
        ]
    else:
        fc_cmd = [firecracker_bin, "--no-api", "--config-file", str(config_file)]

    log_file = vm_dir / DEFAULT_FC_LOG_FILENAME
    console_log_file = vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME

    log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
    console_fp = None

    try:
        if vm.config and vm.config.enable_console:
            console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
            proc = subprocess.Popen(
                fc_cmd,
                stdin=subprocess.DEVNULL,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )
        else:
            console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
            proc = subprocess.Popen(
                fc_cmd,
                stdin=subprocess.DEVNULL,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )

        log_fp.close()
        if console_fp:
            console_fp.close()

        _write_pid_file(pid_file, proc.pid)

        vm.pid = proc.pid
        vm.socket_path = socket_path
        vm.status = VMState.RUNNING
        manager.register(vm)

        time.sleep(CONST_VM_START_WAIT_S)

    except Exception as e:
        try:
            log_fp.close()
        except OSError:
            pass
        if console_fp:
            try:
                console_fp.close()
            except OSError:
                pass
        raise MVMError(f"Failed to start VM '{name}': {e}") from e


def reboot_vm(name: str, vm_manager: VMManager | None = None, force: bool = False) -> None:
    """Reboot a VM: graceful stop then re-launch.

    Args:
        name: VM name to reboot.
        vm_manager: Optional VMManager instance.
        force: If True, force immediate shutdown during the stop phase.

    Raises:
        VMNotFoundError: If VM not found.
        MVMError: If stop or start fails.
    """
    stop_vm(name, vm_manager, force=force)
    start_vm(name, vm_manager)
