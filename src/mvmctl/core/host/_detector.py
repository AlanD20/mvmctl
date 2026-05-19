"""Host capacity detector — reads /proc, sysfs, and OS APIs with zero subprocesses."""

from __future__ import annotations

import logging
import os
import platform
import shutil
import socket
from pathlib import Path

from mvmctl.constants import DEFAULT_IP_LOCAL_PORT_RANGE
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

        return HostLimits(
            pid_max=pid_max,
            fd_max=fd_max,
            conntrack_max=conntrack_max,
            tap_devices_max=tap_devices_max,
            ip_local_port_range=ip_local_port_range,
        )

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
        )


__all__ = [
    "HostDetector",
]
