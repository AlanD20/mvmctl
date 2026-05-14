"""Tests for the Provisioner abstraction layer and backend selection."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._shared._provisioner._backend import (
    _GuestfsBackend,
    _LoopMountBackend,
)
from mvmctl.core.vm._provisioner import VMProvisioner
from mvmctl.models.provisioner import ProvisionerType


class TestLoopMountBackend:
    """Tests for _LoopMountBackend — thin wrapper around LoopMountProvisioner."""

    def test_init_delegates_to_loopmount_provisioner(self):
        """_LoopMountBackend creates a LoopMountProvisioner with rootfs and fs_type."""
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        assert backend._lp is not None
        assert backend._lp._rootfs_path == Path("/fake/rootfs.ext4")
        assert backend._lp._fs_type == "ext4"

    def test_resize(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.resize = MagicMock()
        backend.resize(8_589_934_592)
        backend._lp.resize.assert_called_once_with(8_589_934_592)

    def test_set_hostname(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.set_hostname = MagicMock()
        backend.set_hostname("my-vm")
        backend._lp.set_hostname.assert_called_once_with("my-vm")

    def test_inject_dns(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.inject_dns = MagicMock()
        backend.inject_dns(dns_server="10.0.0.1")
        backend._lp.inject_dns.assert_called_once_with(dns_server="10.0.0.1")

    def test_setup_ssh(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.setup_ssh = MagicMock()
        backend.setup_ssh("myuser", ["ssh-ed25519 key..."])
        backend._lp.setup_ssh.assert_called_once_with(
            "myuser", ["ssh-ed25519 key..."]
        )

    def test_disable_cloud_init(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.disable_cloud_init = MagicMock()
        backend.disable_cloud_init()
        backend._lp.disable_cloud_init.assert_called_once()

    def test_inject_cloud_init(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.inject_cloud_init = MagicMock()
        backend.inject_cloud_init(Path("/fake/cloud-init"))
        backend._lp.inject_cloud_init.assert_called_once_with(
            Path("/fake/cloud-init")
        )

    def test_run(self):
        backend = _LoopMountBackend(Path("/fake/rootfs.ext4"), "ext4")
        backend._lp.run = MagicMock()
        backend.run()
        backend._lp.run.assert_called_once()


class TestGuestfsBackend:
    """Tests for _GuestfsBackend — thin wrapper around GuestfsProvisioner."""

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_init_delegates_to_guestfs(self, MockGuestfsProvisioner):
        """_GuestfsBackend creates a GuestfsProvisioner with correct params."""
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(
            Path("/fake/rootfs.ext4"),
            root_uid=0,
            root_gid=0,
            user_uid=1000,
            user_gid=1000,
        )
        MockGuestfsProvisioner.assert_called_once_with(
            Path("/fake/rootfs.ext4"),
            readonly=False,
            root_uid=0,
            root_gid=0,
            user_uid=1000,
            user_gid=1000,
        )
        assert backend._gp is mock_instance

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_resize(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.resize(8_589_934_592)
        mock_instance.resize.assert_called_once_with(8_589_934_592)

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_set_hostname(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.set_hostname("my-vm")
        mock_instance.set_hostname.assert_called_once_with("my-vm")

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_inject_dns(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.inject_dns(dns_server="10.0.0.1")
        mock_instance.inject_dns.assert_called_once_with(dns_server="10.0.0.1")

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_setup_ssh(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.setup_ssh("myuser", ["key"])
        mock_instance.setup_ssh.assert_called_once_with("myuser", ["key"])

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_disable_cloud_init(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.disable_cloud_init()
        mock_instance.disable_cloud_init.assert_called_once()

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_inject_cloud_init(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.inject_cloud_init(Path("/fake/ci"))
        mock_instance.inject_cloud_init.assert_called_once_with(
            Path("/fake/ci")
        )

    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_run(self, MockGuestfsProvisioner):
        mock_instance = MagicMock()
        MockGuestfsProvisioner.return_value = mock_instance
        backend = _GuestfsBackend(Path("/fake/rootfs.ext4"))
        backend.run()
        mock_instance.run.assert_called_once()


class TestProvisioner:
    """Tests for VMProvisioner — backend selection and dispatch."""

    def test_loop_mount_selection(self):
        """VMProvisioner selects _LoopMountBackend when type is LOOP_MOUNT."""
        p = VMProvisioner(
            Path("/fake/rootfs.ext4"),
            provisioner_type=ProvisionerType.LOOP_MOUNT,
            fs_type="ext4",
        )
        assert isinstance(p._backend, _LoopMountBackend)

    @patch(
        "mvmctl.core._shared._provisioner._backend.ProvisionerBackend."
        "_ensure_guestfs_appliance"
    )
    @patch("mvmctl.core._shared._guestfs.GuestfsProvisioner")
    def test_guestfs_backend_selection(self, _MockGP, _MockAppliance):
        """VMProvisioner selects _GuestfsBackend when type is GUESTFS."""
        p = VMProvisioner(
            Path("/fake/rootfs.ext4"),
            provisioner_type=ProvisionerType.GUESTFS,
            fs_type="ext4",
        )
        assert isinstance(p._backend, _GuestfsBackend)
        _MockAppliance.assert_called_once()

    def test_unknown_type_raises_value_error(self):
        """VMProvisioner raises ValueError for unknown provisioner type."""
        with pytest.raises(ValueError, match="Unknown provisioner type"):
            VMProvisioner(
                Path("/fake/rootfs.ext4"),
                provisioner_type="unknown",  # type: ignore[arg-type]
                fs_type="ext4",
            )

    def test_all_builder_methods_delegate(self):
        """All builder methods on VMProvisioner delegate to the backend."""
        p = VMProvisioner(
            Path("/fake/rootfs.ext4"),
            provisioner_type=ProvisionerType.LOOP_MOUNT,
            fs_type="ext4",
        )
        backend = p._backend
        backend.resize = MagicMock()
        backend.set_hostname = MagicMock()
        backend.inject_dns = MagicMock()
        backend.setup_ssh = MagicMock()
        backend.disable_cloud_init = MagicMock()
        backend.inject_cloud_init = MagicMock()
        backend.run = MagicMock()

        p.resize(8_589_934_592)
        p.set_hostname("test")
        p.inject_dns(dns_server="10.0.0.1")
        p.setup_ssh("user", ["key"])
        p.disable_cloud_init()
        p.inject_cloud_init(Path("/ci"))
        p.run()

        backend.resize.assert_called_once_with(8_589_934_592)
        backend.set_hostname.assert_called_once_with("test")
        backend.inject_dns.assert_called_once_with(dns_server="10.0.0.1")
        backend.setup_ssh.assert_called_once_with("user", ["key"])
        backend.disable_cloud_init.assert_called_once()
        backend.inject_cloud_init.assert_called_once_with(Path("/ci"))
        backend.run.assert_called_once()
