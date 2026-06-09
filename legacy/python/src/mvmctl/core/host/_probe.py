"""HostProbe — pre-flight checks for host readiness."""

from __future__ import annotations

import logging
import os
import re
import shutil
from pathlib import Path

from mvmctl.constants import INIT_BINARIES
from mvmctl.models.host import ProbeCheck, ProbeResult

logger = logging.getLogger(__name__)


class HostProbe:
    """Static pre-flight checks for host readiness.

    Each check method returns a list of ProbeCheck results.  Use run_all()
    to collect all checks into a structured ProbeResult.
    """

    @staticmethod
    def run_all() -> ProbeResult:
        """Run all pre-flight probes and return aggregated result."""
        result = ProbeResult()

        for check in HostProbe.check_vm_host():
            (result.critical if not check.passed else result.info).append(check)

        for check in HostProbe.check_init_binaries():
            (result.critical if not check.passed else result.info).append(check)

        for check in HostProbe.check_firewall_readiness():
            (result.warnings if not check.passed else result.info).append(check)

        for check in HostProbe.check_system_resources():
            (result.warnings if not check.passed else result.info).append(check)

        return result

    @staticmethod
    def check_vm_host() -> list[ProbeCheck]:
        """Check KVM and VM host prerequisites.

        Checks: /dev/kvm, /dev/net/tun, VMX/SVM CPU flag, kvm kernel module,
        minimum kernel version, nested virtualization.
        """
        checks: list[ProbeCheck] = []

        # --- CPU virtualization support (VMX/SVM) ---
        has_virt = False
        try:
            text = Path("/proc/cpuinfo").read_text()
            for line in text.splitlines():
                if line.startswith("flags"):
                    flags = line.split(":", 1)[1].strip().split()
                    has_virt = "vmx" in flags or "svm" in flags
                    break
        except (FileNotFoundError, PermissionError, OSError):
            pass

        checks.append(
            ProbeCheck(
                name="cpu_virtualization",
                passed=has_virt,
                message="CPU virtualization extensions (VMX/SVM)"
                if has_virt
                else "CPU does not support hardware virtualization (VMX/SVM)",
                details=(
                    None
                    if has_virt
                    else "Enable VT-x/AMD-V in BIOS. Without it, VMs will be extremely slow."
                ),
            )
        )

        # --- /dev/kvm ---
        kvm_path = Path("/dev/kvm")
        cpu_virt_ok = has_virt

        if not kvm_path.exists():
            checks.append(
                ProbeCheck(
                    name="dev_kvm",
                    passed=False,
                    message="/dev/kvm does not exist",
                    details="KVM kernel module not loaded. Run: sudo modprobe kvm && sudo modprobe kvm_intel (or kvm_amd)",
                )
            )
        elif not os.access(kvm_path, os.R_OK | os.W_OK):
            checks.append(
                ProbeCheck(
                    name="dev_kvm",
                    passed=False,
                    message="/dev/kvm exists but is not readable/writable",
                    details="Add user to kvm group: sudo usermod -aG kvm $USER && newgrp kvm",
                )
            )
        elif not cpu_virt_ok:
            checks.append(
                ProbeCheck(
                    name="dev_kvm",
                    passed=False,
                    message="/dev/kvm exists but no CPU virtualization support detected",
                    details="CPU may not support virtualization, or KVM is built into the kernel without /dev/kvm",
                )
            )
        else:
            checks.append(
                ProbeCheck(
                    name="dev_kvm",
                    passed=True,
                    message="/dev/kvm is accessible",
                )
            )

        # --- /dev/net/tun ---
        tun_path = Path("/dev/net/tun")
        tun_ok = tun_path.exists() and os.access(tun_path, os.R_OK | os.W_OK)
        checks.append(
            ProbeCheck(
                name="dev_net_tun",
                passed=tun_ok,
                message="/dev/net/tun is accessible"
                if tun_ok
                else "/dev/net/tun is not accessible",
                details=None
                if tun_ok
                else "TUN/TAP networking will not work. Check permissions or load tun module.",
            )
        )

        # --- kvm kernel module (from /proc/modules) ---
        kvm_loaded = False
        try:
            text = Path("/proc/modules").read_text()
            kvm_loaded = any(
                line.split()[0] == "kvm" for line in text.splitlines() if line
            )
        except (FileNotFoundError, PermissionError, OSError):
            pass

        checks.append(
            ProbeCheck(
                name="kvm_module",
                passed=kvm_loaded,
                message="KVM kernel module loaded"
                if kvm_loaded
                else "KVM kernel module not loaded",
                details=None if kvm_loaded else "Run: sudo modprobe kvm",
            )
        )

        # --- Kernel minimum version ---
        release = os.uname().release
        match = re.match(r"(\d+)\.(\d+)", release)
        if match:
            major, minor = int(match.group(1)), int(match.group(2))
            kernel_met = (major, minor) >= (5, 10)
        else:
            kernel_met = False

        checks.append(
            ProbeCheck(
                name="kernel_version",
                passed=kernel_met,
                message=f"Kernel {release} meets minimum 5.10"
                if kernel_met
                else f"Kernel {release} is below minimum 5.10",
                details=(
                    None
                    if kernel_met
                    else "Firecracker requires Linux kernel 5.10 or later."
                ),
            )
        )

        # --- Nested virtualization ---
        nested_virt = False
        for nested_path in (
            "/sys/module/kvm_intel/parameters/nested",
            "/sys/module/kvm_amd/parameters/nested",
        ):
            try:
                val = Path(nested_path).read_text().strip()
                if val.strip().lower() in ("y", "1", "yes", "on"):
                    nested_virt = True
                    break
            except (FileNotFoundError, PermissionError, OSError):
                continue

        checks.append(
            ProbeCheck(
                name="nested_virtualization",
                passed=nested_virt,
                message="Nested virtualization supported"
                if nested_virt
                else "Nested virtualization not available",
                details=None
                if nested_virt
                else "Only needed for running VMs inside VMs. Set kvm_intel.nested=1 or kvm_amd.nested=1.",
            )
        )

        return checks

    @staticmethod
    def check_init_binaries() -> list[ProbeCheck]:
        """Check all binaries required for host initialization."""
        checks: list[ProbeCheck] = []
        for name in INIT_BINARIES:
            found = shutil.which(name) is not None
            checks.append(
                ProbeCheck(
                    name=f"binary:{name}",
                    passed=found,
                    message=f"Required binary '{name}' found"
                    if found
                    else f"Required binary '{name}' not found",
                    details=None
                    if found
                    else f"Install the package that provides '{name}'",
                )
            )
        return checks

    @staticmethod
    def check_firewall_readiness() -> list[ProbeCheck]:
        """Check firewall backend availability and detect conflicts."""
        checks: list[ProbeCheck] = []

        nft_available = shutil.which("nft") is not None
        ipt_available = shutil.which("iptables") is not None

        checks.append(
            ProbeCheck(
                name="nftables",
                passed=nft_available,
                message="nftables available"
                if nft_available
                else "nftables not available",
                details=None,
            )
        )
        checks.append(
            ProbeCheck(
                name="iptables",
                passed=ipt_available,
                message="iptables available"
                if ipt_available
                else "iptables not available",
                details=None,
            )
        )

        # Mixed backend detection
        if nft_available and ipt_available:
            from mvmctl.utils.network import NetworkUtils

            has_conflict, _ = NetworkUtils.detect_iptables_backend_conflict()
            if has_conflict:
                checks.append(
                    ProbeCheck(
                        name="firewall_conflict",
                        passed=False,
                        message="Mixed iptables backends detected",
                        details="Both legacy and nft iptables backends are active. This may cause networking issues.",
                    )
                )

        return checks

    @staticmethod
    def check_system_resources() -> list[ProbeCheck]:
        """Check system resource thresholds and optional tooling."""
        checks: list[ProbeCheck] = []

        # Swap check
        total_mem = 0
        total_swap = 0
        try:
            for line in Path("/proc/meminfo").read_text().splitlines():
                if line.startswith("MemTotal:"):
                    total_mem = int(line.split()[1])
                elif line.startswith("SwapTotal:"):
                    total_swap = int(line.split()[1])
        except (FileNotFoundError, PermissionError, OSError, ValueError):
            pass

        total_mem_mib = total_mem // 1024
        total_swap_mib = total_swap // 1024
        if total_swap_mib < total_mem_mib // 2 and total_mem_mib > 1024:
            checks.append(
                ProbeCheck(
                    name="swap_size",
                    passed=False,
                    message=f"Swap ({total_swap_mib} MiB) is less than half of RAM ({total_mem_mib} MiB)",
                    details="Low swap may cause OOM under high VM load. Consider increasing swap.",
                )
            )

        # cloud-localds
        cl_available = shutil.which("cloud-localds") is not None
        checks.append(
            ProbeCheck(
                name="cloud_localds",
                passed=cl_available,
                message="cloud-localds available"
                if cl_available
                else "cloud-localds not found",
                details=None
                if cl_available
                else "Install cloud-image-utils (Debian/Ubuntu) or cloud-utils (Arch)",
            )
        )

        # Huge pages info
        nr_hugepages = 0
        try:
            nr_hugepages = int(
                Path("/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages")
                .read_text()
                .strip()
            )
        except (FileNotFoundError, PermissionError, OSError, ValueError):
            pass
        if nr_hugepages > 0:
            checks.append(
                ProbeCheck(
                    name="hugepages",
                    passed=True,
                    message=f"{nr_hugepages} x 2MB hugepages configured",
                )
            )

        return checks


__all__ = ["HostProbe"]
