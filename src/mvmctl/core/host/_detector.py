"""Host capacity detector — reads /proc, sysfs, and OS APIs with zero subprocesses."""

from __future__ import annotations

import grp
import logging
import os
import platform
import pwd
import shutil
import socket
from pathlib import Path

from mvmctl.constants import DEFAULT_IP_LOCAL_PORT_RANGE, MIN_KERNEL_VERSION
from mvmctl.models.host import HostHardware, HostLimits, HostResources
from mvmctl.utils.fs import FsUtils

logger = logging.getLogger(__name__)

_CPU_VENDOR_MAP_X86: dict[str, str] = {
    "GenuineIntel": "intel",
    "AuthenticAMD": "amd",
}

_CPU_IMPLEMENTER_MAP_AARCH64: dict[str, str] = {
    "0x41": "arm",
    "0x42": "broadcom",
    "0x43": "cavium",
    "0x44": "dec",
    "0x4e": "nvidia",
    "0x51": "qualcomm",
    "0x53": "samsung",
    "0x56": "marvell",
    "0x61": "apple",
    "0x66": "faraday",
    "0x69": "intel",
}

# Per-VM resource overhead estimates (MiB)
_VM_OVERHEAD_MIB = 50
_VM_MEMORY_MIB = 512
_VM_RESERVED_MIB = 2048
_VM_RESERVED_PIDS = 200
_VM_PIDS_PER_VM = 3
_VM_CONNTRACK_PER_VM = 64


# Kernel modules relevant to VM host operations, checked against /proc/modules.
_VM_HOST_KERNEL_MODULES: list[str] = [
    "kvm",
    "kvm_intel",
    "kvm_amd",
    "tun",
    "bridge",
    "vhost_vsock",
    "nft_chain_nat",
]


class HostDetector:
    """Static methods to detect host hardware, limits, and live resource usage.

    All methods read from /proc, sysfs, and standard OS APIs.
    Zero subprocess calls.
    """

    @staticmethod
    def _read_int(path: str, default: int = 0) -> int:
        """Read an integer from a /proc or sysfs file, returning *default* on error."""
        try:
            text = Path(path).read_text().strip()
            return int(text.split()[0])
        except (
            FileNotFoundError,
            PermissionError,
            OSError,
            ValueError,
            IndexError,
        ):
            return default

    @staticmethod
    def _meminfo_kb_to_mib(key: str) -> int:
        """Parse a key from /proc/meminfo and convert kB to MiB."""
        try:
            for line in Path("/proc/meminfo").read_text().splitlines():
                if line.startswith(key + ":"):
                    parts = line.split()
                    if len(parts) >= 2:
                        return int(parts[1]) // 1024
        except (FileNotFoundError, PermissionError, OSError, ValueError):
            pass
        return 0

    @staticmethod
    def detect_hardware() -> HostHardware:
        """Detect host hardware capabilities from /proc and OS APIs."""
        hostname = socket.gethostname()

        # CPU model from /proc/cpuinfo
        cpu_model = ""
        cpu_vendor = ""
        cpu_architecture = platform.machine()
        cpu_has_vmx = False
        cpu_hypervisor = False
        try:
            text = Path("/proc/cpuinfo").read_text()
            for line in text.splitlines():
                if line.startswith("model name") and not cpu_model:
                    cpu_model = line.split(":", 1)[1].strip()
                if line.startswith("vendor_id") and not cpu_vendor:
                    raw = line.split(":", 1)[1].strip()
                    cpu_vendor = _CPU_VENDOR_MAP_X86.get(raw, raw)
                if not cpu_model and cpu_architecture.startswith("arm"):
                    # On aarch64, model name may come from "CPU part"
                    if line.startswith("CPU part") and not cpu_model:
                        cpu_model = line.split(":", 1)[1].strip()
                if cpu_model and cpu_vendor:
                    break
            # Detect virtualization flags from the first processor flags line
            for line in text.splitlines():
                if line.startswith("flags"):
                    flags = line.split(":", 1)[1].strip().split()
                    cpu_has_vmx = "vmx" in flags or "svm" in flags
                    cpu_hypervisor = "hypervisor" in flags
                    break
        except (FileNotFoundError, PermissionError, OSError):
            pass

        # Fallback vendor detection for aarch64
        if not cpu_vendor and cpu_architecture.startswith(("arm", "aarch64")):
            raw = ""
            try:
                raw = FsUtils.read_raw(Path("/proc/cpuinfo")).strip()
            except Exception:
                pass
            for line in raw.splitlines():
                if line.startswith("CPU implementer"):
                    impl = line.split(":", 1)[1].strip()
                    cpu_vendor = _CPU_IMPLEMENTER_MAP_AARCH64.get(impl, impl)
                    break

        cpu_cores = os.cpu_count() or 1

        # NUMA nodes: count node* directories
        numa_nodes = 1
        try:
            nodes = [
                d.name
                for d in Path("/sys/devices/system/node").iterdir()
                if d.is_dir() and d.name.startswith("node")
            ]
            numa_nodes = max(len(nodes), 1)
        except (FileNotFoundError, PermissionError, OSError):
            pass

        memory_total_mib = HostDetector._meminfo_kb_to_mib("MemTotal")

        # Storage: use root cache dir (fallback to / if needed)
        from mvmctl.utils.common import CacheUtils

        cache_dir = CacheUtils.get_cache_dir()
        storage_total_bytes = 0
        try:
            usage = shutil.disk_usage(cache_dir)
            storage_total_bytes = usage.total
        except (FileNotFoundError, PermissionError, OSError):
            pass

        kernel_version = os.uname().release

        # OS release from /etc/os-release
        os_release = ""
        try:
            text = Path("/etc/os-release").read_text()
            for line in text.splitlines():
                if line.startswith("PRETTY_NAME="):
                    os_release = line.split("=", 1)[1].strip().strip('"')
                    break
            if not os_release:
                os_id = ""
                os_version = ""
                for line in text.splitlines():
                    if line.startswith("ID="):
                        os_id = line.split("=", 1)[1].strip().strip('"')
                    if line.startswith("VERSION_ID="):
                        os_version = line.split("=", 1)[1].strip().strip('"')
                if os_id:
                    os_release = f"{os_id} {os_version}".strip()
        except (FileNotFoundError, PermissionError, OSError):
            pass

        return HostHardware(
            hostname=hostname,
            cpu_model=cpu_model or cpu_architecture,
            cpu_vendor=cpu_vendor or "unknown",
            cpu_cores=cpu_cores,
            cpu_architecture=cpu_architecture,
            numa_nodes=numa_nodes,
            memory_total_mib=memory_total_mib,
            storage_total_bytes=storage_total_bytes,
            kernel_version=kernel_version,
            os_release=os_release or "unknown",
            cpu_has_vmx=cpu_has_vmx,
            cpu_hypervisor=cpu_hypervisor,
        )

    @staticmethod
    def detect_limits() -> HostLimits:
        """Detect kernel-imposed resource limits from /proc and sysfs."""
        pid_max = HostDetector._read_int("/proc/sys/kernel/pid_max", 32768)
        fd_max = HostDetector._read_int("/proc/sys/fs/file-max", 100000)
        conntrack_max = HostDetector._read_int(
            "/proc/sys/net/netfilter/nf_conntrack_max", 0
        )
        tap_devices_max = HostDetector._read_int(
            "/sys/module/tun/parameters/max_tap_devices", 0
        )
        # 0 means unlimited (kernel default when module param not set)
        if tap_devices_max == 0:
            tap_devices_max = -1

        ip_local_port_range = DEFAULT_IP_LOCAL_PORT_RANGE
        try:
            text = (
                Path("/proc/sys/net/ipv4/ip_local_port_range")
                .read_text()
                .strip()
            )
            parts = text.split()
            if len(parts) >= 2:
                ip_local_port_range = (int(parts[0]), int(parts[1]))
        except (FileNotFoundError, PermissionError, OSError, ValueError):
            pass

        # --- Virtualization & system detection ---
        # Nested virtualization: read /sys/module/kvm_intel/parameters/nested
        # (or kvm_amd equivalent)
        nested_virt_available = False
        for nested_path in (
            "/sys/module/kvm_intel/parameters/nested",
            "/sys/module/kvm_amd/parameters/nested",
        ):
            val = HostDetector._read_int(nested_path, -1)
            if val == 1:
                nested_virt_available = True
                break

        # EPT: Intel only - /sys/module/kvm_intel/parameters/ept
        ept_available = bool(
            HostDetector._read_int("/sys/module/kvm_intel/parameters/ept", 0)
        )

        # Hugepages (2MB): /sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages
        hugepage_count_2mb = HostDetector._read_int(
            "/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages", 0
        )

        # KSM: /sys/kernel/mm/ksm/run == 0 means disabled
        ksm_run = HostDetector._read_int("/sys/kernel/mm/ksm/run", 0)
        ksm_disabled = ksm_run == 0

        # Cgroup version: v2 if /sys/fs/cgroup/cgroup.controllers exists
        cgroup_version = (
            2 if Path("/sys/fs/cgroup/cgroup.controllers").exists() else 1
        )

        # Swap total from /proc/meminfo
        swap_total_mib = HostDetector._meminfo_kb_to_mib("SwapTotal")

        # Kernel minimum version check
        kernel_minimum_met = HostDetector._check_kernel_version()

        return HostLimits(
            pid_max=pid_max,
            fd_max=fd_max,
            conntrack_max=conntrack_max,
            tap_devices_max=tap_devices_max,
            ip_local_port_range=ip_local_port_range,
            nested_virt_available=nested_virt_available,
            ept_available=ept_available,
            hugepage_count_2mb=hugepage_count_2mb,
            ksm_disabled=ksm_disabled,
            cgroup_version=cgroup_version,
            swap_total_mib=swap_total_mib,
            kernel_minimum_met=kernel_minimum_met,
        )

    @staticmethod
    def _check_kernel_version() -> bool:
        """Check if the running kernel meets the minimum version (5.10)."""
        import re

        release = os.uname().release
        match = re.match(r"(\d+)\.(\d+)", release)
        if not match:
            return False
        major, minor = int(match.group(1)), int(match.group(2))
        return (major, minor) >= MIN_KERNEL_VERSION

    @staticmethod
    def _parse_modules() -> dict[str, bool]:
        """Parse /proc/modules and return a dict of module_name -> loaded."""
        result: dict[str, bool] = {}
        try:
            text = Path("/proc/modules").read_text()
            for line in text.splitlines():
                parts = line.strip().split()
                if parts:
                    result[parts[0]] = True
        except (FileNotFoundError, PermissionError, OSError):
            pass
        return result

    @staticmethod
    def detect_resources(
        hardware: HostHardware,
        limits: HostLimits,
        vm_dir_path: Path,
    ) -> HostResources:
        """Detect current resource usage and compute recommended VM capacity.

        Args:
            hardware: Previously detected hardware capabilities.
            limits: Previously detected kernel limits.
            vm_dir_path: Path to the VM data directory for disk usage.

        Returns:
            HostResources with current usage and capacity projection.

        """
        memory_available_mib = HostDetector._meminfo_kb_to_mib("MemAvailable")

        # TAP devices in use: count /sys/class/net/*/tun_flags
        tap_devices_used = 0
        try:
            for net_dev in Path("/sys/class/net").iterdir():
                if (net_dev / "tun_flags").exists():
                    tap_devices_used += 1
        except (FileNotFoundError, PermissionError, OSError):
            pass

        # PIDs: count numeric directories in /proc
        pids_current = 0
        try:
            for entry in Path("/proc").iterdir():
                if entry.is_dir() and entry.name.isdigit():
                    pids_current += 1
        except (FileNotFoundError, PermissionError, OSError):
            pass

        # FD current: first field of /proc/sys/fs/file-nr
        fd_current = HostDetector._read_int("/proc/sys/fs/file-nr", 0)

        # Conntrack current
        conntrack_current = HostDetector._read_int(
            "/proc/sys/net/netfilter/nf_conntrack_count", 0
        )

        # ARP entries: line count of /proc/net/arp minus header
        arp_current = 0
        try:
            text = Path("/proc/net/arp").read_text()
            lines = [line for line in text.splitlines() if line.strip()]
            arp_current = max(0, len(lines) - 1)  # -1 for header
        except (FileNotFoundError, PermissionError, OSError):
            pass

        # Storage free
        storage_free_bytes = 0
        try:
            usage = shutil.disk_usage(vm_dir_path)
            storage_free_bytes = usage.free
        except (FileNotFoundError, PermissionError, OSError):
            pass

        # Compute recommended max VMs
        candidates: list[tuple[str, int]] = []

        # CPU: leave 1 core for host
        cpu_vms = max(0, hardware.cpu_cores - 1)
        candidates.append(("cpu", cpu_vms))

        # Memory: reserve 2048 MiB for host, each VM uses 50 + 512 MiB
        memory_vms = max(
            0,
            (memory_available_mib - _VM_RESERVED_MIB)
            // (_VM_OVERHEAD_MIB + _VM_MEMORY_MIB),
        )
        candidates.append(("memory", memory_vms))

        # TAP devices
        tap_available = max(0, limits.tap_devices_max - tap_devices_used)
        if limits.tap_devices_max > 0:
            candidates.append(("tap_devices", tap_available))

        # PIDs
        pid_vms = max(
            0, (limits.pid_max - _VM_RESERVED_PIDS) // _VM_PIDS_PER_VM
        )
        candidates.append(("pids", pid_vms))

        # Conntrack
        if limits.conntrack_max > 0:
            conntrack_vms = limits.conntrack_max // _VM_CONNTRACK_PER_VM
            candidates.append(("conntrack", conntrack_vms))

        # Find minimum
        recommended_max_vms = min(v for _, v in candidates)
        limiting_resource: str | None = None
        for name, val in candidates:
            if val == recommended_max_vms:
                limiting_resource = name
                break

        # --- Module detection ---
        modules = HostDetector._parse_modules()
        modules_loaded: dict[str, bool] = {
            m: m in modules for m in _VM_HOST_KERNEL_MODULES
        }

        # Swap used
        swap_free_mib = HostDetector._meminfo_kb_to_mib("SwapFree")
        swap_total_mib = HostDetector._meminfo_kb_to_mib("SwapTotal")
        swap_used_mib = max(0, swap_total_mib - swap_free_mib)

        # Hugepages free
        hugepages_free_2mb = HostDetector._read_int(
            "/sys/kernel/mm/hugepages/hugepages-2048kB/free_hugepages", 0
        )

        # SMT (Hyper-Threading)
        smt_active = bool(
            HostDetector._read_int("/sys/devices/system/cpu/smt/active", 0)
        )

        # Binary availability
        nftables_available = shutil.which("nft") is not None
        iptables_available = shutil.which("iptables") is not None
        cloud_localds_available = shutil.which("cloud-localds") is not None

        # /dev/kvm status
        kvm_path = Path("/dev/kvm")
        if not kvm_path.exists():
            dev_kvm_status = "missing"
        elif not os.access(kvm_path, os.R_OK | os.W_OK):
            dev_kvm_status = "no_permission"
        elif not hardware.cpu_has_vmx:
            dev_kvm_status = "no_hardware"
        else:
            dev_kvm_status = "ok"

        # User in kvm group
        user_in_kvm_group = False
        try:
            kvm_group = grp.getgrnam("kvm")
            user = pwd.getpwuid(os.getuid()).pw_name
            user_in_kvm_group = user in kvm_group.gr_mem
        except (KeyError, ImportError, PermissionError):
            pass

        # /dev/net/tun accessibility
        tun_path = Path("/dev/net/tun")
        dev_net_tun_accessible = tun_path.exists() and os.access(
            tun_path, os.R_OK | os.W_OK
        )

        return HostResources(
            memory_available_mib=memory_available_mib,
            tap_devices_used=tap_devices_used,
            pids_current=pids_current,
            fd_current=fd_current,
            conntrack_current=conntrack_current,
            arp_current=arp_current,
            storage_free_bytes=storage_free_bytes,
            recommended_max_vms=recommended_max_vms,
            limiting_resource=limiting_resource,
            modules_loaded=modules_loaded,
            swap_used_mib=swap_used_mib,
            hugepages_free_2mb=hugepages_free_2mb,
            smt_active=smt_active,
            nftables_available=nftables_available,
            iptables_available=iptables_available,
            cloud_localds_available=cloud_localds_available,
            dev_kvm_status=dev_kvm_status,
            user_in_kvm_group=user_in_kvm_group,
            dev_net_tun_accessible=dev_net_tun_accessible,
        )


__all__ = [
    "HostDetector",
]
