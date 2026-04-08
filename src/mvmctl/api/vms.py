"""VM lifecycle API — create, remove, list, ssh, logs."""

from __future__ import annotations

import logging
import os
import shutil
import signal
import subprocess
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_DEFAULT_USER_GID,
    CONST_DEFAULT_USER_UID,
    CONST_DIR_PERMS_CACHE,
    CONST_FILE_PERMS_EXECUTABLE,
    CONST_FILE_PERMS_PRIVATE_KEY,
    CONST_FILE_PERMS_PUBLIC_KEY,
    CONST_FILE_PERMS_SHADOW,
    CONST_FILE_PERMS_SUDOERS,
    CONST_ROOT_GID,
    CONST_ROOT_UID,
    CONST_SHADOW_DAYS_SINCE_EPOCH,
    CONST_SHADOW_MAX_DAYS,
    CONST_SHADOW_MIN_DAYS,
    CONST_SHADOW_WARN_DAYS,
    CONST_SIGNAL_EXIT_CODE_BASE,
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    CONST_VM_START_WAIT_S,
    DEFAULT_BRIDGE_NAME,
    DEFAULT_CLOUD_INIT_DIRNAME,
    DEFAULT_CLOUD_INIT_ISO_NAME,
    DEFAULT_FC_API_SOCKET_FILENAME,
    DEFAULT_FC_CONFIG_FILENAME,
    DEFAULT_FC_CONSOLE_LOG_FILENAME,
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_PID_FILENAME,
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_SNAPSHOT_RESUME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_KERNEL_FILENAME,
    MAX_VMS,
)
from mvmctl.core.config_gen import ConfigGenerator, DriveConfig
from mvmctl.core.console import (
    check_escape_sequence,
    connect_to_relay,
    disconnect_from_relay,
    read_console_output,
    send_console_input,
)
from mvmctl.core.console import (
    get_console_state as _get_console_state,
)
from mvmctl.core.firecracker import FirecrackerClient, get_vm_socket_path
from mvmctl.core.firewall import (
    add_nocloud_input_rule,
    remove_nocloud_input_rule,
    setup_nocloud_input_chain,
)
from mvmctl.core.image import copy_from_ready_pool, ensure_image_in_ready_pool
from mvmctl.core.kernel import resolve_kernel_path as _resolve_kernel_path
from mvmctl.core.logs import show_logs
from mvmctl.core.network import (
    add_iptables_forward_rules,
    create_tap,
    delete_tap,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_nat,
    teardown_nat,
)
from mvmctl.core.ssh import connect_to_vm, resolve_ssh_key
from mvmctl.core.vm_lifecycle import _secure_mkdir_vm, grow_rootfs_with_guestfs
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.core.vm_process import (
    _read_pid_file,
    _write_exit_code,
    _write_pid_file,
    cleanup_tap,
    graceful_shutdown,
)
from mvmctl.core.vm_process import (
    pause_vm as _pause_process,
)
from mvmctl.core.vm_process import (
    resume_vm as _resume_process,
)
from mvmctl.exceptions import (
    AssetNotFoundError,
    CloudInitError,
    MVMError,
    NetworkError,
    VMCreateError,
    VMNotFoundError,
)
from mvmctl.models import CloudInitMode, VMConfig, VMExportConfig, VMInstance, VMStatus
from mvmctl.services.console_relay import ConsoleRelayManager
from mvmctl.services.nocloud_server import NoCloudNetServerManager
from mvmctl.utils.fs import get_cache_dir, get_kernels_dir, get_vm_dir_by_hash
from mvmctl.utils.full_hash import generate_vm_id
from mvmctl.utils.network import (
    bridge_exists,
    generate_mac,
    generate_tap_name,
    subnet_mask_from_subnet,
)
from mvmctl.utils.validation import validate_entity_name, validate_fs_type, validate_fs_uuid

logger = logging.getLogger(__name__)

__all__ = [
    "get_vm_status_with_exit_code",
    "list_vms",
    "get_vm",
    "vm_cache_dir",
    "create_vm",
    "remove_vm",
    "snapshot_vm",
    "load_snapshot",
    "pause_vm",
    "resume_vm",
    "stop_vm",
    "start_vm",
    "reboot_vm",
    "ssh_vm",
    "get_logs",
    "cleanup_vms",
    "get_vm_manager",
    "VMManager",
    "resolve_image_path",
    "resolve_kernel_path",
    "resolve_image_id_path",
    "resolve_kernel_id_path",
    "resolve_image_multi_strategy",
    "resolve_kernel_multi_strategy",
    "attach_console",
    "kill_console",
    "get_console_state",
    "check_escape_sequence",
    "connect_to_relay",
    "disconnect_from_relay",
    "read_console_output",
    "send_console_input",
    "export_vm_config",
    "compute_vm_is_missing",
]


def resolve_image_path(image: str) -> Path:
    from mvmctl.api.assets import resolve_image_path as _api_resolve_image_path

    return _api_resolve_image_path(image)


def resolve_kernel_path(kernel: str) -> Path:
    from mvmctl.api.kernel import resolve_kernel_path as _api_resolve_kernel_path

    return _api_resolve_kernel_path(kernel)


def resolve_image_id_path(image: str) -> Path:
    from mvmctl.api.assets import resolve_image_id_path as _api_resolve_image_id_path

    return _api_resolve_image_id_path(image)


def resolve_kernel_id_path(kernel: str) -> Path:
    from mvmctl.api.kernel import resolve_kernel_id_path as _api_resolve_kernel_id_path

    return _api_resolve_kernel_id_path(kernel)


def resolve_image_multi_strategy(value: str) -> Path:
    """Resolve image value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/' or ends with .ext4/.btrfs)
    2. YAML image name lookup (via os_slug)
    3. Short-ID resolution against metadata.json
    """
    from mvmctl.core.metadata import list_image_entries
    from mvmctl.utils.fs import get_cache_dir, get_images_dir

    images_dir = get_images_dir()
    cache_dir = get_cache_dir()

    # Direct path check
    if "/" in value or value.endswith((".ext4", ".btrfs")):
        path = Path(value)
        if path.exists():
            return path

    # YAML image name lookup (check os_slug in metadata)
    all_entries = list_image_entries(cache_dir)
    for full_key, meta in all_entries.items():
        os_slug = str(meta.get("os_slug", ""))
        if os_slug == value:
            path_str = str(meta.get("path", ""))
            if path_str:
                candidate = images_dir / path_str
                if candidate.exists():
                    return candidate
            # Try full_key with extensions
            for ext in (".ext4", ".btrfs"):
                candidate = images_dir / f"{full_key}{ext}"
                if candidate.exists():
                    return candidate
            # Try just the value name with extensions
            for ext in (".ext4", ".btrfs"):
                candidate = images_dir / f"{value}{ext}"
                if candidate.exists():
                    return candidate

    # ID prefix resolution
    from mvmctl.api.assets import resolve_image_id_path as _api_resolve_image_id_path

    return _api_resolve_image_id_path(value)


def resolve_kernel_multi_strategy(value: str) -> Path:
    """Resolve kernel value to path using multiple strategies.

    Resolution order:
    1. Direct path (if contains '/')
    2. Short-ID resolution against metadata.json
    """
    from mvmctl.api.kernel import resolve_kernel_id_path as _api_resolve_kernel_id_path
    from mvmctl.utils.fs import get_kernels_dir

    kernels_dir = get_kernels_dir()

    # Direct path check
    if "/" in value:
        path = Path(value)
        if path.exists():
            return path

    # Check if it's a direct filename in kernels dir
    candidate = kernels_dir / value
    if candidate.exists():
        return candidate

    # ID prefix resolution
    return _api_resolve_kernel_id_path(value)


def resolve_vm_selector(selector: str) -> str:
    """Resolve a VM selector (name or ID prefix) to a VM name.

    Tries ID-prefix lookup first, falls back to treating selector as name.
    Raises MVMError if the prefix is ambiguous (matches multiple VMs).

    Args:
        selector: VM name or ID prefix

    Returns:
        Resolved VM name

    Raises:
        MVMError: If ID prefix is ambiguous (matches multiple VMs)
    """
    from mvmctl.exceptions import MVMError

    manager = get_vm_manager()
    matches = manager.find_by_id_prefix(selector)
    if len(matches) == 1:
        return matches[0].name
    if len(matches) > 1:
        names = ", ".join(m.name for m in matches)
        raise MVMError(f"Ambiguous ID prefix '{selector}' matches {len(matches)} VMs: {names}")
    # No ID match — treat as name
    return selector


@dataclass
class ResolveVMTargetsResult:
    targets: list[VMInstance]
    errors: list[str]
    exit_code: int


def resolve_vm_targets(
    ids: list[str],
    names: list[str],
) -> ResolveVMTargetsResult:
    """Resolve multiple VM ID prefixes and names to VMInstance objects.

    Collects all errors rather than failing on the first, then deduplicates
    targets by ID. Used by CLI commands that accept multiple VM selectors.

    Args:
        ids: List of VM ID prefixes.
        names: List of VM names.

    Returns:
        ResolveVMTargetsResult with resolved targets, error messages, and exit code.
    """
    manager = get_vm_manager()
    targets: list[VMInstance] = []
    errors: list[str] = []

    for prefix in ids:
        matches = manager.find_by_id_prefix(prefix)
        if len(matches) == 0:
            errors.append(f"No VM found with ID prefix '{prefix}'")
        elif len(matches) > 1:
            errors.append(f"Multiple VMs match ID prefix '{prefix}' — use a longer prefix or name")
        else:
            targets.append(matches[0])

    for n in names:
        matches = manager.get_by_name(n)
        if len(matches) == 0:
            errors.append(f"No VM found with name '{n}'")
        elif len(matches) > 1:
            errors.append(
                f"Multiple VMs match name '{n}'. Use ID instead of name, or remove VMs individually."
            )
        else:
            targets.append(matches[0])

    # Deduplicate by ID
    seen: set[str] = set()
    unique: list[VMInstance] = []
    for vm in targets:
        if vm.id not in seen:
            seen.add(vm.id)
            unique.append(vm)
    targets = unique

    exit_code = 1 if errors and not targets else 0
    return ResolveVMTargetsResult(targets=targets, errors=errors, exit_code=exit_code)


def _detect_init_system_and_enable_ssh(guestfs_handle: Any, rootfs_path: Path) -> bool:
    init_system = "unknown"

    if guestfs_handle.exists("/lib/systemd/systemd") or guestfs_handle.exists(
        "/usr/lib/systemd/systemd"
    ):
        init_system = "systemd"
        logger.debug("Detected systemd init system in %s", rootfs_path.name)
    elif guestfs_handle.exists("/sbin/openrc") or guestfs_handle.exists("/usr/sbin/openrc"):
        init_system = "openrc"
        logger.debug("Detected OpenRC init system in %s", rootfs_path.name)
    elif guestfs_handle.exists("/etc/init.d/"):
        init_system = "sysvinit"
        logger.debug("Detected sysvinit in %s", rootfs_path.name)
    else:
        logger.warning("Unknown init system in %s, cannot enable SSH", rootfs_path.name)
        return False

    try:
        if init_system == "systemd":
            ssh_services = [
                "/usr/lib/systemd/system/ssh.service",
                "/lib/systemd/system/ssh.service",
                "/etc/systemd/system/ssh.service",
                "/usr/lib/systemd/system/sshd.service",
                "/lib/systemd/system/sshd.service",
                "/etc/systemd/system/sshd.service",
            ]

            ssh_service_path = None
            for service_path in ssh_services:
                if guestfs_handle.exists(service_path):
                    ssh_service_path = service_path
                    break

            if ssh_service_path:
                guestfs_handle.mkdir_p("/etc/systemd/system/multi-user.target.wants")
                guestfs_handle.ln_s(
                    ssh_service_path,
                    "/etc/systemd/system/multi-user.target.wants/"
                    f"{ssh_service_path.split('/')[-1]}",
                )
                logger.info("Enabled SSH service (systemd) for %s", rootfs_path.name)
                return True
            logger.warning("SSH service unit not found in %s", rootfs_path.name)
            return False

        if init_system == "openrc":
            guestfs_handle.mkdir_p("/etc/runlevels/default")
            if guestfs_handle.exists("/etc/init.d/sshd"):
                guestfs_handle.ln_s("/etc/init.d/sshd", "/etc/runlevels/default/sshd")
                logger.info("Enabled SSH service (OpenRC) for %s", rootfs_path.name)
                return True
            if guestfs_handle.exists("/etc/init.d/ssh"):
                guestfs_handle.ln_s("/etc/init.d/ssh", "/etc/runlevels/default/ssh")
                logger.info("Enabled SSH service (OpenRC) for %s", rootfs_path.name)
                return True
            logger.warning("SSH init script not found for OpenRC in %s", rootfs_path.name)
            return False

        if guestfs_handle.exists("/etc/init.d/ssh"):
            for level in ["2", "3", "4", "5"]:
                guestfs_handle.mkdir_p(f"/etc/rc{level}.d")
                guestfs_handle.ln_s("../init.d/ssh", f"/etc/rc{level}.d/S02ssh")
            logger.info("Enabled SSH service (sysvinit) for %s", rootfs_path.name)
            return True

        logger.warning("SSH init script not found for sysvinit in %s", rootfs_path.name)
        return False
    except Exception as exc:
        logger.error("Failed to enable SSH for %s: %s", rootfs_path.name, exc)
        return False


def _enforce_ssh_key_auth(guestfs_handle: Any, rootfs_path: Path, user: str) -> None:
    try:
        if not guestfs_handle.exists("/etc/ssh/sshd_config"):
            logger.warning("sshd_config not found in %s", rootfs_path.name)
            return

        sshd_config_dir = "/etc/ssh/sshd_config.d"
        guestfs_handle.mkdir_p(sshd_config_dir)
        config_lines = [
            "PubkeyAuthentication yes",
            "AuthorizedKeysFile .ssh/authorized_keys",
            "PasswordAuthentication no",
            "PermitEmptyPasswords no",
            "UsePAM yes",
            "Protocol 2",
        ]
        if user != "root":
            config_lines.append(f"AllowUsers {user}")
        else:
            config_lines.append("PermitRootLogin prohibit-password")

        guestfs_handle.write(f"{sshd_config_dir}/mvm.conf", "\n".join(config_lines) + "\n")
        guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, f"{sshd_config_dir}/mvm.conf")
        logger.info("Configured SSH key authentication for user '%s' in %s", user, rootfs_path.name)
    except Exception as exc:
        logger.warning("Failed to configure sshd: %s", exc)


def _ensure_user_exists(guestfs_handle: Any, user: str, rootfs_path: Path) -> None:
    if user == "root":
        return

    try:
        passwd_content = ""
        if guestfs_handle.exists("/etc/passwd"):
            passwd_content = guestfs_handle.read_file("/etc/passwd")
            if isinstance(passwd_content, bytes):
                passwd_content = passwd_content.decode("utf-8", errors="replace")

        for line in passwd_content.strip().split("\n"):
            if line.startswith(f"{user}:"):
                logger.debug("User '%s' already exists in %s", user, rootfs_path.name)
                return

        home_dir = f"/home/{user}"
        guestfs_handle.mkdir_p(home_dir)
        guestfs_handle.mkdir_p(f"{home_dir}/.ssh")
        guestfs_handle.write(
            "/etc/passwd",
            f"{user}:!:{CONST_DEFAULT_USER_UID}:{CONST_DEFAULT_USER_GID}::{home_dir}:/bin/bash\n",
            mode="a",
        )
        guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, "/etc/passwd")
        guestfs_handle.write(
            "/etc/shadow",
            f"{user}:!:{CONST_SHADOW_DAYS_SINCE_EPOCH}:{CONST_SHADOW_MIN_DAYS}:{CONST_SHADOW_MAX_DAYS}:{CONST_SHADOW_WARN_DAYS}:::\n",
            mode="a",
        )
        guestfs_handle.chmod(CONST_FILE_PERMS_SHADOW, "/etc/shadow")
        guestfs_handle.write("/etc/group", f"{user}:x:{CONST_DEFAULT_USER_GID}:\n", mode="a")
        guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, "/etc/group")
        guestfs_handle.mkdir_p("/etc/sudoers.d")
        guestfs_handle.write(f"/etc/sudoers.d/{user}", f"{user} ALL=(ALL) NOPASSWD: ALL\n")
        guestfs_handle.chmod(CONST_FILE_PERMS_SUDOERS, f"/etc/sudoers.d/{user}")
        guestfs_handle.chown(CONST_DEFAULT_USER_UID, CONST_DEFAULT_USER_GID, home_dir)
        guestfs_handle.chown(CONST_DEFAULT_USER_UID, CONST_DEFAULT_USER_GID, f"{home_dir}/.ssh")
        logger.info("Created user '%s' with UID/GID 1000 in %s", user, rootfs_path.name)
    except Exception as exc:
        logger.warning("Failed to create user '%s': %s", user, exc)


def _generate_ssh_host_keys(guestfs_handle: Any, rootfs_path: Path) -> None:
    try:
        key_types = ["ssh_host_rsa_key", "ssh_host_ecdsa_key", "ssh_host_ed25519_key"]
        missing_keys = [key for key in key_types if not guestfs_handle.exists(f"/etc/ssh/{key}")]
        if not missing_keys:
            logger.debug("All SSH host keys already exist in %s", rootfs_path.name)
            return

        guestfs_handle.mkdir_p("/etc/local.d")
        guestfs_handle.write(
            "/etc/local.d/ssh-keygen.start",
            "#!/bin/bash\n"
            'SSH_KEYDIR="/etc/ssh"\n'
            "for key_type in ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key; do\n"
            '  key_path="$SSH_KEYDIR/$key_type"\n'
            '  if [ ! -f "$key_path" ]; then\n'
            '    case "$key_type" in\n'
            '      ssh_host_rsa_key) ssh-keygen -t rsa -f "$key_path" -N "" -q 2>/dev/null ;;\n'
            '      ssh_host_ecdsa_key) ssh-keygen -t ecdsa -f "$key_path" -N "" -q 2>/dev/null ;;\n'
            '      ssh_host_ed25519_key) ssh-keygen -t ed25519 -f "$key_path" -N "" -q 2>/dev/null ;;\n'
            "    esac\n"
            '    chmod 600 "$key_path" 2>/dev/null\n'
            '    chmod 644 "${key_path}.pub" 2>/dev/null\n'
            "  fi\n"
            "done\n"
            "rm -f /etc/local.d/ssh-keygen.start 2>/dev/null\n"
            "exit 0\n",
        )
        guestfs_handle.chmod(CONST_FILE_PERMS_EXECUTABLE, "/etc/local.d/ssh-keygen.start")
        if guestfs_handle.exists("/sbin/openrc") or guestfs_handle.exists("/usr/sbin/openrc"):
            guestfs_handle.mkdir_p("/etc/runlevels/default")
            if not guestfs_handle.exists("/etc/runlevels/default/local"):
                guestfs_handle.ln_s("/sbin/openrc-local", "/etc/runlevels/default/local")

        guestfs_handle.mkdir_p("/etc/systemd/system")
        guestfs_handle.write(
            "/etc/systemd/system/ssh-hostkeygen.service",
            "[Unit]\nDescription=SSH Host Key Generation\nAfter=local-fs.target\n\n"
            "[Service]\nType=oneshot\nExecStart=/bin/bash /etc/local.d/ssh-keygen.start\nRemainAfterExit=yes\n\n"
            "[Install]\nWantedBy=multi-user.target\n",
        )
        guestfs_handle.chmod(
            CONST_FILE_PERMS_PUBLIC_KEY, "/etc/systemd/system/ssh-hostkeygen.service"
        )
        guestfs_handle.mkdir_p("/etc/systemd/system/multi-user.target.wants")
        guestfs_handle.ln_s(
            "/etc/systemd/system/ssh-hostkeygen.service",
            "/etc/systemd/system/multi-user.target.wants/ssh-hostkeygen.service",
        )
        logger.info("Created SSH host key generation service in %s", rootfs_path.name)
    except Exception as exc:
        logger.warning("Failed to setup SSH host key generation: %s", exc)


def _inject_ssh_keys_for_disabled_mode(
    rootfs_path: Path,
    ssh_pub_key: list[str] | str | None,
    vm_dir: Path,
    user: str,
) -> None:
    from mvmctl.utils.guestfs import check_libguestfs, optimized_guestfs

    if ssh_pub_key is None:
        return
    if not check_libguestfs():
        raise VMCreateError("libguestfs required for SSH key injection")

    keys = [ssh_pub_key] if isinstance(ssh_pub_key, str) else ssh_pub_key
    if not keys:
        return

    try:
        with optimized_guestfs(rootfs_path, readonly=False) as guestfs_handle:
            filesystems: dict[str, str] = guestfs_handle.list_filesystems()
            root_device: str | None = None
            for candidate in ["/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"]:
                if candidate in filesystems:
                    root_device = candidate
                    break
            if root_device is None and filesystems:
                root_device = str(list(filesystems.keys())[0])
            if root_device is None:
                raise VMCreateError(f"No filesystem found in {rootfs_path}")

            guestfs_handle.mount(root_device, "/")
            try:
                ssh_home_dir = "/root" if user == "root" else f"/home/{user}"
                _ensure_user_exists(guestfs_handle, user, rootfs_path)
                _enforce_ssh_key_auth(guestfs_handle, rootfs_path, user)
                _generate_ssh_host_keys(guestfs_handle, rootfs_path)

                if not guestfs_handle.exists("/root"):
                    guestfs_handle.mkdir_p("/root")
                    guestfs_handle.chmod(CONST_DIR_PERMS_CACHE, "/root")
                    guestfs_handle.chown(CONST_ROOT_UID, CONST_ROOT_GID, "/root")

                guestfs_handle.mkdir_p(f"{ssh_home_dir}/.ssh")
                guestfs_handle.chmod(CONST_DIR_PERMS_CACHE, f"{ssh_home_dir}/.ssh")
                guestfs_handle.chown(CONST_ROOT_UID, CONST_ROOT_GID, f"{ssh_home_dir}/.ssh")
                guestfs_handle.sync()

                existing_keys = ""
                auth_keys_path = f"{ssh_home_dir}/.ssh/authorized_keys"
                if guestfs_handle.exists(auth_keys_path):
                    existing_keys = guestfs_handle.read_file(auth_keys_path)
                    if isinstance(existing_keys, bytes):
                        existing_keys = existing_keys.decode("utf-8", errors="replace")

                existing_set = (
                    set(existing_keys.strip().split("\n")) if existing_keys.strip() else set()
                )
                new_keys = [key for key in keys if key.strip() and key.strip() not in existing_set]
                if new_keys:
                    combined = existing_keys
                    if combined and not combined.endswith("\n"):
                        combined += "\n"
                    combined += "\n".join(new_keys) + "\n"
                    guestfs_handle.write(auth_keys_path, combined)
                    guestfs_handle.chmod(CONST_FILE_PERMS_PRIVATE_KEY, auth_keys_path)
                    guestfs_handle.sync()

                guestfs_handle.mkdir_p("/etc/cloud/cloud.cfg.d")
                guestfs_handle.write(
                    "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
                    "datasource_list: [None]\n",
                )
                guestfs_handle.mkdir_p("/etc/systemd/system/snapd.seeded.service.d")
                guestfs_handle.write(
                    "/etc/systemd/system/snapd.seeded.service.d/override.conf",
                    "[Service]\nExecStart=\nExecStart=/bin/true\n",
                )
                guestfs_handle.mkdir_p("/etc/systemd/system/systemd-networkd-wait-online.service.d")
                guestfs_handle.write(
                    "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
                    "[Unit]\nConditionPathExists=/dev/null\n",
                )
                for service_name in [
                    "cloud-init.service",
                    "cloud-init-local.service",
                    "cloud-config.service",
                    "cloud-final.service",
                ]:
                    guestfs_handle.mkdir_p(f"/etc/systemd/system/{service_name}.d")
                    guestfs_handle.write(
                        f"/etc/systemd/system/{service_name}.d/override.conf",
                        "[Unit]\nConditionPathExists=/dev/null\n",
                    )

                _detect_init_system_and_enable_ssh(guestfs_handle, rootfs_path)
                guestfs_handle.mkdir_p("/etc/systemd/system")
                guestfs_handle.write(
                    "/etc/systemd/system/first-boot-ssh-installer.service",
                    "[Unit]\nDescription=First-boot SSH installer\nAfter=network.target\n"
                    "ConditionFirstBoot=yes\n\n[Service]\nType=oneshot\n"
                    "ExecStart=/bin/bash -c '\n"
                    "if ! command -v sshd >/dev/null 2>&1 && ! command -v ssh >/dev/null 2>&1; then\n"
                    "  if command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm openssh 2>/dev/null || true;\n"
                    "  elif command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y openssh-server 2>/dev/null || true;\n"
                    "  elif command -v apk >/dev/null 2>&1; then apk add --no-cache openssh 2>/dev/null || true; fi;\n"
                    "fi\n"
                    "if command -v systemctl >/dev/null 2>&1; then\n"
                    "  systemctl enable --now sshd 2>/dev/null || systemctl enable --now ssh 2>/dev/null || true;\n"
                    "elif [ -f /sbin/openrc ]; then\n"
                    "  rc-update add sshd default 2>/dev/null || rc-update add ssh default 2>/dev/null || true;\n"
                    "  rc-service sshd start 2>/dev/null || rc-service ssh start 2>/dev/null || true;\n"
                    "fi\n"
                    "systemctl disable first-boot-ssh-installer.service 2>/dev/null || true\n'\n"
                    "RemainAfterExit=yes\n\n[Install]\nWantedBy=multi-user.target\n",
                )
                guestfs_handle.chmod(
                    CONST_FILE_PERMS_PUBLIC_KEY,
                    "/etc/systemd/system/first-boot-ssh-installer.service",
                )
                guestfs_handle.mkdir_p("/etc/systemd/system/multi-user.target.wants")
                guestfs_handle.ln_s(
                    "/etc/systemd/system/first-boot-ssh-installer.service",
                    "/etc/systemd/system/multi-user.target.wants/first-boot-ssh-installer.service",
                )
                logger.info("Created first-boot SSH installer for %s", rootfs_path.name)
            finally:
                try:
                    guestfs_handle.sync()
                except Exception:
                    pass
                try:
                    guestfs_handle.umount("/")
                except Exception:
                    pass
    except MVMError as exc:
        logger.warning(
            "SSH key injection skipped: libguestfs failed (%s). SSH keys not injected.",
            str(exc),
        )
        return
    except Exception as exc:
        raise VMCreateError(f"Failed to inject SSH keys: {exc}") from exc


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
    from mvmctl.api.network import release_network_ip

    if log_fp is not None:
        try:
            log_fp.close()
        except OSError as exc:
            logger.warning("Failed to close log file during cleanup: %s", exc)
    if console_fp is not None:
        try:
            console_fp.close()
        except OSError as exc:
            logger.warning("Failed to close console file during cleanup: %s", exc)

    if resources_created.get("nocloud_server") and net_manager is not None and vm_id:
        try:
            net_manager.stop_server(name, vm_id)
        except Exception as exc:
            logger.warning("Failed to stop nocloud server during cleanup: %s", exc)
    if resources_created.get("firewall_rule") and guest_ip:
        try:
            remove_nocloud_input_rule(guest_ip, name, nocloud_net_port)
        except NetworkError as exc:
            logger.warning("Failed to remove firewall rule during cleanup: %s", exc)
    if resources_created.get("tap") and tap_name:
        try:
            cleanup_tap(tap_name, bridge=net_config.bridge if net_config else None)
        except NetworkError as exc:
            logger.warning("Failed to cleanup TAP device during cleanup: %s", exc)
    if resources_created.get("network_ip"):
        try:
            release_network_ip(net_config.name if net_config else DEFAULT_NETWORK_NAME, name)
        except (NetworkError, TypeError) as exc:
            logger.warning("Failed to release network IP during cleanup: %s", exc)
    if resources_created.get("console_relay") and relay_mgr is not None and vm_id is not None:
        try:
            relay_mgr.stop_relay(name, vm_id)
        except Exception as exc:
            logger.warning("Failed to stop console relay during cleanup: %s", exc)
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
        except OSError as exc:
            logger.warning("Failed to remove VM directory during cleanup: %s", exc)


def _resolve_default_public_keys(ssh_key: str | None) -> list[str] | str | None:
    if ssh_key is not None:
        return resolve_ssh_key(ssh_key)

    from mvmctl.core.key_manager import get_default_keys
    from mvmctl.utils.fs import get_keys_dir

    default_names = get_default_keys()
    if default_names:
        keys_dir = get_keys_dir()
        resolved_keys: list[str] = []
        for key_name in default_names:
            pub_file = keys_dir / f"{key_name}.pub"
            if pub_file.exists():
                content = pub_file.read_text().strip()
                if content:
                    resolved_keys.append(content)
            else:
                logger.warning("Default key '%s' not found at %s — skipping", key_name, pub_file)
        return resolved_keys if resolved_keys else None

    return resolve_ssh_key(None)


def create_vm(
    name: str,
    vcpus: int,
    mem: int,
    user: str,
    enable_api_socket: bool,
    enable_pci: bool,
    enable_console: bool,
    firecracker_bin: str,
    lsm_flags: str,
    enable_logging: bool,
    enable_metrics: bool,
    image: str | None = None,
    kernel: str | None = None,
    image_path: Path | None = None,
    kernel_path: Path | None = None,
    disk_size: str | None = None,
    ip: str | None = None,
    network_name: str | None = None,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT,
    cloud_init_iso_path: Path | None = None,
    keep_cloud_init_iso: bool = False,
    vm_manager: VMManager | None = None,
    nocloud_net_port: int = 0,
    image_fs_uuid: str | None = None,
    image_fs_type: str | None = None,
    image_hash: str | None = None,
    binary_id: str | None = None,
) -> VMInstance:
    import ipaddress as ipaddress_module
    import re

    from mvmctl.api.assets import (
        resolve_image_fs_type as _resolve_image_fs_type,
    )
    from mvmctl.api.assets import (
        resolve_image_fs_uuid as _resolve_image_fs_uuid,
    )
    from mvmctl.api.network import (
        allocate_network_ip,
        ensure_default_network,
        get_network,
    )
    from mvmctl.core.cloud_init import create_cloud_init_iso, write_cloud_init
    from mvmctl.core.metadata import list_image_entries
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.core.rootfs_injector import inject_cloud_init
    from mvmctl.utils.disk_size import parse_disk_size

    if image is None and image_path is None:
        db = MVMDatabase()
        default_image = db.get_default_image()
        if default_image is None:
            raise AssetNotFoundError(
                "No image specified and no default image set. "
                "Use 'mvm image fetch <name>' then 'mvm image set-default <name>', or pass --image."
            )
        image = default_image.os_slug

    if network_name is None:
        db = MVMDatabase()
        default_network = db.get_default_network()
        if default_network is None:
            network_name = DEFAULT_NETWORK_NAME
        else:
            network_name = default_network.name

    if binary_id is None:
        db = MVMDatabase()
        default_binary = db.get_default_binary("firecracker")
        binary_id = default_binary.id if default_binary else None

    from mvmctl.api.host import check_privileges_interactive

    check_privileges_interactive("/usr/sbin/ip", f"create VM '{name}'")

    if image_path is not None:
        resolved_image_path = image_path
        resolved_image_fs_uuid = image_fs_uuid or (_resolve_image_fs_uuid(image) if image else None)
        resolved_image_fs_type = image_fs_type or (_resolve_image_fs_type(image) if image else None)
        resolved_image_hash: str | None = image_hash
        if resolved_image_hash is None and resolved_image_path.suffix == ".zst":
            cache_dir = get_cache_dir()
            all_entries = list_image_entries(cache_dir)
            for img_id, meta in all_entries.items():
                if meta.get("path") == resolved_image_path.name:
                    resolved_image_hash = img_id
                    break
            if resolved_image_hash is None:
                resolved_image_hash = resolved_image_path.stem
    else:
        assert image is not None
        resolved_image_path = resolve_image_multi_strategy(image)
        resolved_image_fs_uuid = image_fs_uuid or _resolve_image_fs_uuid(image)
        resolved_image_fs_type = image_fs_type or _resolve_image_fs_type(image)
        resolved_image_hash = image_hash

    vm_dir: Path | None = None
    resources_created = {
        "vm_dir": False,
        "tap": False,
        "network_ip": False,
        "nocloud_server": False,
        "firewall_rule": False,
        "console_relay": False,
    }
    tap_name = ""
    guest_ip = ""
    net_manager: NoCloudNetServerManager | None = None
    relay_mgr: ConsoleRelayManager | None = None
    pty_master_fd: int | None = None
    pty_slave_fd: int | None = None
    effective_mode = CloudInitMode.NET
    net_config = None
    log_fp: Any = None
    console_fp: Any = None
    vm_id: str | None = None

    def _sigterm_cleanup_handler(signum: int, frame: Any) -> None:
        _ = frame
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
        raise SystemExit(CONST_SIGNAL_EXIT_CODE_BASE + signum)

    old_handler = signal.signal(signal.SIGTERM, _sigterm_cleanup_handler)
    try:
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

            if cloud_init_mode == CloudInitMode.INJECT:
                effective_mode = CloudInitMode.INJECT
            elif cloud_init_mode == CloudInitMode.ISO:
                effective_mode = CloudInitMode.ISO
            else:
                effective_mode = cloud_init_mode

            setup_nocloud_input_chain()

            if mac is not None:
                mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
                if not mac_re.match(mac):
                    raise MVMError(
                        f"Invalid MAC address format: {mac!r}. Expected format: XX:XX:XX:XX:XX:XX"
                    )

            vm_id = generate_vm_id(name)
            vm_dir = get_vm_dir_by_hash(vm_id)
            _secure_mkdir_vm(vm_dir, name)
            resources_created["vm_dir"] = True

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
            if (fc_bin_path.is_absolute() or "/" in firecracker_bin) and (
                not fc_bin_path.exists() or not os.access(fc_bin_path, os.X_OK)
            ):
                raise MVMError(f"Firecracker binary not found: {firecracker_bin}")

            if resolved_image_fs_uuid:
                validate_fs_uuid(resolved_image_fs_uuid)
            if resolved_image_fs_type:
                validate_fs_type(resolved_image_fs_type)
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
                    ip_net = ipaddress_module.IPv4Network(net_config.subnet, strict=False)
                    if ipaddress_module.IPv4Address(ip.split("/")[0]) not in ip_net:
                        raise NetworkError(
                            f"IP {ip} is outside network '{network_name}' subnet {net_config.subnet}"
                        )
                except ValueError as exc:
                    raise NetworkError(f"Invalid IP address: {exc}") from exc
                guest_ip = ip
            else:
                guest_ip = allocate_network_ip(network_name, name)
                resources_created["network_ip"] = True

            guest_mac = mac if mac else generate_mac()
            tap_name = generate_tap_name(network_name, name)
            bridge = net_config.bridge

            if resolved_image_path.suffix == ".zst":
                rootfs_ext = resolved_image_path.suffixes[-2]
                vm_rootfs_path = vm_dir / f"rootfs{rootfs_ext}"
                fs_type = rootfs_ext.lstrip(".")
                if resolved_image_hash is None:
                    raise MVMError(
                        f"image_hash required for compressed images: {resolved_image_path}"
                    )
                ensure_image_in_ready_pool(resolved_image_path, resolved_image_hash, fs_type)
                copy_from_ready_pool(resolved_image_hash, fs_type, vm_rootfs_path)
            else:
                rootfs_ext = resolved_image_path.suffix
                vm_rootfs_path = vm_dir / f"rootfs{rootfs_ext}"
                shutil.copy2(resolved_image_path, vm_rootfs_path)
            rootfs_path = vm_rootfs_path

            if disk_size is not None:
                grow_rootfs_with_guestfs(vm_rootfs_path, parse_disk_size(disk_size))

            disabled_ssh_pub_key: list[str] | str | None = None
            if effective_mode == CloudInitMode.OFF:
                disabled_ssh_pub_key = _resolve_default_public_keys(ssh_key)
            if effective_mode == CloudInitMode.OFF and disabled_ssh_pub_key is not None:
                _inject_ssh_keys_for_disabled_mode(rootfs_path, disabled_ssh_pub_key, vm_dir, user)

            cloud_init_iso: Path | None = None
            extra_drives: list[DriveConfig] = []
            nocloud_net_url: str | None = None
            nocloud_server_pid: int | None = None

            if effective_mode != CloudInitMode.OFF:
                cloud_init_dir = vm_dir / DEFAULT_CLOUD_INIT_DIRNAME
                cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)
                ssh_pub_key = _resolve_default_public_keys(ssh_key)
                prefix_len = ipaddress_module.IPv4Network(net_config.subnet, strict=False).prefixlen
                write_cloud_init(
                    cloud_init_dir,
                    name,
                    guest_ip,
                    user,
                    ssh_pub_key=ssh_pub_key,
                    custom_user_data=user_data,
                    ipv4_gateway=net_config.ipv4_gateway,
                    prefix_len=prefix_len,
                    skip_network_config=False,
                )

                if effective_mode == CloudInitMode.ISO:
                    if cloud_init_iso_path is None:
                        raise MVMError(
                            "cloud_init_iso_path required when cloud_init_mode is CUSTOM"
                        )
                    if not cloud_init_iso_path.exists():
                        raise MVMError(f"Custom cloud-init ISO not found: {cloud_init_iso_path}")
                    cloud_init_iso = cloud_init_iso_path
                elif effective_mode == CloudInitMode.NET:
                    net_manager = NoCloudNetServerManager()
                    url, port = net_manager.start_server(
                        name,
                        cloud_init_dir,
                        net_config.ipv4_gateway,
                        vm_id,
                        preferred_port=nocloud_net_port,
                    )
                    nocloud_net_url = url
                    nocloud_net_port = port
                    nocloud_server_pid = net_manager.get_server_pid(name, vm_id)
                    resources_created["nocloud_server"] = True
                    add_nocloud_input_rule(guest_ip, name, nocloud_net_port)
                    resources_created["firewall_rule"] = True
                elif effective_mode == CloudInitMode.INJECT:
                    try:
                        inject_cloud_init(str(rootfs_path), str(cloud_init_dir))
                    except Exception as exc:
                        raise CloudInitError(f"Direct injection failed: {exc}") from exc
                elif effective_mode in (CloudInitMode.INJECT, CloudInitMode.ISO):
                    cloud_init_iso = vm_dir / DEFAULT_CLOUD_INIT_ISO_NAME
                    try:
                        create_cloud_init_iso(cloud_init_dir, cloud_init_iso)
                    except CloudInitError as exc:
                        raise MVMError(f"Failed to create cloud-init ISO: {exc}") from exc

            socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME if enable_api_socket else None
            subnet_mask = subnet_mask_from_subnet(net_config.subnet)
            vm_config = VMConfig(
                name=name,
                vm_id=vm_id,
                vcpu_count=vcpus,
                mem_size_mib=mem,
                kernel_path=kernel_path_resolved,
                rootfs_path=rootfs_path,
                root_uuid=resolved_image_fs_uuid,
                root_fs_type=resolved_image_fs_type,
                enable_api_socket=enable_api_socket,
                enable_pci=enable_pci,
                lsm_flags=lsm_flags,
                enable_logging=enable_logging,
                enable_metrics=enable_metrics,
                enable_console=enable_console,
                cloud_init_mode=effective_mode,
                cloud_init_iso_path=cloud_init_iso,
                keep_cloud_init_iso=keep_cloud_init_iso,
                nocloud_net_url=nocloud_net_url,
                extra_drives=extra_drives,
            )
            vm_instance = VMInstance(
                name=name,
                id=vm_id,
                ipv4=guest_ip,
                mac=guest_mac,
                network_name=network_name,
                tap_device=tap_name,
                ipv4_gateway=net_config.ipv4_gateway,
                subnet_mask=subnet_mask,
                created_at=datetime.now(tz=timezone.utc),
                status=VMStatus.RUNNING,
                config=vm_config,
                rootfs_suffix=rootfs_ext,
                kernel_id=str(kernel_path_resolved),
                image_id=str(resolved_image_path),
            )

            config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME
            ConfigGenerator(vm_config, vm_instance, vm_dir).write_to_file(config_file)

            console_socket_path: Path | None = None
            console_relay_pid: int | None = None
            if enable_console:
                pty_master_fd, pty_slave_fd = os.openpty()
                relay_mgr = ConsoleRelayManager()

            if not bridge_exists(bridge):
                gateway_cidr = (
                    f"{net_config.ipv4_gateway}/"
                    f"{ipaddress_module.IPv4Network(net_config.subnet, strict=False).prefixlen}"
                )
                setup_bridge(bridge, ipv4_gateway_subnet=gateway_cidr)
                if net_config.nat_enabled:
                    setup_nat(
                        bridge,
                        nat_gateways=net_config.nat_gateways or None,
                        subnet=net_config.subnet,
                    )

            try:
                create_tap(tap_name, bridge=bridge)
                resources_created["tap"] = True
                add_iptables_forward_rules(tap_name, bridge=bridge)
            except NetworkError as exc:
                raise NetworkError(f"Network setup failed: {exc}") from exc

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
                except MVMError as exc:
                    logger.warning("Failed to start console relay: %s", exc)
                    try:
                        os.close(pty_master_fd)
                    except OSError:
                        pass

            _write_pid_file(pid_file, proc.pid)
            vm_instance.pid = proc.pid
            vm_instance.api_socket_path = socket_path
            vm_instance.nocloud_net_port = nocloud_net_port
            vm_instance.nocloud_server_pid = nocloud_server_pid
            vm_instance.console_relay_pid = console_relay_pid
            vm_instance.console_socket_path = console_socket_path
            manager.register(vm_instance, binary_id)

            from mvmctl.utils.audit import log_audit

            log_audit("vm.create", f"name={name}")

            return vm_instance
        except (VMCreateError, NetworkError, CloudInitError, MVMError):
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
        except FileNotFoundError as exc:
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
            raise MVMError(f"Firecracker binary not found: {firecracker_bin}") from exc
        except Exception as exc:
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
            raise VMCreateError(f"Failed to create VM: {exc}") from exc
    finally:
        signal.signal(signal.SIGTERM, old_handler)


def remove_vm(name: str, vm_manager: VMManager | None = None) -> None:
    from mvmctl.api.host import check_privileges_interactive
    from mvmctl.api.network import get_network, release_network_ip

    check_privileges_interactive("/usr/sbin/ip", f"remove VM '{name}'")
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = get_vm_dir_by_hash(vm.id)
    net_name = vm.network_name or DEFAULT_NETWORK_NAME
    tap_name = vm.tap_device or generate_tap_name(net_name, name)
    net_config = get_network(net_name)
    bridge = net_config.bridge if net_config else DEFAULT_BRIDGE_NAME
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
    pid = _read_pid_file(pid_file)
    if pid is None:
        pid = vm.pid

    graceful_shutdown(pid, vm.api_socket_path)
    if pid is not None:
        try:
            _, status = os.waitpid(pid, os.WNOHANG)
            if os.WIFEXITED(status):
                _write_exit_code(vm_dir, os.WEXITSTATUS(status))
            elif os.WIFSIGNALED(status):
                _write_exit_code(vm_dir, CONST_SIGNAL_EXIT_CODE_BASE + os.WTERMSIG(status))
        except (ChildProcessError, OSError):
            pass

    if vm.console_relay_pid is not None:
        try:
            ConsoleRelayManager().stop_relay(name, vm.id)
        except (OSError, RuntimeError) as exc:
            logger.warning("Failed to cleanup console relay: %s", exc)

    if vm.nocloud_net_port is not None and vm.ipv4 is not None:
        try:
            nocloud_manager = NoCloudNetServerManager()
            nocloud_manager.stop_server(name, vm.id) if vm.id else nocloud_manager.stop_server(name)
            remove_nocloud_input_rule(vm.ipv4, name, vm.nocloud_net_port)
        except (OSError, RuntimeError, NetworkError) as exc:
            logger.warning("Failed to cleanup nocloud-net resources: %s", exc)

    remove_iptables_forward_rules(tap_name, bridge=bridge)
    try:
        teardown_nat(bridge, force=False, subnet=net_config.subnet if net_config else None)
    except NetworkError as exc:
        logger.debug("NAT teardown for bridge %s: %s", bridge, exc)
    try:
        delete_tap(tap_name)
    except NetworkError:
        pass
    try:
        release_network_ip(net_name, name)
    except NetworkError as exc:
        logger.warning("Failed to release network IP: %s", exc)

    if vm.ipv4:
        try:
            subprocess.run(["ssh-keygen", "-R", vm.ipv4], capture_output=True, check=False)
        except FileNotFoundError:
            pass

    manager.deregister(vm.id)
    if vm_dir.exists():
        shutil.rmtree(vm_dir)
    try:
        NoCloudNetServerManager().cleanup_orphans()
    except Exception:
        pass

    from mvmctl.utils.audit import log_audit

    log_audit("vm.remove", f"name={name}")


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


def load_snapshot(
    name: str, mem_in: Path, state_in: Path, resume_after: bool | None = None
) -> None:
    effective_resume = resume_after if resume_after is not None else DEFAULT_SNAPSHOT_RESUME
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )
    client = FirecrackerClient(socket_path)
    try:
        client.load_snapshot(mem_in, state_in, effective_resume)
    finally:
        client.close()


def pause_vm(name: str) -> None:
    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status != VMStatus.RUNNING:
        raise MVMError(f"VM '{name}' is not running (current state: {vm.status.value})")
    if not vm.api_socket_path:
        raise MVMError(f"VM '{name}' has no API socket enabled")
    client = FirecrackerClient(vm.api_socket_path)
    try:
        _pause_process(client)
        manager.update_status(name, VMStatus.PAUSED)
    finally:
        client.close()


def resume_vm(name: str) -> None:
    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status != VMStatus.PAUSED:
        raise MVMError(f"VM '{name}' is not paused (current state: {vm.status.value})")
    if not vm.api_socket_path:
        raise MVMError(f"VM '{name}' has no API socket enabled")
    client = FirecrackerClient(vm.api_socket_path)
    try:
        _resume_process(client)
        manager.update_status(name, VMStatus.RUNNING)
    finally:
        client.close()


def stop_vm(name: str, force: bool = False) -> None:
    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status not in (VMStatus.RUNNING, VMStatus.PAUSED):
        raise MVMError(f"VM '{name}' is not running (current state: {vm.status.value})")
    manager.update_status(name, VMStatus.STOPPING)
    try:
        graceful_shutdown(vm.pid, vm.api_socket_path, force=force)
        manager.update_status(name, VMStatus.STOPPED)
    except Exception as exc:
        manager.update_status(name, VMStatus.ERROR)
        raise MVMError(f"Failed to stop VM '{name}': {exc}") from exc


def start_vm(name: str) -> None:
    from mvmctl.core.mvm_db import MVMDatabase

    db = MVMDatabase()
    default_binary = db.get_default_binary("firecracker")
    binary_id = default_binary.id if default_binary else None

    manager = get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")
    if vm.status != VMStatus.STOPPED:
        raise MVMError(f"VM '{name}' is not stopped (current state: {vm.status.value})")
    if not vm.id:
        raise MVMError(f"VM '{name}' has no ID")

    vm_dir = get_vm_dir_by_hash(vm.id)
    config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
    if not config_file.exists():
        raise MVMError(f"VM config not found: {config_file}")

    firecracker_bin = DEFAULT_FIRECRACKER_BIN_NAME
    if vm.config and vm.config.kernel_path:
        fc_bin_path = Path(firecracker_bin)
        if (fc_bin_path.is_absolute() or "/" in firecracker_bin) and not fc_bin_path.exists():
            raise MVMError(f"Firecracker binary not found: {firecracker_bin}")

    enable_api_socket_runtime = (
        vm.config.enable_api_socket if vm.config else DEFAULT_VM_ENABLE_API_SOCKET
    )
    socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME if enable_api_socket_runtime else None
    if enable_api_socket_runtime and socket_path:
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
    console_fp: Any = None
    try:
        console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
        proc = subprocess.Popen(
            fc_cmd,
            stdin=subprocess.DEVNULL,
            stdout=console_fp,
            stderr=log_fp,
            start_new_session=True,
        )
        log_fp.close()
        console_fp.close()
        _write_pid_file(pid_file, proc.pid)
        vm.pid = proc.pid
        vm.api_socket_path = socket_path
        vm.status = VMStatus.RUNNING
        manager.register(vm, binary_id)
        time.sleep(CONST_VM_START_WAIT_S)
    except Exception as exc:
        try:
            log_fp.close()
        except OSError:
            pass
        if console_fp is not None:
            try:
                console_fp.close()
            except OSError:
                pass
        raise MVMError(f"Failed to start VM '{name}': {exc}") from exc


def reboot_vm(name: str, force: bool = False) -> None:
    stop_vm(name, force=force)
    start_vm(name)


def list_vms(include_stopped: bool = True, vm_manager: VMManager | None = None) -> list[VMInstance]:
    """Return all registered VMs, optionally filtering out stopped ones.

    Reconciles live VM state from process status and Firecracker API
    before returning the list.
    """
    manager = vm_manager or get_vm_manager()
    all_vms = manager.list_all()

    # Reconcile live state for VMs that might have changed
    from mvmctl.core.vm_monitor import reconcile_vm

    for vm in all_vms:
        # Skip VMs with no PID — they're definitively stopped/unstarted
        if vm.pid is not None:
            new_state = reconcile_vm(vm, manager)
            vm.status = new_state

    if not include_stopped:
        terminal_states = {VMStatus.STOPPED, VMStatus.ERROR, VMStatus.CRASHED}
        return [vm for vm in all_vms if vm.status not in terminal_states]
    return all_vms


def get_vm(name: str, vm_manager: VMManager | None = None) -> VMInstance | None:
    """Return the VMInstance for the given name, or None if not found."""
    manager = vm_manager or get_vm_manager()
    return manager.get(name)


def vm_cache_dir(vm: VMInstance) -> Path:
    """Return the cache directory path for a VM using its hash ID."""
    from mvmctl.utils.fs import get_vm_dir_by_hash

    return get_vm_dir_by_hash(vm.id)


def ssh_vm(
    name: str,
    user: str,
    key: Path | None = None,
    cmd: str | None = None,
) -> int:
    """Open SSH session or execute command on a VM."""
    from mvmctl.exceptions import MVMError, VMNotFoundError

    # Resolve VM name to IP address
    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")
    if not vm.ipv4:
        raise MVMError(f"VM '{name}' has no IP address")

    from mvmctl.utils.audit import log_audit

    log_audit("vm.ssh", f"name={name},user={user}")

    return connect_to_vm(
        ip=vm.ipv4,
        user=user,
        key_path=key,
        command=cmd,
        exec_mode=cmd is None,
    )


def get_logs(
    name: str,
    log_type: str,
    lines: int,
    follow: bool,
) -> list[str]:
    """View VM logs. Returns log lines."""
    manager = get_vm_manager()
    vm = manager.get(name)
    # Use VM hash if found, otherwise fall back to name (for backward compatibility)
    vm_hash = vm.id if vm is not None else name
    return show_logs(
        vm_hash=vm_hash,
        log_type=log_type,
        lines=lines,
        follow=follow,
    )


def cleanup_vms(
    all_vms: bool = False, dry_run: bool = False, vm_manager: VMManager | None = None
) -> list[VMInstance]:
    """Stop and remove stale or all VMs, tearing down their TAP devices and iptables rules."""
    from mvmctl.api.host import check_privileges_interactive
    from mvmctl.api.network import get_network

    check_privileges_interactive("/usr/sbin/ip", "cleanup VMs")
    import logging
    import os
    import shutil
    import signal

    from mvmctl.core.firewall import remove_nocloud_input_rule
    from mvmctl.core.network import delete_tap, remove_iptables_forward_rules
    from mvmctl.exceptions import NetworkError
    from mvmctl.services.nocloud_server import NoCloudNetServerManager
    from mvmctl.utils.fs import get_cache_dir

    log = logging.getLogger(__name__)

    manager = vm_manager or get_vm_manager()
    vms = manager.list_all()

    targets = vms if all_vms else [v for v in vms if v.status != VMStatus.RUNNING]

    if dry_run or not targets:
        return targets

    cache_dir = Path(get_cache_dir())

    for v in targets:
        vm_dir = vm_cache_dir(v) if v.id else None

        tap_name = v.tap_device
        if not tap_name:
            log.warning("VM %s has no tap_device in state, skipping TAP cleanup", v.name)

        if v.nocloud_net_port is not None and v.ipv4 is not None:
            try:
                nocloud_manager = NoCloudNetServerManager()
                nocloud_manager.stop_server(v.name, v.id)
            except (OSError, RuntimeError):
                pass

            try:
                remove_nocloud_input_rule(v.ipv4, v.name, v.nocloud_net_port)
            except NetworkError:
                pass

        if v.pid:
            try:
                os.kill(v.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass

        if tap_name:
            net_config = get_network(v.network_name or "")
            bridge = net_config.bridge if net_config else ""
            remove_iptables_forward_rules(tap_name, bridge=bridge)
            try:
                delete_tap(tap_name)
            except NetworkError:
                pass
            try:
                teardown_nat(bridge)
            except NetworkError:
                pass

        manager.deregister(v.id if v.id else v.name)

        nocloud_cache_dir = cache_dir / f"nocloud-{v.id}" if v.id else None
        if nocloud_cache_dir is not None and nocloud_cache_dir.exists():
            shutil.rmtree(nocloud_cache_dir)

        if vm_dir is not None and vm_dir.exists():
            shutil.rmtree(vm_dir)

    # Clean up any orphaned nocloud servers
    try:
        nocloud_manager = NoCloudNetServerManager()
        nocloud_manager.cleanup_orphans()
    except Exception:
        # Don't fail cleanup if orphan cleanup fails
        pass

    return targets


def attach_console(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    if not mgr.is_relay_running(name, vm_hash):
        raise MVMError(f"No console relay running for VM '{name}'")

    socket_path = mgr.get_socket_path(vm_hash if vm_hash else name)
    return {"socket_path": str(socket_path), "vm_name": name}


def kill_console(name: str) -> bool:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    mgr = ConsoleRelayManager()
    vm_hash = vm.id if vm.id else None
    return mgr.kill_relay(name, vm_hash)


def get_console_state(name: str) -> dict[str, Any]:
    from mvmctl.exceptions import VMNotFoundError

    manager = get_vm_manager()
    vm = manager.get(name)
    if vm is None:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_hash = vm.id if vm.id else None
    return _get_console_state(name, vm_hash)


def inspect_vm(name: str) -> dict[str, Any]:
    """Get detailed VM information."""
    from mvmctl.exceptions import MVMError, VMNotFoundError

    manager = get_vm_manager()

    # Try ID prefix first
    vm = manager.get_by_id_prefix(name)
    if vm:
        return _gather_vm_details(vm)

    # Fall back to name lookup
    matches = manager.get_by_name(name)
    if len(matches) == 1:
        return _gather_vm_details(matches[0])
    elif len(matches) > 1:
        raise MVMError(f"Multiple VMs match name '{name}' — use ID prefix")

    raise VMNotFoundError(f"VM '{name}' not found")


def _resolve_asset_names(
    image_id: str | None, kernel_id: str | None
) -> tuple[str | None, str | None]:
    """Resolve friendly names for image and kernel IDs from database.

    Args:
        image_id: Image ID prefix (can be None)
        kernel_id: Kernel ID prefix (can be None)

    Returns:
        Tuple of (image_name, kernel_name) — either resolved from DB or None if not found
    """
    from mvmctl.api.metadata import find_images_by_id_prefix, find_kernels_by_id_prefix
    from mvmctl.utils.fs import get_cache_dir

    image_name: str | None = None
    kernel_name: str | None = None

    if image_id:
        try:
            matches = find_images_by_id_prefix(get_cache_dir(), image_id)
            if matches:
                _, meta = matches[0]
                image_name = meta.get("os_slug") or image_id
        except Exception:
            image_name = image_id
    if kernel_id:
        try:
            matches = find_kernels_by_id_prefix(get_cache_dir(), kernel_id)
            if matches:
                _, meta = matches[0]
                kernel_name = meta.get("version") or kernel_id
        except Exception:
            kernel_name = kernel_id

    return image_name, kernel_name


def _gather_vm_details(vm: VMInstance) -> dict[str, Any]:
    """Gather comprehensive VM details."""
    from mvmctl.utils.fs import get_vm_dir_by_hash

    vm_dir = get_vm_dir_by_hash(vm.id)

    rootfs_path, rootfs_source = _resolve_rootfs_path(vm, vm_dir)

    config_path = vm_dir / "firecracker.json"

    image_name, kernel_name = _resolve_asset_names(vm.image_id, vm.kernel_id)

    info: dict[str, Any] = {
        "id": vm.id,
        "name": vm.name,
        "status": vm.status.value,
        "created_at": vm.created_at.isoformat() if vm.created_at else None,
        "pid": vm.pid,
        "ip": vm.ipv4,
        "mac": vm.mac,
        "network_name": vm.network_name,
        "tap_device": vm.tap_device,
        "cloud_init_mode": vm.config.cloud_init_mode.value if vm.config else "inject",
        "image_id": vm.image_id,
        "image_name": image_name,
        "kernel_id": vm.kernel_id,
        "kernel_name": kernel_name,
        "paths": {
            "vm_dir": str(vm_dir),
            "rootfs": str(rootfs_path) if rootfs_path else None,
            "rootfs_source": rootfs_source,
            "config": str(config_path) if config_path.exists() else None,
        },
        "features": {
            "api_socket": vm.api_socket_path is not None,
            "console": vm.console_socket_path is not None,
            "nocloud_net": vm.nocloud_net_port is not None,
        },
    }

    if vm.nocloud_net_port:
        info["nocloud_net"] = {
            "port": vm.nocloud_net_port,
            "server_pid": vm.nocloud_server_pid,
        }

    if vm.console_socket_path:
        info["console"] = {
            "socket_path": str(vm.console_socket_path),
            "relay_pid": vm.console_relay_pid,
        }

    return info


def _resolve_rootfs_path(vm: VMInstance, vm_dir: Path) -> tuple[Path | None, str]:
    """Resolve rootfs path from multiple sources.

    Checks sources in priority order:
    1. vm.config.rootfs_path - if config exists and path is set
    2. VM-local rootfs{suffix} - fallback for legacy VMs

    Args:
        vm: VM instance to resolve rootfs for
        vm_dir: Path to VM directory

    Returns:
        Tuple of (resolved_path, source_name) where source_name indicates
        which source provided the path: "config", "local", or "none"
    """
    # Priority 1: Check config.rootfs_path if config exists
    if vm.config is not None and vm.config.rootfs_path is not None:
        config_path = Path(vm.config.rootfs_path)
        if config_path.exists():
            return config_path, "config"

    # Priority 2: Fallback to VM-local rootfs file
    if not vm.rootfs_suffix:
        return None, "none"
    local_path = vm_dir / f"rootfs{vm.rootfs_suffix}"
    if local_path.exists():
        return local_path, "local"

    # No rootfs found
    return None, "none"


def get_vm_status_with_exit_code(vm: VMInstance) -> tuple[str, int | None]:
    """Get VM status with exit code if process has exited.

    Args:
        vm: VM instance to check

    Returns:
        Tuple of (status_string, exit_code_or_none)
    """
    import os

    from mvmctl.models import VMStatus

    # Check if process is running
    if vm.pid is not None:
        try:
            os.kill(vm.pid, 0)
            return "running", None
        except (ProcessLookupError, OSError):
            # Process exited - try to get exit code
            pass

    # Try to get exit code from various sources
    exit_code = _get_exit_code_from_sources(vm)

    if exit_code is not None:
        return f"exited({exit_code})", exit_code

    # Check VM state from metadata
    if vm.status == VMStatus.RUNNING:
        return "exited", None  # Was running but process died
    return vm.status.value, None


def _get_exit_code_from_sources(vm: VMInstance) -> int | None:
    """Try to extract exit code from various sources.

    Sources checked in order:
    1. firecracker.exitcode file in VM directory
    2. firecracker.log for exit code patterns
    """
    import re

    from mvmctl.constants import DEFAULT_FC_EXITCODE_FILENAME, DEFAULT_FC_LOG_FILENAME
    from mvmctl.utils.fs import get_vm_dir_by_hash

    if not vm.id:
        return None

    vm_dir = get_vm_dir_by_hash(vm.id)

    # Check for explicit exit code file
    exitcode_path = vm_dir / DEFAULT_FC_EXITCODE_FILENAME
    if exitcode_path.exists():
        try:
            return int(exitcode_path.read_text().strip())
        except (ValueError, OSError):
            pass

    # Check firecracker.log for exit patterns
    log_path = vm_dir / DEFAULT_FC_LOG_FILENAME
    if log_path.exists():
        try:
            content = log_path.read_text()
            # Look for common exit code patterns
            patterns = [
                r"exit(?:ed| code)[\s:]+(\d+)",
                r"returned\s+(\d+)",
                r"exit_status[=:\s]+(\d+)",
            ]
            for pattern in patterns:
                match = re.search(pattern, content, re.IGNORECASE)
                if match:
                    return int(match.group(1))
        except OSError:
            pass

    return None


def compute_vm_is_missing(vm: VMInstance) -> bool:
    """Check if a VM's runtime state suggests it's missing from the filesystem.

    A VM is considered "missing" if:
    - The VM directory is missing from the filesystem
    - OR the status says running but the PID is not actually running

    Args:
        vm: The VM instance to check.

    Returns:
        True if the VM appears to be missing, False otherwise.
    """
    from mvmctl.utils.fs import get_vm_dir_by_hash, is_file_missing
    from mvmctl.utils.process import is_process_running

    if not vm.id:
        return False
    vm_dir = get_vm_dir_by_hash(vm.id)
    dir_missing = is_file_missing(vm_dir)
    process_running = is_process_running(vm.pid) if vm.pid else False
    return dir_missing or (vm.status.value == "running" and not process_running)


def export_vm_config(name: str) -> "VMExportConfig":
    """Export a VM's configuration as a portable VMExportConfig.

    Uses semantic references (os_slug, version, name) — NEVER internal SHA256 IDs.

    Args:
        name: VM name or ID prefix

    Returns:
        VMExportConfig with semantic references

    Raises:
        VMNotFoundError: If VM not found
    """
    from mvmctl.api.metadata import find_images_by_id_prefix, find_kernels_by_id_prefix
    from mvmctl.core.metadata import list_image_entries, list_kernel_entries
    from mvmctl.exceptions import VMNotFoundError
    from mvmctl.models.vm_config_file import (
        VMExportBinaryConfig,
        VMExportBootConfig,
        VMExportCloudInitConfig,
        VMExportComputeConfig,
        VMExportFirecrackerConfig,
        VMExportImageConfig,
        VMExportKernelConfig,
        VMExportNetworkConfig,
    )
    from mvmctl.utils.fs import get_cache_dir

    manager = get_vm_manager()

    # Try ID prefix first
    vm = manager.get_by_id_prefix(name)
    if not vm:
        # Fall back to name lookup
        matches = manager.get_by_name(name)
        if len(matches) == 1:
            vm = matches[0]
        elif len(matches) > 1:
            from mvmctl.exceptions import MVMError

            raise MVMError(f"Multiple VMs match name '{name}' — use ID prefix")
        else:
            raise VMNotFoundError(f"VM '{name}' not found")

    if vm.config is None:
        raise VMNotFoundError(f"VM '{name}' has no configuration")

    config = vm.config

    # Resolve image os_slug from metadata
    image_os_slug = ""
    image_arch = ""
    if vm.image_id:
        cache_dir = get_cache_dir()
        try:
            image_matches = find_images_by_id_prefix(cache_dir, vm.image_id)
            if image_matches:
                _, meta = image_matches[0]
                image_os_slug = meta.get("os_slug", "")
                image_arch = meta.get("arch", "")
        except Exception:
            pass

        # Fallback: search all entries by matching the image_id
        if not image_os_slug:
            try:
                all_entries = list_image_entries(cache_dir)
                for img_id, meta in all_entries.items():
                    if img_id == vm.image_id or img_id.startswith(vm.image_id):
                        image_os_slug = meta.get("os_slug", "")
                        image_arch = meta.get("arch", "")
                        break
            except Exception:
                pass

    # Resolve kernel version from metadata
    kernel_version: str | None = None
    kernel_arch: str | None = None
    kernel_type: str | None = None
    if vm.kernel_id:
        cache_dir = get_cache_dir()
        try:
            kernel_matches = find_kernels_by_id_prefix(cache_dir, vm.kernel_id)
            if kernel_matches:
                _, meta = kernel_matches[0]
                kernel_version = meta.get("version")
                kernel_arch = meta.get("arch")
                kernel_type = meta.get("type")
        except Exception:
            pass

        # Fallback: search all entries
        if not kernel_version:
            try:
                all_entries = list_kernel_entries(cache_dir)
                for kern_id, meta in all_entries.items():
                    if kern_id == vm.kernel_id or kern_id.startswith(vm.kernel_id):
                        kernel_version = meta.get("version")
                        kernel_arch = meta.get("arch")
                        kernel_type = meta.get("type")
                        break
            except Exception:
                pass

    # Resolve binary version from metadata
    binary_version: str | None = None
    try:
        from mvmctl.core.metadata import list_binary_entries

        cache_dir = get_cache_dir()
        all_binaries = list_binary_entries(cache_dir)
        for bin_name, meta in all_binaries.items():
            if meta.get("is_default"):
                binary_version = meta.get("version")
                break
    except Exception:
        pass

    # Build network config
    network_name = vm.network_name
    network_ip = vm.ipv4
    network_mac = vm.mac

    return VMExportConfig(
        name=vm.name,
        compute=VMExportComputeConfig(
            vcpus=config.vcpu_count,
            mem=config.mem_size_mib,
        ),
        image=VMExportImageConfig(
            os_slug=image_os_slug,
            arch=image_arch,
        ),
        kernel=VMExportKernelConfig(
            version=kernel_version,
            arch=kernel_arch,
            type=kernel_type,
        ),
        binary=VMExportBinaryConfig(
            version=binary_version,
        ),
        network=VMExportNetworkConfig(
            name=network_name,
            ip=network_ip,
            mac=network_mac,
        ),
        boot=VMExportBootConfig(
            args=config.boot_args,
            enable_console=config.enable_console,
        ),
        firecracker=VMExportFirecrackerConfig(
            enable_api_socket=config.enable_api_socket,
            enable_pci=config.enable_pci,
            lsm_flags=config.lsm_flags,
        ),
        cloud_init=VMExportCloudInitConfig(
            mode=config.cloud_init_mode.value,
            user=config.name,  # VM name doubles as default user
            keep_iso=config.keep_cloud_init_iso,
        ),
    )
