"""
Tests for FirecrackerClient and FirecrackerSpawner.

Tests cover: Unix socket HTTP connection, API request methods
(GET, PUT, PATCH), snapshot operations, instance control (start,
pause, resume, ctrl+alt+del), and spawner config generation.
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.vm._firecracker import (
    FirecrackerClient,
    FirecrackerSpawner,
    UnixSocketHTTPConnection,
)
from mvmctl.exceptions import (
    FirecrackerClientError,
    FirecrackerSpawnError,
    SocketNotFoundError,
)
from mvmctl.models import CloudInitMode, FirecrackerConfig

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _mock_response(
    status: int, body: dict[str, object] | None = None
) -> MagicMock:
    """Create a mock HTTP response."""
    resp = MagicMock()
    resp.status = status
    resp.read.return_value = json.dumps(body).encode() if body else b""
    return resp


# ---------------------------------------------------------------------------
# Tests: UnixSocketHTTPConnection
# ---------------------------------------------------------------------------


class TestUnixSocketHTTPConnection:
    def test_connect_creates_unix_socket(self) -> None:
        with patch("socket.socket") as mock_socket:
            mock_sock = MagicMock()
            mock_socket.return_value = mock_sock

            conn = UnixSocketHTTPConnection(Path("/tmp/test.sock"))
            conn.connect()

            mock_socket.assert_called_once()
            mock_sock.settimeout.assert_called_once()
            mock_sock.connect.assert_called_once_with("/tmp/test.sock")


# ---------------------------------------------------------------------------
# Tests: FirecrackerClient connection
# ---------------------------------------------------------------------------


class TestConnect:
    def test_raises_socket_not_found_when_missing(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "missing.socket")
        with pytest.raises(SocketNotFoundError, match="Socket not found"):
            client._connect()

    def test_connects_when_socket_exists(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        client._connect()
        assert client._conn is not None
        client.close()

    def test_close_clears_connection(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        client._connect()
        assert client._conn is not None
        client.close()
        assert client._conn is None

    def test_context_manager(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        with FirecrackerClient(sock) as client:
            assert client._conn is not None
        assert client._conn is None

    def test_reconnect_after_close(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        client._connect()
        client.close()
        client._connect()
        assert client._conn is not None
        client.close()


# ---------------------------------------------------------------------------
# Tests: FirecrackerClient._request
# ---------------------------------------------------------------------------


class TestRequest:
    def test_sends_get_request(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(200, {"id": "test"})
        client._conn = mock_conn

        status, data = client._request("GET", "/")

        assert status == 200
        assert data == {"id": "test"}
        mock_conn.request.assert_called_once_with(
            "GET", "/", body=None, headers={}
        )

    def test_sends_put_with_body(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(204)
        client._conn = mock_conn

        status, data = client._request(
            "PUT", "/snapshot/create", {"key": "val"}
        )

        assert status == 204
        assert data is None
        expected_body = json.dumps({"key": "val"})
        mock_conn.request.assert_called_once_with(
            "PUT",
            "/snapshot/create",
            body=expected_body,
            headers={"Content-Type": "application/json"},
        )

    def test_returns_none_for_empty_body(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(204)
        client._conn = mock_conn

        status, data = client._request("DELETE", "/vm")

        assert status == 204
        assert data is None

    def test_raises_on_oserror(self, tmp_path: Path) -> None:
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.request.side_effect = OSError("connection reset")
        client._conn = mock_conn

        with pytest.raises(FirecrackerClientError, match="API request failed"):
            client._request("GET", "/")


# ---------------------------------------------------------------------------
# Tests: FirecrackerClient snapshot operations
# ---------------------------------------------------------------------------


class TestSnapshot:
    def test_create_snapshot_success(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(204, None)
        ) as mock_req:
            result = client.create_snapshot(
                tmp_path / "mem", tmp_path / "state"
            )
        assert result is True
        mock_req.assert_called_once_with(
            "PUT",
            "/snapshot/create",
            {
                "mem_file_path": str(tmp_path / "mem"),
                "snapshot_path": str(tmp_path / "state"),
            },
        )

    def test_create_snapshot_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(400, {"error": "fail"})
        ):
            with pytest.raises(
                FirecrackerClientError, match="Failed to create snapshot"
            ):
                client.create_snapshot(tmp_path / "mem", tmp_path / "state")

    def test_load_snapshot_success(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(204, None)
        ) as mock_req:
            result = client.load_snapshot(
                tmp_path / "mem", tmp_path / "state", resume=True
            )
        assert result is True
        mock_req.assert_called_once_with(
            "PUT",
            "/snapshot/load",
            {
                "mem_file_path": str(tmp_path / "mem"),
                "snapshot_path": str(tmp_path / "state"),
                "resume_vm": True,
            },
        )

    def test_load_snapshot_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(400, {"error": "nope"})
        ):
            with pytest.raises(
                FirecrackerClientError, match="Failed to load snapshot"
            ):
                client.load_snapshot(tmp_path / "mem", tmp_path / "state")


# ---------------------------------------------------------------------------
# Tests: FirecrackerClient instance control
# ---------------------------------------------------------------------------


class TestInstanceControl:
    def test_get_instance_info_success(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(200, {"id": "i-123"})
        ):
            info = client.get_instance_info()
        assert info is not None
        assert info["id"] == "i-123"

    def test_get_instance_info_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(500, None)):
            info = client.get_instance_info()
        assert info is None

    def test_describe_instance(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(200, {"state": "Running"})
        ):
            desc = client.describe_instance()
        assert desc is not None
        assert desc["state"] == "Running"

    def test_start_instance_success(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(204, None)):
            assert client.start_instance() is True

    def test_start_instance_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(400, None)):
            with pytest.raises(
                FirecrackerClientError, match="Failed to start VM"
            ):
                client.start_instance()

    def test_send_ctrl_alt_del_success(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(204, None)):
            assert client.send_ctrl_alt_del() is True

    def test_send_ctrl_alt_del_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(400, None)):
            assert client.send_ctrl_alt_del() is False

    def test_send_ctrl_alt_del_handles_exception(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", side_effect=FirecrackerClientError("no vm")
        ):
            assert client.send_ctrl_alt_del() is False

    def test_pause_vm_sends_correct_request(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(204, None)
        ) as mock_req:
            client.pause_vm()
        mock_req.assert_called_once_with("PATCH", "/vm", {"state": "Paused"})

    def test_pause_vm_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(400, None)):
            with pytest.raises(
                FirecrackerClientError, match="Failed to pause VM"
            ):
                client.pause_vm()

    def test_resume_vm_sends_correct_request(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(
            client, "_request", return_value=(204, None)
        ) as mock_req:
            client.resume_vm()
        mock_req.assert_called_once_with("PATCH", "/vm", {"state": "Resumed"})

    def test_resume_vm_failure(self, tmp_path: Path) -> None:
        client = FirecrackerClient(tmp_path / "test.sock")
        with patch.object(client, "_request", return_value=(400, None)):
            with pytest.raises(
                FirecrackerClientError, match="Failed to resume VM"
            ):
                client.resume_vm()


# ---------------------------------------------------------------------------
# Tests: FirecrackerClient drive operations
# ---------------------------------------------------------------------------


class TestFirecrackerClientDriveOps:
    def test_put_drive_success(self, tmp_path: Path) -> None:
        """put_drive should succeed with valid drive config."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(204)
        client._conn = mock_conn

        drive_config = {
            "drive_id": "vol-1",
            "path_on_host": "/volumes/test.raw",
            "is_root_device": False,
            "is_read_only": False,
            "cache_type": "Unsafe",
            "io_engine": "Sync",
        }
        client.put_drive(drive_config)
        mock_conn.request.assert_called_once()

    def test_put_drive_success_200(self, tmp_path: Path) -> None:
        """put_drive with status 200 should also succeed."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(200)
        client._conn = mock_conn

        drive_config = {
            "drive_id": "vol-1",
            "path_on_host": "/volumes/test.raw",
            "is_root_device": False,
            "is_read_only": False,
            "cache_type": "Unsafe",
            "io_engine": "Sync",
        }
        client.put_drive(drive_config)
        mock_conn.request.assert_called_once()

    def test_put_drive_failure(self, tmp_path: Path) -> None:
        """put_drive should raise on non-success status."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(500)
        client._conn = mock_conn

        with pytest.raises(
            FirecrackerClientError, match="Failed to attach drive"
        ):
            client.put_drive(
                {
                    "drive_id": "v1",
                    "path_on_host": "/p",
                    "is_root_device": False,
                    "is_read_only": False,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                }
            )

    def test_put_drive_failure_with_data(self, tmp_path: Path) -> None:
        """put_drive failure should include response data in error."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(
            500, {"error": "no space"}
        )
        client._conn = mock_conn

        with pytest.raises(
            FirecrackerClientError, match="Response: {'error': 'no space'}"
        ):
            client.put_drive(
                {
                    "drive_id": "v1",
                    "path_on_host": "/p",
                    "is_root_device": False,
                    "is_read_only": False,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                }
            )

    def test_patch_drive_success(self, tmp_path: Path) -> None:
        """patch_drive should succeed with valid drive_id."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(204)
        client._conn = mock_conn

        client.patch_drive("vol-1")
        mock_conn.request.assert_called_once()

    def test_patch_drive_success_200(self, tmp_path: Path) -> None:
        """patch_drive with status 200 should also succeed."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(200)
        client._conn = mock_conn

        client.patch_drive("vol-1")
        mock_conn.request.assert_called_once()

    def test_patch_drive_failure(self, tmp_path: Path) -> None:
        """patch_drive should raise on non-success status."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(500)
        client._conn = mock_conn

        with pytest.raises(
            FirecrackerClientError, match="Failed to detach drive"
        ):
            client.patch_drive("vol-1")

    def test_patch_drive_failure_with_data(self, tmp_path: Path) -> None:
        """patch_drive failure should include response data in error."""
        sock = tmp_path / "test.sock"
        sock.touch()
        client = FirecrackerClient(sock)
        mock_conn = MagicMock()
        mock_conn.getresponse.return_value = _mock_response(
            500, {"error": "not found"}
        )
        client._conn = mock_conn

        with pytest.raises(
            FirecrackerClientError, match="Response: {'error': 'not found'}"
        ):
            client.patch_drive("vol-1")


class TestFirecrackerSpawnerExtraDrives:
    """Test _build_drives_config with extra drives."""

    def _make_config(
        self,
        extra_drives: list[dict[str, object]] | None = None,
    ) -> FirecrackerConfig:
        kwargs: dict[str, object] = {}
        if extra_drives is not None:
            kwargs["extra_drives"] = extra_drives
        return FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/rootfs.ext4"),
            binary_path="/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:fc:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid=None,
            image_fs_type="ext4",
            boot_args="console=ttyS0",
            lsm_flags="",
            pci_enabled=False,
            nested_virt=False,
            enable_console=True,
            enable_logging=False,
            enable_metrics=False,
            log_level="Info",
            log_filename="fc.log",
            serial_output_filename="serial.log",
            metrics_filename="fc.metrics",
            api_socket_filename="fc.socket",
            pid_filename="fc.pid",
            config_filename="vm.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            **kwargs,
        )

    def test_extra_drives_included(self) -> None:
        """_build_drives_config should include extra drives."""
        config = self._make_config(
            extra_drives=[
                {
                    "drive_id": "vol-1",
                    "path_on_host": "/volumes/test.raw",
                    "is_root_device": False,
                    "is_read_only": False,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                }
            ]
        )
        spawner = FirecrackerSpawner(config)
        drives = spawner._build_drives_config()
        assert len(drives) == 2  # rootfs + extra drive
        assert drives[1]["drive_id"] == "vol-1"
        assert drives[1]["path_on_host"] == "/volumes/test.raw"

    def test_no_extra_drives(self) -> None:
        """_build_drives_config without extra drives should return only rootfs."""
        config = self._make_config()
        spawner = FirecrackerSpawner(config)
        drives = spawner._build_drives_config()
        assert len(drives) == 1
        assert drives[0]["drive_id"] == "rootfs"

    def test_multiple_extra_drives(self) -> None:
        """_build_drives_config should include multiple extra drives."""
        config = self._make_config(
            extra_drives=[
                {
                    "drive_id": "vol-1",
                    "path_on_host": "/volumes/test1.raw",
                    "is_root_device": False,
                    "is_read_only": False,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                },
                {
                    "drive_id": "vol-2",
                    "path_on_host": "/volumes/test2.raw",
                    "is_root_device": False,
                    "is_read_only": True,
                    "cache_type": "Unsafe",
                    "io_engine": "Sync",
                },
            ]
        )
        spawner = FirecrackerSpawner(config)
        drives = spawner._build_drives_config()
        assert len(drives) == 3  # rootfs + 2 extra drives
        assert drives[1]["drive_id"] == "vol-1"
        assert drives[2]["drive_id"] == "vol-2"


# ---------------------------------------------------------------------------
# Tests: FirecrackerSpawner
# ---------------------------------------------------------------------------


class TestFirecrackerSpawnerGenerate:
    def test_generates_minimal_config(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        fc_config = spawner.generate()

        assert "boot-source" in fc_config
        assert "drives" in fc_config
        assert "network-interfaces" in fc_config
        assert "machine-config" in fc_config
        assert "logger" not in fc_config
        assert "metrics" not in fc_config

        assert fc_config["boot-source"]["kernel_image_path"] == "/tmp/vmlinux"
        assert fc_config["machine-config"]["vcpu_count"] == 2
        assert fc_config["machine-config"]["mem_size_mib"] == 512

    def test_includes_logger_when_enabled(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=True,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        fc_config = spawner.generate()

        logger_config = fc_config.get("logger")
        assert logger_config is not None
        assert logger_config["log_path"] == str(Path("/tmp/vm/firecracker.log"))

    def test_includes_metrics_when_enabled(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=True,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        fc_config = spawner.generate()

        metrics_config = fc_config.get("metrics")
        assert metrics_config is not None
        assert metrics_config["metrics_path"] == str(Path("/tmp/vm/metrics"))

    def test_cloud_init_iso_drive_included(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=CloudInitMode.ISO,
            cloud_init_iso_path=Path("/tmp/vm/cloud-init.iso"),
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        fc_config = spawner.generate()

        drives = fc_config["drives"]
        assert len(drives) == 2
        assert drives[1]["drive_id"] == "cloud-init"
        assert drives[1]["path_on_host"] == "/tmp/vm/cloud-init.iso"

    def test_cloud_init_off_omits_iso_drive(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=CloudInitMode.OFF,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        fc_config = spawner.generate()

        drives = fc_config["drives"]
        assert len(drives) == 1  # Only rootfs, no cloud-init


class TestFirecrackerSpawnerSpawn:
    def test_spawn_creates_process(self, tmp_path: Path) -> None:
        vm_dir = tmp_path / "vm"
        vm_dir.mkdir()
        fc_bin = tmp_path / "firecracker"
        fc_bin.write_text("")
        fc_bin.chmod(0o755)

        config = FirecrackerConfig(
            vm_dir=vm_dir,
            rootfs_path=vm_dir / "rootfs.ext4",
            binary_path=str(fc_bin),
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )

        spawner = FirecrackerSpawner(config)

        # Replace _api_socket_path with a mock so we can control exists().
        # PosixPath.exists is read-only, so patch.object won't work on it.
        mock_socket_path = MagicMock()
        mock_socket_path.exists.side_effect = [
            False,
            True,
        ]  # no stale → appears
        spawner._api_socket_path = mock_socket_path

        mock_proc = MagicMock()
        mock_proc.pid = 99999
        mock_proc.poll.return_value = None

        with (
            patch(
                "mvmctl.core.vm._firecracker.subprocess.Popen",
                return_value=mock_proc,
            ),
            patch("mvmctl.core.vm._firecracker.FsUtils.write_pid_file"),
            patch(
                "mvmctl.utils._system.ProcessSignalHandler._get_process_start_time",
                return_value=2000000,
            ),
        ):
            spawner.spawn()

        assert spawner.pid == 99999
        assert spawner.process_start_time == 2000000

    def test_spawn_exits_immediately(self, tmp_path: Path) -> None:
        vm_dir = tmp_path / "vm"
        vm_dir.mkdir()
        fc_bin = tmp_path / "firecracker"
        fc_bin.write_text("")
        fc_bin.chmod(0o755)

        config = FirecrackerConfig(
            vm_dir=vm_dir,
            rootfs_path=vm_dir / "rootfs.ext4",
            binary_path=str(fc_bin),
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )

        spawner = FirecrackerSpawner(config)

        mock_proc = MagicMock()
        mock_proc.poll.return_value = 1  # Exited with code 1

        with patch(
            "mvmctl.core.vm._firecracker.subprocess.Popen",
            return_value=mock_proc,
        ):
            with pytest.raises(
                FirecrackerSpawnError, match="exited immediately"
            ):
                spawner.spawn()


class TestFirecrackerSpawnerBootArgs:
    def test_boot_args_include_ip(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="12345678-1234-1234-1234-123456789abc",
            image_fs_type="ext4",
            pci_enabled=True,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        # Access internal boot_args logic via generate()
        fc_config = spawner.generate()
        boot_args = fc_config["boot-source"]["boot_args"]

        # Should contain ip= parameter
        assert "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off" in boot_args

    def test_boot_args_pci_off(self) -> None:
        config = FirecrackerConfig(
            vm_dir=Path("/tmp/vm"),
            rootfs_path=Path("/tmp/vm/rootfs.ext4"),
            binary_path="/usr/bin/firecracker",
            kernel_path="/tmp/vmlinux",
            vcpu_count=2,
            mem_size_mib=512,
            guest_ip="10.0.0.2",
            guest_mac="02:FC:00:00:00:01",
            tap_name="tap0",
            network_gateway="10.0.0.1",
            network_netmask="255.255.255.0",
            image_fs_uuid="",
            image_fs_type="ext4",
            pci_enabled=False,
            nested_virt=False,
            enable_console=False,
            enable_logging=False,
            enable_metrics=False,
            log_level="Debug",
            log_filename="firecracker.log",
            serial_output_filename="console.log",
            metrics_filename="metrics",
            api_socket_filename="api.sock",
            pid_filename="firecracker.pid",
            config_filename="firecracker.json",
            cloud_init_mode=None,
            cloud_init_iso_path=None,
            cloud_init_nocloud_url=None,
            boot_args=None,
            lsm_flags="",
        )
        spawner = FirecrackerSpawner(config)
        fc_config = spawner.generate()
        boot_args = fc_config["boot-source"]["boot_args"]

        assert "pci=off" in boot_args
