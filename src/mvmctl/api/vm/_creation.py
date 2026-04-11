"""VM creation classes - GuestfsProvisioner, CloudInitProvisioner, VMCreationContext."""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import TYPE_CHECKING, Any, Callable

from mvmctl.constants import (
    CONST_DEFAULT_NAMESERVER,
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
)
from mvmctl.exceptions import VMCreateError
from src.mvmctl.utils.fs import get_vm_dir_by_hash

if TYPE_CHECKING:
    from mvmctl.models.cloud_init import CloudInitMode
    from mvmctl.models.network import NetworkConfig
    from mvmctl.services.console_relay.manager import ConsoleRelayManager
    from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

logger = logging.getLogger(__name__)


@dataclass
class CloudInitProvisionResult:
    """Result of cloud-init provisioning."""

    iso_path: Path | None = None
    nocloud_url: str | None = None
    nocloud_port: int = 0
    nocloud_pid: int | None = None


class GuestfsProvisioner:
    """All SSH/guestfs setup operations. Stateful - holds guestfs handle."""

    def __init__(
        self,
        rootfs_path: Path,
        hostname: str,
        user: str,
        ssh_pub_key: list[str] | str | None,
    ):
        """Initialize the GuestfsProvisioner.

        Args:
            rootfs_path: Path to the root filesystem image.
            hostname: Hostname to set for the VM.
            user: Username to create/configure in the VM.
            ssh_pub_key: SSH public key(s) to configure for the user.
        """
        self._rootfs_path = rootfs_path
        self._hostname = hostname
        self._user = user
        self._ssh_pub_key = ssh_pub_key
        self._guestfs_handle: Any = None

    def provision(self, target_size_bytes: int | None = None) -> None:
        """Main entry point - unified guestfs session for resize + SSH/DNS."""
        from mvmctl.utils.guestfs import check_libguestfs, optimized_guestfs

        if not check_libguestfs():
            raise VMCreateError("libguestfs required for rootfs setup")

        keys: list[str] = (
            [self._ssh_pub_key] if isinstance(self._ssh_pub_key, str) else (self._ssh_pub_key or [])
        )
        has_keys = bool(keys)

        if target_size_bytes is not None:
            try:
                current_size = self._rootfs_path.stat().st_size
                if isinstance(current_size, int) and current_size < target_size_bytes:
                    with open(self._rootfs_path, "r+b") as f:
                        f.truncate(target_size_bytes)
            except (OSError, AttributeError):
                pass

        with optimized_guestfs(self._rootfs_path, readonly=False) as guestfs_handle:
            self._guestfs_handle = guestfs_handle
            filesystems: dict[str, str] = guestfs_handle._g.list_filesystems()
            root_device: str | None = None
            for candidate in ["/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"]:
                if candidate in filesystems:
                    root_device = candidate
                    break
            if root_device is None and filesystems:
                root_device = str(list(filesystems.keys())[0])
            if root_device is None:
                raise VMCreateError(f"No filesystem found in {self._rootfs_path}")

            if target_size_bytes is not None:
                fs_type = guestfs_handle._g.vfs_type(root_device)
                if fs_type in ("ext2", "ext3", "ext4"):
                    guestfs_handle._g.resize2fs(root_device)
                elif fs_type == "btrfs":
                    guestfs_handle._g.mount(root_device, "/")
                    guestfs_handle._g.btrfs_filesystem_resize("/", target_size_bytes)
                    guestfs_handle._g.umount(root_device)

            guestfs_handle._g.mount(root_device, "/")
            try:
                if has_keys:
                    ssh_home_dir = "/root" if self._user == "root" else f"/home/{self._user}"
                    self.ensure_user(guestfs_handle._g)
                    self.configure_ssh_keys(guestfs_handle._g)
                    self.generate_host_keys(guestfs_handle._g)

                    if not guestfs_handle._g.exists("/root"):
                        guestfs_handle._g.mkdir_p("/root")
                        guestfs_handle._g.chmod(CONST_DIR_PERMS_CACHE, "/root")
                        guestfs_handle._g.chown(CONST_ROOT_UID, CONST_ROOT_GID, "/root")

                    guestfs_handle._g.mkdir_p(f"{ssh_home_dir}/.ssh")
                    guestfs_handle._g.chmod(CONST_DIR_PERMS_CACHE, f"{ssh_home_dir}/.ssh")
                    guestfs_handle._g.chown(CONST_ROOT_UID, CONST_ROOT_GID, f"{ssh_home_dir}/.ssh")
                    guestfs_handle._g.sync()

                    existing_keys = ""
                    auth_keys_path = f"{ssh_home_dir}/.ssh/authorized_keys"
                    if guestfs_handle._g.exists(auth_keys_path):
                        existing_keys = guestfs_handle._g.read_file(auth_keys_path)
                        if isinstance(existing_keys, bytes):
                            existing_keys = existing_keys.decode("utf-8", errors="replace")

                    existing_set = (
                        set(existing_keys.strip().split("\n")) if existing_keys.strip() else set()
                    )
                    new_keys = [
                        key for key in keys if key.strip() and key.strip() not in existing_set
                    ]
                    if new_keys:
                        combined = existing_keys
                        if combined and not combined.endswith("\n"):
                            combined += "\n"
                        combined += "\n".join(new_keys) + "\n"
                        guestfs_handle._g.write(auth_keys_path, combined)
                        guestfs_handle._g.chmod(CONST_FILE_PERMS_PRIVATE_KEY, auth_keys_path)
                        guestfs_handle._g.sync()

                    guestfs_handle._g.mkdir_p("/etc/cloud/cloud.cfg.d")
                    guestfs_handle._g.write(
                        "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
                        "datasource_list: [None]\n",
                    )
                    guestfs_handle._g.write(
                        "/etc/cloud/cloud-init.disabled", "disabled by mvmctl\n"
                    )
                    guestfs_handle._g.mkdir_p("/etc/systemd/system/snapd.seeded.service.d")
                    guestfs_handle._g.write(
                        "/etc/systemd/system/snapd.seeded.service.d/override.conf",
                        "[Service]\nExecStart=\nExecStart=/bin/true\n",
                    )
                    guestfs_handle._g.mkdir_p(
                        "/etc/systemd/system/systemd-networkd-wait-online.service.d"
                    )
                    guestfs_handle._g.write(
                        "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
                        "[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n",
                    )

                    for service_name in [
                        "cloud-init.service",
                        "cloud-init-local.service",
                        "cloud-config.service",
                        "cloud-final.service",
                    ]:
                        guestfs_handle._g.ln_sf("/dev/null", f"/etc/systemd/system/{service_name}")

                    self.enable_ssh(guestfs_handle._g)
                    guestfs_handle._g.mkdir_p("/etc/systemd/system")
                    guestfs_handle._g.write(
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
                    guestfs_handle._g.chmod(
                        CONST_FILE_PERMS_PUBLIC_KEY,
                        "/etc/systemd/system/first-boot-ssh-installer.service",
                    )
                    guestfs_handle._g.mkdir_p("/etc/systemd/system/multi-user.target.wants")
                    guestfs_handle._g.ln_s(
                        "/etc/systemd/system/first-boot-ssh-installer.service",
                        "/etc/systemd/system/multi-user.target.wants/first-boot-ssh-installer.service",
                    )
                    logger.info("Created first-boot SSH installer for %s", self._rootfs_path.name)

                resolv_path = "/etc/resolv.conf"
                needs_dns = True

                if guestfs_handle._g.exists(resolv_path):
                    try:
                        existing_content = guestfs_handle._g.read_file(resolv_path)
                        if isinstance(existing_content, bytes):
                            existing_content = existing_content.decode("utf-8", errors="replace")
                        stripped = existing_content.strip()
                        if stripped and "nameserver" in stripped.lower():
                            needs_dns = False
                    except RuntimeError:
                        needs_dns = True

                if needs_dns:
                    dns_content = f"nameserver {CONST_DEFAULT_NAMESERVER}\n"
                    try:
                        guestfs_handle._g.write(resolv_path, dns_content)
                    except RuntimeError:
                        guestfs_handle._g.rm(resolv_path)
                        guestfs_handle._g.write(resolv_path, dns_content)
                    logger.debug("Injected default DNS into %s", self._rootfs_path.name)

                guestfs_handle._g.write("/etc/hostname", self._hostname)

                hosts_content = ""
                if guestfs_handle._g.exists("/etc/hosts"):
                    hosts_content = guestfs_handle._g.read_file("/etc/hosts")
                    if isinstance(hosts_content, bytes):
                        hosts_content = hosts_content.decode("utf-8", errors="replace")

                lines = hosts_content.splitlines() if hosts_content else []
                new_lines = []
                found_host_entry = False
                for line in lines:
                    stripped = line.strip()
                    if stripped.startswith("#") or not stripped:
                        new_lines.append(line)
                    elif stripped.startswith("127.0.1.1"):
                        new_lines.append(f"127.0.1.1\t{self._hostname}")
                        found_host_entry = True
                    else:
                        new_lines.append(line)

                if not found_host_entry:
                    new_lines.append(f"127.0.1.1\t{self._hostname}")

                guestfs_handle._g.write("/etc/hosts", "\n".join(new_lines) + "\n")
                guestfs_handle._g.sync()
            finally:
                try:
                    guestfs_handle._g.umount("/")
                except Exception:
                    pass

    def enable_ssh(self, guestfs_handle: Any) -> bool:
        """Detect init system and enable SSH service."""
        init_system = "unknown"

        if guestfs_handle.exists("/lib/systemd/systemd") or guestfs_handle.exists(
            "/usr/lib/systemd/systemd"
        ):
            init_system = "systemd"
        elif guestfs_handle.exists("/sbin/openrc") or guestfs_handle.exists("/usr/sbin/openrc"):
            init_system = "openrc"
        elif guestfs_handle.exists("/etc/init.d/"):
            init_system = "sysvinit"
        else:
            logger.warning("Unknown init system in %s", self._rootfs_path.name)
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
                    service_name = ssh_service_path.split("/")[-1]
                    target = f"/etc/systemd/system/multi-user.target.wants/{service_name}"
                    guestfs_handle.mkdir_p("/etc/systemd/system/multi-user.target.wants")
                    if not guestfs_handle.exists(target):
                        guestfs_handle.ln_s(ssh_service_path, target)
                    logger.info("Enabled SSH service (systemd) for %s", self._rootfs_path.name)
                    return True
                logger.warning("SSH service unit not found in %s", self._rootfs_path.name)
                return False

            if init_system == "openrc":
                guestfs_handle.mkdir_p("/etc/runlevels/default")
                if guestfs_handle.exists("/etc/init.d/sshd"):
                    if not guestfs_handle.exists("/etc/runlevels/default/sshd"):
                        guestfs_handle.ln_s("/etc/init.d/sshd", "/etc/runlevels/default/sshd")
                    logger.info("Enabled SSH service (OpenRC) for %s", self._rootfs_path.name)
                    return True
                if guestfs_handle.exists("/etc/init.d/ssh"):
                    if not guestfs_handle.exists("/etc/runlevels/default/ssh"):
                        guestfs_handle.ln_s("/etc/init.d/ssh", "/etc/runlevels/default/ssh")
                    logger.info("Enabled SSH service (OpenRC) for %s", self._rootfs_path.name)
                    return True
                logger.warning("SSH init script not found for OpenRC in %s", self._rootfs_path.name)
                return False

            if guestfs_handle.exists("/etc/init.d/ssh"):
                for level in ["2", "3", "4", "5"]:
                    guestfs_handle.mkdir_p(f"/etc/rc{level}.d")
                    link_path = f"/etc/rc{level}.d/S02ssh"
                    if not guestfs_handle.exists(link_path):
                        guestfs_handle.ln_s("../init.d/ssh", link_path)
                logger.info("Enabled SSH service (sysvinit) for %s", self._rootfs_path.name)
                return True

            logger.warning("SSH init script not found for sysvinit in %s", self._rootfs_path.name)
            return False
        except Exception as exc:
            logger.error("Failed to enable SSH for %s: %s", self._rootfs_path.name, exc)
            return False

    def configure_ssh_keys(self, guestfs_handle: Any) -> None:
        """Configure SSH key authentication in guest."""
        try:
            if not guestfs_handle.exists("/etc/ssh/sshd_config"):
                logger.warning("sshd_config not found in %s", self._rootfs_path.name)
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
            if self._user != "root":
                config_lines.append(f"AllowUsers {self._user}")
            else:
                config_lines.append("PermitRootLogin prohibit-password")

            guestfs_handle.write(f"{sshd_config_dir}/mvm.conf", "\n".join(config_lines) + "\n")
            guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, f"{sshd_config_dir}/mvm.conf")
            logger.info(
                "Configured SSH key authentication for user '%s' in %s",
                self._user,
                self._rootfs_path.name,
            )
        except Exception as exc:
            logger.warning("Failed to configure sshd: %s", exc)

    def ensure_user(self, guestfs_handle: Any) -> None:
        """Create user in guest with sudoers."""
        if self._user == "root":
            return

        try:
            passwd_content = ""
            if guestfs_handle.exists("/etc/passwd"):
                passwd_content = guestfs_handle.read_file("/etc/passwd")
                if isinstance(passwd_content, bytes):
                    passwd_content = passwd_content.decode("utf-8", errors="replace")

            for line in passwd_content.strip().split("\n"):
                if line.startswith(f"{self._user}:"):
                    logger.debug(
                        "User '%s' already exists in %s", self._user, self._rootfs_path.name
                    )
                    return

            home_dir = f"/home/{self._user}"
            guestfs_handle.mkdir_p(home_dir)
            guestfs_handle.mkdir_p(f"{home_dir}/.ssh")
            guestfs_handle.write(
                "/etc/passwd",
                f"{self._user}:!:{CONST_DEFAULT_USER_UID}:{CONST_DEFAULT_USER_GID}::{home_dir}:/bin/bash\n",
                mode="a",
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, "/etc/passwd")
            guestfs_handle.write(
                "/etc/shadow",
                f"{self._user}:!:{CONST_SHADOW_DAYS_SINCE_EPOCH}:{CONST_SHADOW_MIN_DAYS}:{CONST_SHADOW_MAX_DAYS}:{CONST_SHADOW_WARN_DAYS}:::\n",
                mode="a",
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_SHADOW, "/etc/shadow")
            guestfs_handle.write(
                "/etc/group", f"{self._user}:x:{CONST_DEFAULT_USER_GID}:\n", mode="a"
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_PUBLIC_KEY, "/etc/group")
            guestfs_handle.mkdir_p("/etc/sudoers.d")
            guestfs_handle.write(
                f"/etc/sudoers.d/{self._user}", f"{self._user} ALL=(ALL) NOPASSWD: ALL\n"
            )
            guestfs_handle.chmod(CONST_FILE_PERMS_SUDOERS, f"/etc/sudoers.d/{self._user}")
            guestfs_handle.chown(CONST_DEFAULT_USER_UID, CONST_DEFAULT_USER_GID, home_dir)
            guestfs_handle.chown(CONST_DEFAULT_USER_UID, CONST_DEFAULT_USER_GID, f"{home_dir}/.ssh")
            logger.info(
                "Created user '%s' with UID/GID 1000 in %s", self._user, self._rootfs_path.name
            )
        except Exception as exc:
            logger.warning("Failed to create user '%s': %s", self._user, exc)

    def generate_host_keys(self, guestfs_handle: Any) -> None:
        """Set up SSH host key generation service."""
        try:
            key_types = ["ssh_host_rsa_key", "ssh_host_ecdsa_key", "ssh_host_ed25519_key"]
            missing_keys = [
                key for key in key_types if not guestfs_handle.exists(f"/etc/ssh/{key}")
            ]
            if not missing_keys:
                logger.debug("All SSH host keys already exist in %s", self._rootfs_path.name)
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
            logger.info("Created SSH host key generation service in %s", self._rootfs_path.name)
        except Exception as exc:
            logger.warning("Failed to setup SSH host key generation: %s", exc)


class CloudInitProvisioner:
    """Handle all cloud-init modes cleanly."""

    def provision(
        self,
        mode: CloudInitMode,
        vm_dir: Path,
        guest_ip: str,
        user: str,
        ssh_pub_key: list[str] | str | None,
        user_data: Path | None,
        net_config: NetworkConfig,
        vm_id: str,
        nocloud_net_port: int | None,
        cloud_init_iso_path: Path | None,
        keep_cloud_init_iso: bool,
    ) -> CloudInitProvisionResult:
        """Returns what was created (iso path, nocloud url, etc.)."""
        from mvmctl.models.cloud_init import CloudInitMode

        if mode == CloudInitMode.OFF:
            return self._provision_off()
        elif mode == CloudInitMode.NET:
            return self._provision_net(
                vm_dir, guest_ip, user, ssh_pub_key, user_data, net_config, vm_id, nocloud_net_port
            )
        elif mode == CloudInitMode.ISO:
            return self._provision_iso(
                vm_dir,
                guest_ip,
                user,
                ssh_pub_key,
                user_data,
                net_config,
                cloud_init_iso_path,
                keep_cloud_init_iso,
            )
        elif mode == CloudInitMode.INJECT:
            return self._provision_inject(
                vm_dir, guest_ip, user, ssh_pub_key, user_data, net_config
            )
        else:
            return CloudInitProvisionResult()

    def _provision_off(self) -> CloudInitProvisionResult:
        """Provision with cloud-init disabled."""
        return CloudInitProvisionResult()

    def _provision_net(
        self,
        vm_dir: Path,
        guest_ip: str,
        user: str,
        ssh_pub_key: list[str] | str | None,
        user_data: Path | None,
        net_config: NetworkConfig,
        vm_id: str,
        nocloud_net_port: int | None,
    ) -> CloudInitProvisionResult:
        """Provision using nocloud-net mode with HTTP server."""
        import ipaddress

        from mvmctl.core.cloud_init import write_cloud_init
        from mvmctl.models.cloud_init import CloudInitWriteConfig
        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        cloud_init_dir = vm_dir / "cloud-init"
        cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        prefix_len = ipaddress.IPv4Network(net_config.subnet, strict=False).prefixlen
        cloud_init_write_config = CloudInitWriteConfig(
            cloud_init_dir=cloud_init_dir,
            vm_name=vm_dir.name,
            guest_ip=guest_ip,
            user=user,
            ssh_pub_key=ssh_pub_key,
            custom_user_data=user_data,
            ipv4_gateway=net_config.ipv4_gateway,
            prefix_len=prefix_len,
            skip_network_config=False,
        )
        write_cloud_init(cloud_init_write_config)

        net_manager = NoCloudNetServerManager()
        url, port = net_manager.start_server(
            vm_dir.name,
            cloud_init_dir,
            net_config.ipv4_gateway,
            vm_id,
            preferred_port=nocloud_net_port if nocloud_net_port is not None else 0,
        )
        nocloud_server_pid = net_manager.get_server_pid(vm_dir.name, vm_id)

        return CloudInitProvisionResult(
            nocloud_url=url,
            nocloud_port=port,
            nocloud_pid=nocloud_server_pid,
        )

    def _provision_iso(
        self,
        vm_dir: Path,
        guest_ip: str,
        user: str,
        ssh_pub_key: list[str] | str | None,
        user_data: Path | None,
        net_config: NetworkConfig,
        cloud_init_iso_path: Path | None,
        keep_cloud_init_iso: bool,
    ) -> CloudInitProvisionResult:
        """Provision using ISO mode with cloud-init ISO image."""
        import ipaddress

        from mvmctl.constants import DEFAULT_CLOUD_INIT_ISO_NAME
        from mvmctl.core.cloud_init import create_cloud_init_iso, write_cloud_init
        from mvmctl.exceptions import MVMError
        from mvmctl.models.cloud_init import CloudInitWriteConfig

        if cloud_init_iso_path is not None:
            if not cloud_init_iso_path.exists():
                raise MVMError(f"Custom cloud-init ISO not found: {cloud_init_iso_path}")
            return CloudInitProvisionResult(iso_path=cloud_init_iso_path)

        cloud_init_dir = vm_dir / "cloud-init"
        cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        prefix_len = ipaddress.IPv4Network(net_config.subnet, strict=False).prefixlen
        cloud_init_write_config = CloudInitWriteConfig(
            cloud_init_dir=cloud_init_dir,
            vm_name=vm_dir.name,
            guest_ip=guest_ip,
            user=user,
            ssh_pub_key=ssh_pub_key,
            custom_user_data=user_data,
            ipv4_gateway=net_config.ipv4_gateway,
            prefix_len=prefix_len,
            skip_network_config=False,
        )
        write_cloud_init(cloud_init_write_config)

        iso_path = vm_dir / DEFAULT_CLOUD_INIT_ISO_NAME
        try:
            create_cloud_init_iso(cloud_init_dir, iso_path)
        except Exception as exc:
            from mvmctl.exceptions import CloudInitError

            raise CloudInitError(f"Failed to create cloud-init ISO: {exc}") from exc

        return CloudInitProvisionResult(iso_path=iso_path)

    def _provision_inject(
        self,
        vm_dir: Path,
        guest_ip: str,
        user: str,
        ssh_pub_key: list[str] | str | None,
        user_data: Path | None,
        net_config: NetworkConfig,
    ) -> CloudInitProvisionResult:
        """Provision using inject mode with direct rootfs injection."""
        import ipaddress

        from mvmctl.core.cloud_init import write_cloud_init
        from mvmctl.core.rootfs_injector import inject_cloud_init
        from mvmctl.exceptions import CloudInitError
        from mvmctl.models.cloud_init import CloudInitWriteConfig

        cloud_init_dir = vm_dir / "cloud-init"
        cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

        prefix_len = ipaddress.IPv4Network(net_config.subnet, strict=False).prefixlen
        cloud_init_write_config = CloudInitWriteConfig(
            cloud_init_dir=cloud_init_dir,
            vm_name=vm_dir.name,
            guest_ip=guest_ip,
            user=user,
            ssh_pub_key=ssh_pub_key,
            custom_user_data=user_data,
            ipv4_gateway=net_config.ipv4_gateway,
            prefix_len=prefix_len,
            skip_network_config=False,
        )
        write_cloud_init(cloud_init_write_config)

        rootfs_path = vm_dir / "rootfs.ext4"
        if not rootfs_path.exists():
            for ext in [".ext4", ".btrfs"]:
                rootfs_path = vm_dir / f"rootfs{ext}"
                if rootfs_path.exists():
                    break

        try:
            inject_cloud_init(str(rootfs_path), str(cloud_init_dir))
        except Exception as exc:
            raise CloudInitError(f"Direct injection failed: {exc}") from exc

        return CloudInitProvisionResult()


@dataclass
@dataclass
class VMBuilder:
    """Builder for VM creation - tracks state and spawns processes.

    Generates VM ID automatically on instantiation based on name.
    NOTE: PURE STATE TRACKER for creation. Does NOT call core modules directly
    except for spawn() which is a builder action.
    Core call sequencing stays in _orchestration.py (the orchestrator).
    """

    name: str
    vm_id: str = field(init=False)
    resolved: Any = None  # VMResolvedDependencies

    vm_dir: Path = field(init=False)
    tap_name: str = ""
    guest_ip: str = ""
    net_manager: NoCloudNetServerManager | None = None
    relay_mgr: ConsoleRelayManager | None = None
    pty_master_fd: int | None = None
    pty_slave_fd: int | None = None
    nocloud_net_port: int = 0
    log_fp: Any = None
    console_fp: Any = None
    cloud_init_result: CloudInitProvisionResult | None = None

    resources_created: dict[str, bool] = field(default_factory=dict)

    def __post_init__(self) -> None:
        """Generate VM ID after initialization."""
        created_at = datetime.now()
        self.vm_id = self._generate_vm_id(self.name, created_at)
        self.vm_dir = Path(get_vm_dir_by_hash(self.vm_id))

    @staticmethod
    def _generate_vm_id(name: str, created_at: datetime) -> str:
        """Generate a unique VM ID from name and creation time."""
        import hashlib

        data = f"{name}:{created_at.isoformat()}"
        return hashlib.sha256(data.encode()).hexdigest()[:16]

    def mark_created(self, resource: str) -> None:
        """Mark a resource as created (for cleanup tracking)."""
        self.resources_created[resource] = True

    def was_created(self, resource: str) -> bool:
        """Check if a resource was created."""
        return self.resources_created.get(resource, False)

    def cleanup(self) -> None:
        """Clean up all created resources. Delegates to _orchestration.py.

        VMBuilder is a pure state tracker. Actual cleanup orchestration
        lives in _orchestration.py to maintain clean layer separation.
        """
        from mvmctl.api.vm._orchestration import _perform_creation_cleanup

        _perform_creation_cleanup(self)

    def spawn(self, resolved: Any, config_file: Path) -> tuple[int, Path | None, int | None]:
        """Spawn firecracker process and return PID, socket path, and console relay PID.

        Args:
            resolved: Resolved VM inputs
            config_file: Path to firecracker config file

        Returns:
            Tuple of (pid, socket_path, console_relay_pid)

        Raises:
            VMCreateError: If firecracker process fails to start
        """
        import logging
        import os
        import subprocess
        import time
        from typing import Any as TypingAny

        from mvmctl.constants import (
            CONST_POLL_STEP_SECONDS,
            DEFAULT_FC_API_SOCKET_FILENAME,
            DEFAULT_FC_CONSOLE_LOG_FILENAME,
            DEFAULT_FC_LOG_FILENAME,
            DEFAULT_FC_PID_FILENAME,
        )
        from mvmctl.exceptions import VMCreateError
        from mvmctl.utils.fs import write_pid_file

        logger = logging.getLogger(__name__)

        if self.vm_dir is None:
            raise VMCreateError("VM directory not set in context")

        log_file = self.vm_dir / DEFAULT_FC_LOG_FILENAME
        console_log_file = self.vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME
        pid_file = self.vm_dir / DEFAULT_FC_PID_FILENAME

        socket_path: Path | None = None
        if resolved.enable_api_socket:
            socket_path = self.vm_dir / DEFAULT_FC_API_SOCKET_FILENAME

        fc_cmd = [resolved.firecracker_bin, "--no-api", "--config-file", str(config_file)]
        if resolved.enable_api_socket and socket_path:
            fc_cmd = [
                resolved.firecracker_bin,
                "--api-sock",
                str(socket_path),
                "--config-file",
                str(config_file),
            ]

        log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
        self.log_fp = log_fp

        console_fp = None
        proc: subprocess.Popen[TypingAny] | None = None

        try:
            if resolved.enable_console and self.pty_slave_fd is not None:
                proc = subprocess.Popen(
                    fc_cmd,
                    stdin=self.pty_slave_fd,
                    stdout=self.pty_slave_fd,
                    stderr=log_fp,
                    start_new_session=True,
                    pass_fds=[self.pty_slave_fd],
                )
            else:
                console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
                self.console_fp = console_fp
                proc = subprocess.Popen(
                    fc_cmd,
                    stdin=subprocess.DEVNULL,
                    stdout=console_fp,
                    stderr=log_fp,
                    start_new_session=True,
                )

            time.sleep(CONST_POLL_STEP_SECONDS)
            poll_result = proc.poll()
            if poll_result is not None and isinstance(poll_result, int):
                raise VMCreateError(
                    f"Firecracker process exited immediately with code {poll_result}"
                )

            if resolved.enable_console and self.pty_slave_fd is not None:
                try:
                    os.close(self.pty_slave_fd)
                    self.pty_slave_fd = None
                except OSError:
                    pass

            try:
                log_fp.close()
                self.log_fp = None
            except OSError:
                pass

            if console_fp is not None:
                try:
                    console_fp.close()
                    self.console_fp = None
                except OSError:
                    pass

            console_relay_pid = _setup_console_relay(
                enable_console=resolved.enable_console,
                relay_mgr=self.relay_mgr,
                pty_master_fd=self.pty_master_fd,
                vm_dir=self.vm_dir,
                vm_name=resolved.name,
                mark_created=self.mark_created,
            )

            write_pid_file(pid_file, proc.pid)

            return proc.pid, socket_path, console_relay_pid

        except Exception as exc:
            logger.error("Failed to start Firecracker VM: %s", exc)
            if log_fp is not None:
                try:
                    log_fp.close()
                except OSError:
                    pass
            if console_fp is not None:
                try:
                    console_fp.close()
                except OSError:
                    pass
            raise


def _setup_console_relay(
    enable_console: bool,
    relay_mgr: ConsoleRelayManager | None,
    pty_master_fd: int | None,
    vm_dir: Path | None,
    vm_name: str,
    mark_created: Callable[[str], None] | None = None,
) -> int | None:
    """Setup console relay for VM.

    Args:
        enable_console: Whether console is enabled
        relay_mgr: Console relay manager instance
        pty_master_fd: PTY master file descriptor
        vm_dir: VM directory path
        vm_name: VM name for relay identification
        mark_created: Optional callback to mark resource as created

    Returns:
        Console relay PID or None if not started
    """
    import logging
    import os

    logger = logging.getLogger(__name__)

    if not enable_console or relay_mgr is None or pty_master_fd is None or vm_dir is None:
        return None

    try:
        console_relay_pid = relay_mgr.start_relay(vm_name, pty_master_fd, vm_dir)[1]
        if mark_created:
            mark_created("console_relay")
        return console_relay_pid
    except Exception as exc:
        logger.warning("Failed to start console relay: %s", exc)
        try:
            os.close(pty_master_fd)
        except OSError:
            pass
        return None


__all__ = [
    "GuestfsProvisioner",
    "CloudInitProvisioner",
    "CloudInitProvisionResult",
    "VMBuilder",
]
