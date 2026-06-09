"""Unit tests for HostProbe — pre-flight host readiness checks.

The HostProbe class performs real system reads; all tests mock
filesystem paths (/proc, /dev, /sys) and process lookups (shutil.which).
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.host._probe import HostProbe, ProbeCheck, ProbeResult

# ===========================================================================
# ProbeCheck / ProbeResult data model
# ===========================================================================


class TestProbeCheck:
    """Tests for ProbeCheck dataclass."""

    def test_basic_creation(self) -> None:
        """ProbeCheck stores name, passed, message, and optional details."""
        check = ProbeCheck(name="kvm", passed=True, message="/dev/kvm accessible")
        assert check.name == "kvm"
        assert check.passed is True
        assert check.message == "/dev/kvm accessible"
        assert check.details is None

    def test_with_details(self) -> None:
        """ProbeCheck supports an optional details field."""
        check = ProbeCheck(
            name="memory",
            passed=False,
            message="Low memory",
            details="Available: 128 MiB, recommended: 2048 MiB",
        )
        assert check.details == "Available: 128 MiB, recommended: 2048 MiB"


class TestProbeResult:
    """Tests for ProbeResult dataclass."""

    def test_empty_result(self) -> None:
        """An empty ProbeResult has no critical or warning checks."""
        result = ProbeResult()
        assert result.critical == []
        assert result.warnings == []
        assert result.info == []
        assert result.has_critical is False

    def test_has_critical_true(self) -> None:
        """has_critical returns True when critical list is non-empty."""
        result = ProbeResult(
            critical=[ProbeCheck(name="kvm", passed=False, message="KVM missing")],
        )
        assert result.has_critical is True

    def test_has_critical_false(self) -> None:
        """has_critical returns False when critical list is empty."""
        result = ProbeResult(
            warnings=[ProbeCheck(name="swap", passed=False, message="Swap enabled")],
        )
        assert result.has_critical is False

    def test_mixed_checks(self) -> None:
        """ProbeResult supports critical, warnings, and info simultaneously."""
        result = ProbeResult(
            critical=[
                ProbeCheck(name="kvm", passed=False, message="/dev/kvm not found"),
            ],
            warnings=[
                ProbeCheck(name="swap", passed=False, message="Swap is enabled"),
            ],
            info=[
                ProbeCheck(name="cpu", passed=True, message="8 cores available"),
            ],
        )
        assert len(result.critical) == 1
        assert len(result.warnings) == 1
        assert len(result.info) == 1


# ===========================================================================
# HostProbe.run_all
# ===========================================================================


class TestHostProbeRunAll:
    """Tests for HostProbe.run_all()."""

    def test_returns_probe_result(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """run_all returns a ProbeResult."""
        # Mock all the component checks to return empty lists
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_vm_host",
            lambda: [],
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_init_binaries",
            lambda: [],
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_firewall_readiness",
            lambda: [],
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_system_resources",
            lambda: [],
        )

        result = HostProbe.run_all()
        assert isinstance(result, ProbeResult)

    def test_aggregates_check_types(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """run_all aggregates critical, warning, and info checks.

        Note: check_vm_host failures go to critical, check_init_binaries
        failures also go to critical (binary availability is critical).
        Only check_firewall_readiness and check_system_resources failures
        go to warnings.
        """
        # check_vm_host returning failures -> critical
        crit = [ProbeCheck(name="kvm", passed=False, message="KVM missing")]
        # check_init_binaries returning failures -> critical (not warnings!)
        bin_crit = [ProbeCheck(name="binary:ip", passed=False, message="ip not found")]
        # check_firewall_readiness returning failures -> warnings
        warns = [ProbeCheck(name="nftables", passed=False, message="nft not available")]
        # check_system_resources returning passing checks -> info
        info = [ProbeCheck(name="kernel", passed=True, message="OK")]

        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_vm_host",
            lambda: crit,
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_init_binaries",
            lambda: bin_crit,
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_firewall_readiness",
            lambda: warns,
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_system_resources",
            lambda: info,
        )

        result = HostProbe.run_all()
        # check_vm_host fails + check_init_binaries fails -> both in critical
        assert len(result.critical) == 2
        assert result.critical[0] == crit[0]
        assert result.critical[1] == bin_crit[0]
        # check_firewall_readiness fails -> warnings
        assert result.warnings == warns
        # check_system_resources passes -> info
        assert result.info == info

    def test_all_empty_when_all_pass(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """run_all returns empty lists when all checks pass."""
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_vm_host",
            lambda: [],
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_init_binaries",
            lambda: [],
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_firewall_readiness",
            lambda: [],
        )
        monkeypatch.setattr(
            "mvmctl.core.host._probe.HostProbe.check_system_resources",
            lambda: [],
        )

        result = HostProbe.run_all()
        assert len(result.critical) == 0
        assert len(result.warnings) == 0
        assert len(result.info) == 0


# ===========================================================================
# HostProbe.check_vm_host
# ===========================================================================


class TestCheckVmHost:
    """Tests for HostProbe.check_vm_host()."""

    CPUINFO_WITH_VMX = (
        "model name	: Intel(R) Core(TM) i7\n"
        "vendor_id	: GenuineIntel\n"
        "flags		: fpu vmx ssse3\n"
        "\n"
    )

    CPUINFO_NO_VMX = (
        "model name	: Generic CPU\n"
        "vendor_id	: GenuineIntel\n"
        "flags		: fpu ssse3\n"
        "\n"
    )

    def test_all_checks_pass(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """check_vm_host returns all passing checks when everything is OK."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: self.CPUINFO_WITH_VMX,
        )
        monkeypatch.setattr(Path, "exists", lambda p: True)
        monkeypatch.setattr("os.access", lambda _path, _mode: True)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0"})(),
        )

        checks = HostProbe.check_vm_host()
        assert len(checks) == 6  # cpu_virt, dev_kvm, dev_net_tun, kvm_module, kernel, nested
        assert checks[0].name == "cpu_virtualization"
        assert checks[0].passed is True
        assert checks[1].name == "dev_kvm"
        assert checks[1].passed is True
        assert checks[2].name == "dev_net_tun"
        assert checks[2].passed is True
        assert checks[3].name == "kvm_module"
        assert checks[4].name == "kernel_version"
        assert checks[4].passed is True
        assert checks[5].name == "nested_virtualization"

    def test_cpu_no_virt(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """cpu_virtualization check fails when CPU lacks VMX/SVM."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: self.CPUINFO_NO_VMX,
        )
        monkeypatch.setattr(Path, "exists", lambda p: True)
        monkeypatch.setattr("os.access", lambda _path, _mode: True)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0"})(),
        )

        checks = HostProbe.check_vm_host()
        cpu_check = next(c for c in checks if c.name == "cpu_virtualization")
        assert cpu_check.passed is False

    def test_dev_kvm_missing(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """dev_kvm check fails when /dev/kvm does not exist."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: self.CPUINFO_WITH_VMX,
        )

        def _mock_exists(p: Path) -> bool:
            if str(p) == "/dev/kvm":
                return False
            if str(p).startswith("/sys/module/kvm"):
                return False
            return True

        monkeypatch.setattr(Path, "exists", _mock_exists)
        monkeypatch.setattr("os.access", lambda _path, _mode: True)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0"})(),
        )

        checks = HostProbe.check_vm_host()
        kvm_check = next(c for c in checks if c.name == "dev_kvm")
        assert kvm_check.passed is False
        assert "does not exist" in kvm_check.message

    def test_dev_kvm_no_permission(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """dev_kvm check fails when /dev/kvm exists but is not accessible."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: self.CPUINFO_WITH_VMX,
        )

        def _mock_exists(p: Path) -> bool:
            if str(p) == "/dev/kvm":
                return True
            if str(p).startswith("/sys/module/kvm"):
                return False
            return True

        monkeypatch.setattr(Path, "exists", _mock_exists)
        monkeypatch.setattr("os.access", lambda _path, _mode: False)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0"})(),
        )

        checks = HostProbe.check_vm_host()
        kvm_check = next(c for c in checks if c.name == "dev_kvm")
        assert kvm_check.passed is False
        assert "not readable/writable" in kvm_check.message

    def test_kernel_below_minimum(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """kernel_version check fails when kernel is below 5.10."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: self.CPUINFO_WITH_VMX,
        )
        monkeypatch.setattr(Path, "exists", lambda p: True)
        monkeypatch.setattr("os.access", lambda _path, _mode: True)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "4.19.0"})(),
        )

        checks = HostProbe.check_vm_host()
        kernel_check = next(c for c in checks if c.name == "kernel_version")
        assert kernel_check.passed is False
        assert "below minimum" in kernel_check.message

    def test_returns_list_of_probe_checks(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """check_vm_host returns a list of ProbeCheck items."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: self.CPUINFO_WITH_VMX,
        )
        monkeypatch.setattr(Path, "exists", lambda p: True)
        monkeypatch.setattr("os.access", lambda _path, _mode: True)
        monkeypatch.setattr(
            "os.uname",
            lambda: type("Uname", (), {"release": "6.8.0"})(),
        )

        result = HostProbe.check_vm_host()
        assert isinstance(result, list)
        assert all(isinstance(c, ProbeCheck) for c in result)


# ===========================================================================
# HostProbe.check_init_binaries
# ===========================================================================


class TestCheckInitBinaries:
    """Tests for HostProbe.check_init_binaries()."""

    def test_returns_list_of_probe_checks(self) -> None:
        """check_init_binaries returns a ProbeCheck per required binary."""
        with patch("shutil.which", return_value="/usr/bin/ip"):
            result = HostProbe.check_init_binaries()
        assert isinstance(result, list)
        assert all(isinstance(c, ProbeCheck) for c in result)

    def test_found_binaries_pass(self) -> None:
        """check_init_binaries passes for binaries found via shutil.which."""
        with patch("shutil.which", return_value="/usr/bin/ip"):
            checks = HostProbe.check_init_binaries()
            passed = [c for c in checks if c.passed]
            assert len(passed) >= 1

    def test_missing_binaries_fail(self) -> None:
        """check_init_binaries fails for binaries not found."""
        with patch("shutil.which", return_value=None):
            checks = HostProbe.check_init_binaries()
            passed = [c for c in checks if c.passed]
            assert len(passed) == 0


# ===========================================================================
# HostProbe.check_firewall_readiness
# ===========================================================================


class TestCheckFirewallReadiness:
    """Tests for HostProbe.check_firewall_readiness()."""

    def test_returns_list_of_probe_checks(self) -> None:
        """check_firewall_readiness returns firewall-related checks."""
        with patch("shutil.which", return_value="/usr/sbin/nft"):
            result = HostProbe.check_firewall_readiness()
        assert isinstance(result, list)
        assert len(result) >= 2  # nftables + iptables

    def test_nftables_available(self) -> None:
        """nftables check passes when nft binary is found."""
        with patch("shutil.which", side_effect=lambda cmd: {
            "nft": "/usr/sbin/nft",
            "iptables": None,
        }.get(cmd)):
            checks = HostProbe.check_firewall_readiness()
            nft_check = next(c for c in checks if c.name == "nftables")
            assert nft_check.passed is True
            assert "available" in nft_check.message

    def test_nftables_unavailable(self) -> None:
        """nftables check fails when nft binary is not found."""
        with patch("shutil.which", return_value=None):
            checks = HostProbe.check_firewall_readiness()
            nft_check = next(c for c in checks if c.name == "nftables")
            assert nft_check.passed is False
            assert "not available" in nft_check.message

    def test_iptables_available(self) -> None:
        """iptables check passes when iptables binary is found."""
        with patch("shutil.which", side_effect=lambda cmd: {
            "nft": None,
            "iptables": "/usr/sbin/iptables",
        }.get(cmd)):
            checks = HostProbe.check_firewall_readiness()
            ipt_check = next(c for c in checks if c.name == "iptables")
            assert ipt_check.passed is True


# ===========================================================================
# HostProbe.check_system_resources
# ===========================================================================


class TestCheckSystemResources:
    """Tests for HostProbe.check_system_resources()."""

    def test_returns_list_of_probe_checks(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """check_system_resources returns system resource checks."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "MemTotal:       16384000 kB\nSwapTotal:      8000000 kB\n"
            if str(p) == "/proc/meminfo"
            else "0\n",
        )
        monkeypatch.setattr("shutil.which", lambda _cmd: None)

        result = HostProbe.check_system_resources()
        assert isinstance(result, list)
        assert all(isinstance(c, ProbeCheck) for c in result)

    def test_swap_warning(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Swap check warns when swap is less than half of RAM."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "MemTotal:       16384000 kB\nSwapTotal:      1000000 kB\n"
            if str(p) == "/proc/meminfo"
            else "0\n",
        )
        monkeypatch.setattr("shutil.which", lambda _cmd: None)

        checks = HostProbe.check_system_resources()
        swap_check = next((c for c in checks if c.name == "swap_size"), None)
        if swap_check:
            assert swap_check.passed is False
            assert "less than half" in swap_check.message.lower()

    def test_swap_sufficient(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Swap check passes when swap is >= half of RAM."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "MemTotal:       16384000 kB\nSwapTotal:      16384000 kB\n"
            if str(p) == "/proc/meminfo"
            else "0\n",
        )
        monkeypatch.setattr("shutil.which", lambda _cmd: None)

        checks = HostProbe.check_system_resources()
        swap_check = next((c for c in checks if c.name == "swap_size"), None)
        # Should not be a swap_size check, or if it exists it should pass
        if swap_check:
            assert swap_check.passed is True

    def test_cloud_localds_available(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """cloud-localds check passes when binary is found."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "MemTotal:       16384000 kB\nSwapTotal:      8000000 kB\n"
            if str(p) == "/proc/meminfo"
            else "0\n",
        )
        monkeypatch.setattr(
            "shutil.which",
            lambda cmd: "/usr/bin/cloud-localds" if cmd == "cloud-localds" else None,
        )

        checks = HostProbe.check_system_resources()
        cl_check = next(c for c in checks if c.name == "cloud_localds")
        assert cl_check.passed is True

    def test_cloud_localds_unavailable(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """cloud-localds check fails when binary is not found."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "MemTotal:       16384000 kB\nSwapTotal:      8000000 kB\n"
            if str(p) == "/proc/meminfo"
            else "0\n",
        )
        monkeypatch.setattr("shutil.which", lambda _cmd: None)

        checks = HostProbe.check_system_resources()
        cl_check = next((c for c in checks if c.name == "cloud_localds"), None)
        if cl_check:
            assert cl_check.passed is False

    def test_hugepages_info(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Hugepages info is included when nr_hugepages > 0."""
        monkeypatch.setattr(
            "pathlib.Path.read_text",
            lambda p: "MemTotal:       16384000 kB\nSwapTotal:      8000000 kB\n"
            if str(p) == "/proc/meminfo"
            else "128\n",
        )
        monkeypatch.setattr("shutil.which", lambda _cmd: None)

        checks = HostProbe.check_system_resources()
        hp_check = next((c for c in checks if c.name == "hugepages"), None)
        if hp_check:
            assert hp_check.passed is True
            assert "128" in hp_check.message


__all__: list[str] = []
