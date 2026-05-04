"""Test the SSH config generation in GuestfsProvisioner."""
from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

from mvmctl.core._shared._guestfs._provisioner import GuestfsProvisioner


def test_configure_ssh_keys_no_protocol_2() -> None:
    """The provisioner's sshd config must NOT contain deprecated Protocol 2."""
    handle = MagicMock()
    handle.exists.return_value = True

    gp = GuestfsProvisioner(Path("/fake/rootfs.ext4"), readonly=False)
    gp._user = "root"  # set user before calling ssh config

    # This writes /etc/ssh/sshd_config.d/mvm.conf through the handle
    gp.configure_ssh_keys(handle)

    # Get what was written to the config file
    written = handle.write.call_args
    assert written is not None, "Expected handle.write to be called"
    filepath, content = written[0]

    assert filepath == "/etc/ssh/sshd_config.d/mvm.conf"
    assert "Protocol 2" not in content, (
        "Protocol 2 was removed in OpenSSH 8.8 and must not appear in config"
    )
    assert "PubkeyAuthentication yes" in content
    assert "PasswordAuthentication no" in content
    assert "UsePAM yes" in content
    assert "PermitRootLogin prohibit-password" in content


def test_configure_ssh_keys_skips_when_no_sshd_config() -> None:
    """Should skip config if sshd_config doesn't exist."""
    handle = MagicMock()
    handle.exists.return_value = False  # sshd_config doesn't exist

    gp = GuestfsProvisioner(Path("/fake/rootfs.ext4"), readonly=False)
    gp._user = "root"
    gp.configure_ssh_keys(handle)

    # Should NOT write mvm.conf
    for call_args in handle.write.call_args_list:
        if "mvm.conf" in str(call_args):
            raise AssertionError("mvm.conf should not be written when sshd_config is missing")


def test_configure_ssh_keys_allow_users_for_non_root() -> None:
    """Should write AllowUsers for non-root users."""
    handle = MagicMock()
    handle.exists.return_value = True

    gp = GuestfsProvisioner(Path("/fake/rootfs.ext4"), readonly=False)
    gp._user = "testuser"

    gp.configure_ssh_keys(handle)

    written = handle.write.call_args
    assert written is not None
    _, content = written[0]

    assert "AllowUsers testuser" in content
    assert "PermitRootLogin" not in content
