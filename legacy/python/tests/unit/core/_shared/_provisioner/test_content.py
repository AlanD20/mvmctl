"""Tests for ProvisionerContent raw content and builder methods."""

from __future__ import annotations

from pathlib import Path

from mvmctl.core._shared._provisioner._content import (
    ChrootOp,
    CopyDirOp,
    FileOp,
    Operation,
    ProvisionerContent,
    ResizeOp,
)

# ═══════════════════════════════════════════════════════════════════════
# 1. Raw content methods
# ═══════════════════════════════════════════════════════════════════════


class TestSshdConfig:
    """Tests for ProvisionerContent.sshd_config()."""

    def test_non_root_user_contains_allow_users(self) -> None:
        """Non-root user should produce AllowUsers in output."""
        result = ProvisionerContent.sshd_config("myuser")
        assert "AllowUsers myuser" in result
        assert "PermitRootLogin" not in result

    def test_root_user_contains_permit_root_login(self) -> None:
        """Root user should produce PermitRootLogin prohibit-password."""
        result = ProvisionerContent.sshd_config("root")
        assert "PermitRootLogin prohibit-password" in result
        assert "AllowUsers" not in result

    def test_ends_with_newline(self) -> None:
        """SSHD config should end with a trailing newline."""
        result = ProvisionerContent.sshd_config("myuser")
        assert result.endswith("\n")

    def test_contains_required_directives(self) -> None:
        """Common SSH directives are present regardless of user."""
        result = ProvisionerContent.sshd_config("testuser")
        assert "PubkeyAuthentication yes" in result
        assert "AuthorizedKeysFile .ssh/authorized_keys" in result
        assert "PasswordAuthentication no" in result
        assert "PermitEmptyPasswords no" in result
        assert "UsePAM yes" in result


class TestFirstBootInstaller:
    """Tests for ProvisionerContent.first_boot_installer()."""

    def test_starts_with_shebang(self) -> None:
        """First-boot installer should start with #!/bin/bash."""
        result = ProvisionerContent.first_boot_installer()
        assert result.startswith("#!/bin/bash")

    def test_is_non_empty_string(self) -> None:
        """First-boot installer should be a non-empty string."""
        result = ProvisionerContent.first_boot_installer()
        assert isinstance(result, str)
        assert len(result) > 0

    def test_contains_sshd_enable_logic(self) -> None:
        """First-boot installer should contain systemctl enable logic."""
        result = ProvisionerContent.first_boot_installer()
        assert "systemctl enable" in result
        assert "sshd" in result or "ssh" in result


class TestFirstBootService:
    """Tests for ProvisionerContent.first_boot_service()."""

    def test_is_valid_systemd_unit(self) -> None:
        """First-boot service should be a valid systemd unit string."""
        result = ProvisionerContent.first_boot_service()
        assert result.startswith("[Unit]")
        assert "[Service]" in result
        assert "[Install]" in result

    def test_contains_required_fields(self) -> None:
        """Systemd unit should contain essential metadata."""
        result = ProvisionerContent.first_boot_service()
        assert "Description=First-boot SSH installer" in result
        assert "After=network.target" in result
        assert "ConditionFirstBoot=yes" in result
        assert "WantedBy=multi-user.target" in result
        assert "first-boot-ssh-installer.sh" in result


class TestHosts:
    """Tests for ProvisionerContent.hosts()."""

    def test_contains_127_0_1_1_entry(self) -> None:
        """Hosts should contain a 127.0.1.1 entry with the given hostname."""
        result = ProvisionerContent.hosts("myvm")
        assert "127.0.1.1\tmyvm" in result

    def test_contains_standard_entries(self) -> None:
        """Hosts should contain standard localhost entries."""
        result = ProvisionerContent.hosts("testvm")
        assert "127.0.0.1\tlocalhost" in result
        assert "::1\tlocalhost" in result

    def test_ends_with_newline(self) -> None:
        """Hosts file should end with a trailing newline."""
        result = ProvisionerContent.hosts("testvm")
        assert result.endswith("\n")


class TestCloudInitDisableConstants:
    """Tests for cloud-init disable byte constants."""

    def test_cloud_init_disable_datasource_is_bytes(self) -> None:
        """CLOUD_INIT_DISABLE_DATASOURCE should be bytes."""
        val = ProvisionerContent.CLOUD_INIT_DISABLE_DATASOURCE
        assert isinstance(val, bytes)
        assert len(val) > 0

    def test_cloud_init_disabled_marker_is_bytes(self) -> None:
        """CLOUD_INIT_DISABLED_MARKER should be bytes."""
        val = ProvisionerContent.CLOUD_INIT_DISABLED_MARKER
        assert isinstance(val, bytes)
        assert len(val) > 0

    def test_snapd_override_is_bytes(self) -> None:
        """SNAPD_OVERRIDE should be bytes."""
        val = ProvisionerContent.SNAPD_OVERRIDE
        assert isinstance(val, bytes)
        assert len(val) > 0

    def test_networkd_wait_override_is_bytes(self) -> None:
        """NETWORKD_WAIT_OVERRIDE should be bytes."""
        val = ProvisionerContent.NETWORKD_WAIT_OVERRIDE
        assert isinstance(val, bytes)
        assert len(val) > 0


# ═══════════════════════════════════════════════════════════════════════
# 2. Builder methods
# ═══════════════════════════════════════════════════════════════════════


class TestBuildHostnameOps:
    """Tests for ProvisionerContent.build_hostname_ops()."""

    def test_returns_two_file_ops(self) -> None:
        """build_hostname_ops should return exactly two FileOps."""
        ops = ProvisionerContent.build_hostname_ops("myvm")
        assert len(ops) == 2
        assert all(isinstance(op, FileOp) for op in ops)

    def test_first_op_is_etc_hostname(self) -> None:
        """First op should write /etc/hostname."""
        ops = ProvisionerContent.build_hostname_ops("myvm")
        assert ops[0].path == "/etc/hostname"
        assert ops[0].data == b"myvm"
        assert ops[0].mode == 0o644

    def test_second_op_is_etc_hosts(self) -> None:
        """Second op should write /etc/hosts with hostname entry."""
        ops = ProvisionerContent.build_hostname_ops("myvm")
        assert ops[1].path == "/etc/hosts"
        assert b"127.0.1.1\tmyvm" in ops[1].data
        assert ops[1].mode == 0o644


class TestBuildDnsOps:
    """Tests for ProvisionerContent.build_dns_ops()."""

    def test_returns_one_file_op(self) -> None:
        """build_dns_ops should return exactly one FileOp."""
        ops = ProvisionerContent.build_dns_ops("8.8.8.8")
        assert len(ops) == 1
        assert isinstance(ops[0], FileOp)

    def test_op_is_etc_resolv_conf(self) -> None:
        """The FileOp should write /etc/resolv.conf with nameserver."""
        ops = ProvisionerContent.build_dns_ops("1.1.1.1")
        op = ops[0]
        assert op.path == "/etc/resolv.conf"
        assert op.data == b"nameserver 1.1.1.1\n"
        assert op.mode == 0o644
        assert op.uid == 0
        assert op.gid == 0


class TestBuildSshOps:
    """Tests for ProvisionerContent.build_ssh_ops()."""

    def test_empty_pubkeys_returns_empty_list(self) -> None:
        """When ssh_pubkeys is empty, should return empty list."""
        ops = ProvisionerContent.build_ssh_ops("testuser", [])
        assert ops == []

    def test_non_root_user_includes_useradd_chroot_ops(self) -> None:
        """Non-root user should include useradd, sudoers, and ssh-keygen ChrootOps."""
        ops = ProvisionerContent.build_ssh_ops(
            "testuser", ["ssh-ed25519 AAA... key"]
        )
        # Filter only ChrootOps
        chroot_ops = [op for op in ops if isinstance(op, ChrootOp)]
        chroot_commands = [op.command for op in chroot_ops]

        assert any("useradd -m testuser" in cmd for cmd in chroot_commands)
        assert any("ALL=(ALL) NOPASSWD: ALL" in cmd for cmd in chroot_commands)
        assert any(
            "chmod 440 /etc/sudoers.d/testuser" in cmd
            for cmd in chroot_commands
        )
        # ssh-keygen is added at image build time (build_deblob_ops), not in
        # build_ssh_ops

    def test_root_user_omits_useradd_and_sudoers(self) -> None:
        """Root user should NOT include useradd or sudoers ChrootOps."""
        ops = ProvisionerContent.build_ssh_ops(
            "root", ["ssh-ed25519 AAA... root-key"]
        )
        chroot_ops = [op for op in ops if isinstance(op, ChrootOp)]
        chroot_commands = [op.command for op in chroot_ops]

        assert not any("useradd" in cmd for cmd in chroot_commands)
        assert not any("sudoers" in cmd for cmd in chroot_commands)
        # ssh-keygen is added at image build time (build_deblob_ops), not in
        # build_ssh_ops

    def test_includes_authorized_keys_file_op(self) -> None:
        """Should include FileOp for authorized_keys."""
        ops = ProvisionerContent.build_ssh_ops(
            "testuser", ["ssh-ed25519 AAA... key1"]
        )
        file_ops = [op for op in ops if isinstance(op, FileOp)]
        auth_file = next(
            (op for op in file_ops if op.path.endswith("authorized_keys")), None
        )
        assert auth_file is not None
        assert auth_file.mode == 0o600

    # sshd_config.d/mvm.conf is added at image build time (build_deblob_ops),
    # not in build_ssh_ops

    # First-boot scripts are added at image build time (build_deblob_ops),
    # not in build_ssh_ops

    def test_root_user_authorized_keys_in_root_home(self) -> None:
        """Root user's authorized_keys should be in /root/.ssh/."""
        ops = ProvisionerContent.build_ssh_ops(
            "root", ["ssh-ed25519 AAA... root-key"]
        )
        file_ops = [op for op in ops if isinstance(op, FileOp)]
        auth_file = next(
            (op for op in file_ops if op.path.endswith("authorized_keys")), None
        )
        assert auth_file is not None
        assert auth_file.path == "/root/.ssh/authorized_keys"


class TestBuildCloudInitDisableOps:
    """Tests for ProvisionerContent.build_cloud_init_disable_ops()."""

    def test_includes_cloud_cfg_d_file_op(self) -> None:
        """Should include FileOp for cloud.cfg.d/99-disable-datasources.cfg."""
        ops = ProvisionerContent.build_cloud_init_disable_ops()
        file_ops = [op for op in ops if isinstance(op, FileOp)]
        assert any("99-disable-datasources.cfg" in op.path for op in file_ops)

    def test_includes_cloud_init_disabled_file_op(self) -> None:
        """Should include FileOp for cloud-init.disabled marker."""
        ops = ProvisionerContent.build_cloud_init_disable_ops()
        file_ops = [op for op in ops if isinstance(op, FileOp)]
        assert any(
            op.path == "/etc/cloud/cloud-init.disabled" for op in file_ops
        )

    def test_includes_snapd_override_file_op(self) -> None:
        """Should include FileOp for snapd override.conf."""
        ops = ProvisionerContent.build_cloud_init_disable_ops()
        file_ops = [op for op in ops if isinstance(op, FileOp)]
        assert any(
            "snapd.seeded.service.d/override.conf" in op.path for op in file_ops
        )

    def test_includes_networkd_wait_override_file_op(self) -> None:
        """Should include FileOp for networkd-wait-online override.conf."""
        ops = ProvisionerContent.build_cloud_init_disable_ops()
        file_ops = [op for op in ops if isinstance(op, FileOp)]
        assert any(
            "systemd-networkd-wait-online.service.d/override.conf" in op.path
            for op in file_ops
        )

    def test_includes_four_chroot_symlink_ops(self) -> None:
        """Should include exactly 4 ChrootOps for cloud-init service symlinks."""
        ops = ProvisionerContent.build_cloud_init_disable_ops()
        chroot_ops = [op for op in ops if isinstance(op, ChrootOp)]
        assert len(chroot_ops) == 4
        for op in chroot_ops:
            assert "ln -sf /dev/null" in op.command

    def test_chroot_ops_target_cloud_init_services(self) -> None:
        """Each ChrootOp should target a cloud-init systemd service."""
        ops = ProvisionerContent.build_cloud_init_disable_ops()
        chroot_commands = {op.command for op in ops if isinstance(op, ChrootOp)}
        expected_services = [
            "cloud-init.service",
            "cloud-init-local.service",
            "cloud-config.service",
            "cloud-final.service",
        ]
        for svc in expected_services:
            assert any(svc in cmd for cmd in chroot_commands)


class TestBuildCloudInitInjectOps:
    """Tests for ProvisionerContent.build_cloud_init_inject_ops()."""

    def test_existing_dir_returns_copy_dir_op(self, tmp_path: Path) -> None:
        """When the directory exists, should return a CopyDirOp."""
        src_dir = tmp_path / "cloud-init-data"
        src_dir.mkdir(parents=True)
        (src_dir / "meta-data").write_text("")

        ops = ProvisionerContent.build_cloud_init_inject_ops(src_dir)
        assert len(ops) == 1
        op = ops[0]
        assert isinstance(op, CopyDirOp)
        assert op.src == str(src_dir)
        assert op.dst == "/var/lib/cloud/seed/nocloud-net"

    def test_non_existing_dir_returns_empty_list(self, tmp_path: Path) -> None:
        """When the directory does not exist, should return empty list."""
        nonexistent = tmp_path / "does-not-exist"
        ops = ProvisionerContent.build_cloud_init_inject_ops(nonexistent)
        assert ops == []


class TestBuildResizeOps:
    """Tests for ProvisionerContent.build_resize_ops()."""

    def test_returns_resize_op_with_grow_action(self) -> None:
        """Should return a ResizeOp with action='grow'."""
        ops = ProvisionerContent.build_resize_ops(
            8 * 1024 * 1024 * 1024
        )  # 8 GiB
        assert len(ops) == 1
        op = ops[0]
        assert isinstance(op, ResizeOp)
        assert op.action == "grow"

    def test_returns_correct_byte_count(self) -> None:
        """Should return ResizeOp with the correct byte count."""
        target = 16 * 1024 * 1024 * 1024  # 16 GiB
        ops = ProvisionerContent.build_resize_ops(target)
        assert ops[0].bytes == target


# ═══════════════════════════════════════════════════════════════════════
# 3. Operation dataclasses
# ═══════════════════════════════════════════════════════════════════════


class TestOperationDataclasses:
    """Verify operation dataclasses are proper dataclasses with correct types."""

    def test_file_op_is_dataclass(self) -> None:
        """FileOp should be a proper dataclass with expected fields."""
        op = FileOp(path="/test", data=b"content")
        assert op.path == "/test"
        assert op.data == b"content"
        assert op.mode == 0o644
        assert op.uid == 0
        assert op.gid == 0
        # Verify frozen-like immutability (dataclasses are mutable by default)
        op.mode = 0o755
        assert op.mode == 0o755

    def test_chroot_op_is_dataclass(self) -> None:
        """ChrootOp should be a proper dataclass with a command field."""
        op = ChrootOp(command="echo hello")
        assert op.command == "echo hello"
        # Verify it's a dataclass instance
        assert hasattr(op, "__dataclass_fields__")

    def test_copy_dir_op_is_dataclass(self) -> None:
        """CopyDirOp should be a proper dataclass with src and dst fields."""
        op = CopyDirOp(src="/source", dst="/dest")
        assert op.src == "/source"
        assert op.dst == "/dest"
        assert hasattr(op, "__dataclass_fields__")

    def test_resize_op_is_dataclass(self) -> None:
        """ResizeOp should be a proper dataclass with action and bytes fields."""
        op = ResizeOp(action="grow", bytes=4096)
        assert op.action == "grow"
        assert op.bytes == 4096
        assert hasattr(op, "__dataclass_fields__")

    def test_resize_op_default_bytes_zero(self) -> None:
        """ResizeOp should default bytes to 0."""
        op = ResizeOp(action="shrink")
        assert op.bytes == 0

    def test_operation_type_alias_resolves(self) -> None:
        """Operation type alias should resolve to the union of all op types."""
        ops: list[Operation] = [
            FileOp(path="/a", data=b""),
            ChrootOp(command="true"),
            CopyDirOp(src="/a", dst="/b"),
            ResizeOp(action="grow"),
        ]
        assert len(ops) == 4
        for op in ops:
            assert isinstance(op, (FileOp, ChrootOp, CopyDirOp, ResizeOp))
