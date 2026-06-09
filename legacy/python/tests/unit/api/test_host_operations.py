"""Tests for HostOperation — host management orchestration."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

from mvmctl.api.host_operations import HostOperation
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.models import VMStatus
from mvmctl.models.host import (
    HostHardware,
    HostInfo,
    HostLimits,
    HostResources,
    HostStateItem,
)
from mvmctl.models.result import OperationResult


class TestHostOperationDelegations:
    """Tests for simple delegation methods — verifies API boundary."""

    def test_get_state(self, mocker):
        """get_state() delegates to HostRepository.get_state()."""
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = {"initialized": True}
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        assert HostOperation.get_state() == {"initialized": True}
        mock_repo.get_state.assert_called_once()

    def test_get_state_none(self, mocker):
        """get_state() returns None when no state exists."""
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = None
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        assert HostOperation.get_state() is None

    def test_check_required_binaries(self, mocker):
        """check_required_binaries() delegates to HostService."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_required_binaries",
            return_value=[],
        )
        assert HostOperation.check_required_binaries() == []

    def test_check_required_binaries_missing(self, mocker):
        """check_required_binaries() returns missing binary list."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_required_binaries",
            return_value=["missing_bin"],
        )
        assert HostOperation.check_required_binaries() == ["missing_bin"]

    def test_get_ip_forward_status(self, mocker):
        """get_ip_forward_status() delegates to HostService."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService._get_ip_forward_status",
            return_value="1",
        )
        assert HostOperation.get_ip_forward_status() == "1"

    def test_get_ip_forward_status_disabled(self, mocker):
        """get_ip_forward_status() returns 0 when disabled."""
        mocker.patch(
            "mvmctl.api.host_operations.HostService._get_ip_forward_status",
            return_value="0",
        )
        assert HostOperation.get_ip_forward_status() == "0"

    def test_get_running_vms(self, mocker):
        """get_running_vms() delegates to VMRepository."""
        mock_vms = [MagicMock(), MagicMock()]
        mock_repo = MagicMock()
        mock_repo.list_by_status.return_value = mock_vms
        mocker.patch(
            "mvmctl.api.host_operations.VMRepository",
            return_value=mock_repo,
        )
        result = HostOperation.get_running_vms()
        assert result == mock_vms
        mock_repo.list_by_status.assert_called_once_with(VMStatus.RUNNING)

    def test_get_running_vms_empty(self, mocker):
        """get_running_vms() returns empty list when none running."""
        mock_repo = MagicMock()
        mock_repo.list_by_status.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.VMRepository",
            return_value=mock_repo,
        )
        assert HostOperation.get_running_vms() == []


class TestHostOperationBuildInfoDict:
    """Tests for HostInfo.to_dict() — info response formatting."""

    def _make_state(self) -> HostStateItem:
        return HostStateItem(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-01-01T12:00:00+00:00",
            updated_at="2026-01-01T12:00:00+00:00",
            hostname="testhost",
            cpu_model="Test CPU",
            cpu_vendor="intel",
            cpu_cores=8,
            cpu_architecture="x86_64",
            numa_nodes=2,
            memory_total_mib=32000,
            storage_total_bytes=500_000_000_000,
            kernel_version="6.8.0",
            os_release="TestOS 1.0",
            pid_max=4194304,
            fd_max=9223372036854775807,
            conntrack_max=262144,
            tap_devices_max=-1,
            ip_local_port_range="32768,60999",
            detected_at="2026-01-01T12:00:00+00:00",
        )

    def _make_hardware(self) -> HostHardware:
        return HostHardware(
            hostname="testhost",
            cpu_model="Test CPU",
            cpu_vendor="intel",
            cpu_cores=8,
            cpu_architecture="x86_64",
            numa_nodes=2,
            memory_total_mib=32000,
            storage_total_bytes=500_000_000_000,
            kernel_version="6.8.0",
            os_release="TestOS 1.0",
        )

    def _make_limits(self) -> HostLimits:
        return HostLimits(
            pid_max=4194304,
            fd_max=9223372036854775807,
            conntrack_max=262144,
            tap_devices_max=-1,
            ip_local_port_range=(32768, 60999),
        )

    def _make_resources(self) -> HostResources:
        return HostResources(
            memory_available_mib=8192,
            tap_devices_used=2,
            pids_current=512,
            fd_current=2048,
            conntrack_current=128,
            arp_current=5,
            storage_free_bytes=200_000_000_000,
            recommended_max_vms=10,
            limiting_resource="memory",
        )

    def test_build_info_dict_structure(self, mocker):
        """to_dict() returns a dict with all expected top-level keys."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()

        assert isinstance(info_dict, dict)
        assert "detected_at" in info_dict
        assert "hostname" in info_dict
        assert "os" in info_dict
        assert "cpu" in info_dict
        assert "memory" in info_dict
        assert "storage" in info_dict
        assert "limits" in info_dict
        assert "capacity" in info_dict
        assert "setup" in info_dict

    def test_build_info_dict_cpu_section(self, mocker):
        """to_dict() cpu section contains model, vendor, cores, architecture, numa_nodes."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        cpu = info_dict["cpu"]
        assert isinstance(cpu, dict)
        assert cpu["model"] == "Test CPU"
        assert cpu["vendor"] == "intel"
        assert cpu["cores"] == 8
        assert cpu["architecture"] == "x86_64"
        assert cpu["numa_nodes"] == 2

    def test_build_info_dict_memory_section(self, mocker):
        """to_dict() memory section contains total_mib and available_mib."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        mem = info_dict["memory"]
        assert isinstance(mem, dict)
        assert mem["total_mib"] == 32000
        assert mem["available_mib"] == 8192

    def test_build_info_dict_limits_section(self, mocker):
        """to_dict() limits section contains pid_max, fd_max, conntrack_max, tap_devices_max, ip_local_port_range."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        lim = info_dict["limits"]
        assert isinstance(lim, dict)
        assert lim["pid_max"] == 4194304
        assert lim["fd_max"] == 9223372036854775807
        assert lim["conntrack_max"] == 262144
        assert lim["tap_devices_max"] == -1
        assert lim["ip_local_port_range"] == [32768, 60999]

    def test_build_info_dict_capacity_section(self, mocker):
        """to_dict() capacity section contains current usage and recommended_max_vms."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        cap = info_dict["capacity"]
        assert isinstance(cap, dict)
        assert "current" in cap
        current = cap["current"]
        assert current["pids"] == 512
        assert current["fds"] == 2048
        assert current["conntrack"] == 128
        assert current["tap_devices"] == 2
        assert current["arp_entries"] == 5
        assert cap["recommended_max_vms"] == 10
        assert cap["limiting_resource"] == "memory"

    def test_build_info_dict_setup_section(self, mocker):
        """to_dict() setup section contains initialized and initialized_at."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        setup = info_dict["setup"]
        assert isinstance(setup, dict)
        assert setup["initialized"] is True
        assert setup["initialized_at"] == "2026-01-01T12:00:00+00:00"

    def test_build_info_dict_detected_at(self, mocker):
        """to_dict() detected_at is populated from state."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        assert info_dict["detected_at"] == "2026-01-01T12:00:00+00:00"

    def test_build_info_dict_empty_detected_at(self, mocker):
        """to_dict() detected_at returns empty string when state has no detection."""
        state = self._make_state()
        state.detected_at = None
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()
        hw.hostname = "testhost"

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        assert info_dict["detected_at"] == ""

    def test_build_info_dict_os_section(self, mocker):
        """to_dict() os section contains kernel and release."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        os_section = info_dict["os"]
        assert isinstance(os_section, dict)
        assert os_section["kernel"] == "6.8.0"
        assert os_section["release"] == "TestOS 1.0"

    def test_build_info_dict_storage_section(self, mocker):
        """to_dict() storage section contains total_bytes and free_bytes."""
        state = self._make_state()
        hw = self._make_hardware()
        limits = self._make_limits()
        resources = self._make_resources()

        info_dict = HostInfo(state=state, resources=resources, limits=limits, hardware=hw).to_dict()
        storage = info_dict["storage"]
        assert isinstance(storage, dict)
        assert storage["total_bytes"] == 500_000_000_000
        assert storage["free_bytes"] == 200_000_000_000


class TestHostOperationInfo:
    """Tests for HostOperation.info() — host info retrieval."""

    def test_info_returns_error_when_no_state(self, mocker):
        """info() returns error when no host state exists."""
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
        )
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = None
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )

        result = HostOperation.info()
        assert result.status == "error"
        assert result.code == "host.info.no_state"

    def test_info_returns_success_with_info_dict(self, mocker):
        """info() returns success with nested info dict when state exists."""
        state = HostStateItem(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-01-01T12:00:00+00:00",
            updated_at="2026-01-01T12:00:00+00:00",
            hostname="testhost",
            cpu_model="Test CPU",
            cpu_vendor="intel",
            cpu_cores=8,
            cpu_architecture="x86_64",
            numa_nodes=2,
            memory_total_mib=32000,
            storage_total_bytes=500_000_000_000,
            kernel_version="6.8.0",
            os_release="TestOS 1.0",
            pid_max=4194304,
            fd_max=9223372036854775807,
            conntrack_max=262144,
            tap_devices_max=-1,
            ip_local_port_range="32768,60999",
            detected_at="2026-01-01T12:00:00+00:00",
        )
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = state
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_cache_dir",
            return_value=Path("/tmp/cache"),
        )
        mock_detector = mocker.patch(
            "mvmctl.api.host_operations.HostDetector.detect_resources",
            return_value=HostResources(
                memory_available_mib=8192,
                tap_devices_used=2,
                pids_current=512,
                fd_current=2048,
                conntrack_current=128,
                arp_current=5,
                storage_free_bytes=200_000_000_000,
                recommended_max_vms=10,
                limiting_resource="memory",
            ),
        )

        result = HostOperation.info()
        assert result.status == "success"
        assert result.code == "host.info"
        assert result.item is not None
        info_dict = result.item
        assert info_dict["hostname"] == "testhost"
        assert info_dict["capacity"]["recommended_max_vms"] == 10
        mock_detector.assert_called_once()

    def test_info_auto_detects_when_no_hardware(self, mocker):
        """info() auto-detects hardware when state has no cpu_model."""
        state = HostStateItem(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-01-01T12:00:00+00:00",
            updated_at="2026-01-01T12:00:00+00:00",
        )
        hardware = HostHardware(
            hostname="testhost",
            cpu_model="Auto CPU",
            cpu_vendor="intel",
            cpu_cores=4,
            cpu_architecture="x86_64",
            numa_nodes=1,
            memory_total_mib=16000,
            storage_total_bytes=250_000_000_000,
            kernel_version="6.8.0",
            os_release="AutoOS",
        )
        limits = HostLimits(
            pid_max=32768, fd_max=100000, conntrack_max=65536,
            tap_devices_max=-1, ip_local_port_range=(32768, 60999),
        )

        mock_repo = MagicMock()
        mock_repo.get_state.side_effect = [state, state]
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mock_detect = mocker.patch(
            "mvmctl.api.host_operations.HostService.detect_and_save_capacity",
            return_value=(hardware, limits),
        )
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_cache_dir",
            return_value=Path("/tmp/cache"),
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostDetector.detect_resources",
            return_value=HostResources(
                memory_available_mib=4096,
                tap_devices_used=0,
                pids_current=100,
                fd_current=500,
                conntrack_current=32,
                arp_current=0,
                storage_free_bytes=100_000_000_000,
                recommended_max_vms=5,
                limiting_resource="cpu",
            ),
        )

        result = HostOperation.info()
        assert result.status == "success"
        assert result.code == "host.info"
        mock_detect.assert_called_once()

    def test_info_detect_fails(self, mocker):
        """info() returns error when auto-detection fails."""
        state = HostStateItem(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-01-01T12:00:00+00:00",
            updated_at="2026-01-01T12:00:00+00:00",
        )
        mock_repo = MagicMock()
        mock_repo.get_state.side_effect = [state, None]  # First call returns state, second returns None
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.detect_and_save_capacity",
            return_value=(
                HostHardware("h", "m", "v", 1, "x86_64", 1, 1000, 1000, "6.8", "OS"),
                HostLimits(32768, 100000, 65536, -1, (32768, 60999)),
            ),
        )

        result = HostOperation.info()
        assert result.status == "error"
        assert result.code == "host.info.detect_failed"


class TestHostOperationRefreshCapacity:
    """Tests for HostOperation.refresh_capacity() — capacity re-detection."""

    def test_refresh_returns_success_with_info_dict(self, mocker):
        """refresh_capacity() returns info dict with refreshed values."""
        hardware = HostHardware(
            hostname="refreshhost",
            cpu_model="Refreshed CPU",
            cpu_vendor="intel",
            cpu_cores=8,
            cpu_architecture="x86_64",
            numa_nodes=1,
            memory_total_mib=64000,
            storage_total_bytes=1_000_000_000_000,
            kernel_version="6.10.0",
            os_release="NewOS 2.0",
        )
        limits = HostLimits(
            pid_max=4194304, fd_max=9223372036854775807,
            conntrack_max=262144, tap_devices_max=-1,
            ip_local_port_range=(32768, 60999),
        )
        state = HostStateItem(
            id=1,
            initialized=True,
            mvm_group_created=True,
            sudoers_configured=True,
            default_network_created=True,
            initialized_at="2026-01-01T12:00:00+00:00",
            updated_at="2026-01-01T12:00:00+00:00",
            hostname="refreshhost",
            cpu_model="Refreshed CPU",
            cpu_vendor="intel",
            cpu_cores=8,
            cpu_architecture="x86_64",
            numa_nodes=1,
            memory_total_mib=64000,
            storage_total_bytes=1_000_000_000_000,
            kernel_version="6.10.0",
            os_release="NewOS 2.0",
            pid_max=4194304,
            fd_max=9223372036854775807,
            conntrack_max=262144,
            tap_devices_max=-1,
            ip_local_port_range="32768,60999",
            detected_at="2026-06-01T12:00:00+00:00",
        )

        mock_repo = MagicMock()
        mock_repo.get_state.return_value = state
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.detect_and_save_capacity",
            return_value=(hardware, limits),
        )
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_cache_dir",
            return_value=Path("/tmp/cache"),
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostDetector.detect_resources",
            return_value=HostResources(
                memory_available_mib=32000,
                tap_devices_used=1,
                pids_current=300,
                fd_current=1500,
                conntrack_current=64,
                arp_current=2,
                storage_free_bytes=500_000_000_000,
                recommended_max_vms=20,
                limiting_resource="cpu",
            ),
        )

        result = HostOperation.refresh_capacity()
        assert result.status == "success"
        assert result.code == "host.capacity.refreshed"
        assert result.item is not None
        assert result.item["hostname"] == "refreshhost"

    def test_refresh_detect_failure(self, mocker):
        """refresh_capacity() returns error when detection fails."""
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.detect_and_save_capacity",
            side_effect=Exception("Detection failed"),
        )

        result = HostOperation.refresh_capacity()
        assert result.status == "error"
        assert result.code == "host.capacity.detect_failed"

    def test_refresh_no_state_after_detect(self, mocker):
        """refresh_capacity() returns error when state is None after detection."""
        hardware = HostHardware(
            hostname="h", cpu_model="m", cpu_vendor="v", cpu_cores=1,
            cpu_architecture="x86_64", numa_nodes=1, memory_total_mib=1000,
            storage_total_bytes=1000, kernel_version="6.8", os_release="OS",
        )
        limits = HostLimits(
            pid_max=32768, fd_max=100000, conntrack_max=65536,
            tap_devices_max=-1, ip_local_port_range=(32768, 60999),
        )
        mock_repo = MagicMock()
        mock_repo.get_state.return_value = None
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.detect_and_save_capacity",
            return_value=(hardware, limits),
        )

        result = HostOperation.refresh_capacity()
        assert result.status == "error"
        assert result.code == "host.info.no_state"
        assert "Failed to retrieve host state" in result.message


class TestHostOperationInit:
    """Tests for HostOperation.init() — the core host initialization flow."""

    def test_init_returns_needs_interaction_on_privilege_error(self, mocker):
        """init() returns NeedsInteraction when not root."""
        from mvmctl.exceptions import PrivilegeError

        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges",
            side_effect=PrivilegeError("not root"),
        )

        from mvmctl.models.result import NeedsInteraction

        result = HostOperation.init(Path("/tmp"))
        assert isinstance(result, NeedsInteraction)

    def test_init_calls_host_privilege_check(self, mocker):
        """init() checks privileges on /usr/sbin/ip."""
        mock_check = mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch("mvmctl.api.host_operations.Database")
        mocker.patch("mvmctl.api.host_operations.HostRepository")
        mocker.patch("mvmctl.api.host_operations.HostService")
        mocker.patch("mvmctl.api.host_operations.HostController")
        mocker.patch("mvmctl.api.host_operations.NetworkRepository")
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        # Mock the orchestration method that sequences HostService calls.
        # This avoids hitting real file/system state during the init flow.
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation._setup_host_environment",
            return_value=[],
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.detect_iptables_backend_conflict",
            return_value=(False, ""),
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_physical_interfaces",
            return_value=["eth0"],
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.detect_outbound_interface",
            return_value="eth0",
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch("mvmctl.api.host_operations.FsUtils")
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch("mvmctl.api.host_operations.AuditLog")
        mocker.patch("mvmctl.api.host_operations.HostService")

        HostOperation.init(Path("/tmp"))
        mock_check.assert_called_once_with("/usr/sbin/ip", "initialize host")


class TestHostOperationClean:
    """Tests for HostOperation.clean() — network cleanup orchestration."""

    def test_clean_calls_privilege_check(self, mocker):
        """clean() checks privileges before cleaning."""
        mock_check = mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch("mvmctl.api.host_operations.NetworkUtils")
        mocker.patch("mvmctl.api.host_operations.NetworkRepository")
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mocker.patch("mvmctl.api.host_operations.SettingsService.resolve")
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        HostOperation.clean(Path("/tmp"))
        mock_check.assert_called_once_with("/usr/sbin/ip", "clean host")

    def test_clean_returns_summary_list(self, mocker):
        """clean() returns a summary list of actions."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_handles_no_networks(self, mocker):
        """clean() works when no networks exist."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mock_audit = mocker.patch("mvmctl.api.host_operations.AuditLog")

        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        mock_audit.log.assert_called_once()

    def test_clean_handles_missing_database_table(self, mocker):
        """clean() handles missing database table gracefully via decorator."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mock_repo = MagicMock()
        # Simulate missing table — decorator returns []
        mock_repo.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")

        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        mock_repo.list_all.assert_called_once()


class TestHostOperationReset:
    """Tests for HostOperation.reset() — full host reset."""

    def test_reset_calls_clean_then_restore(self, mocker):
        """reset() calls clean() then restores state."""
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=OperationResult(
                status="success", code="ok", item=["Cleaned TAPs"]
            ),
        )
        mock_repo = MagicMock()
        mock_repo.list_changes.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository",
            return_value=mock_repo,
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog")
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )

        result = HostOperation.reset(Path("/tmp"))
        assert "Cleaned TAPs" in result.item
        mock_service.restore_state.assert_called_once()
