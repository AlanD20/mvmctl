"""Tests for constants.py — project constants."""

from __future__ import annotations

from unittest.mock import patch

from mvmctl.constants import (
    CLI_NAME,
    CONST_FILE_PERMS_PRIVATE_KEY,
    CONST_FILE_PERMS_PUBLIC_KEY,
    CONST_SHADOW_DAYS_SINCE_EPOCH,
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    CONST_VM_VCPU_MAX,
    CONST_VM_VCPU_MIN,
    DEBUG_MODE,
    HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
    MVM_DB_FILENAME,
    OVERRIDABLE_DEFAULTS,
    PRIVILEGED_BINARIES,
    REQUIRED_BINARIES,
    _resolve_project_name,
    env_var,
)


class TestProjectIdentity:
    """Tests for project identity constants."""

    def test_resolve_project_name_package_found(self):
        import importlib.metadata

        _resolve_project_name.cache_clear()
        with patch.object(
            importlib.metadata, "metadata", return_value={"Name": "mvmctl"}
        ):
            result = _resolve_project_name()
        _resolve_project_name.cache_clear()
        assert result == "mvmctl"

    def test_resolve_project_name_package_not_found(self):
        import importlib.metadata

        _resolve_project_name.cache_clear()
        with patch.object(
            importlib.metadata,
            "metadata",
            side_effect=importlib.metadata.PackageNotFoundError(
                "not installed"
            ),
        ):
            result = _resolve_project_name()
        _resolve_project_name.cache_clear()
        assert result == "mvmctl"

    def test_cli_name(self):
        assert CLI_NAME == "mvmctl" or CLI_NAME == "mvm"

    def test_env_var(self):
        assert env_var("CACHE_DIR").endswith("_CACHE_DIR")
        assert env_var("LOG_LEVEL").endswith("_LOG_LEVEL")

    def test_mvm_db_filename(self):
        assert MVM_DB_FILENAME == "mvmdb.db"


class TestOverridableDefaults:
    """Tests for OVERRIDABLE_DEFAULTS."""

    def test_has_vm_defaults(self):
        assert "defaults.vm" in OVERRIDABLE_DEFAULTS
        vm_defaults = OVERRIDABLE_DEFAULTS["defaults.vm"]
        assert "vcpu_count" in vm_defaults
        assert "mem_size_mib" in vm_defaults
        assert "ssh_user" in vm_defaults

    def test_has_network_defaults(self):
        assert "defaults.network" in OVERRIDABLE_DEFAULTS
        net_defaults = OVERRIDABLE_DEFAULTS["defaults.network"]
        assert "subnet" in net_defaults

    def test_has_image_defaults(self):
        assert "defaults.image" in OVERRIDABLE_DEFAULTS

    def test_has_kernel_defaults(self):
        assert "defaults.kernel" in OVERRIDABLE_DEFAULTS

    def test_has_firecracker_defaults(self):
        assert "defaults.firecracker" in OVERRIDABLE_DEFAULTS

    def test_has_cloudinit_defaults(self):
        assert "defaults.cloudinit" in OVERRIDABLE_DEFAULTS

    def test_vm_default_values(self):
        vm_defaults = OVERRIDABLE_DEFAULTS["defaults.vm"]
        assert vm_defaults["vcpu_count"] == 1
        assert vm_defaults["mem_size_mib"] == 512
        assert vm_defaults["ssh_user"] == "root"

    def test_network_default_values(self):
        net_defaults = OVERRIDABLE_DEFAULTS["defaults.network"]
        assert "172.27.0.0/24" in net_defaults["subnet"]

    def test_get_default_success(self):
        """get_default() returns the correct default value for a known key."""
        from mvmctl.constants import get_default

        result = get_default("defaults.vm", "vcpu_count")
        assert result == 1

    def test_get_default_nested(self):
        """get_default() handles nested category/key lookups."""
        from mvmctl.constants import get_default

        result = get_default("settings", "guestfs_enabled")
        assert result is False

    def test_is_compiled_mode(self):
        """is_compiled_mode() returns False in development (non-frozen) mode."""
        from mvmctl.constants import is_compiled_mode

        assert is_compiled_mode() is False


class TestConstants:
    """Tests for specific constant values."""

    def test_vm_mem_limits(self):
        assert CONST_VM_MEM_MIN_MIB == 128
        assert CONST_VM_MEM_MAX_MIB == 65536

    def test_vm_vcpu_limits(self):
        assert CONST_VM_VCPU_MIN == 1
        assert CONST_VM_VCPU_MAX == 32

    def test_file_permissions(self):
        assert CONST_FILE_PERMS_PRIVATE_KEY == 0o600
        assert CONST_FILE_PERMS_PUBLIC_KEY == 0o644

    def test_shadow_days_since_epoch(self):
        assert CONST_SHADOW_DAYS_SINCE_EPOCH == 19700

    def test_debug_mode_default(self):
        assert DEBUG_MODE is False

    def test_http_timeouts(self):
        assert isinstance(HTTP_TIMEOUT_KERNEL_DOWNLOAD_S, int)

    def test_privileged_binaries(self):
        assert isinstance(PRIVILEGED_BINARIES, dict)
        assert "/usr/sbin/ip" in PRIVILEGED_BINARIES
        assert "/usr/sbin/iptables" in PRIVILEGED_BINARIES

    def test_required_binaries(self):
        assert isinstance(REQUIRED_BINARIES, list)
        assert "ip" in REQUIRED_BINARIES
        assert "ssh-keygen" in REQUIRED_BINARIES
