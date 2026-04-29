"""Host service — stateless setup and stateful restore operations."""

from __future__ import annotations

import grp
import logging
import os
import re
import shutil
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.constants import (
    CONST_FILE_PERMS_CONFIG,
    CONST_FILE_PERMS_SUDOERS,
    DEFAULT_SUDOERS_DIR,
    DEFAULT_SYSCTL_CONF_DIR,
    DEFAULT_SYSCTL_CONF_PATH,
    IPTABLES_RULES_V4,
    ISO_BINARIES,
    PRIVILEGED_BINARIES,
    PROJECT_NAME,
    REQUIRED_BINARIES,
    SUDOERS_DROP_IN_PATH,
)
from mvmctl.core.host._repository import HostRepository
from mvmctl.exceptions import HostError
from mvmctl.models.host import HostStateChangeItem

logger = logging.getLogger(__name__)

SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path(DEFAULT_SYSCTL_CONF_PATH)


class HostService:
    """Stateless host setup operations and stateful restore."""

    def __init__(self, repo: HostRepository) -> None:
        self._repo = repo

    @staticmethod
    def _run(
        cmd: list[str],
        *,
        failure_msg: str,
        missing_msg: str,
        capture: bool = True,
        check: bool = True,
    ) -> subprocess.CompletedProcess[str]:
        """Run a subprocess with consistent error handling.

        Args:
            cmd: Command and arguments as a list.
            failure_msg: Message prefix for HostError on CalledProcessError.
            missing_msg: Message for HostError on FileNotFoundError.
            capture: Whether to capture stdout/stderr. Default True.
            check: Whether to raise on non-zero exit. Default True.

        Returns:
            CompletedProcess result.

        Raises:
            HostError: On CalledProcessError or FileNotFoundError.
        """
        try:
            result = subprocess.run(
                cmd,
                capture_output=capture,
                text=True,
                check=check,
            )
        except subprocess.CalledProcessError as e:
            raise HostError(f"{failure_msg}: {e}") from e
        except FileNotFoundError as e:
            raise HostError(missing_msg) from e
        return result

    @staticmethod
    def check_kvm_access() -> bool:
        """Check if /dev/kvm is accessible."""
        kvm = Path("/dev/kvm")
        return kvm.exists() and os.access(kvm, os.R_OK | os.W_OK)

    @staticmethod
    def check_required_binaries() -> list[str]:
        """Check for required binaries and return list of missing ones."""
        missing: list[str] = []
        for name in REQUIRED_BINARIES:
            if not shutil.which(name):
                missing.append(name)
        if ISO_BINARIES:
            has_iso = any(shutil.which(b) for b in ISO_BINARIES)
            if not has_iso:
                missing.append(" or ".join(ISO_BINARIES))
        return missing

    @staticmethod
    def check_cloud_localds() -> bool:
        """Check if cloud-localds is available."""
        return shutil.which("cloud-localds") is not None

    @staticmethod
    def _group_exists(group_name: str) -> bool:
        """Return True if the named group exists on the system."""
        try:
            grp.getgrnam(group_name)
            return True
        except KeyError:
            return False

    @staticmethod
    def _user_in_group(username: str, group_name: str) -> bool:
        """Return True if username is a member of group_name."""
        try:
            g = grp.getgrnam(group_name)
            return username in g.gr_mem
        except KeyError:
            return False

    @staticmethod
    def create_group(group_name: str) -> bool:
        """Create a system group. Return True if created, False if it already existed."""
        if HostService._group_exists(group_name):
            return False
        HostService._run(
            ["groupadd", "--system", group_name],
            failure_msg=f"Failed to create group {group_name}",
            missing_msg="groupadd command not found",
        )
        return True

    @staticmethod
    def add_user_to_group(username: str, group_name: str) -> bool:
        """Add a user to a group. Return True if added, False if already a member."""
        if HostService._user_in_group(username, group_name):
            return False
        HostService._run(
            ["usermod", "-aG", group_name, username],
            failure_msg=f"Failed to add {username} to group {group_name}",
            missing_msg="usermod command not found",
        )
        return True

    @staticmethod
    def remove_user_from_group(username: str, group_name: str) -> bool:
        """Remove a user from a system group.

        Returns:
            True if the user was removed from the group, False if not a member.
        """
        try:
            grp_info = grp.getgrnam(group_name)
            if username not in grp_info.gr_mem:
                return False
        except KeyError:
            return False

        HostService._run(
            ["gpasswd", "-d", username, group_name],
            failure_msg=f"Failed to remove user {username} from group {group_name}",
            missing_msg="gpasswd command not found",
        )
        return True

    @staticmethod
    def validate_sudoers_binaries() -> None:
        """Verify that all privileged binaries referenced in the sudoers drop-in exist on disk."""
        for binary, pkg in PRIVILEGED_BINARIES.items():
            if not Path(binary).exists():
                raise HostError(
                    f"Required binary not found: {binary} (install {pkg})"
                )

    @staticmethod
    def _generate_sudoers_content(group_name: str) -> str:
        """Generate the sudoers drop-in content granting the group passwordless access."""
        from mvmctl.constants import PROJECT_NAME

        if not re.fullmatch(r"[a-z][a-z0-9_-]{0,30}", group_name):
            raise HostError(f"Invalid group name: {group_name!r}")
        binaries_str = ", ".join(PRIVILEGED_BINARIES)
        return (
            f"# Managed by {PROJECT_NAME} — do not edit manually.\n"
            f"# To remove: {PROJECT_NAME} host reset\n"
            f"%{group_name} ALL=(root) NOPASSWD: {binaries_str}\n"
        )

    @staticmethod
    def write_sudoers(path: Path, group_name: str) -> bool:
        """Write and validate the sudoers drop-in file for the given group."""
        HostService.validate_sudoers_binaries()
        content = HostService._generate_sudoers_content(group_name)
        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".sudoers", delete=False
        ) as f:
            f.write(content)
            tmp_path = f.name
        try:
            result = subprocess.run(
                ["visudo", "-c", "-f", tmp_path],
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                raise HostError(
                    f"Generated sudoers file failed visudo validation: {result.stderr}"
                )
        except FileNotFoundError:
            raise HostError("visudo not found — cannot validate sudoers syntax")
        finally:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
            path.chmod(CONST_FILE_PERMS_SUDOERS)
        except OSError as e:
            raise HostError(f"Failed to write sudoers file {path}: {e}") from e
        return True

    @staticmethod
    def remove_sudoers(path: Path) -> bool:
        """Remove the sudoers drop-in file if it exists."""
        if not path.exists():
            return False
        try:
            path.unlink()
            return True
        except OSError as e:
            raise HostError(f"Failed to remove sudoers file {path}: {e}") from e

    @staticmethod
    def remove_group(group_name: str) -> bool:
        """Delete a system group if it exists."""
        if not HostService._group_exists(group_name):
            return False
        HostService._run(
            ["groupdel", group_name],
            failure_msg=f"Failed to remove group {group_name}",
            missing_msg="groupdel command not found",
        )
        return True

    @staticmethod
    def _get_ip_forward_status() -> str:
        """Get the current IP forwarding status."""
        result = HostService._run(
            ["sysctl", "-n", SYSCTL_KEY],
            failure_msg=f"Failed to read {SYSCTL_KEY}",
            missing_msg="sysctl command not found",
        )
        return result.stdout.strip()

    @staticmethod
    def enable_ip_forward() -> HostStateChangeItem | None:
        """Enable IP forwarding if not already enabled."""
        current = HostService._get_ip_forward_status()
        if current == "1":
            logger.debug("IP forwarding already enabled")
            return None
        HostService._run(
            ["sysctl", "-w", f"{SYSCTL_KEY}=1"],
            failure_msg="Failed to enable IP forwarding",
            missing_msg="sysctl command not found",
        )
        return HostStateChangeItem(
            session_id="",
            init_timestamp="",
            setting=SYSCTL_KEY,
            mechanism="sysctl",
            applied_value="1",
            reverted=False,
            change_order=0,
            created_at="",
            original_value=current,
        )

    @staticmethod
    def persist_sysctl() -> HostStateChangeItem | None:
        """Persist sysctl configuration to file."""
        content = f"{SYSCTL_KEY} = 1\n"
        if SYSCTL_CONF.exists() and SYSCTL_CONF.read_text() == content:
            logger.debug(
                "sysctl persist file already exists with correct content"
            )
            return None
        original: str | None = None
        if SYSCTL_CONF.exists():
            original = SYSCTL_CONF.read_text()
        try:
            SYSCTL_CONF.parent.mkdir(parents=True, exist_ok=True)
            SYSCTL_CONF.write_text(content)
        except OSError as e:
            raise HostError(f"Failed to write {SYSCTL_CONF}: {e}") from e
        return HostStateChangeItem(
            session_id="",
            init_timestamp="",
            setting="sysctl_persist_file",
            mechanism="file_create",
            applied_value=str(SYSCTL_CONF),
            reverted=False,
            change_order=0,
            created_at="",
            original_value=original,
        )

    @staticmethod
    def _is_module_loaded(module: str) -> bool:
        """Check if a kernel module is loaded."""
        try:
            result = subprocess.run(
                ["lsmod"],
                capture_output=True,
                text=True,
                check=False,
            )
            if result.returncode != 0:
                return False
            return any(
                line.split()[0] == module
                for line in result.stdout.splitlines()
                if line
            )
        except (OSError, subprocess.CalledProcessError):
            return False

    @staticmethod
    def _load_module(module: str) -> None:
        """Load a kernel module."""
        HostService._run(
            ["modprobe", module],
            failure_msg=f"Failed to load kernel module {module}",
            missing_msg="modprobe command not found",
        )

    @staticmethod
    def ensure_kvm_modules() -> list[HostStateChangeItem]:
        """Ensure KVM modules are loaded and return list of changes."""
        changes: list[HostStateChangeItem] = []
        kvm_modules = ["kvm"]
        vendor_modules = ["kvm_intel", "kvm_amd"]
        for module in kvm_modules:
            if HostService._is_module_loaded(module):
                logger.debug("Module %s already loaded", module)
                continue
            HostService._load_module(module)
            changes.append(
                HostStateChangeItem(
                    session_id="",
                    init_timestamp="",
                    setting=f"module:{module}",
                    mechanism="modprobe",
                    applied_value=module,
                    reverted=False,
                    change_order=0,
                    created_at="",
                )
            )
        vendor_loaded = any(
            HostService._is_module_loaded(m) for m in vendor_modules
        )
        if not vendor_loaded:
            for module in vendor_modules:
                try:
                    HostService._load_module(module)
                    changes.append(
                        HostStateChangeItem(
                            session_id="",
                            init_timestamp="",
                            setting=f"module:{module}",
                            mechanism="modprobe",
                            applied_value=module,
                            reverted=False,
                            change_order=0,
                            created_at="",
                        )
                    )
                    break
                except HostError:
                    continue
        return changes

    @staticmethod
    def save_iptables_rules() -> HostStateChangeItem | None:
        """Save iptables rules to file."""
        rules_path = Path(IPTABLES_RULES_V4)
        try:
            result = subprocess.run(
                ["iptables-save"],
                capture_output=True,
                text=True,
                check=True,
            )
        except (subprocess.CalledProcessError, FileNotFoundError):
            logger.warning(
                "iptables-save unavailable — rules will not survive reboot. "
                "Install iptables-persistent (Debian/Ubuntu) or iptables-services (RHEL)."
            )
            return None
        raw = result.stdout
        if not isinstance(raw, str):
            logger.debug(
                "iptables-save stdout is not a str (likely mocked); skipping persistence"
            )
            return None
        from mvmctl.utils.network import NetworkUtils

        filtered = NetworkUtils.strip_tap_rules(raw)
        original: str | None = None
        if rules_path.exists():
            try:
                original = rules_path.read_text()
            except OSError:
                original = None
        if original == filtered:
            logger.debug("iptables rules already up-to-date in %s", rules_path)
            return None
        try:
            rules_path.parent.mkdir(parents=True, exist_ok=True)
            rules_path.write_text(filtered)
        except OSError as e:
            raise HostError(f"Failed to write {rules_path}: {e}") from e
        logger.info(
            "Persisted iptables rules to %s (TAP rules excluded)", rules_path
        )
        return HostStateChangeItem(
            session_id="",
            init_timestamp="",
            setting="iptables_rules_v4",
            mechanism="iptables_save",
            applied_value=str(rules_path),
            reverted=False,
            change_order=0,
            created_at="",
            original_value=original,
        )

    def restore_state(self) -> list[HostStateChangeItem]:
        """Revert host changes recorded in the database."""
        self._repo.initialize_state()
        changes = self._repo.list_changes(include_reverted=False)
        if not changes:
            raise HostError("No saved host state to restore")
        reverted: list[HostStateChangeItem] = []
        reverted_at = datetime.now(timezone.utc).isoformat()
        restorable_sysctl: frozenset[str] = frozenset({SYSCTL_KEY})
        restorable_files: frozenset[Path] = frozenset(
            {
                Path(SUDOERS_DROP_IN_PATH),
                Path(f"{DEFAULT_SYSCTL_CONF_DIR}/{PROJECT_NAME}.conf"),
            }
        )

        for change in reversed(changes):
            if (
                change.mechanism == "sysctl"
                and change.original_value is not None
            ):
                if change.setting not in restorable_sysctl:
                    logger.warning(
                        "Skipping disallowed sysctl key '%s' from state",
                        change.setting,
                    )
                    continue
                HostService._run(
                    [
                        "sysctl",
                        "-w",
                        f"{change.setting}={change.original_value}",
                    ],
                    failure_msg=f"Failed to revert {change.setting}",
                    missing_msg="sysctl command not found",
                )
                reverted.append(
                    HostStateChangeItem(
                        session_id=change.session_id,
                        init_timestamp=change.init_timestamp,
                        setting=change.setting,
                        mechanism="sysctl",
                        applied_value=change.original_value,
                        reverted=False,
                        change_order=change.change_order,
                        created_at=change.created_at,
                        original_value=change.applied_value,
                    )
                )

            elif change.mechanism == "iptables_save":
                rules_path = Path(IPTABLES_RULES_V4)
                try:
                    if change.original_value is not None:
                        rules_path.write_text(change.original_value)
                        rules_path.chmod(CONST_FILE_PERMS_CONFIG)
                        logger.info(
                            "Restored original iptables rules to %s", rules_path
                        )
                    elif rules_path.exists():
                        rules_path.unlink()
                        logger.info(
                            "Removed %s (did not exist before host init)",
                            rules_path,
                        )
                    reverted.append(
                        HostStateChangeItem(
                            session_id=change.session_id,
                            init_timestamp=change.init_timestamp,
                            setting=change.setting,
                            mechanism="iptables_restore",
                            applied_value=change.original_value or "(removed)",
                            reverted=False,
                            change_order=change.change_order,
                            created_at=change.created_at,
                            original_value=change.applied_value,
                        )
                    )
                except OSError as e:
                    raise HostError(
                        f"Failed to restore iptables rules: {e}"
                    ) from e

            elif change.mechanism == "file_create":
                target = Path(change.applied_value).resolve()
                if not any(
                    target == allowed.resolve() for allowed in restorable_files
                ):
                    logger.warning(
                        "Skipping disallowed file path '%s' from state", target
                    )
                    continue
                if target.exists():
                    try:
                        if change.original_value is not None:
                            if str(target).startswith(DEFAULT_SUDOERS_DIR):
                                result = subprocess.run(
                                    ["visudo", "-c", "-f", "-"],
                                    input=change.original_value,
                                    capture_output=True,
                                    text=True,
                                )
                                if result.returncode != 0:
                                    raise HostError(
                                        "Sudoers content from state failed visudo validation: "
                                        f"{result.stderr}"
                                    )
                            target.write_text(change.original_value)
                        else:
                            target.unlink()
                        reverted.append(
                            HostStateChangeItem(
                                session_id=change.session_id,
                                init_timestamp=change.init_timestamp,
                                setting=change.setting,
                                mechanism="file_remove",
                                applied_value=change.original_value
                                or "(removed)",
                                reverted=False,
                                change_order=change.change_order,
                                created_at=change.created_at,
                                original_value=change.applied_value,
                            )
                        )
                    except OSError as e:
                        raise HostError(
                            f"Failed to revert file {target}: {e}"
                        ) from e

            if change.id is not None:
                self._repo.mark_change_reverted(change.id, reverted_at)

        return reverted


__all__ = ["HostService"]
