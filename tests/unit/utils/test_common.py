"""Tests for utils/common.py — common utilities."""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.common import (
    CacheUtils,
    CommonUtils,
    is_debug_mode,
    set_debug_mode,
)

# ---------------------------------------------------------------------------
# Debug state
# ---------------------------------------------------------------------------


class TestDebugState:
    """Tests for debug state functions."""

    def setup_method(self):
        set_debug_mode(False)

    def test_set_and_get(self):
        set_debug_mode(True)
        assert is_debug_mode() is True
        set_debug_mode(False)
        assert is_debug_mode() is False

    def test_default_is_false(self):
        set_debug_mode(False)
        assert is_debug_mode() is False


# ---------------------------------------------------------------------------
# CommonUtils
# ---------------------------------------------------------------------------


class TestCommonUtilsHumanReadableDatetime:
    """Tests for CommonUtils.human_readable_datetime()."""

    def test_valid_iso(self):
        result = CommonUtils.human_readable_datetime("2026-04-01T12:30:00")
        assert result == "2026/04/01 12:30:00"

    def test_z_suffix(self):
        result = CommonUtils.human_readable_datetime("2026-04-01T12:30:00Z")
        assert result == "2026/04/01 12:30:00"

    def test_none_returns_dash(self):
        result = CommonUtils.human_readable_datetime(None)
        assert result == "-"

    def test_empty_string_returns_dash(self):
        result = CommonUtils.human_readable_datetime("")
        assert result == "-"

    def test_invalid_timestamp_returns_original(self):
        result = CommonUtils.human_readable_datetime("not-a-timestamp")
        assert result == "not-a-timestamp"


class TestCommonUtilsFormatBytes:
    """Tests for CommonUtils.format_bytes_human_readable()."""

    def test_bytes(self):
        result = CommonUtils.format_bytes_human_readable(512)
        assert result == "512 B"

    def test_kib(self):
        result = CommonUtils.format_bytes_human_readable(2048)
        assert result == "2.0 KiB"

    def test_mib(self):
        result = CommonUtils.format_bytes_human_readable(5 * 1024 * 1024)
        assert result == "5.0 MiB"

    def test_gib(self):
        result = CommonUtils.format_bytes_human_readable(2 * 1024 * 1024 * 1024)
        assert result == "2.0 GiB"

    def test_tib(self):
        result = CommonUtils.format_bytes_human_readable(
            2048 * 1024 * 1024 * 1024
        )
        assert result == "2048.0 TiB"


class TestCommonUtilsValidateEntityName:
    """Tests for CommonUtils.validate_entity_name()."""

    def test_valid_name(self):
        result = CommonUtils.validate_entity_name("my-vm-1", "VM")
        assert result == "my-vm-1"

    def test_empty_name_raises(self):
        with pytest.raises(MVMError, match="cannot be empty"):
            CommonUtils.validate_entity_name("", "VM")

    def test_reserved_name_raises(self):
        with pytest.raises(MVMError, match="reserved"):
            CommonUtils.validate_entity_name("help", "VM")

    def test_dangerous_chars_raises(self):
        with pytest.raises(MVMError, match="forbidden characters"):
            CommonUtils.validate_entity_name("my;vm", "VM")

    def test_hyphen_start_raises(self):
        with pytest.raises(MVMError, match="cannot start with a hyphen"):
            CommonUtils.validate_entity_name("-myvm", "VM")


class TestCommonUtilsSanitizeForLog:
    """Tests for CommonUtils.sanitize_for_log()."""

    def test_noop_on_clean(self):
        result = CommonUtils.sanitize_for_log("hello world")
        assert result == "hello world"

    def test_removes_control_chars(self):
        result = CommonUtils.sanitize_for_log("hello\x00world\n")
        assert result == "helloworld"


class TestCommonUtilsContainsDangerousChars:
    """Tests for CommonUtils.contains_dangerous_chars()."""

    def test_no_dangerous_chars(self):
        assert CommonUtils.contains_dangerous_chars("hello-world") is False

    def test_shell_metachar(self):
        assert CommonUtils.contains_dangerous_chars("hello;world") is True
        assert CommonUtils.contains_dangerous_chars("hello|world") is True

    def test_path_traversal(self):
        assert CommonUtils.contains_dangerous_chars("../etc") is True


class TestCommonUtilsIsReservedName:
    """Tests for CommonUtils.is_reserved_name()."""

    def test_reserved_names(self):
        for name in ("help", "all", "default", "none", "root", "self", "null"):
            assert CommonUtils.is_reserved_name(name) is True

    def test_not_reserved(self):
        assert CommonUtils.is_reserved_name("my-vm") is False


# ---------------------------------------------------------------------------
# CacheUtils
# ---------------------------------------------------------------------------


class TestCacheUtilsGetCacheDir:
    """Tests for CacheUtils.get_cache_dir()."""

    def test_uses_env_var(self, monkeypatch, tmp_path: Path):
        custom = tmp_path / "custom_cache"
        monkeypatch.setenv("MVM_CACHE_DIR", str(custom))
        result = CacheUtils.get_cache_dir()
        assert result == custom

    def test_rejects_unsafe_path(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", "/etc/shadow")
        with pytest.raises(MVMError, match="Unsafe"):
            CacheUtils.get_cache_dir()

    def test_accepts_tmp_subdir(self, monkeypatch):
        monkeypatch.setenv("MVM_CACHE_DIR", "/tmp/mvm-test-cache")
        result = CacheUtils.get_cache_dir()
        assert result == Path("/tmp/mvm-test-cache")


class TestCacheUtilsGetConfigDir:
    """Tests for CacheUtils.get_config_dir()."""

    def test_uses_env_var(self, monkeypatch, tmp_path: Path):
        custom = tmp_path / "custom_config"
        monkeypatch.setenv("MVM_CONFIG_DIR", str(custom))
        result = CacheUtils.get_config_dir()
        assert result == custom

    def test_rejects_unsafe_path(self, monkeypatch):
        monkeypatch.setenv("MVM_CONFIG_DIR", "/etc/shadow")
        with pytest.raises(MVMError, match="Unsafe"):
            CacheUtils.get_config_dir()


class TestCacheUtilsSubdirs:
    """Tests for CacheUtils subdirectory methods."""

    def test_vms_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_vms_dir()
        assert result == tmp_path / "vms"
        assert result.exists()

    def test_images_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_images_dir()
        assert result == tmp_path / "images"
        assert result.exists()

    def test_kernels_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_kernels_dir()
        assert result == tmp_path / "kernels"
        assert result.exists()

    def test_logs_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_logs_dir()
        assert result == tmp_path / "logs"
        assert result.exists()

    def test_vm_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_vm_dir("vm1")
        assert result == tmp_path / "vms" / "vm1"

    def test_bin_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_bin_dir()
        assert result == tmp_path / "bin"
        assert result.exists()

    def test_keys_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path / "cache"))
        result = CacheUtils.get_keys_dir()
        assert result == tmp_path / "keys"

    def test_mvm_db_path(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_mvm_db_path()
        assert result.name == "mvmdb.db"
        assert result.parent == tmp_path

    def test_audit_log_path(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_audit_log_path()
        assert result == tmp_path / "audit.log"

    def test_resolve_dir(self, tmp_path: Path):
        path = tmp_path / "new_dir" / "nested"
        result = CacheUtils.resolve_dir(path)
        assert result == path
        assert path.exists()

    def test_temp_dir(self, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("MVM_TEMP_DIR", str(tmp_path / "mytemp"))
        result = CacheUtils.get_temp_dir()
        assert result == tmp_path / "mytemp"
        assert result.exists()


class TestCacheUtilsExtended:
    """Extended tests for CacheUtils uncovered paths."""

    def test_get_cache_dir_fallback(self, monkeypatch):
        monkeypatch.delenv("MVM_CACHE_DIR", raising=False)
        result = CacheUtils.get_cache_dir()
        assert "mvmctl" in str(result)
        assert ".cache" in str(result)

    def test_get_config_dir_fallback(self, monkeypatch):
        monkeypatch.delenv("MVM_CONFIG_DIR", raising=False)
        result = CacheUtils.get_config_dir()
        assert "mvmctl" in str(result)
        assert ".config" in str(result)

    def test_get_config_path(self, monkeypatch, tmp_path):
        monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
        result = CacheUtils.get_config_path()
        assert result == tmp_path / "config.json"

    def test_get_log_path(self, monkeypatch, tmp_path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_log_path()
        assert result == tmp_path / "mvmctl.log"

    def test_get_warm_image_dir_default(self, monkeypatch):
        monkeypatch.delenv("MVM_TEMP_DIR", raising=False)
        result = CacheUtils.get_warm_image_dir()
        assert result.name == "ready"
        assert result.parent.name == "mvmctl"

    def test_get_warm_image_dir_custom(self, tmp_path):
        result = CacheUtils.get_warm_image_dir(tmp_path=tmp_path)
        assert result.parent.parent == tmp_path
        assert result.parent.name == "mvmctl"
        assert result.name == "ready"

    def test_get_real_home_with_sudo_user(self, mocker, monkeypatch):
        monkeypatch.delenv("MVM_CACHE_DIR", raising=False)
        monkeypatch.setenv("SUDO_USER", "testuser")
        mock_getpwnam = mocker.patch("pwd.getpwnam")
        mock_getpwnam.return_value = mocker.MagicMock(pw_dir="/home/testuser")
        result = CacheUtils.get_cache_dir()
        assert "/home/testuser" in str(result)

    def test_get_real_home_sudo_user_not_found(self, mocker, monkeypatch):
        monkeypatch.setenv("SUDO_USER", "nonexistent")
        monkeypatch.delenv("MVM_CACHE_DIR", raising=False)
        mock_getpwnam = mocker.patch("pwd.getpwnam")
        mock_getpwnam.side_effect = KeyError("not found")
        result = CacheUtils.get_cache_dir()
        assert result is not None

    def test_accepts_var_tmp_path(self, monkeypatch):
        monkeypatch.setenv("MVM_CACHE_DIR", "/var/tmp/mvm-test-cache")
        result = CacheUtils.get_cache_dir()
        assert str(result) == "/var/tmp/mvm-test-cache"

    def test_resolve_dir_existing(self, tmp_path):
        existing = tmp_path / "already_exists"
        existing.mkdir()
        result = CacheUtils.resolve_dir(existing)
        assert result == existing
        assert result.exists()

    def test_get_auditlog_path(self, monkeypatch, tmp_path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_audit_log_path()
        assert result == tmp_path / "audit.log"

    def test_get_mvm_db_path(self, monkeypatch, tmp_path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_mvm_db_path()
        assert result == tmp_path / "mvmdb.db"

    def test_get_images_dir_creates(self, monkeypatch, tmp_path):
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
        result = CacheUtils.get_images_dir()
        assert result == tmp_path / "images"
        assert result.exists()


class TestCommonUtilsExtended:
    """Extended tests for CommonUtils uncovered paths."""

    def test_sanitize_for_log_none(self):
        with pytest.raises(TypeError):
            CommonUtils.sanitize_for_log(None)

    def test_sanitize_for_log_zero_width(self):
        result = CommonUtils.sanitize_for_log("test\u200b\u200cstring")
        assert result == "teststring"

    def test_sanitize_for_log_full_control_set(self):
        result = CommonUtils.sanitize_for_log("a\x00b\x01c\x7fd")
        assert result == "abcd"

    def test_human_readable_datetime_with_timezone(self):
        result = CommonUtils.human_readable_datetime(
            "2026-04-01T12:30:00+05:30"
        )
        assert result == "2026/04/01 12:30:00"

    def test_human_readable_datetime_utc_zulu(self):
        result = CommonUtils.human_readable_datetime("2026-04-01T12:30:00Z")
        assert result == "2026/04/01 12:30:00"

    def test_human_readable_datetime_attribute_error(self):
        result = CommonUtils.human_readable_datetime(12345)
        assert result == "12345"

    def test_format_bytes_zero(self):
        assert CommonUtils.format_bytes_human_readable(0) == "0 B"

    def test_format_bytes_exact_kib(self):
        assert CommonUtils.format_bytes_human_readable(1024) == "1.0 KiB"

    def test_format_bytes_exact_mib(self):
        assert CommonUtils.format_bytes_human_readable(1048576) == "1.0 MiB"

    def test_format_bytes_exact_gib(self):
        assert CommonUtils.format_bytes_human_readable(1073741824) == "1.0 GiB"

    def test_format_bytes_exact_tib(self):
        assert (
            CommonUtils.format_bytes_human_readable(1099511627776)
            == "1024.0 TiB"
        )

    def test_safe_int_exact(self):
        assert CommonUtils.safe_int(42) == 42

    def test_safe_int_float(self):
        assert CommonUtils.safe_int(3.14) == 3

    def test_safe_int_float_negative(self):
        assert CommonUtils.safe_int(-3.9) == -3

    def test_safe_int_str_valid(self):
        assert CommonUtils.safe_int("123") == 123

    def test_safe_int_str_invalid(self):
        assert CommonUtils.safe_int("not-a-number") == 0

    def test_safe_int_none(self):
        assert CommonUtils.safe_int(None) == 0

    def test_safe_int_list(self):
        assert CommonUtils.safe_int([1, 2, 3]) == 0

    def test_safe_int_custom_default(self):
        assert CommonUtils.safe_int(None, default=-1) == -1

    def test_validate_entity_name_exceeds_max_length(self):
        long_name = "a" * 64
        with pytest.raises(MVMError, match="exceeds maximum length"):
            CommonUtils.validate_entity_name(long_name, "VM")

    def test_validate_entity_name_invalid_pattern(self):
        with pytest.raises(MVMError, match="must match"):
            CommonUtils.validate_entity_name("UPPERCASE", "VM")

    def test_get_combined_marker_default_missing(self):
        assert CommonUtils._get_combined_marker(True, True) == "*X "

    def test_get_combined_marker_missing_only(self):
        assert CommonUtils._get_combined_marker(False, True) == " X "

    def test_get_combined_marker_default_only(self):
        assert CommonUtils._get_combined_marker(True, False) == "*  "

    def test_get_combined_marker_normal(self):
        assert CommonUtils._get_combined_marker(False, False) == "   "

    def test_coerce_bool_true(self):
        assert CommonUtils.coerce("true", bool) is True

    def test_coerce_bool_false(self):
        assert CommonUtils.coerce("false", bool) is False

    def test_coerce_bool_on(self):
        assert CommonUtils.coerce("on", bool) is True

    def test_coerce_int(self):
        assert CommonUtils.coerce("42", int) == 42

    def test_coerce_float(self):
        assert CommonUtils.coerce("3.14", float) == 3.14

    def test_coerce_dict(self):
        result = CommonUtils.coerce('{"a": 1}', dict)
        assert result == {"a": 1}

    def test_coerce_type_error(self):
        with pytest.raises(TypeError):
            CommonUtils.coerce(42, dict)

    def test_coerce_same_type_passthrough(self):
        assert CommonUtils.coerce("hello", str) == "hello"
