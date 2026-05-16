"""Tests for LoopMountProvisioner — builder methods and run() serialization."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

from mvmctl.core._shared._loopmount._provisioner import LoopMountProvisioner
from mvmctl.core._shared._provisioner._content import (
    ChrootOp,
    CopyDirOp,
    FileOp,
    ResizeOp,
)


class TestLoopMountProvisionerBuilder:
    """Tests that builder methods accumulate operations."""

    def test_init(self):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        assert lp._rootfs_path == Path("/fake/rootfs.ext4")
        assert lp._fs_type == "ext4"
        assert lp._ops == []

    def test_resize_queues_op(self):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.resize(8_589_934_592)
        assert len(lp._ops) == 1
        op = lp._ops[0]
        assert isinstance(op, ResizeOp)
        assert op.action == "grow"
        assert op.bytes == 8_589_934_592

    def test_set_hostname_queues_ops(self):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.set_hostname("my-vm")
        assert len(lp._ops) == 2
        assert isinstance(lp._ops[0], FileOp)
        assert lp._ops[0].path == "/etc/hostname"
        assert lp._ops[0].data == b"my-vm"
        assert isinstance(lp._ops[1], FileOp)
        assert lp._ops[1].path == "/etc/hosts"
        assert b"127.0.1.1" in lp._ops[1].data

    def test_inject_dns_queues_op(self):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.inject_dns(dns_server="10.0.0.1")
        assert len(lp._ops) == 1
        op = lp._ops[0]
        assert isinstance(op, FileOp)
        assert op.path == "/etc/resolv.conf"
        assert b"10.0.0.1" in op.data

    def test_setup_ssh_queues_ops(self):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.setup_ssh("myuser", ["ssh-ed25519 AAA..."])
        assert len(lp._ops) > 0
        # Should have FileOps for authorized_keys, sshd_config, first-boot scripts
        file_paths = [op.path for op in lp._ops if isinstance(op, FileOp)]
        assert any(".ssh/authorized_keys" in p for p in file_paths)
        # sshd_config, first-boot-ssh-installer, and ssh-keygen are added
        # at image build time (build_deblob_ops), not at VM creation time via setup_ssh
        # Should have ChrootOps for user creation
        chroot_cmds = [op.command for op in lp._ops if isinstance(op, ChrootOp)]
        assert any("useradd" in c for c in chroot_cmds)

    def test_setup_ssh_root_user(self):
        """Root user should NOT have useradd/sudoers ChrootOps."""
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.setup_ssh("root", ["ssh-ed25519 AAA..."])
        chroot_cmds = [op.command for op in lp._ops if isinstance(op, ChrootOp)]
        assert not any("useradd" in c for c in chroot_cmds)
        # ssh-keygen is added at image build time, not via setup_ssh

    def test_setup_ssh_empty_keys(self):
        """No SSH pubkeys should queue NO ops."""
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.setup_ssh("myuser", [])
        assert lp._ops == []

    def test_disable_cloud_init_queues_ops(self):
        """Cloud-init disable ops are added at image build time, not at VM creation."""
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.disable_cloud_init()
        # Cloud-init disable is handled during image import (build_deblob_ops),
        # not at VM creation time. LoopMountProvisioner.disable_cloud_init is a no-op.
        assert len(lp._ops) == 0

    def test_inject_cloud_init_with_existing_dir(self, tmp_path):
        ci_dir = tmp_path / "cloud-init"
        ci_dir.mkdir()
        (ci_dir / "meta-data").write_text("instance-id: test\n")
        (ci_dir / "user-data").write_text("#cloud-config\n")

        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.inject_cloud_init(ci_dir)
        assert len(lp._ops) == 1
        op = lp._ops[0]
        assert isinstance(op, CopyDirOp)
        assert str(ci_dir) in op.src
        assert "nocloud-net" in op.dst

    def test_inject_cloud_init_nonexistent_dir(self, tmp_path):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.inject_cloud_init(tmp_path / "nonexistent")
        assert lp._ops == []

    def test_multiple_operations_accumulate(self):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.resize(8_589_934_592)
        lp.set_hostname("vm1")
        lp.inject_dns(dns_server="10.0.0.1")
        lp.setup_ssh("user", ["key"])
        lp.disable_cloud_init()
        # All 5 methods should have added ops
        assert len(lp._ops) > 5


class TestLoopMountProvisionerRun:
    """Tests for LoopMountProvisioner.run() — serialization and execution."""

    @patch("mvmctl.core._shared._loopmount._provisioner.LoopMountManager")
    def test_run_empty_ops(self, MockManager):
        """run() with no ops should still call execute with minimal args."""
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.run()
        MockManager.execute.assert_called_once_with(
            image_path="/fake/rootfs.ext4",
            fs_type="ext4",
            files=None,
            commands=None,
            copy_dirs=None,
            resize=None,
        )

    @patch("mvmctl.core._shared._loopmount._provisioner.LoopMountManager")
    def test_run_with_file_ops(self, MockManager):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.set_hostname("test-vm")
        lp.run()

        call_kwargs = MockManager.execute.call_args[1]
        assert call_kwargs["image_path"] == "/fake/rootfs.ext4"
        assert call_kwargs["fs_type"] == "ext4"
        assert call_kwargs["files"] is not None
        assert len(call_kwargs["files"]) == 2  # /etc/hostname + /etc/hosts
        # Verify base64 encoding
        file0 = call_kwargs["files"][0]
        import base64

        assert file0["path"] == "/etc/hostname"
        assert base64.b64decode(file0["data"]) == b"test-vm"

    @patch("mvmctl.core._shared._loopmount._provisioner.LoopMountManager")
    def test_run_with_chroot_ops(self, MockManager):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.setup_ssh("myuser", ["ssh-ed25519 key"])
        lp.run()

        call_kwargs = MockManager.execute.call_args[1]
        assert call_kwargs["commands"] is not None
        assert any("useradd" in c for c in call_kwargs["commands"])
        # ssh-keygen is added at image build time (build_deblob_ops), not at VM creation

    @patch("mvmctl.core._shared._loopmount._provisioner.LoopMountManager")
    def test_run_with_copy_dir_op(self, MockManager, tmp_path):
        ci_dir = tmp_path / "cloud-init"
        ci_dir.mkdir()
        (ci_dir / "meta-data").write_text("id: test")

        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.inject_cloud_init(ci_dir)
        lp.run()

        call_kwargs = MockManager.execute.call_args[1]
        assert call_kwargs["copy_dirs"] is not None
        assert len(call_kwargs["copy_dirs"]) == 1
        assert call_kwargs["copy_dirs"][0]["src"] == str(ci_dir)
        assert "nocloud-net" in call_kwargs["copy_dirs"][0]["dst"]

    @patch("mvmctl.core._shared._loopmount._provisioner.LoopMountManager")
    def test_run_with_resize_op(self, MockManager):
        lp = LoopMountProvisioner(Path("/fake/rootfs.ext4"), "ext4")
        lp.resize(16_000_000_000)
        lp.run()

        call_kwargs = MockManager.execute.call_args[1]
        assert call_kwargs["resize"] is not None
        assert call_kwargs["resize"]["action"] == "grow"
        assert call_kwargs["resize"]["bytes"] == 16_000_000_000
