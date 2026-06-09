"""Tests for HostPrivilegeHelper — privilege checks for host operations."""

from __future__ import annotations

import grp
import pwd
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.exceptions import PrivilegeError

# Save the original staticmethod descriptor before the conftest autouse
# fixtures have a chance to monkeypatch it with a MagicMock.  We need to
# test the real implementation, not a mock that always returns None.
_ORIGINAL_CHECK_PRIVILEGES = HostPrivilegeHelper.__dict__["check_privileges"]


@pytest.fixture(autouse=True)
def _unmock_privilege_checks() -> None:
    """Undo the conftest autouse mock that patches HostPrivilegeHelper.check_privileges.

    Restores the original staticmethod descriptor so tests exercise the
    real privilege-checking logic.
    """
    import mvmctl.core.host._helper as _helper_mod

    setattr(
        _helper_mod.HostPrivilegeHelper,
        "check_privileges",
        _ORIGINAL_CHECK_PRIVILEGES,
    )


class TestHostPrivilegeHelper:
    """Test suite for HostPrivilegeHelper.check_privileges."""

    # ------------------------------------------------------------------
    # Binary checks
    # ------------------------------------------------------------------

    def test_binary_not_found(self) -> None:
        """check_privileges should raise PrivilegeError when binary is not found."""
        with (
            patch("shutil.which", return_value=None),
            patch("pathlib.Path.exists", return_value=False),
            patch("os.getuid", return_value=1000),
            patch("grp.getgrnam", side_effect=KeyError("mvm")),
        ):
            with pytest.raises(PrivilegeError):
                HostPrivilegeHelper.check_privileges("/usr/sbin/nonexistent")

    def test_binary_found_via_which(self) -> None:
        """check_privileges should pass when binary is found via shutil.which."""
        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=0),
        ):
            HostPrivilegeHelper.check_privileges(
                "/usr/sbin/ip"
            )  # Should not raise

    def test_binary_found_via_path_exists(self) -> None:
        """check_privileges should pass when binary path exists."""
        with (
            patch("shutil.which", return_value=None),
            patch("pathlib.Path.exists", return_value=True),
            patch("os.getuid", return_value=0),
        ):
            HostPrivilegeHelper.check_privileges("/usr/sbin/ip")

    # ------------------------------------------------------------------
    # Root user
    # ------------------------------------------------------------------

    def test_root_user_passes(self) -> None:
        """check_privileges should pass when running as root (uid=0)."""
        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=0),
        ):
            HostPrivilegeHelper.check_privileges(
                "/usr/sbin/ip"
            )  # Should not raise

    # ------------------------------------------------------------------
    # Group does not exist
    # ------------------------------------------------------------------

    def test_group_not_exists(self) -> None:
        """check_privileges should raise PrivilegeError when mvm group is missing."""
        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("grp.getgrnam", side_effect=KeyError("mvm")),
        ):
            with pytest.raises(PrivilegeError) as excinfo:
                HostPrivilegeHelper.check_privileges("/usr/sbin/ip")
            assert excinfo.value.details is not None

    def test_group_not_exists_suggestions(self) -> None:
        """check_privileges should include helpful suggestions when group is missing."""
        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("grp.getgrnam", side_effect=KeyError("mvm")),
        ):
            with pytest.raises(PrivilegeError) as excinfo:
                HostPrivilegeHelper.check_privileges("/usr/sbin/ip")
            details = excinfo.value.details
            assert details is not None
            assert "suggestions" in details
            assert any(
                "sudo mvm host init" in s for s in details["suggestions"]
            )

    # ------------------------------------------------------------------
    # User not in group (supplementary)
    # ------------------------------------------------------------------

    def test_user_not_in_supplementary_group(self) -> None:
        """check_privileges should raise when user is not a supplementary group member."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["otheruser"]
        mock_group.gr_gid = 1001

        mock_pwd = MagicMock()
        mock_pwd.pw_name = "testuser"
        mock_pwd.pw_gid = 1000

        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("os.getgroups", return_value=[1000]),
            patch("os.getgid", return_value=1000),
            patch("os.getegid", return_value=1000),
            patch.object(grp, "getgrnam", return_value=mock_group),
            patch.object(pwd, "getpwuid", return_value=mock_pwd),
        ):
            with pytest.raises(
                PrivilegeError, match="not in the|Elevated privileges"
            ):
                HostPrivilegeHelper.check_privileges("/usr/sbin/ip")

    # ------------------------------------------------------------------
    # User in group via primary GID
    # ------------------------------------------------------------------

    def test_user_in_group_via_primary(self) -> None:
        """check_privileges should pass when user's primary GID matches group."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["otheruser"]  # Not supplementary
        mock_group.gr_gid = 1001

        mock_pwd = MagicMock()
        mock_pwd.pw_name = "testuser"
        mock_pwd.pw_gid = 1001  # Primary group matches mvm group gid

        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("os.getgroups", return_value=[1000]),
            patch("os.getgid", return_value=1001),
            patch("os.getegid", return_value=1001),
            patch.object(grp, "getgrnam", return_value=mock_group),
            patch.object(pwd, "getpwuid", return_value=mock_pwd),
        ):
            HostPrivilegeHelper.check_privileges(
                "/usr/sbin/ip"
            )  # Should not raise

    # ------------------------------------------------------------------
    # User in group but process doesn't have the credentials
    # ------------------------------------------------------------------

    def test_user_in_group_but_process_missing_creds(self) -> None:
        """check_privileges should raise when user is in group but process lacks credentials."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["testuser"]
        mock_group.gr_gid = 1001

        mock_pwd = MagicMock()
        mock_pwd.pw_name = "testuser"
        mock_pwd.pw_gid = 1000

        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("os.getgroups", return_value=[1000]),
            patch("os.getgid", return_value=1000),
            patch("os.getegid", return_value=1000),
            patch.object(grp, "getgrnam", return_value=mock_group),
            patch.object(pwd, "getpwuid", return_value=mock_pwd),
        ):
            with pytest.raises(
                PrivilegeError, match="Elevated privileges required"
            ):
                HostPrivilegeHelper.check_privileges("/usr/sbin/ip")

    # ------------------------------------------------------------------
    # Full privilege check passing
    # ------------------------------------------------------------------

    def test_user_in_supplementary_group_passes(self) -> None:
        """check_privileges should pass when user is supplementary member and process has creds."""
        mock_group = MagicMock()
        mock_group.gr_mem = ["testuser"]
        mock_group.gr_gid = 1001

        mock_pwd = MagicMock()
        mock_pwd.pw_name = "testuser"
        mock_pwd.pw_gid = 1000

        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("os.getgroups", return_value=[1000, 1001]),
            patch("os.getgid", return_value=1000),
            patch("os.getegid", return_value=1000),
            patch.object(grp, "getgrnam", return_value=mock_group),
            patch.object(pwd, "getpwuid", return_value=mock_pwd),
        ):
            HostPrivilegeHelper.check_privileges(
                "/usr/sbin/ip"
            )  # Should not raise

    # ------------------------------------------------------------------
    # Operation description
    # ------------------------------------------------------------------

    def test_operation_description_in_error(self) -> None:
        """check_privileges should include operation description in error message."""
        with (
            patch("shutil.which", return_value="/usr/sbin/ip"),
            patch("os.getuid", return_value=1000),
            patch("grp.getgrnam", side_effect=KeyError("mvm")),
        ):
            with pytest.raises(PrivilegeError) as excinfo:
                HostPrivilegeHelper.check_privileges(
                    "/usr/sbin/ip", "initialize host"
                )
            assert "for: initialize host" in str(excinfo.value)

    # ------------------------------------------------------------------
    # Missing binary details in PrivilegeError
    # ------------------------------------------------------------------

    def test_missing_binary_in_details(self) -> None:
        """check_privileges should include missing binary in details."""
        with (
            patch("shutil.which", return_value=None),
            patch("pathlib.Path.exists", return_value=False),
            patch("os.getuid", return_value=1000),
            patch("grp.getgrnam", side_effect=KeyError("mvm")),
        ):
            with pytest.raises(PrivilegeError) as excinfo:
                HostPrivilegeHelper.check_privileges(
                    "/usr/sbin/nonexistent", "test op"
                )
            details = excinfo.value.details
            assert details is not None
            assert "missing_binaries" in details
            assert "/usr/sbin/nonexistent" in details["missing_binaries"]
