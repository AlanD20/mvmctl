from __future__ import annotations

import logging
import re
import subprocess
from functools import lru_cache
from pathlib import Path

logger = logging.getLogger(__name__)


class KernelDetector:
    """Detects the best host kernel for libguestfs appliance builds."""

    _KERNEL_NAMES: tuple[str, ...] = (
        "vmlinuz",
        "bzImage",
        "kernel",
    )

    _DRIVER_EXTENSIONS: tuple[str, ...] = (".ko", ".ko.zst", ".ko.xz")

    _VIRTIO_NET_PATHS: tuple[Path, ...] = (Path("kernel/drivers/net"),)

    @classmethod
    @lru_cache(maxsize=1)
    def find_best_kernel(cls) -> tuple[Path, Path] | None:
        """Find the best host kernel for libguestfs.

        Scans /boot for kernel images, extracts their version strings,
        and scores candidates based on virtio module availability and
        whether the kernel looks like a custom build.

        A kernel is NOT rejected outright if virtio modules are missing
        (some distros build virtio drivers into the kernel). Instead,
        kernels with more virtio modules and without custom suffixes
        are ranked higher.

        Returns:
            Tuple of (kernel_path, modules_dir) for the best candidate,
            or None if no kernel is found.
        """
        candidates = cls._scan_boot_directory()
        if not candidates:
            return None

        scored: list[tuple[tuple[Path, Path], int]] = []
        for kernel_path, version in candidates:
            modules_dir = Path(f"/lib/modules/{version}")
            if not modules_dir.is_dir():
                logger.debug(
                    "Modules directory missing for %s: %s",
                    kernel_path,
                    modules_dir,
                )
                continue

            # Score: virtio_net bonus (critical for guestfs) + total virtio count
            virtio_net_bonus = cls._count_virtio_net(modules_dir) * 2
            virtio_count = cls._count_virtio_drivers(modules_dir)
            custom_penalty = cls._custom_suffix_penalty(version)
            score = virtio_net_bonus + virtio_count - custom_penalty
            scored.append(((kernel_path, modules_dir), score))

            logger.debug(
                "Kernel %s: virtio_net=%d total=%d penalty=%d score=%d",
                kernel_path.name,
                virtio_net_bonus // 2,
                virtio_count,
                custom_penalty,
                score,
            )

        if not scored:
            return None

        scored.sort(key=lambda item: item[1], reverse=True)
        best = scored[0][0]
        logger.debug(
            "Selected kernel %s with modules %s",
            best[0],
            best[1],
        )
        return best

    @classmethod
    def _scan_boot_directory(cls) -> list[tuple[Path, str]]:
        """Scan /boot for kernel files and extract versions."""
        boot_dir = Path("/boot")
        candidates: list[tuple[Path, str]] = []

        if not boot_dir.is_dir():
            return candidates

        paths: list[Path] = []
        for name in cls._KERNEL_NAMES:
            for path in boot_dir.glob(f"{name}*"):
                if path.is_file():
                    paths.append(path)

        for path in paths:
            version = cls._extract_version(path)
            if version:
                candidates.append((path, version))

        return candidates

    @classmethod
    def _extract_version(cls, kernel_path: Path) -> str | None:
        """Extract kernel version from a kernel image using the file command."""
        try:
            result = subprocess.run(
                ["file", str(kernel_path)],
                capture_output=True,
                text=True,
                check=False,
                timeout=5,
            )
            output = result.stdout
        except (subprocess.TimeoutExpired, FileNotFoundError, OSError) as e:
            logger.debug("Failed to run file on %s: %s", kernel_path, e)
            return None

        match = re.search(r"version\s+(\S+)", output)
        if match:
            return match.group(1)

        # Fallback: try to extract from filename for vmlinuz-X.Y.Z...
        name = kernel_path.name
        for prefix in cls._KERNEL_NAMES:
            if name.startswith(prefix):
                remainder = name[len(prefix) :]
                if remainder.startswith("-"):
                    version = remainder[1:]
                    if version and re.match(r"^\d", version):
                        return version
                break

        return None

    @classmethod
    def _count_virtio_net(cls, modules_dir: Path) -> int:
        """Count virtio_net driver files (most critical for guestfs)."""
        count = 0
        for rel_path in cls._VIRTIO_NET_PATHS:
            search_path = modules_dir / rel_path
            if not search_path.is_dir():
                continue
            for ext in cls._DRIVER_EXTENSIONS:
                count += len(list(search_path.glob(f"virtio_net{ext}")))
        return count

    @classmethod
    def _count_virtio_drivers(cls, modules_dir: Path) -> int:
        """Count all virtio drivers under the modules directory."""
        drivers_dir = modules_dir / "kernel/drivers"
        if not drivers_dir.is_dir():
            return 0

        count = 0
        for pattern in ("virtio_*.ko", "virtio_*.ko.zst", "virtio_*.ko.xz"):
            count += len(list(drivers_dir.rglob(pattern)))
        return count

    @classmethod
    def _custom_suffix_penalty(cls, version: str) -> int:
        """Return penalty for custom kernel suffixes.

        Clean versions like 6.9.3 get 0 penalty.
        Distro versions with standard suffixes like 6.9.3-1, 6.8.0-40-generic,
        7.0.2-arch1-1, 6.1.0-rc7 get 1 penalty.
        Custom versions with non-standard suffixes like -g14, -custom get 5 penalty.
        """
        import re

        # Explicit custom suffix patterns
        if re.search(r"-(g14|custom)$", version):
            return 5

        if re.match(r"^\d+\.\d+\.\d+$", version):
            return 0
        if re.match(r"^\d+\.\d+\.\d+[-.]", version):
            return 1
        return 5
