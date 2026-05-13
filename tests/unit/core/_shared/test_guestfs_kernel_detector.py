"""Tests for the KernelDetector — host kernel selection for libguestfs."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.core._shared._guestfs._kernel_detector import KernelDetector
from mvmctl.exceptions import ProcessError

# =========================================================================
# Helpers
# =========================================================================


def _make_file_mock(name: str) -> MagicMock:
    """Create a Path-like mock for a kernel file."""
    m = MagicMock(spec=Path)
    m.name = name
    m.is_file.return_value = True
    return m


def _make_modules_mock(version: str, virtio_files: list[str]) -> MagicMock:
    """Create a Path-like mock for /lib/modules/<version> with virtio files."""
    m = MagicMock(spec=Path)
    m.is_dir.return_value = True
    m.__truediv__.side_effect = lambda other: _make_path_in_modules(
        version, str(other), virtio_files
    )
    return m


def _make_path_in_modules(
    version: str, subpath: str, virtio_files: list[str]
) -> MagicMock:
    """Create a mock for a path like /lib/modules/<version>/kernel/drivers/..."""
    m = MagicMock(spec=Path)
    m.is_dir.return_value = True
    m.rglob.side_effect = lambda pattern: _resolve_virtio_glob(
        pattern, virtio_files
    )
    m.glob.side_effect = lambda pattern: _resolve_virtio_glob(
        pattern, virtio_files
    )
    return m


def _resolve_virtio_glob(
    pattern: str, virtio_files: list[str]
) -> list[MagicMock]:
    """Match virtio files against a glob pattern like virtio_net.ko.zst."""
    parts = pattern.replace("*.", ".").split(".")
    if len(parts) < 2:
        return []
    prefix, suffix = parts[0], parts[1] if len(parts) == 2 else parts[-1]

    results: list[MagicMock] = []
    for vf in virtio_files:
        vf_name = vf.rsplit("/", 1)[-1] if "/" in vf else vf
        if vf_name.startswith(prefix) and vf_name.endswith(suffix or ".ko"):
            m = MagicMock(spec=Path)
            m.name = vf_name
            m.__str__.return_value = vf_name
            results.append(m)
    return results


# =========================================================================
# Fixtures
# =========================================================================


@pytest.fixture(autouse=True)
def _clear_detector_cache() -> None:
    """Clear the lru_cache between tests so state doesn't leak."""
    KernelDetector.find_best_kernel.cache_clear()


@pytest.fixture
def mock_boot_dir(monkeypatch: pytest.MonkeyPatch) -> MagicMock:
    """Mock /boot as an existing directory."""
    m = MagicMock(spec=Path)
    m.is_dir.return_value = True
    monkeypatch.setattr(
        Path,
        "is_dir",
        lambda self: True if str(self) == "/boot" else Path.is_dir(self),
    )
    return m


# =========================================================================
# Test: _extract_version
# =========================================================================


class TestExtractVersion:
    """Covers version extraction from kernel files (file command + filename fallback)."""

    def test_extract_from_file_output(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """file command returns a standard version string."""

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            result = MagicMock()
            result.stdout = "Linux kernel x86 boot executable, bzImage, version 6.8.0-40-generic (buildd@ubuntu) ..."
            return result

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )
        kernel = _make_file_mock("vmlinuz-6.8.0-40-generic")
        version = KernelDetector._extract_version(kernel)
        assert version == "6.8.0-40-generic"

    def test_extract_from_arch_filename(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Arch-style vmlinuz-linux without version in file output falls back to filename."""

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            result = MagicMock()
            result.stdout = "Linux kernel x86 boot executable, bzImage, ..."
            return result

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )
        kernel = _make_file_mock("vmlinuz-linux")
        # No "version X.Y.Z" in file output, and filename doesn't start with digit
        version = KernelDetector._extract_version(kernel)
        assert version is None

    def test_extract_from_file_with_arch_version(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Arch kernel with version in file output."""

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            result = MagicMock()
            result.stdout = "Linux kernel x86 boot executable, bzImage, version 7.0.2-arch1-1 (linux@archlinux) ..."
            return result

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )
        kernel = _make_file_mock("vmlinuz-linux")
        version = KernelDetector._extract_version(kernel)
        assert version == "7.0.2-arch1-1"

    def test_file_command_timed_out(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Timeout raises ProcessError → re-raised by _extract_version."""

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            raise ProcessError("Command timed out after 5s: file")

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )
        with pytest.raises(ProcessError, match="timed out"):
            KernelDetector._extract_version(_make_file_mock("vmlinuz-xyz"))

    def test_file_not_found(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """ProcessError when file command itself is missing."""

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            raise ProcessError("Command not found: file")

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )
        assert (
            KernelDetector._extract_version(_make_file_mock("vmlinuz-xyz"))
            is None
        )


# =========================================================================
# Test: _scan_boot_directory
# =========================================================================


class TestScanBootDirectory:
    """Covers scanning /boot for kernel files."""

    def test_no_boot_dir(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """No /boot directory → empty list."""
        monkeypatch.setattr(Path, "is_dir", lambda self: False)
        assert KernelDetector._scan_boot_directory() == []

    def test_no_kernel_files(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """/boot exists but empty → empty list."""
        monkeypatch.setattr(Path, "is_dir", lambda self: True)

        def empty_glob(*args: object, **kwargs: object) -> list[MagicMock]:
            return []

        monkeypatch.setattr(Path, "glob", empty_glob)
        assert KernelDetector._scan_boot_directory() == []

    def test_skips_directories(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """/boot contains directory entries that should be skipped."""
        monkeypatch.setattr(Path, "is_dir", lambda self: True)

        dir_mock = MagicMock(spec=Path)
        dir_mock.name = "vmlinuz-old"
        dir_mock.is_file.return_value = False

        def mock_glob(*args: object, **kwargs: object) -> list[MagicMock]:
            return [dir_mock]

        monkeypatch.setattr(Path, "glob", mock_glob)

        def mock_extract(*args: object, **kwargs: object) -> str | None:
            return "6.8.0"

        monkeypatch.setattr(KernelDetector, "_extract_version", mock_extract)
        assert KernelDetector._scan_boot_directory() == []


# =========================================================================
# Test: _custom_suffix_penalty
# =========================================================================


class TestCustomSuffixPenalty:
    """Covers penalty scoring for custom kernel suffixes."""

    @pytest.mark.parametrize(
        ("version", "expected"),
        [
            ("6.8.0", 0),
            ("6.8.0-1", 1),
            ("6.8.0-40-generic", 1),
            ("7.0.2-arch1-1", 1),
            ("7.0.2-arch1-1-g14", 5),
            ("6.8.0-custom", 5),
            ("6.1.0-rc7", 1),
            ("5.15.0-91-generic", 1),
            ("linux-g14", 5),
            ("5.4.0-150-generic", 1),
        ],
    )
    def test_penalty_values(self, version: str, expected: int) -> None:
        assert KernelDetector._custom_suffix_penalty(version) == expected


# =========================================================================
# Test: Scoring helpers
# =========================================================================


class TestScoringHelpers:
    """Covers virtio counting helpers."""

    def test_count_virtio_net_present(self) -> None:
        """virtio_net.ko.zst exists → count = 1."""
        modules = _make_modules_mock("6.8.0", ["virtio_net.ko.zst"])
        assert KernelDetector._count_virtio_net(modules) == 1

    def test_count_virtio_net_absent(self) -> None:
        """No virtio_net files → count = 0."""
        modules = _make_modules_mock("6.8.0", ["virtio_blk.ko.zst"])
        assert KernelDetector._count_virtio_net(modules) == 0

    def test_count_virtio_drivers_total(self) -> None:
        """Multiple virtio files across subdirectories."""
        modules = _make_modules_mock(
            "6.8.0",
            ["virtio_net.ko.zst", "virtio_blk.ko.zst", "virtio_scsi.ko.zst"],
        )
        assert KernelDetector._count_virtio_drivers(modules) == 3

    def test_count_virtio_drivers_none(self) -> None:
        """No virtio files → count = 0."""
        modules = _make_modules_mock("6.8.0", [])
        assert KernelDetector._count_virtio_drivers(modules) == 0


# =========================================================================
# Test: find_best_kernel (full integration)
# =========================================================================


class TestFindBestKernel:
    """End-to-end detection with mocked filesystem."""

    def test_no_kernels_returns_none(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """No kernels in /boot → returns None."""
        monkeypatch.setattr(Path, "is_dir", lambda self: str(self) == "/boot")
        monkeypatch.setattr(
            KernelDetector,
            "_scan_boot_directory",
            classmethod(lambda cls: []),
        )
        assert KernelDetector.find_best_kernel() is None

    def test_single_kernel_picked(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Single kernel with modules → returns it."""
        version = "6.8.0-40-generic"

        monkeypatch.setattr(
            KernelDetector,
            "_scan_boot_directory",
            classmethod(
                lambda cls: [
                    (_make_file_mock("vmlinuz-6.8.0-40-generic"), version)
                ]
            ),
        )

        monkeypatch.setattr(Path, "is_dir", lambda self: True)

        # Mock modules directory with virtio files
        orig_rglob = Path.rglob
        virtio_files = {"virtio_net.ko.zst", "virtio_blk.ko.zst"}

        def mock_rglob(self: Path, pattern: str) -> list[MagicMock]:
            if "virtio" in pattern:
                return [_make_file_mock(f) for f in virtio_files]
            if self.name == "net":
                return [_make_file_mock("virtio_net.ko.zst")]
            return (
                orig_rglob(self, pattern)
                if hasattr(orig_rglob, "__call__")
                else []
            )

        monkeypatch.setattr(Path, "rglob", mock_rglob)
        monkeypatch.setattr(Path, "glob", mock_rglob)
        monkeypatch.setattr(Path, "__truediv__", lambda self, other: self)

        result = KernelDetector.find_best_kernel()
        assert result is not None
        assert result[0].name == "vmlinuz-6.8.0-40-generic"

    def test_upstream_preferred_over_custom(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Upstream kernel (with virtio_net, no custom suffix) wins over custom."""
        upstream_version = "7.0.2-arch1-1"
        custom_version = "7.0.2-arch1-1-g14"

        upstream_kernel = _make_file_mock("vmlinuz-linux")
        custom_kernel = _make_file_mock("vmlinuz-linux-g14")

        monkeypatch.setattr(
            KernelDetector,
            "_scan_boot_directory",
            classmethod(
                lambda cls: [
                    (custom_kernel, custom_version),
                    (upstream_kernel, upstream_version),
                ]
            ),
        )

        monkeypatch.setattr(Path, "is_dir", lambda self: True)

        # Track calls to build module path mocks
        module_paths: dict[str, list[str]] = {
            "7.0.2-arch1-1": [
                "virtio_net.ko.zst",
                "virtio_balloon.ko.zst",
                "virtio_input.ko.zst",
                "virtio_mmio.ko.zst",
            ],
            "7.0.2-arch1-1-g14": [
                "virtio_mem.ko.zst",
                "virtio_vdpa.ko.zst",
            ],
        }

        orig_truediv = Path.__truediv__

        def mock_truediv(self: Path, other: object) -> MagicMock:
            self_str = str(self)
            if "modules" in self_str:
                for v, files in module_paths.items():
                    if v in self_str:
                        m = MagicMock(spec=Path)
                        m.is_dir.return_value = True

                        def make_rglob(vf_list: list[str]) -> object:
                            def rglob_fn(pattern: str) -> list[MagicMock]:
                                if "virtio" not in pattern:
                                    return []
                                return [_make_file_mock(f) for f in vf_list]

                            return rglob_fn

                        m.rglob.side_effect = make_rglob(files)
                        return m
            return orig_truediv(self, other)

        monkeypatch.setattr(Path, "__truediv__", mock_truediv)

        result = KernelDetector.find_best_kernel()
        assert result is not None
        assert result[0].name == "vmlinuz-linux", (
            f"Expected upstream vmlinuz-linux, got {result[0].name}"
        )

    def test_only_custom_kernel_still_returns(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """When only a custom kernel is available, it's still returned (not None)."""
        version = "7.0.2-arch1-1-g14"

        monkeypatch.setattr(
            KernelDetector,
            "_scan_boot_directory",
            classmethod(
                lambda cls: [(_make_file_mock("vmlinuz-linux-g14"), version)]
            ),
        )

        monkeypatch.setattr(Path, "is_dir", lambda self: True)
        monkeypatch.setattr(
            Path,
            "rglob",
            lambda self, p: [_make_file_mock("virtio_mem.ko.zst")],
        )
        monkeypatch.setattr(
            Path, "glob", lambda self, p: [_make_file_mock("virtio_mem.ko.zst")]
        )
        monkeypatch.setattr(Path, "__truediv__", lambda self, other: self)

        result = KernelDetector.find_best_kernel()
        assert result is not None
        assert result[0].name == "vmlinuz-linux-g14"

    def test_missing_modules_skipped(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Kernel without matching /lib/modules/<version> is skipped."""
        monkeypatch.setattr(
            KernelDetector,
            "_scan_boot_directory",
            classmethod(
                lambda cls: [
                    (_make_file_mock("vmlinuz-missing"), "9.9.9-nonexistent")
                ]
            ),
        )

        def mock_is_dir(self: Path) -> bool:
            self_str = str(self)
            if "/lib/modules/9.9.9-nonexistent" in self_str:
                return False
            if str(self) == "/boot":
                return True
            return True

        monkeypatch.setattr(Path, "is_dir", mock_is_dir)
        monkeypatch.setattr(Path, "rglob", lambda self, p: [])
        monkeypatch.setattr(Path, "glob", lambda self, p: [])
        monkeypatch.setattr(Path, "__truediv__", lambda self, other: self)

        assert KernelDetector.find_best_kernel() is None


# =========================================================================
# Test: Real-world version strings
# =========================================================================


class TestRealWorldPatterns:
    """Common distro kernel version patterns."""

    @pytest.mark.parametrize(
        ("file_output", "expected_version"),
        [
            (
                "Linux kernel x86 boot executable bzImage, version 6.8.0-40-generic "
                "(buildd@ubuntu) #40-Ubuntu SMP PREEMPT_DYNAMIC Mon Mar 25 ...",
                "6.8.0-40-generic",
            ),
            (
                "Linux kernel x86 boot executable bzImage, version 5.15.0-91-generic "
                "(buildd@amd64) #101-Ubuntu SMP Tue Nov 14 ...",
                "5.15.0-91-generic",
            ),
            (
                "Linux kernel x86 boot executable, bzImage, version "
                "7.0.2-arch1-1 (linux@archlinux) #1 SMP PREEMPT_DYNAMIC ...",
                "7.0.2-arch1-1",
            ),
            (
                "Linux kernel x86 boot executable, bzImage, version "
                "6.1.0-0.deb11.13-amd64 (debian-kernel@lists.debian.org) ...",
                "6.1.0-0.deb11.13-amd64",
            ),
        ],
    )
    def test_version_regex(
        self,
        file_output: str,
        expected_version: str,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Version regex extracts correctly from real-world file outputs."""

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            result = MagicMock()
            result.stdout = file_output
            return result

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )
        kernel = _make_file_mock("vmlinuz")
        assert KernelDetector._extract_version(kernel) == expected_version

    def test_custom_suffix_detected(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Custom kernel with -g14 suffix gets correct penalty and doesn't win."""
        file_output = (
            "Linux kernel x86 boot executable, bzImage, version "
            "7.0.2-arch1-1-g14 (user@custom) #1 SMP ..."
        )

        def mock_run(*args: object, **kwargs: object) -> MagicMock:
            result = MagicMock()
            result.stdout = file_output
            return result

        monkeypatch.setattr(
            "mvmctl.core._shared._guestfs._kernel_detector.run_cmd",
            mock_run,
        )

        version = KernelDetector._extract_version(
            _make_file_mock("vmlinuz-linux-g14")
        )
        assert version == "7.0.2-arch1-1-g14"
        assert KernelDetector._custom_suffix_penalty(version) == 5
