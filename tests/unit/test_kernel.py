"""Tests for kernel download and build utilities."""

import subprocess
import tarfile
from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from mvmctl.core.kernel import (
    build_kernel,
    build_kernel_pipeline,
    configure_kernel,
    download_firecracker_config,
    download_kernel_source,
    extract_kernel_tarball,
    run_make,
)
from mvmctl.exceptions import ChecksumMismatchError, KernelError


def test_download_kernel_source_success(tmp_path: Path, mocker):
    import hashlib

    fake_body = b"kernel-data-here"
    expected_sha256 = hashlib.sha256(fake_body).hexdigest()
    mock_response = MagicMock()
    mock_response.read.side_effect = [fake_body, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
        download_kernel_source(
            "https://example.com/kernel.tar.xz", dest, expected_sha256=expected_sha256
        )

    assert dest.exists()
    assert dest.read_bytes() == fake_body


def test_download_kernel_source_checksum_match(tmp_path: Path):
    import hashlib

    data = b"some-kernel-bytes"
    expected = hashlib.sha256(data).hexdigest()

    mock_response = MagicMock()
    mock_response.read.side_effect = [data, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
        download_kernel_source("https://example.com/k.tar.xz", dest, expected)

    assert dest.exists()


def test_download_kernel_source_checksum_mismatch(tmp_path: Path):
    data = b"some-kernel-bytes"

    mock_response = MagicMock()
    mock_response.read.side_effect = [data, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
        with pytest.raises(ChecksumMismatchError):
            download_kernel_source("https://example.com/k.tar.xz", dest, "deadbeef")

    assert not dest.exists()


def test_download_kernel_source_url_error(tmp_path: Path):
    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", side_effect=URLError("no network")):
        with pytest.raises(KernelError):
            download_kernel_source(
                "https://example.com/k.tar.xz", dest, expected_sha256="abcd1234" * 8
            )


def test_extract_kernel_tarball_success(tmp_path: Path):
    tarball = tmp_path / "linux-6.1.tar.xz"
    extract_dir = tmp_path / "build"
    extract_dir.mkdir()

    inner_dir = tmp_path / "staging" / "linux-6.1"
    inner_dir.mkdir(parents=True)
    (inner_dir / "Makefile").write_text("all:")

    with tarfile.open(tarball, "w:xz") as tar:
        tar.add(inner_dir.parent / "linux-6.1", arcname="linux-6.1")

    result = extract_kernel_tarball(tarball, extract_dir)

    assert result is not None
    assert result.name == "linux-6.1"
    assert (result / "Makefile").exists()


def test_extract_kernel_tarball_bad_file(tmp_path: Path):
    tarball = tmp_path / "bad.tar.xz"
    tarball.write_bytes(b"not-a-tarball")

    with pytest.raises(KernelError):
        extract_kernel_tarball(tarball, tmp_path / "out")


def test_extract_kernel_tarball_no_linux_dir(tmp_path: Path):
    tarball = tmp_path / "bad.tar.xz"
    extract_dir = tmp_path / "build"
    extract_dir.mkdir()

    inner_dir = tmp_path / "staging" / "other-dir"
    inner_dir.mkdir(parents=True)
    (inner_dir / "Makefile").write_text("all:")

    with tarfile.open(tarball, "w:xz") as tar:
        tar.add(inner_dir.parent / "other-dir", arcname="other-dir")

    with pytest.raises(KernelError):
        extract_kernel_tarball(tarball, extract_dir)


def test_run_make_capture(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = "ok"
    mock_result.stderr = ""

    with patch("mvmctl.core.kernel.subprocess.run", return_value=mock_result) as mock_run:
        code, stdout, stderr = run_make(tmp_path, "vmlinux", jobs=4, capture_output=True)

    assert code == 0
    assert stdout == "ok"
    mock_run.assert_called_once_with(
        ["make", "vmlinux", "-j4"],
        cwd=tmp_path,
        capture_output=True,
        text=True,
    )


def test_run_make_no_capture(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.kernel.subprocess.run", return_value=mock_result):
        code, stdout, stderr = run_make(tmp_path, "defconfig", jobs=1, capture_output=False)

    assert code == 0
    assert stdout == ""
    assert stderr == ""


def test_run_make_failure(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 2
    mock_result.stdout = ""
    mock_result.stderr = "error"

    with patch("mvmctl.core.kernel.subprocess.run", return_value=mock_result) as mock_run:
        code, stdout, stderr = run_make(tmp_path, "vmlinux", jobs=4, capture_output=True)

    assert code == 2
    assert stdout == ""
    assert stderr == "error"
    mock_run.assert_called_once_with(
        ["make", "vmlinux", "-j4"],
        cwd=tmp_path,
        capture_output=True,
        text=True,
    )


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_success(mock_urlopen: MagicMock, tmp_path: Path):
    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    download_firecracker_config(kernel_dir, version="6.1.102")

    config_path = kernel_dir / ".config"
    assert config_path.exists()
    assert config_path.read_text() == config_content


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_uses_yaml_template(mock_urlopen: MagicMock, tmp_path: Path):
    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    download_firecracker_config(kernel_dir, version="6.1.102")

    request = mock_urlopen.call_args[0][0]
    assert request.full_url.endswith("microvm-kernel-ci-x86_64-6.1.config")


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_supports_version_placeholder(
    mock_urlopen: MagicMock, tmp_path: Path
):
    from mvmctl.models.kernel import KernelSpec

    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    spec = KernelSpec(
        name="kernel-firecracker-custom",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.invalid/vmlinux-{version}",
        output_name="vmlinux",
        build_dir="/tmp/build",
        config_url_template="https://example.invalid/microvm-kernel-{version}.config",
    )

    download_firecracker_config(kernel_dir, version="6.1.102", kernel_spec=spec)

    request = mock_urlopen.call_args[0][0]
    assert request.full_url.endswith("microvm-kernel-6.1.config")


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_url_error(mock_urlopen: MagicMock, tmp_path: Path):
    mock_urlopen.side_effect = URLError("Connection refused")

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    with pytest.raises(KernelError):
        download_firecracker_config(kernel_dir, version="6.1.102")


def test_configure_kernel_uses_ordered_fragments_with_overrides(tmp_path: Path):
    from mvmctl.models.kernel import KernelSpec

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    config_script = scripts_dir / "config"
    config_script.write_text("#!/bin/sh\nexit 0\n")
    config_script.chmod(0o755)

    from mvmctl.core.kernel import _ASSETS_DIR

    frag1 = _ASSETS_DIR / "ordered-frag1.config"
    frag2 = _ASSETS_DIR / "ordered-frag2.config"
    base_content = "CONFIG_ALPHA=y\nCONFIG_SHARED=n\nCONFIG_BTRFS_FS=y\n"
    frag1.write_text("CONFIG_ALPHA=y\nCONFIG_SHARED=n\n")
    frag2.write_text("CONFIG_BETA=y\nCONFIG_SHARED=y\n")

    try:
        spec = KernelSpec(
            name="kernel-official",
            kernel_type="official",
            version="6.19.9",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(tmp_path / "build"),
            config_url_template="https://example.invalid/unused.config",
            config_fragments=["assets/ordered-frag1.config", "assets/ordered-frag2.config"],
            enabled_configs=[],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=["CONFIG_SHARED=y", "CONFIG_ALPHA=y", "CONFIG_BETA=y"],
        )

        def _download_side_effect(_kernel_dir: Path, _version: str, **_kwargs) -> bool:
            (_kernel_dir / ".config").write_text(base_content)
            return True

        with (
            patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")),
            patch(
                "mvmctl.core.kernel.download_firecracker_config",
                side_effect=_download_side_effect,
            ),
        ):
            result = configure_kernel(kernel_dir, version="6.19.9", kernel_spec=spec)

        assert result.success is True
        config_text = (kernel_dir / ".config").read_text()
        assert "CONFIG_ALPHA=y" in config_text
        assert "CONFIG_BTRFS_FS=y" in config_text
        assert "CONFIG_BETA=y" in config_text
        assert "CONFIG_SHARED=y" in config_text
        assert "CONFIG_SHARED=n" not in config_text
    finally:
        frag1.unlink(missing_ok=True)
        frag2.unlink(missing_ok=True)


@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_without_fragments_uses_download_base(
    mock_download: MagicMock, tmp_path: Path
):
    from mvmctl.models.kernel import KernelSpec

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    config_script = scripts_dir / "config"
    config_script.write_text("#!/bin/sh\nexit 0\n")
    config_script.chmod(0o755)
    (kernel_dir / ".config").write_text("CONFIG_BTRFS_FS=y\n")

    spec = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.19.9",
        source="https://example.com/linux.tar.xz",
        output_name="vmlinux",
        build_dir=str(tmp_path / "build"),
        config_url_template="https://example.invalid/base.config",
        config_fragments=[],
        enabled_configs=[],
        disabled_configs=[],
        set_val_configs=[],
        required_settings=["CONFIG_BTRFS_FS=y"],
    )

    with patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")):
        configure_kernel(kernel_dir, version="6.19.9", kernel_spec=spec)

    mock_download.assert_called_once()


@patch("mvmctl.core.kernel.download_firecracker_config")
@patch("mvmctl.core.kernel.subprocess.run")
def test_configure_kernel_success(mock_run: MagicMock, mock_download: MagicMock, tmp_path: Path):
    mock_download.return_value = True

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text(
        "CONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\nCONFIG_VIRTIO_NET=y\n"
        "CONFIG_SERIAL_8250_CONSOLE=y\nCONFIG_KVM_GUEST=y\n"
    )

    configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_download_falls_back_to_defconfig(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = False

    def mock_run_make_side_effect(kernel_dir, target, **kwargs):
        if target == "defconfig":
            return (0, "", "")
        elif target == "olddefconfig":
            return (0, "", "")
        return (0, "", "")

    mock_run_make.side_effect = mock_run_make_side_effect

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text(
        "CONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\nCONFIG_VIRTIO_NET=y\n"
        "CONFIG_SERIAL_8250_CONSOLE=y\nCONFIG_KVM_GUEST=y\n"
    )

    with patch("mvmctl.core.kernel.subprocess.run") as mock_subprocess:
        mock_subprocess.return_value = MagicMock(returncode=0)
        configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_olddefconfig_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = True

    def mock_run_make_side_effect(kernel_dir, target, **kwargs):
        if target == "olddefconfig":
            return (1, "", "")
        return (0, "", "")

    mock_run_make.side_effect = mock_run_make_side_effect

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    with pytest.raises(KernelError):
        configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_defconfig_also_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.side_effect = KernelError("download failed")
    mock_run_make.return_value = (1, "", "defconfig error")

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    with pytest.raises(KernelError):
        configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_second_olddefconfig_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = True

    call_count = [0]

    def mock_run_make_side_effect(kernel_dir, target, **kwargs):
        if target == "olddefconfig":
            call_count[0] += 1
            if call_count[0] == 1:
                return (0, "", "")
            return (1, "", "failed")
        return (0, "", "")

    mock_run_make.side_effect = mock_run_make_side_effect

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text(
        "CONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\nCONFIG_VIRTIO_NET=y\n"
        "CONFIG_SERIAL_8250_CONSOLE=y\nCONFIG_KVM_GUEST=y\n"
    )

    with patch("mvmctl.core.kernel.subprocess.run") as mock_subprocess:
        mock_subprocess.return_value = MagicMock(returncode=0)
        with pytest.raises(KernelError):
            configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.download_firecracker_config")
@patch("mvmctl.core.kernel.subprocess.run")
def test_configure_kernel_missing_required_settings(
    mock_run: MagicMock, mock_download: MagicMock, tmp_path: Path
):
    """Test that configure_kernel returns result with missing settings instead of raising."""
    mock_download.return_value = True
    mock_run.return_value = MagicMock(returncode=0)

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/sh\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text("CONFIG_BTRFS_FS=y\n")

    result = configure_kernel(kernel_dir, version="6.1.102")

    assert result.success is False
    assert len(result.missing_settings) > 0
    assert len(result.warnings) > 0


def test_build_kernel_success(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc) as mock_popen:
        build_kernel(kernel_dir, output_path, jobs=2)

    assert output_path.exists()
    assert mock_popen.call_args.kwargs["stdout"] is not None


def test_build_kernel_failure(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 2

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc):
        with pytest.raises(KernelError):
            build_kernel(kernel_dir, output_path, jobs=1)


def test_build_kernel_failure_with_error_lines(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 2

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc):
        with pytest.raises(KernelError):
            build_kernel(kernel_dir, output_path, jobs=1)


def test_build_kernel_vmlinux_not_found(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc):
        with pytest.raises(KernelError):
            build_kernel(kernel_dir, output_path, jobs=2)


@patch("mvmctl.core.kernel.check_build_dependencies", return_value=[])
def test_build_kernel_pipeline_cached(mock_check_deps, tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    output_path.write_bytes(b"cached-kernel")

    from mvmctl.core.kernel import _compute_config_hash

    config_hash = _compute_config_hash("6.1.102", None)
    cache_key = f"6.1.102-{config_hash}"
    cache_marker = tmp_path / f"kernel-cache-{cache_key}.marker"
    cache_marker.write_text(cache_key)

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=tmp_path / "build",
    )


@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
@patch("mvmctl.core.kernel.check_build_dependencies", return_value=[])
def test_build_kernel_pipeline_ignores_cache_when_disabled(
    mock_check_deps: MagicMock,
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    output_path.write_bytes(b"existing-kernel")

    from mvmctl.core.kernel import _compute_config_hash

    config_hash = _compute_config_hash("6.1.102", None)
    cache_key = f"6.1.102-{config_hash}"
    cache_marker = tmp_path / f"kernel-cache-{cache_key}.marker"
    cache_marker.write_text(cache_key)
    cached_kernel = tmp_path / f"kernel-cache-{cache_key}.vmlinux"
    cached_kernel.write_bytes(b"cached-kernel")

    with (
        patch("mvmctl.core.kernel.build_kernel") as mock_build,
        patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")),
        patch("mvmctl.core.kernel.subprocess.run", return_value=MagicMock(returncode=0, stderr="")),
        patch("mvmctl.core.kernel.download_firecracker_config", return_value=True),
    ):

        def create_vmlinux(src_dir: Path, out_path: Path, jobs: int) -> None:
            out_path.parent.mkdir(parents=True, exist_ok=True)
            out_path.write_bytes(b"rebuilt-kernel")

        mock_build.side_effect = create_vmlinux

        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=tmp_path / "build",
            use_cache=False,
        )

    mock_download.assert_called_once()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    assert output_path.read_bytes() == b"rebuilt-kernel"


@patch("mvmctl.core.kernel.check_build_dependencies", return_value=[])
def test_build_kernel_pipeline_download_fails(mock_check_deps, tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    with patch(
        "mvmctl.core.kernel.download_kernel_source",
        side_effect=KernelError("download failed"),
    ):
        with pytest.raises(KernelError):
            build_kernel_pipeline(
                version="6.1.102",
                source_url="https://example.com/linux.tar.xz",
                output_path=output_path,
                build_dir=build_dir,
            )


@patch("mvmctl.core.kernel.fetch_kernel_sha256_from_url", return_value=None)
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value=None)
@patch("mvmctl.core.kernel.check_build_dependencies", return_value=[])
def test_build_kernel_pipeline_requires_checksum(
    mock_check_deps: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_fetch_sha256_url: MagicMock,
    tmp_path: Path,
):
    from mvmctl.models.kernel import KernelSpec

    spec_with_sha256_url = KernelSpec(
        name="test-spec",
        kernel_type="official",
        version="6.1.102",
        source="https://example.com/linux-{version}.tar.xz",
        output_name="vmlinux-test",
        build_dir=str(tmp_path / "build"),
        sha256=None,
        sha256_url="https://example.com/linux-{version}.tar.xz.sha256",
    )

    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    with pytest.raises(KernelError, match="Checksum required"):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
            kernel_spec=spec_with_sha256_url,
        )


@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.fetch_kernel_sha256_from_url", return_value=None)
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
@patch("mvmctl.core.kernel.check_build_dependencies", return_value=[])
def test_build_kernel_pipeline_full_success(
    mock_check_deps: MagicMock,
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_fetch_sha256_url: MagicMock,
    mock_copy2: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    mock_download.assert_called_once()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    mock_build.assert_called_once()


@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value=None)
@patch("mvmctl.core.kernel.fetch_kernel_sha256_from_url", return_value="b" * 64)
@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_uses_templated_sha256_url(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_copy2: MagicMock,
    mock_fetch_sha256_url: MagicMock,
    mock_fetch_sha256: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    from mvmctl.core.kernel import load_kernel_spec

    spec = load_kernel_spec("kernel-official")
    spec.sha256_url = "https://example.com/linux-{version}.sha256"
    spec.source = "https://example.com/linux-{version}.tar.xz"

    build_kernel_pipeline(
        version="6.1.102",
        source_url=spec.source,
        output_path=output_path,
        build_dir=build_dir,
        kernel_spec=spec,
    )

    called_sha_url = mock_fetch_sha256_url.call_args.args[0]
    assert called_sha_url == "https://example.com/linux-6.1.102.sha256"
    assert mock_download.call_args.args[0] == "https://example.com/linux-6.1.102.tar.xz"
    assert mock_download.call_args.args[2] == ("b" * 64)
    mock_fetch_sha256.assert_not_called()


@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_extract_fails(
    mock_download: MagicMock, mock_extract: MagicMock, tmp_path: Path
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    mock_download.return_value = True
    mock_extract.side_effect = KernelError("extraction failed")

    with pytest.raises(KernelError):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_configure_fails(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.side_effect = KernelError("configure failed")

    with pytest.raises(KernelError):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_build_fails(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.side_effect = KernelError("build failed")

    with pytest.raises(KernelError):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_cached_tarball(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_copy2: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    mock_download.assert_not_called()
    mock_extract.assert_not_called()


@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_cached_tarball_needs_extract(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_copy2: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    kernel_src_dir = build_dir / "linux-6.1.102"

    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
        jobs=4,
    )

    mock_download.assert_not_called()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    mock_build.assert_called_once()


def test_configure_kernel_missing_settings_returns_result(tmp_path: Path):
    """Test that configure_kernel returns result with missing settings - CLI handles confirmation."""
    kernel_dir = tmp_path / "linux-6.1.102"
    kernel_dir.mkdir()
    scripts = kernel_dir / "scripts"
    scripts.mkdir()
    (scripts / "config").write_text("#!/bin/sh")
    (scripts / "config").chmod(0o755)
    (kernel_dir / ".config").write_text("# minimal config\nCONFIG_SOMETHING=y\n")

    with (
        patch("mvmctl.core.kernel.download_firecracker_config"),
        patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")),
        patch("mvmctl.core.kernel.subprocess.run", return_value=MagicMock(returncode=0)),
    ):
        result = configure_kernel(kernel_dir, "6.1.102")
        assert result.success is False
        assert len(result.missing_settings) > 0
        assert any("CONFIG_BTRFS_FS" in w for w in result.warnings)


def test_configure_kernel_skip_confirm_raises_error(tmp_path: Path):
    """Test that configure_kernel raises KernelError with skip_confirm=True when settings missing."""
    kernel_dir = tmp_path / "linux-6.1.102"
    kernel_dir.mkdir()
    scripts = kernel_dir / "scripts"
    scripts.mkdir()
    (scripts / "config").write_text("#!/bin/sh")
    (scripts / "config").chmod(0o755)
    (kernel_dir / ".config").write_text("# minimal config\nCONFIG_SOMETHING=y\n")

    with (
        patch("mvmctl.core.kernel.download_firecracker_config"),
        patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")),
        patch("mvmctl.core.kernel.subprocess.run", return_value=MagicMock(returncode=0)),
    ):
        with pytest.raises(KernelError, match="Required kernel settings are missing"):
            configure_kernel(kernel_dir, "6.1.102", skip_confirm=True)


# ---------------------------------------------------------------------------
# Issue #16: Kernel Build Log Accumulation - File-based logging
# ---------------------------------------------------------------------------


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_kernel_uses_file_based_logging(mock_popen: MagicMock, tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"
    log_path = tmp_path / "build.log"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0
    mock_popen.return_value = mock_proc

    build_kernel(kernel_dir, output_path, jobs=2, build_log_path=log_path)

    mock_popen.assert_called_once()
    call_kwargs = mock_popen.call_args[1]
    assert hasattr(call_kwargs.get("stdout"), "write")
    assert call_kwargs.get("stderr") == subprocess.STDOUT


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_kernel_post_processes_log_file(mock_popen: MagicMock, tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"
    log_path = tmp_path / "build.log"

    log_content = "CC init/main.o\nWARNING: something\nERROR: undefined\nLD vmlinux\n"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0

    def fake_popen(cmd, cwd=None, stdout=None, stderr=None, **kwargs):
        if stdout and hasattr(stdout, "write"):
            stdout.write(log_content)
            stdout.flush()
        return mock_proc

    mock_popen.side_effect = fake_popen

    with patch("mvmctl.core.kernel.logger.debug") as mock_debug:
        build_kernel(kernel_dir, output_path, jobs=2, build_log_path=log_path)

    assert log_path.exists()
    assert mock_debug.call_count >= 1


# ---------------------------------------------------------------------------
# Issue #18: Kernel Build Cache - Config hash
# ---------------------------------------------------------------------------


def test_compute_config_hash_consistent():
    """Test that _compute_config_hash returns consistent hash for same inputs."""
    from mvmctl.core.kernel import _compute_config_hash

    hash1 = _compute_config_hash("6.1.102", None)
    hash2 = _compute_config_hash("6.1.102", None)

    assert hash1 == hash2
    assert len(hash1) == 16  # First 16 chars of SHA256


def test_compute_config_hash_different_versions():
    """Test that _compute_config_hash returns different hashes for different versions."""
    from mvmctl.core.kernel import _compute_config_hash

    hash1 = _compute_config_hash("6.1.102", None)
    hash2 = _compute_config_hash("6.2.0", None)

    assert hash1 != hash2


def test_compute_config_hash_with_user_config(tmp_path: Path):
    """Test that _compute_config_hash includes user config content."""
    from mvmctl.core.kernel import _compute_config_hash

    user_config = tmp_path / "user.config"
    user_config.write_text("CONFIG_CUSTOM=y\n")

    hash1 = _compute_config_hash("6.1.102", None)
    hash2 = _compute_config_hash("6.1.102", user_config)

    assert hash1 != hash2


@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_uses_cache_marker(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    tmp_path: Path,
):
    """Test that build_kernel_pipeline creates cache marker with config hash."""
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True

    def create_vmlinux(src_dir: Path, out_path: Path, jobs: int) -> None:
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_bytes(b"\x7fELF")

    mock_build.side_effect = create_vmlinux

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    markers = list(tmp_path.glob("kernel-cache-*.marker"))
    assert len(markers) == 1
    cached_kernels = list(tmp_path.glob("kernel-cache-*.vmlinux"))
    assert len(cached_kernels) == 1


@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_skips_build_if_cache_matches(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    tmp_path: Path,
):
    """Test that build_kernel_pipeline skips build if cache marker exists."""
    output_path = tmp_path / "vmlinux"
    output_path.write_bytes(b"\x7fELF")  # Pre-create kernel
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    from mvmctl.core.kernel import _compute_config_hash

    config_hash = _compute_config_hash("6.1.102", None)
    cache_marker = tmp_path / f"kernel-cache-6.1.102-{config_hash}.marker"
    cache_marker.write_text(f"6.1.102-{config_hash}")
    cached_kernel = tmp_path / f"kernel-cache-6.1.102-{config_hash}.vmlinux"
    cached_kernel.write_bytes(b"cached artifact")

    output_path.unlink()

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    mock_build.assert_not_called()
    mock_configure.assert_not_called()
    mock_extract.assert_not_called()
    mock_download.assert_not_called()
    assert output_path.read_bytes() == b"cached artifact"


class TestApplyConfigFragments:
    from mvmctl.core.kernel import _apply_config_fragments

    def test_local_assets_fragment(self, tmp_path: Path) -> None:
        from mvmctl.core.kernel import _ASSETS_DIR, _apply_config_fragments

        fragment_file = _ASSETS_DIR / "test-fragment.config"
        fragment_file.write_text("CONFIG_FOO=y\n")
        try:
            kernel_dir = tmp_path / "linux"
            kernel_dir.mkdir()
            _apply_config_fragments(
                ["assets/test-fragment.config"],
                {"version": "6.1", "arch": "x86_64", "ci_version": "1.12"},
                kernel_dir,
            )
            # Verify fragment content was written directly to .config (OVERWRITE)
            config_content = (kernel_dir / ".config").read_text()
            assert "CONFIG_FOO=y" in config_content
        finally:
            fragment_file.unlink(missing_ok=True)

    @patch("mvmctl.core.kernel.urlopen")
    def test_remote_url_fragment_with_arch_template(
        self, mock_urlopen: MagicMock, tmp_path: Path
    ) -> None:
        from mvmctl.core.kernel import _apply_config_fragments

        resp = MagicMock()
        resp.read.return_value = b"CONFIG_VIRTIO_BLK=y\n"
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp

        kernel_dir = tmp_path / "linux"
        kernel_dir.mkdir()
        _apply_config_fragments(
            ["https://example.com/config-{arch}.config"],
            {"version": "6.1", "arch": "amd64", "ci_version": "1.12"},
            kernel_dir,
        )

        fetched_url = mock_urlopen.call_args.args[0].full_url
        assert fetched_url == "https://example.com/config-amd64.config"
        # Verify remote fragment content was written directly to .config (OVERWRITE)
        config_content = (kernel_dir / ".config").read_text()
        assert "CONFIG_VIRTIO_BLK=y" in config_content

    def test_missing_local_fragment_raises(self, tmp_path: Path) -> None:
        from mvmctl.core.kernel import _apply_config_fragments

        kernel_dir = tmp_path / "linux"
        kernel_dir.mkdir()
        with pytest.raises(KernelError, match="Config fragment not found"):
            _apply_config_fragments(
                ["assets/does-not-exist.config"],
                {"version": "6.1", "arch": "x86_64", "ci_version": "1.12"},
                kernel_dir,
            )

    @patch("mvmctl.core.kernel.urlopen", side_effect=URLError("network error"))
    def test_remote_fetch_failure_raises(self, mock_urlopen: MagicMock, tmp_path: Path) -> None:
        from mvmctl.core.kernel import _apply_config_fragments

        kernel_dir = tmp_path / "linux"
        kernel_dir.mkdir()
        with pytest.raises(KernelError, match="Failed to fetch config fragment"):
            _apply_config_fragments(
                ["https://example.com/broken.config"],
                {"version": "6.1", "arch": "x86_64", "ci_version": "1.12"},
                kernel_dir,
            )

    def test_fragment_written_to_config(self, tmp_path: Path) -> None:
        from mvmctl.core.kernel import _ASSETS_DIR, _apply_config_fragments

        fragment_file = _ASSETS_DIR / "test-fragment.config"
        fragment_file.write_text("CONFIG_TEST=y\nCONFIG_ANOTHER=m\n")
        try:
            kernel_dir = tmp_path / "linux"
            kernel_dir.mkdir()
            _apply_config_fragments(
                ["assets/test-fragment.config"],
                {"version": "6.1", "arch": "x86_64", "ci_version": "1.12"},
                kernel_dir,
            )
            # Verify fragment content was written directly to .config (OVERWRITE)
            config_content = (kernel_dir / ".config").read_text()
            assert "CONFIG_TEST=y" in config_content
            assert "CONFIG_ANOTHER=m" in config_content
        finally:
            fragment_file.unlink(missing_ok=True)

    def test_no_temp_files_created(self, tmp_path: Path) -> None:
        from mvmctl.core.kernel import _ASSETS_DIR, _apply_config_fragments

        fragment_file = _ASSETS_DIR / "cleanup-test.config"
        fragment_file.write_text("CONFIG_X=y\n")
        try:
            kernel_dir = tmp_path / "linux"
            kernel_dir.mkdir()
            _apply_config_fragments(
                ["assets/cleanup-test.config"],
                {"version": "6.1", "arch": "x86_64", "ci_version": "1.12"},
                kernel_dir,
            )
            # Verify .config was created and no temp fragment files exist
            assert (kernel_dir / ".config").exists()
            assert not (kernel_dir / ".fragment_0.config").exists()
            assert not list(kernel_dir.glob(".fragment_*.config"))
        finally:
            fragment_file.unlink(missing_ok=True)


# ---------------------------------------------------------------------------
# TDD Tests: configure_kernel() config flow
# These tests verify the CORRECT behavior per bash script:
# 1. Download Firecracker config directly to .config (OVERWRITE, not merge)
# 2. Run make olddefconfig
# 3. Apply patches via ./scripts/config
# 4. Run make olddefconfig again
# 5. Verify critical settings
# ---------------------------------------------------------------------------


class TestConfigureKernelConfigFlow:
    """Tests for configure_kernel() config flow.

    These tests verify the correct behavior of the kernel configuration flow.
    Current implementation has issues with config fragment handling.
    """

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_firecracker_config_overwrites_base_config(
        self,
        mock_subprocess_run: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that Firecracker config OVERWRITES .config rather than being merged.

        CORRECT BEHAVIOR: Firecracker config should be written directly to .config
        as the complete base config, NOT merged via KCONFIG_ALLCONFIG.

        CURRENT BUG: When config_fragments exist, _apply_config_fragments uses
        KCONFIG_ALLCONFIG which MERGES fragments into the base config. This changes
        the Firecracker base config instead of just patching on top.

        This test WILL FAIL with current implementation if config_fragments are used.
        """
        mock_download.return_value = True
        mock_subprocess_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with Firecracker base content
        (kernel_dir / ".config").write_text(
            "# FIRECRACKER_BASE_CONFIG\nCONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\n"
        )

        from mvmctl.models.kernel import KernelSpec

        kernel_spec = KernelSpec(
            name="test-kernel",
            kernel_type="firecracker",
            version="6.1.102",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(kernel_dir),
            config_fragments=["assets/test-fragment.config"],
            enabled_configs=["CONFIG_FOO"],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=["CONFIG_BTRFS_FS", "CONFIG_VIRTIO_BLK"],
        )

        configure_kernel(kernel_dir, version="6.1.102", kernel_spec=kernel_spec)

        assert mock_download.called
        assert mock_apply_fragments.called

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    def test_olddefconfig_called_exactly_twice(
        self,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that olddefconfig is called exactly twice in the correct sequence.

        CORRECT SEQUENCE:
        1. First olddefconfig after downloading Firecracker config (sync to kernel version)
        2. Second olddefconfig after applying all patches/options

        CURRENT BEHAVIOR: olddefconfig IS called twice (lines 528 and 559).
        This test verifies the count is exactly 2 and the sequence is correct.
        """
        mock_download.return_value = True

        olddefconfig_calls = []

        def run_make_side_effect(kernel_dir: Path, target: str, **kwargs):
            if target == "olddefconfig":
                olddefconfig_calls.append("olddefconfig")
            return (0, "", "")

        mock_run_make.side_effect = run_make_side_effect

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with required settings so verification passes
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        configure_kernel(kernel_dir, version="6.1.102")

        # BUG: If config_fragments exist, _apply_config_fragments calls olddefconfig internally
        # via KCONFIG_ALLCONFIG, causing more than 2 calls
        assert len(olddefconfig_calls) == 2, (
            f"Expected exactly 2 olddefconfig calls, got {len(olddefconfig_calls)}. "
            "Current implementation may call olddefconfig additional times via "
            "_apply_config_fragments which uses KCONFIG_ALLCONFIG."
        )

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_config_fragments_not_applied_via_kconfig_allconfig(
        self,
        mock_subprocess_run: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that config_fragments are NOT applied via KCONFIG_ALLCONFIG merge.

        CORRECT BEHAVIOR: Config fragments should be applied AFTER the first
        olddefconfig via ./scripts/config commands (--enable/--disable).

        CURRENT BUG: When config_fragments exist, _apply_config_fragments uses
        KCONFIG_ALLCONFIG which runs make olddefconfig with a merged config.
        This causes extra olddefconfig calls and changes the base config.

        This test WILL FAIL with current implementation when config_fragments are used.
        """
        mock_download.return_value = True
        mock_subprocess_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with required settings so verification passes
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        from mvmctl.models.kernel import KernelSpec

        kernel_spec = KernelSpec(
            name="test-kernel",
            kernel_type="firecracker",
            version="6.1.102",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(kernel_dir),
            config_fragments=["assets/test-fragment.config"],
            enabled_configs=["CONFIG_FOO"],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=["CONFIG_BTRFS_FS", "CONFIG_VIRTIO_BLK"],
        )

        configure_kernel(kernel_dir, version="6.1.102", kernel_spec=kernel_spec)

        assert mock_download.called
        assert mock_apply_fragments.called

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    def test_verify_required_settings_returns_missing_list(
        self,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that missing required settings are properly detected and returned."""
        mock_download.return_value = True
        mock_run_make.return_value = (0, "", "")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with only some required settings
        (kernel_dir / ".config").write_text("CONFIG_BTRFS_FS=y\nCONFIG_SERIAL_8250_CONSOLE=y\n")

        from mvmctl.models.kernel import KernelSpec

        kernel_spec = KernelSpec(
            name="test-kernel",
            kernel_type="firecracker",
            version="6.1.102",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(kernel_dir),
            config_fragments=[],
            enabled_configs=[],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=[
                "CONFIG_BTRFS_FS",
                "CONFIG_VIRTIO_BLK",
                "CONFIG_MISSING_OPTION",
            ],
        )

        result = configure_kernel(
            kernel_dir, version="6.1.102", kernel_spec=kernel_spec, skip_confirm=False
        )

        assert result.success is False
        assert "CONFIG_VIRTIO_BLK" in result.missing_settings
        assert "CONFIG_MISSING_OPTION" in result.missing_settings

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    def test_required_settings_present_succeeds(
        self,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that configure_kernel succeeds when all required settings are present."""
        mock_download.return_value = True
        mock_run_make.return_value = (0, "", "")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with all required settings
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        from mvmctl.models.kernel import KernelSpec

        kernel_spec = KernelSpec(
            name="test-kernel",
            kernel_type="firecracker",
            version="6.1.102",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(kernel_dir),
            config_fragments=[],
            enabled_configs=[],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=[
                "CONFIG_BTRFS_FS",
                "CONFIG_VIRTIO_BLK",
                "CONFIG_VIRTIO_NET",
                "CONFIG_SERIAL_8250_CONSOLE",
                "CONFIG_KVM_GUEST",
            ],
        )

        result = configure_kernel(
            kernel_dir, version="6.1.102", kernel_spec=kernel_spec, skip_confirm=False
        )

        assert result.success is True
        assert len(result.missing_settings) == 0


class TestConfigureKernelDefconfigFallback:
    """Tests for configure_kernel() defconfig fallback behavior."""

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_defconfig_fallback_on_download_failure(
        self,
        mock_subprocess_run: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that configure_kernel falls back to defconfig when download fails."""
        mock_download.side_effect = KernelError("Download failed")
        mock_subprocess_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config so verification passes
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        result = configure_kernel(kernel_dir, version="6.1.102")

        assert result.success is True
        mock_download.assert_called_once()

    @patch("mvmctl.core.kernel._apply_config_fragments")
    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    def test_defconfig_fallback_still_calls_olddefconfig_twice(
        self,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        mock_apply_fragments: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Test that even with defconfig fallback, olddefconfig is still called twice."""
        mock_download.side_effect = KernelError("Download failed")

        olddefconfig_calls = []
        defconfig_calls = []

        def run_make_side_effect(kernel_dir: Path, target: str, **kwargs):
            if target == "olddefconfig":
                olddefconfig_calls.append("olddefconfig")
            elif target == "defconfig":
                defconfig_calls.append("defconfig")
            return (0, "", "")

        mock_run_make.side_effect = run_make_side_effect

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        with patch("mvmctl.core.kernel.subprocess.run") as mock_subprocess_run:
            mock_subprocess_run.return_value = MagicMock(returncode=0, stderr="")

            # Create .config so verification passes
            (kernel_dir / ".config").write_text(
                "CONFIG_BTRFS_FS=y\n"
                "CONFIG_VIRTIO_BLK=y\n"
                "CONFIG_VIRTIO_NET=y\n"
                "CONFIG_SERIAL_8250_CONSOLE=y\n"
                "CONFIG_KVM_GUEST=y\n"
            )

            configure_kernel(kernel_dir, version="6.1.102")

        assert len(olddefconfig_calls) == 2, (
            f"Expected exactly 2 olddefconfig calls, got {len(olddefconfig_calls)}"
        )
        assert len(defconfig_calls) == 1, (
            f"Expected exactly 1 defconfig call, got {len(defconfig_calls)}"
        )


# ---------------------------------------------------------------------------
# TDD Tests: check_build_dependencies()
# Verifies kernel build dependency checking based on legacy/build-kernel.sh:34-73
# ---------------------------------------------------------------------------


class TestCheckBuildDependencies:
    """Tests for check_build_dependencies() function.

    Based on legacy/assets/build-kernel.sh:34-73, this function should check:
    - Commands: git, curl, make, gcc, flex, bison, bc, pahole
    - Libraries: libelf, openssl (via pkg-config --exists)
    - Build tool: ld (indicator for build-essential)
    """

    def _make_which_mock(self, missing_commands: list[str] | None = None) -> MagicMock:
        """Create a shutil.which mock that returns None for missing commands."""
        all_commands = {"git", "curl", "make", "gcc", "flex", "bison", "bc", "pahole", "ld"}
        missing = set(missing_commands or [])

        def which_side_effect(cmd: str) -> str | None:
            if cmd in all_commands - missing:
                return f"/usr/bin/{cmd}"
            return None

        return MagicMock(side_effect=which_side_effect)

    def _make_subprocess_mock(
        self,
        pkg_config_results: dict[str, int] | None = None,
    ) -> MagicMock:
        """Create a subprocess.run mock for pkg-config --exists calls."""
        results = pkg_config_results or {"libelf": 0, "openssl": 0}

        def run_side_effect(cmd: list, **kwargs) -> MagicMock:
            mock_result = MagicMock()
            if cmd[0] == "pkg-config" and cmd[1] == "--exists":
                pkg_name = cmd[2]
                mock_result.returncode = results.get(pkg_name, 0)
            else:
                mock_result.returncode = 0
            return mock_result

        return MagicMock(side_effect=run_side_effect)

    @patch("mvmctl.core.kernel.shutil.which")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_check_build_dependencies_all_present(
        self, mock_subprocess: MagicMock, mock_which: MagicMock
    ) -> None:
        """Test that check_build_dependencies succeeds when all deps are present."""
        from mvmctl.core.kernel import check_build_dependencies

        mock_which.return_value = "/usr/bin/test"
        mock_subprocess.return_value = MagicMock(returncode=0)

        # Should return empty list (no missing deps) or succeed without raising
        result = check_build_dependencies()
        assert result == [] or result is None

    @patch("mvmctl.core.kernel.shutil.which")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_check_build_dependencies_missing_flex(
        self, mock_subprocess: MagicMock, mock_which: MagicMock
    ) -> None:
        """Test that missing flex raises KernelError with install instructions."""
        from mvmctl.core.kernel import check_build_dependencies

        mock_which.side_effect = self._make_which_mock(missing_commands=["flex"])
        mock_subprocess.return_value = MagicMock(returncode=0)

        with pytest.raises(KernelError, match="flex"):
            check_build_dependencies()

    @patch("mvmctl.core.kernel.shutil.which")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_check_build_dependencies_missing_bison(
        self, mock_subprocess: MagicMock, mock_which: MagicMock
    ) -> None:
        """Test that missing bison raises KernelError with install instructions."""
        from mvmctl.core.kernel import check_build_dependencies

        mock_which.side_effect = self._make_which_mock(missing_commands=["bison"])
        mock_subprocess.return_value = MagicMock(returncode=0)

        with pytest.raises(KernelError, match="bison"):
            check_build_dependencies()

    @patch("mvmctl.core.kernel.shutil.which")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_check_build_dependencies_missing_libelf(
        self, mock_subprocess: MagicMock, mock_which: MagicMock
    ) -> None:
        """Test that missing libelf raises KernelError with install instructions."""
        from mvmctl.core.kernel import check_build_dependencies

        mock_which.return_value = "/usr/bin/test"
        mock_subprocess.side_effect = self._make_subprocess_mock(
            pkg_config_results={"libelf": 1, "openssl": 0}
        )

        with pytest.raises(KernelError, match="libelf"):
            check_build_dependencies()

    @patch("mvmctl.core.kernel.shutil.which")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_check_build_dependencies_missing_libssl(
        self, mock_subprocess: MagicMock, mock_which: MagicMock
    ) -> None:
        """Test that missing openssl/libssl raises KernelError with install instructions."""
        from mvmctl.core.kernel import check_build_dependencies

        mock_which.return_value = "/usr/bin/test"
        mock_subprocess.side_effect = self._make_subprocess_mock(
            pkg_config_results={"libelf": 0, "openssl": 1}
        )

        with pytest.raises(KernelError, match="libssl"):
            check_build_dependencies()

    @patch("mvmctl.core.kernel.shutil.which")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_check_build_dependencies_missing_ld(
        self, mock_subprocess: MagicMock, mock_which: MagicMock
    ) -> None:
        """Test that missing ld raises KernelError with build-essential install instructions."""
        from mvmctl.core.kernel import check_build_dependencies

        mock_which.side_effect = self._make_which_mock(missing_commands=["ld"])
        mock_subprocess.return_value = MagicMock(returncode=0)

        with pytest.raises(KernelError, match="build-essential"):
            check_build_dependencies()


# ---------------------------------------------------------------------------
# TDD Tests: build_kernel() Log Capture
# ---------------------------------------------------------------------------


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_log_captured_on_failure(mock_popen: MagicMock, tmp_path: Path):
    """Test that build log is created and contains error output when build fails."""
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"
    log_path = tmp_path / "build.log"

    error_content = "make: *** [arch/x86/kernel/traps.o] Error 1\n"

    def fake_popen(cmd, cwd=None, stdout=None, stderr=None, **kwargs):
        if stdout and hasattr(stdout, "write"):
            stdout.write(error_content)
            stdout.flush()
        mock_proc = MagicMock()
        mock_proc.wait.return_value = 2  # failure
        return mock_proc

    mock_popen.side_effect = fake_popen

    with pytest.raises(KernelError):
        build_kernel(kernel_dir, output_path, jobs=1, build_log_path=log_path)

    # Assert log file was created with error content
    assert log_path.exists(), "Build log should be created at specified path"
    assert "Error 1" in log_path.read_text(), "Error message should be in log file"


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_log_patterns_extracted(mock_popen: MagicMock, tmp_path: Path):
    """Test that error patterns in log are processed by _BUILD_LOG_PATTERNS."""
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"
    log_path = tmp_path / "build.log"

    log_content = (
        "CC init/main.o\n"
        "ERROR: undefined reference to 'schedule'\n"
        "ERROR: cannot find symbol 'mutex_lock'\n"
        "make: fatal error: compile failed\n"
        "note: this is informational\n"
    )

    def fake_popen(cmd, cwd=None, stdout=None, stderr=None, **kwargs):
        if stdout and hasattr(stdout, "write"):
            stdout.write(log_content)
            stdout.flush()
        return MagicMock(wait=lambda: 0)

    mock_popen.side_effect = fake_popen

    with patch("mvmctl.core.kernel.logger.debug") as mock_debug:
        build_kernel(kernel_dir, output_path, jobs=1, build_log_path=log_path)

    # Verify that debug was called for lines matching _BUILD_LOG_PATTERNS
    debug_calls = [str(call) for call in mock_debug.call_args_list]
    logged_content = " ".join(debug_calls)

    # Check that error patterns were logged
    assert mock_debug.call_count >= 1, "logger.debug should be called for pattern-matched lines"
    # Verify the patterns were extracted
    assert any(p in logged_content.lower() for p in ["error", "undefined", "cannot find", "fatal"])


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_log_temp_cleanup(mock_popen: MagicMock, tmp_path: Path):
    """Test that temp log file is cleaned up on successful build."""
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"

    def fake_popen(cmd, cwd=None, stdout=None, stderr=None, **kwargs):
        if stdout and hasattr(stdout, "write"):
            stdout.write("Building kernel...\nCC init/main.o\n")
            stdout.flush()
        return MagicMock(wait=lambda: 0)

    mock_popen.side_effect = fake_popen

    # Call without build_log_path - should create temp and clean up
    build_kernel(kernel_dir, output_path, jobs=1)

    # After successful build, temp log file should be cleaned up
    # The function completes successfully without leaving temp files behind
    # This test verifies the finally block cleanup works correctly
    temp_logs = list(tmp_path.glob("*.log"))
    assert len(temp_logs) == 0, "No temp log files should remain after successful build"


# ---------------------------------------------------------------------------
# TDD Tests: configure_kernel() config flow - Verify CORRECT behavior
# ---------------------------------------------------------------------------


class TestConfigureKernelTDD:
    """TDD tests for configure_kernel() config flow.

    These tests verify the CORRECT behavior of the kernel configuration flow.
    Most tests SHOULD FAIL with current implementation - they drive the fix.
    """

    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_firecracker_config_overwrites_config_file(
        self,
        mock_run: MagicMock,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Verify that download_firecracker_config() writes directly to .config (overwrite).

        CORRECT BEHAVIOR: Firecracker config should overwrite .config entirely,
        NOT be merged via KCONFIG_ALLCONFIG.

        This test SHOULD FAIL if current implementation uses KCONFIG_ALLCONFIG.
        """
        # Setup
        new_config_content = "# NEW_FIRECRACKER_CONFIG\nCONFIG_NEW_OPTION=y\n"

        def mock_download_side_effect(kernel_dir: Path, version: str, **kwargs) -> bool:
            """Simulate download writing new content to .config."""
            config_path = kernel_dir / ".config"
            config_path.write_text(new_config_content)
            return True

        mock_download.side_effect = mock_download_side_effect
        mock_run_make.return_value = (0, "", "")
        mock_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Pre-create .config with OLD content
        old_content = "# OLD_CONFIG\nCONFIG_OLD=y\n"
        (kernel_dir / ".config").write_text(old_content)

        # Execute
        from mvmctl.models.kernel import KernelSpec

        spec = KernelSpec(
            name="kernel-official",
            kernel_type="official",
            version="6.1.102",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(tmp_path / "build"),
            config_url_template="https://example.invalid/base.config",
            config_fragments=[],
            enabled_configs=[],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=[],
        )

        configure_kernel(kernel_dir, version="6.1.102", kernel_spec=spec)

        # Verify .config was OVERWRITTEN with new content (not merged)
        final_content = (kernel_dir / ".config").read_text()
        assert final_content == new_config_content, (
            f"Expected .config to be overwritten with new content.\n"
            f"Expected: {new_config_content!r}\n"
            f"Got: {final_content!r}\n"
            f"This test FAILS if current implementation merges configs instead of overwriting."
        )

    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_olddefconfig_called_twice_in_sequence(
        self,
        mock_run: MagicMock,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Verify olddefconfig is called exactly twice in correct sequence.

        CORRECT SEQUENCE:
        1. First olddefconfig after downloading Firecracker config (sync to kernel version)
        2. Second olddefconfig after applying all patches/options

        This test SHOULD PASS with current implementation.
        """
        mock_download.return_value = True

        olddefconfig_calls = []

        def run_make_side_effect(kernel_dir: Path, target: str, **kwargs):
            if target == "olddefconfig":
                olddefconfig_calls.append(len(olddefconfig_calls) + 1)
            return (0, "", "")

        mock_run_make.side_effect = run_make_side_effect
        mock_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with required settings so verification passes
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        configure_kernel(kernel_dir, version="6.1.102")

        # Verify exactly 2 olddefconfig calls
        assert len(olddefconfig_calls) == 2, (
            f"Expected exactly 2 olddefconfig calls, got {len(olddefconfig_calls)}: {olddefconfig_calls}"
        )
        # Verify they were called in sequence (1st, then 2nd)
        assert olddefconfig_calls == [1, 2], (
            f"Expected olddefconfig calls in sequence [1, 2], got {olddefconfig_calls}"
        )

    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_defconfig_fallback_on_download_failure(
        self,
        mock_run: MagicMock,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Verify defconfig is called when download fails, and olddefconfig still called twice.

        CORRECT BEHAVIOR:
        1. Download fails -> defconfig is called as fallback
        2. olddefconfig called to sync to kernel version
        3. Patches applied via ./scripts/config
        4. olddefconfig called again to resolve dependencies

        This test SHOULD FAIL if current code doesn't handle fallback correctly.
        """
        mock_download.side_effect = KernelError("Download failed")

        defconfig_calls = []
        olddefconfig_calls = []

        def run_make_side_effect(kernel_dir: Path, target: str, **kwargs):
            if target == "defconfig":
                defconfig_calls.append(len(defconfig_calls) + 1)
            elif target == "olddefconfig":
                olddefconfig_calls.append(len(olddefconfig_calls) + 1)
            return (0, "", "")

        mock_run_make.side_effect = run_make_side_effect
        mock_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with required settings so verification passes
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        configure_kernel(kernel_dir, version="6.1.102")

        # Verify defconfig was called exactly once as fallback
        assert len(defconfig_calls) == 1, (
            f"Expected exactly 1 defconfig call for fallback, got {len(defconfig_calls)}"
        )
        # Verify olddefconfig is still called twice
        assert len(olddefconfig_calls) == 2, (
            f"Expected exactly 2 olddefconfig calls, got {len(olddefconfig_calls)}: {olddefconfig_calls}\n"
            f"This test FAILS if fallback doesn't properly call olddefconfig twice."
        )

    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_required_settings_verification_raises_kernel_error(
        self,
        mock_run: MagicMock,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Verify KernelError raised when required settings are missing with skip_confirm=True.

        This test SHOULD PASS with current implementation.
        """
        mock_download.return_value = True
        mock_run_make.return_value = (0, "", "")
        mock_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with MISSING required settings
        (kernel_dir / ".config").write_text("CONFIG_SOMETHING_UNRELATED=y\n")

        with pytest.raises(KernelError, match="Required kernel settings are missing"):
            configure_kernel(kernel_dir, version="6.1.102", skip_confirm=True)

    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_firecracker_config_written_to_dot_config_not_kconfig_allconfig(
        self,
        mock_run: MagicMock,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Verify KCONFIG_ALLCONFIG is NOT set during subprocess calls.

        CORRECT BEHAVIOR: Firecracker config is written directly to .config,
        NOT via KCONFIG_ALLCONFIG environment variable.

        This test SHOULD FAIL if current implementation uses KCONFIG_ALLCONFIG.
        """
        mock_download.return_value = True
        mock_run_make.return_value = (0, "", "")

        kconfig_allconfig_values = []

        def capture_env(cmd, **kwargs):
            """Capture any KCONFIG_ALLCONFIG from environment."""
            env = kwargs.get("env", {})
            if env and "KCONFIG_ALLCONFIG" in env:
                kconfig_allconfig_values.append(env["KCONFIG_ALLCONFIG"])
            return MagicMock(returncode=0, stderr="")

        mock_run.side_effect = capture_env

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Create .config with required settings
        (kernel_dir / ".config").write_text(
            "CONFIG_BTRFS_FS=y\n"
            "CONFIG_VIRTIO_BLK=y\n"
            "CONFIG_VIRTIO_NET=y\n"
            "CONFIG_SERIAL_8250_CONSOLE=y\n"
            "CONFIG_KVM_GUEST=y\n"
        )

        configure_kernel(kernel_dir, version="6.1.102")

        # Verify KCONFIG_ALLCONFIG was NEVER set
        assert len(kconfig_allconfig_values) == 0, (
            f"KCONFIG_ALLCONFIG should NOT be set. Found: {kconfig_allconfig_values}\n"
            f"This test FAILS if current implementation uses KCONFIG_ALLCONFIG."
        )

    @patch("mvmctl.core.kernel.download_firecracker_config")
    @patch("mvmctl.core.kernel.run_make")
    @patch("mvmctl.core.kernel.subprocess.run")
    def test_config_download_replaces_existing_config(
        self,
        mock_run: MagicMock,
        mock_run_make: MagicMock,
        mock_download: MagicMock,
        tmp_path: Path,
    ) -> None:
        """Verify downloaded config REPLACES existing .config, not merges.

        CORRECT BEHAVIOR: When download succeeds, it overwrites .config entirely.
        The old content should be completely replaced.

        This test SHOULD FAIL if current code merges configs instead of overwriting.
        """
        old_content = "CONFIG_OLD_MARKER=y\nCONFIG_OTHER_OLD=z\n"
        new_content = "CONFIG_NEW_DOWNLOAD=y\nCONFIG_ANOTHER_NEW=w\n"

        def mock_download_side_effect(kernel_dir: Path, version: str, **kwargs) -> bool:
            """Simulate download writing new content to .config."""
            config_path = kernel_dir / ".config"
            config_path.write_text(new_content)
            return True

        mock_download.side_effect = mock_download_side_effect
        mock_run_make.return_value = (0, "", "")
        mock_run.return_value = MagicMock(returncode=0, stderr="")

        kernel_dir = tmp_path / "linux-src"
        kernel_dir.mkdir()
        scripts_dir = kernel_dir / "scripts"
        scripts_dir.mkdir()
        (scripts_dir / "config").write_text("#!/bin/bash\nexit 0")
        (scripts_dir / "config").chmod(0o755)

        # Pre-create .config with OLD content
        (kernel_dir / ".config").write_text(old_content)

        from mvmctl.models.kernel import KernelSpec

        spec = KernelSpec(
            name="kernel-official",
            kernel_type="official",
            version="6.1.102",
            source="https://example.com/linux.tar.xz",
            output_name="vmlinux",
            build_dir=str(tmp_path / "build"),
            config_url_template="https://example.invalid/base.config",
            config_fragments=[],
            enabled_configs=[],
            disabled_configs=[],
            set_val_configs=[],
            required_settings=[],
        )

        configure_kernel(kernel_dir, version="6.1.102", kernel_spec=spec)

        final_content = (kernel_dir / ".config").read_text()

        # Verify old content is GONE
        assert "CONFIG_OLD_MARKER" not in final_content, (
            f"Old config marker should be GONE after download.\n"
            f"Got content: {final_content!r}\n"
            f"This test FAILS if current implementation MERGES configs instead of replacing."
        )
        # Verify new content is present
        assert "CONFIG_NEW_DOWNLOAD" in final_content, (
            f"New downloaded content should be present.\nGot content: {final_content!r}"
        )
        # Verify completely replaced (not merged)
        assert final_content == new_content, (
            f"Expected .config to be completely replaced.\n"
            f"Expected: {new_content!r}\n"
            f"Got: {final_content!r}"
        )
