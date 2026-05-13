"""Tests for LogController — stateful log controller bound to a VM entity."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.logs._controller import LogController
from mvmctl.core.logs._service import LogService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstanceItem, VMStatus


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied."""
    database = Database()
    database.migrate()
    return database


def _seed_network(db: Database) -> str:
    """Insert a network row and return its ID."""
    from datetime import UTC, datetime

    now = datetime.now(tz=UTC).isoformat()
    nid = "net-test"
    with db.connect() as conn:
        conn.execute(
            """
            INSERT OR IGNORE INTO networks (id, name, subnet, bridge, ipv4_gateway,
                                            bridge_active, nat_enabled, is_default,
                                            is_present, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                nid,
                "testnet",
                "10.0.0.0/24",
                "mvmbr0",
                "10.0.0.1",
                1,
                1,
                1,
                1,
                now,
                now,
            ),
        )
    return nid


def _seed_image(db: Database, img_id: str = "img-test") -> str:
    """Insert an image row and return its ID."""
    from datetime import UTC, datetime

    now = datetime.now(tz=UTC).isoformat()
    with db.connect() as conn:
        conn.execute(
            """
            INSERT OR IGNORE INTO images (id, os_slug, os_name, arch, path, fs_type,
                                          original_size, minimum_rootfs_size_mib,
                                          pulled_at, is_default, is_present,
                                          created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                img_id,
                "ubuntu",
                "Ubuntu 22.04",
                "x86_64",
                "/tmp/test.img",
                "ext4",
                2048,
                1024,
                now,
                0,
                1,
                now,
                now,
            ),
        )
    return img_id


def _seed_kernel(db: Database, kern_id: str = "kern-test") -> str:
    """Insert a kernel row and return its ID."""
    from datetime import UTC, datetime

    now = datetime.now(tz=UTC).isoformat()
    with db.connect() as conn:
        conn.execute(
            """
            INSERT OR IGNORE INTO kernels (id, name, base_name, version, arch, type, path,
                                           is_default, is_present, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                kern_id,
                "test-kernel",
                "vmlinux-6.1.0",
                "6.1.0",
                "x86_64",
                "vmlinux",
                "/tmp/vmlinux.bin",
                0,
                1,
                now,
                now,
            ),
        )
    return kern_id


def _seed_binary(db: Database, bin_id: str = "bin-test") -> str:
    """Insert a binary row and return its ID."""
    from datetime import UTC, datetime

    now = datetime.now(tz=UTC).isoformat()
    with db.connect() as conn:
        conn.execute(
            """
            INSERT OR IGNORE INTO binaries (id, name, version, full_version, ci_version, path,
                                            is_default, is_present, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                bin_id,
                "firecracker",
                "1.15.0",
                "v1.15.0",
                "v1.15",
                "firecracker-v1.15.0",
                0,
                1,
                now,
                now,
            ),
        )
    return bin_id


def _seed_vm(db: Database, name: str, vm_id: str, vm_dir: Path) -> str:
    """Insert a VM into the database and return its hash."""
    from datetime import UTC, datetime

    net_id = _seed_network(db)
    img_id = _seed_image(db)
    kern_id = _seed_kernel(db)
    bin_id = _seed_binary(db)
    now = datetime.now(tz=UTC).isoformat()
    vm_hash = vm_id  # Use vm_id as hash for simplicity
    with db.connect() as conn:
        conn.execute(
            """
            INSERT INTO vm_instances (
                id, name, status, vcpu_count, mem_size_mib, disk_size_mib,
                pid, ipv4, mac, network_id, tap_device, image_id, kernel_id,
                binary_id, api_socket_path, config_path, cloud_init_mode,
                rootfs_path, rootfs_suffix, enable_pci, enable_logging,
                enable_metrics, enable_console, created_at, updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                vm_hash,
                name,
                VMStatus.STOPPED.value,
                2,
                512,
                2048,
                0,
                "10.0.0.10",
                "aa:bb:cc:dd:ee:ff",
                net_id,
                "tap0",
                img_id,
                kern_id,
                bin_id,
                "/tmp/api.sock",
                "/tmp/config.json",
                "off",
                "/tmp/rootfs.ext4",
                ".ext4",
                0,
                0,
                0,
                0,
                now,
                now,
            ),
        )
    return vm_hash


class TestLogController:
    """Tests for LogController."""

    def test_show_boot_log(self, db: Database, tmp_path: Path) -> None:
        """show() returns boot log lines."""
        vm_hash = _seed_vm(db, "testvm", "abc123", tmp_path)
        vm_dir = tmp_path / "abc123"
        vm_dir.mkdir(parents=True)
        log_file = vm_dir / "firecracker.console.log"
        log_file.write_text("boot line 1\nboot line 2\nboot line 3\n")

        with patch(
            "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=vm_dir
        ):
            repo = VMRepository(db)
            vm = repo.get(vm_hash)
            assert vm is not None
            controller = LogController(vm)
            result = controller.show(
                "boot",
                lines=50,
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )
        assert len(result) == 3
        assert result[0] == "boot line 1"

    def test_show_os_log(self, db: Database, tmp_path: Path) -> None:
        """show() returns OS log lines."""
        vm_hash = _seed_vm(db, "testvm", "abc123", tmp_path)
        vm_dir = tmp_path / "abc123"
        vm_dir.mkdir(parents=True)
        log_file = vm_dir / "firecracker.log"
        log_file.write_text("os line 1\nos line 2\n")

        with patch(
            "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=vm_dir
        ):
            repo = VMRepository(db)
            vm = repo.get(vm_hash)
            assert vm is not None
            controller = LogController(vm)
            result = controller.show(
                "os",
                lines=50,
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )
        assert len(result) == 2

    def test_show_boot_returns_last_n_lines(
        self, db: Database, tmp_path: Path
    ) -> None:
        """show() returns the last N lines."""
        vm_hash = _seed_vm(db, "testvm", "abc123", tmp_path)
        vm_dir = tmp_path / "abc123"
        vm_dir.mkdir(parents=True)
        log_file = vm_dir / "firecracker.console.log"
        log_file.write_text("".join(f"line {i}\n" for i in range(100)))

        with patch(
            "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=vm_dir
        ):
            repo = VMRepository(db)
            vm = repo.get(vm_hash)
            assert vm is not None
            controller = LogController(vm)
            result = controller.show(
                "boot",
                lines=5,
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )
        assert len(result) == 5
        assert result[0] == "line 95"
        assert result[-1] == "line 99"

    def test_show_nonexistent_log_file(
        self, db: Database, tmp_path: Path
    ) -> None:
        """show() raises VMNotFoundError when log file does not exist."""
        vm_hash = _seed_vm(db, "testvm", "abc123", tmp_path)
        vm_dir = tmp_path / "abc123"
        vm_dir.mkdir(parents=True)

        with patch(
            "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=vm_dir
        ):
            repo = VMRepository(db)
            vm = repo.get(vm_hash)
            assert vm is not None
            controller = LogController(vm)
            with pytest.raises(VMNotFoundError, match="Log file not found"):
                controller.show(
                    "boot",
                    lines=50,
                    log_filename="firecracker.log",
                    serial_output_filename="firecracker.console.log",
                )

    def test_show_unknown_log_type(self, db: Database, tmp_path: Path) -> None:
        """show() treats unknown type as 'os' (validation is at API layer)."""
        vm_hash = _seed_vm(db, "testvm", "abc123", tmp_path)
        vm_dir = tmp_path / "abc123"
        vm_dir.mkdir(parents=True)
        log_file = vm_dir / "firecracker.log"
        log_file.write_text("os line 1\nos line 2\n")

        with patch(
            "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=vm_dir
        ):
            repo = VMRepository(db)
            vm = repo.get(vm_hash)
            assert vm is not None
            controller = LogController(vm)
            result = controller.show(
                "unknown",
                lines=50,
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )
        assert len(result) == 2

    def test_follow_yields_lines(self, db: Database, tmp_path: Path) -> None:
        """follow() yields log lines as they become available."""
        vm_hash = _seed_vm(db, "testvm", "abc123", tmp_path)
        vm_dir = tmp_path / "abc123"
        vm_dir.mkdir(parents=True)
        (vm_dir / "firecracker.console.log").write_text("line 1\nline 2\n")

        def _fake_follow(f):
            yield "line 1"
            yield "line 2"
            raise KeyboardInterrupt

        with (
            patch(
                "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=vm_dir
            ),
            patch.object(LogService, "follow_log", side_effect=_fake_follow),
        ):
            repo = VMRepository(db)
            vm = repo.get(vm_hash)
            assert vm is not None
            controller = LogController(vm)
            gen = controller.follow(
                "boot",
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )

            lines = []
            try:
                for line in gen:
                    lines.append(line)
            except KeyboardInterrupt:
                pass

            assert len(lines) > 0

    def test_controller_vm_property(self, db: Database) -> None:
        """vm property returns the resolved VM."""
        from datetime import UTC, datetime

        now = datetime.now(tz=UTC).isoformat()
        repo = VMRepository(db)
        net_id = _seed_network(db)
        img_id = _seed_image(db)
        kern_id = _seed_kernel(db)
        bin_id = _seed_binary(db)
        vm = VMInstanceItem(
            id="abc123",
            name="testvm",
            status=VMStatus.STOPPED.value,
            pid=0,
            ipv4="10.0.0.10",
            mac="aa:bb:cc:dd:ee:ff",
            network_id=net_id,
            tap_device="tap0",
            image_id=img_id,
            kernel_id=kern_id,
            binary_id=bin_id,
            api_socket_path="/tmp/api.sock",
            config_path="/tmp/config.json",
            cloud_init_mode="off",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=2048,
            rootfs_path="/tmp/rootfs.ext4",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(vm)

        controller = LogController(vm)
        assert controller.vm.name == "testvm"
        assert controller.vm.id == "abc123"
