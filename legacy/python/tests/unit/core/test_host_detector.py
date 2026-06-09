"""Unit tests for HostDetector — host hardware, limits, and resource detection.

All tests mock filesystem paths (/proc, /sys, /etc) using monkeypatch
so they work on any machine without side effects.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.host._detector import HostDetector
from mvmctl.models.host import HostHardware, HostLimits, HostResources

# ===========================================================================
# detect_hardware
# ===========================================================================


class TestDetectHardware:
    """Tests for HostDetector.detect_hardware()."""

    CPUINFO_BASIC = (
        "model name	: Intel(R) Core(TM) i7-10750H CPU @ 2.60GHz\n"
        "vendor_id	: GenuineIntel\n"
        "flags		: fpu vmx ssse3 hypervisor\n"
        "\n"
    )

    def _patch_hardware_common(
        self,
        monkeypatch: pytest.MonkeyPatch,
        read_text_map: dict[str, str] | None = None,
    ) -> None:
        """Apply common mocks needed by detect_hardware tests."""
        monkeypatch.setattr("socket.gethostname", lambda: "testhost")
        monkeypatch.setattr("platform.machine", lambda: "x86_64")
        monkeypatch.setattr("os.cpu_count", lambda: 8)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0-arch1-1"})(),
        )

        if read_text_map is None:
            read_text_map = {}
        # Fill in defaults for paths not in the map
        defaults = {
            "/proc/cpuinfo": self.CPUINFO_BASIC,
            "/etc/os-release": 'PRETTY_NAME="Arch Linux"\n',
            "/proc/meminfo": "MemTotal:       16384000 kB\n",
            "/proc/sys/net/ipv4/ip_local_port_range": "32768 60999\n",
        }
        for k, v in defaults.items():
            read_text_map.setdefault(k, v)

        def _mock_read_text(p: Path) -> str:
            path_str = str(p)
            if path_str in read_text_map:
                val = read_text_map[path_str]
                if isinstance(val, type):
                    raise val(path_str)
                return val
            return ""

        monkeypatch.setattr(Path, "read_text", _mock_read_text)

        # Mock /sys/devices/system/node for NUMA detection
        def _fake_iterdir(p: Path) -> list[Path]:
            if str(p) == "/sys/devices/system/node":
                return [Path("/sys/devices/system/node/node0")]
            return []

        monkeypatch.setattr(Path, "iterdir", _fake_iterdir)

        # Mock disk usage
        monkeypatch.setattr(
            "shutil.disk_usage",
            lambda _path: type("Usage", (), {"total": 500_000_000_000})(),
        )

        # Mock CacheUtils.get_cache_dir
        monkeypatch.setattr(
            "mvmctl.utils.common.CacheUtils.get_cache_dir",
            lambda: Path("/tmp/cache"),
        )

    def test_basic_structure(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """detect_hardware returns a HostHardware with expected fields."""
        self._patch_hardware_common(monkeypatch)

        hw = HostDetector.detect_hardware()
        assert isinstance(hw, HostHardware)
        assert hw.hostname == "testhost"
        assert hw.cpu_model == "Intel(R) Core(TM) i7-10750H CPU @ 2.60GHz"
        assert hw.cpu_vendor == "intel"
        assert hw.cpu_cores == 8
        assert hw.cpu_architecture == "x86_64"
        assert hw.numa_nodes == 1
        assert hw.memory_total_mib > 0
        assert hw.kernel_version == "6.8.0-arch1-1"
        assert hw.os_release == "Arch Linux"
        # New virtualization fields
        assert hw.cpu_has_vmx is True  # "vmx" in flags
        assert hw.cpu_hypervisor is True  # "hypervisor" in flags

    def test_no_virtualization_flags(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """When cpuinfo has no flags line, cpu_has_vmx and cpu_hypervisor are False."""
        cpuinfo = (
            "model name	: Generic CPU\n"
            "vendor_id	: GenuineIntel\n"
        )
        self._patch_hardware_common(monkeypatch, {
            "/proc/cpuinfo": cpuinfo,
        })

        hw = HostDetector.detect_hardware()
        assert hw.cpu_has_vmx is False
        assert hw.cpu_hypervisor is False

    def test_vmx_without_hypervisor(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """CPU has VMX but is not a hypervisor guest."""
        cpuinfo = (
            "model name	: Intel Xeon\n"
            "vendor_id	: GenuineIntel\n"
            "flags		: fpu vmx ssse3\n"
        )
        self._patch_hardware_common(monkeypatch, {
            "/proc/cpuinfo": cpuinfo,
        })

        hw = HostDetector.detect_hardware()
        assert hw.cpu_has_vmx is True
        assert hw.cpu_hypervisor is False

    def test_unknown_vendor(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Unmapped vendor_id falls through as-is."""
        cpuinfo = "vendor_id	: HygonGenuine\n"
        self._patch_hardware_common(monkeypatch, {
            "/proc/cpuinfo": cpuinfo,
            "/etc/os-release": 'ID="ubuntu"\nVERSION_ID="24.04"\n',
            "/proc/meminfo": "MemTotal:       8192000 kB\n",
        })

        hw = HostDetector.detect_hardware()
        assert hw.cpu_vendor == "HygonGenuine"

    def test_missing_cpuinfo_fallback(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """When /proc/cpuinfo is missing, fallbacks are used."""
        self._patch_hardware_common(monkeypatch, {
            "/proc/cpuinfo": FileNotFoundError,
            "/etc/os-release": 'ID="ubuntu"\nVERSION_ID="24.04"\n',
            "/proc/meminfo": "MemTotal:       4096000 kB\n",
        })

        hw = HostDetector.detect_hardware()
        assert hw.hostname == "testhost"
        # cpu_model falls back to architecture
        assert hw.cpu_model == "x86_64"
        assert hw.cpu_vendor == "unknown"
        assert hw.cpu_has_vmx is False

    def test_os_release_fallback_id_version(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """When PRETTY_NAME is absent, ID+VERSION_ID are concatenated."""
        cpuinfo = "vendor_id	: GenuineIntel\nmodel name	: Intel Xeon\n"
        self._patch_hardware_common(monkeypatch, {
            "/proc/cpuinfo": cpuinfo,
            "/etc/os-release": 'ID="debian"\nVERSION_ID="12"\n',
            "/proc/meminfo": "MemTotal:       4096000 kB\n",
        })

        hw = HostDetector.detect_hardware()
        assert hw.os_release == "debian 12"


# ===========================================================================
# detect_limits
# ===========================================================================


class TestDetectLimits:
    """Tests for HostDetector.detect_limits()."""

    def _mock_read_int(
        self,
        monkeypatch: pytest.MonkeyPatch,
        values: dict[str, int] | None = None,
    ) -> None:
        """Mock _read_int with specified values, defaulting to basic set."""
        if values is None:
            values = {
                "/proc/sys/kernel/pid_max": 4194304,
                "/proc/sys/fs/file-max": 9223372036854775807,
                "/proc/sys/net/netfilter/nf_conntrack_max": 262144,
                "/sys/module/tun/parameters/max_tap_devices": 0,
                "/sys/module/kvm_intel/parameters/nested": 0,
                "/sys/module/kvm_amd/parameters/nested": 0,
                "/sys/module/kvm_intel/parameters/ept": 0,
                "/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages": 0,
                "/sys/kernel/mm/ksm/run": 0,
            }

        def _mock_read_int_impl(path: str, default: int = 0) -> int:
            return values.get(path, default)

        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._read_int",
            _mock_read_int_impl,
        )
        # Default: cgroup v1 (no cgroup.controllers)
        monkeypatch.setattr(Path, "exists", lambda p: False)
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._meminfo_kb_to_mib",
            lambda _key: 0,
        )
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._check_kernel_version",
            lambda: False,
        )

    def test_basic_structure(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """detect_limits returns a HostLimits with expected fields."""
        self._mock_read_int(monkeypatch)
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "32768 60999\n"
            if str(p) == "/proc/sys/net/ipv4/ip_local_port_range"
            else "",
        )

        limits = HostDetector.detect_limits()
        assert isinstance(limits, HostLimits)
        assert limits.pid_max == 4194304
        assert limits.fd_max == 9223372036854775807
        assert limits.conntrack_max == 262144
        assert limits.tap_devices_max == -1  # 0 → unlimited (-1)
        assert limits.ip_local_port_range == (32768, 60999)
        # New virtualization/limits fields
        assert limits.nested_virt_available is False
        assert limits.ept_available is False
        assert limits.hugepage_count_2mb == 0
        assert limits.ksm_disabled is True  # ksm/run == 0
        assert limits.cgroup_version == 1  # no cgroup.controllers
        assert limits.swap_total_mib == 0
        assert limits.kernel_minimum_met is False

    def test_with_nested_virt_and_ept(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """detect_limits returns True for nested_virt_available and ept when supported."""
        self._mock_read_int(monkeypatch, {
            "/proc/sys/kernel/pid_max": 32768,
            "/proc/sys/fs/file-max": 100000,
            "/proc/sys/net/netfilter/nf_conntrack_max": 65536,
            "/sys/module/tun/parameters/max_tap_devices": 512,
            "/sys/module/kvm_intel/parameters/nested": 1,
            "/sys/module/kvm_amd/parameters/nested": 0,
            "/sys/module/kvm_intel/parameters/ept": 1,
            "/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages": 64,
            "/sys/kernel/mm/ksm/run": 1,
        })
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "10000 65535\n"
            if str(p) == "/proc/sys/net/ipv4/ip_local_port_range"
            else "",
        )
        monkeypatch.setattr(Path, "exists", lambda p: True)  # cgroup v2
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._meminfo_kb_to_mib",
            lambda key: 4096 if key == "SwapTotal" else 0,
        )
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._check_kernel_version",
            lambda: True,
        )

        limits = HostDetector.detect_limits()
        assert limits.nested_virt_available is True
        assert limits.ept_available is True
        assert limits.hugepage_count_2mb == 64
        assert limits.ksm_disabled is False  # ksm/run == 1
        assert limits.cgroup_version == 2  # cgroup.controllers exists
        assert limits.swap_total_mib == 4096  # mock returns 4096 MiB directly
        assert limits.kernel_minimum_met is True
        assert limits.tap_devices_max == 512

    def test_read_int_defaults(self) -> None:
        """_read_int returns default when file is missing."""
        with patch("pathlib.Path.read_text", side_effect=FileNotFoundError):
            assert HostDetector._read_int("/proc/nonexistent", 42) == 42
            assert HostDetector._read_int("/proc/nonexistent") == 0

    def test_read_int_invalid_content(self) -> None:
        """_read_int returns default when content is not an integer."""
        with patch("pathlib.Path.read_text", return_value="not a number\n"):
            assert HostDetector._read_int("/proc/some/file", 99) == 99

    def test_ip_local_port_range_default_on_error(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """detect_limits uses DEFAULT_IP_LOCAL_PORT_RANGE on read error."""
        self._mock_read_int(monkeypatch)
        with patch("pathlib.Path.read_text", side_effect=FileNotFoundError):
            limits = HostDetector.detect_limits()
        assert limits.ip_local_port_range == (32768, 60999)

    def test_tap_devices_positive(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """tap_devices_max preserves positive values."""
        self._mock_read_int(monkeypatch, {
            "/proc/sys/kernel/pid_max": 32768,
            "/proc/sys/fs/file-max": 100000,
            "/proc/sys/net/netfilter/nf_conntrack_max": 262144,
            "/sys/module/tun/parameters/max_tap_devices": 512,
            "/sys/module/kvm_intel/parameters/nested": 0,
            "/sys/module/kvm_amd/parameters/nested": 0,
            "/sys/module/kvm_intel/parameters/ept": 0,
            "/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages": 0,
            "/sys/kernel/mm/ksm/run": 0,
        })
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "32768 60999\n"
            if str(p) == "/proc/sys/net/ipv4/ip_local_port_range"
            else "",
        )

        limits = HostDetector.detect_limits()
        assert limits.tap_devices_max == 512


# ===========================================================================
# detect_resources
# ===========================================================================


class TestDetectResources:
    """Tests for HostDetector.detect_resources()."""

    def _make_hardware(self, **overrides: object) -> HostHardware:
        defaults: dict[str, object] = {
            "hostname": "test",
            "cpu_model": "Test CPU",
            "cpu_vendor": "intel",
            "cpu_cores": 8,
            "cpu_architecture": "x86_64",
            "numa_nodes": 1,
            "memory_total_mib": 16000,
            "storage_total_bytes": 500_000_000_000,
            "kernel_version": "6.8.0",
            "os_release": "TestOS 1.0",
            "cpu_has_vmx": True,
            "cpu_hypervisor": False,
        }
        defaults.update(overrides)
        return HostHardware(**defaults)  # type: ignore[arg-type]

    def _make_limits(self, **overrides: object) -> HostLimits:
        defaults: dict[str, object] = {
            "pid_max": 4194304,
            "fd_max": 9223372036854775807,
            "conntrack_max": 262144,
            "tap_devices_max": -1,
            "ip_local_port_range": (32768, 60999),
            "nested_virt_available": False,
            "ept_available": False,
            "hugepage_count_2mb": 0,
            "ksm_disabled": True,
            "cgroup_version": 2,
            "swap_total_mib": 0,
            "kernel_minimum_met": True,
        }
        defaults.update(overrides)
        return HostLimits(**defaults)  # type: ignore[arg-type]

    def _patch_resources_common(
        self,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Apply common mocks for detect_resources tests."""
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._meminfo_kb_to_mib",
            lambda key: {
                "MemAvailable": 8192,
                "SwapTotal": 0,
                "SwapFree": 0,
            }.get(key, 0),
        )
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._read_int",
            lambda path, default=0: {
                "/proc/sys/fs/file-nr": 1024,
                "/proc/sys/net/netfilter/nf_conntrack_count": 64,
                "/sys/kernel/mm/hugepages/hugepages-2048kB/free_hugepages": 0,
                "/sys/devices/system/cpu/smt/active": 0,
            }.get(path, default),
        )
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._parse_modules",
            lambda: {},
        )
        # Mock /sys/class/net to have one TAP device
        def _fake_iterdir(path: Path) -> list[Path]:
            if str(path) == "/sys/class/net":
                return [
                    Path("/sys/class/net/eth0"),
                    Path("/sys/class/net/mvm-tap0"),
                ]
            if str(path) == "/proc":
                return [Path(str(p)) for p in [Path("/proc/1"), Path("/proc/2")]]
            return []

        monkeypatch.setattr(Path, "iterdir", _fake_iterdir)

        def _fake_exists(path: Path) -> bool:
            path_str = str(path)
            if path_str.endswith("/tun_flags") and "mvm-tap" in path_str:
                return True
            if path_str in ("/dev/kvm", "/dev/net/tun"):
                return True
            return False

        monkeypatch.setattr(Path, "exists", _fake_exists)

        # Mock /proc/net/arp
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "IP address       HW type     Flags       HW address            Mask     Device\n"
            "192.168.1.1      0x1         0x2         00:11:22:33:44:55     *        eth0\n"
            if str(p) == "/proc/net/arp"
            else "",
        )
        # Mock disk usage
        monkeypatch.setattr(
            "shutil.disk_usage",
            lambda _path: type("Usage", (), {"free": 200_000_000_000})(),
        )
        # Mock binary availability — none available by default
        monkeypatch.setattr("shutil.which", lambda _cmd: None)
        # Mock /dev/kvm accessibility and os.access
        monkeypatch.setattr("os.access", lambda _path, _mode: True)
        # Mock grp/pwd
        monkeypatch.setattr("grp.getgrnam", lambda _name: type(
            "Grp", (), {"gr_mem": []}
        )())
        monkeypatch.setattr("pwd.getpwuid", lambda _uid: type(
            "Pwd", (), {"pw_name": "testuser"}
        )())

    def test_basic_structure(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """detect_resources returns a HostResources with expected fields."""
        self._patch_resources_common(monkeypatch)

        hw = self._make_hardware()
        limits = self._make_limits()
        resources = HostDetector.detect_resources(hw, limits, Path("/tmp/vms"))

        assert isinstance(resources, HostResources)
        assert resources.memory_available_mib == 8192
        assert resources.tap_devices_used == 1  # mvm-tap0 has tun_flags
        assert resources.pids_current == 2  # /proc/1, /proc/2
        assert resources.fd_current == 1024
        assert resources.conntrack_current == 64
        assert resources.arp_current == 1  # 2 lines - 1 header
        assert resources.storage_free_bytes == 200_000_000_000
        assert resources.recommended_max_vms >= 0

        # New fields — modules_loaded always contains all keys even if empty
        assert isinstance(resources.modules_loaded, dict)
        assert all(v is False for v in resources.modules_loaded.values())
        assert resources.swap_used_mib == 0
        assert resources.hugepages_free_2mb == 0
        assert resources.smt_active is False
        assert resources.nftables_available is False
        assert resources.iptables_available is False
        assert resources.cloud_localds_available is False
        assert resources.dev_kvm_status == "ok"
        assert resources.user_in_kvm_group is False
        assert resources.dev_net_tun_accessible is True

    def test_recommended_max_vms_computation(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """recommended_max_vms is computed from limiting resource."""
        self._patch_resources_common(monkeypatch)

        # Override meminfo to have lots of memory
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._meminfo_kb_to_mib",
            lambda key: {
                "MemAvailable": 32000,
                "SwapTotal": 0,
                "SwapFree": 0,
            }.get(key, 0),
        )
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._read_int",
            lambda path, default=0: {
                "/proc/sys/fs/file-nr": 500,
                "/proc/sys/net/netfilter/nf_conntrack_count": 32,
                "/sys/kernel/mm/hugepages/hugepages-2048kB/free_hugepages": 0,
                "/sys/devices/system/cpu/smt/active": 0,
            }.get(path, default),
        )

        # Override iterdir for minimal /proc
        def _minimal_iterdir(path: Path) -> list[Path]:
            if str(path) == "/sys/class/net":
                return [Path("/sys/class/net/eth0")]
            if str(path) == "/proc":
                return [Path("/proc/1")]
            return []

        monkeypatch.setattr(Path, "iterdir", _minimal_iterdir)
        monkeypatch.setattr(Path, "exists", lambda p: False)

        hw = self._make_hardware(cpu_cores=4)
        limits = self._make_limits(
            pid_max=32768, tap_devices_max=16, conntrack_max=256,
        )
        resources = HostDetector.detect_resources(hw, limits, Path("/tmp/vms"))

        # cpu: 4 cores - 1 reserve = 3
        # memory: (32000 - 2048) // (50 + 512) = 29952 // 562 ≈ 53
        # tap: 16 - 0 = 16
        # pids: (32768 - 200) // 3 ≈ 10856
        # conntrack: 256 // 64 = 4
        assert resources.recommended_max_vms == 3  # CPU-limited
        assert resources.limiting_resource == "cpu"

    def test_empty_sys_class_net(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Handles empty /sys/class/net gracefully."""
        self._patch_resources_common(monkeypatch)

        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._meminfo_kb_to_mib",
            lambda key: {
                "MemAvailable": 4096,
                "SwapTotal": 0,
                "SwapFree": 0,
            }.get(key, 0),
        )
        monkeypatch.setattr(
            "mvmctl.core.host._detector.HostDetector._read_int",
            lambda path, default=0: {
                "/proc/sys/fs/file-nr": 256,
                "/proc/sys/net/netfilter/nf_conntrack_count": 0,
                "/sys/kernel/mm/hugepages/hugepages-2048kB/free_hugepages": 0,
                "/sys/devices/system/cpu/smt/active": 0,
            }.get(path, default),
        )

        def _empty_iterdir(path: Path) -> list[Path]:
            if str(path) in ("/sys/class/net", "/proc"):
                return []
            return []

        monkeypatch.setattr(Path, "iterdir", _empty_iterdir)
        monkeypatch.setattr(Path, "exists", lambda p: False)
        # Override ARP mock to return empty (no ARP entries)
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "" if str(p) == "/proc/net/arp" else "",
        )

        hw = self._make_hardware(cpu_cores=2)
        limits = self._make_limits(tap_devices_max=-1)
        resources = HostDetector.detect_resources(hw, limits, Path("/tmp/vms"))

        assert resources.tap_devices_used == 0
        assert resources.pids_current == 0
        assert resources.arp_current == 0


# ===========================================================================
# _meminfo_kb_to_mib
# ===========================================================================


class TestMeminfoKbToMib:
    """Tests for _meminfo_kb_to_mib helper."""

    def test_parses_meminfo_line(self) -> None:
        """Parses a valid /proc/meminfo line correctly."""
        with patch(
            "pathlib.Path.read_text",
            return_value="MemTotal:       16384000 kB\nMemFree:        8000000 kB\n",
        ):
            result = HostDetector._meminfo_kb_to_mib("MemTotal")
            assert result == 16000  # 16384000 / 1024

    def test_key_not_found(self) -> None:
        """Returns 0 when the key is not in /proc/meminfo."""
        with patch(
            "pathlib.Path.read_text",
            return_value="MemTotal: 16384000 kB\n",
        ):
            result = HostDetector._meminfo_kb_to_mib("SwapTotal")
            assert result == 0

    def test_file_not_found(self) -> None:
        """Returns 0 when /proc/meminfo cannot be read."""
        with patch("pathlib.Path.read_text", side_effect=FileNotFoundError):
            result = HostDetector._meminfo_kb_to_mib("MemTotal")
            assert result == 0

    def test_malformed_line(self) -> None:
        """Returns 0 when the value cannot be parsed."""
        with patch("pathlib.Path.read_text", return_value="MemTotal: not_a_number\n"):
            result = HostDetector._meminfo_kb_to_mib("MemTotal")
            assert result == 0


# ===========================================================================
# _check_kernel_version
# ===========================================================================


class TestCheckKernelVersion:
    """Tests for _check_kernel_version."""

    def test_meets_minimum(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Kernel 6.8.0 meets minimum 5.10."""
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0"})(),
        )
        assert HostDetector._check_kernel_version() is True

    def test_below_minimum(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Kernel 4.19 is below minimum 5.10."""
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "4.19.0"})(),
        )
        assert HostDetector._check_kernel_version() is False

    def test_exactly_minimum(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Kernel 5.10 exactly meets minimum."""
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "5.10.0"})(),
        )
        assert HostDetector._check_kernel_version() is True

    def test_unparseable_release(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Unparseable release string returns False."""
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "rolling"})(),
        )
        assert HostDetector._check_kernel_version() is False


# ===========================================================================
# _parse_modules
# ===========================================================================


class TestParseModules:
    """Tests for _parse_modules."""

    def test_parses_proc_modules(self) -> None:
        """_parse_modules returns dict from /proc/modules content."""
        with patch(
            "pathlib.Path.read_text",
            return_value="kvm 12345 1 kvm_intel, Live 0x...\n"
            "kvm_intel 54321 0 Live 0x...\n"
            "tun 9876 0 Live 0x...\n",
        ):
            modules = HostDetector._parse_modules()
            assert modules == {"kvm": True, "kvm_intel": True, "tun": True}

    def test_returns_empty_on_error(self) -> None:
        """_parse_modules returns empty dict when file can't be read."""
        with patch("pathlib.Path.read_text", side_effect=FileNotFoundError):
            assert HostDetector._parse_modules() == {}

    def test_empty_file(self) -> None:
        """_parse_modules returns empty dict for empty file."""
        with patch("pathlib.Path.read_text", return_value=""):
            assert HostDetector._parse_modules() == {}


__all__: list[str] = []
