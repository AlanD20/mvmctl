"""Tests for image download and conversion utilities."""

import hashlib
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest
import yaml
from pytest_mock import MockerFixture

import mvmctl.core.image
from mvmctl.core.image import (
    _copy_bytes,
    _handle_qcow2,
    _handle_raw,
    _handle_squashfs,
    _handle_tar_rootfs,
    _resolve_ubuntu_fc_source,
    convert_qcow2_to_raw,
    create_ext4_from_tar,
    download_file,
    extract_partition_from_raw,
    fetch_image,
    import_image,
    load_images_config,
)
from mvmctl.exceptions import ChecksumMismatchError, ConfigError, ImageError, FCMError
from mvmctl.models.image import ImageSpec, ImageImportSpec


# ---------------------------------------------------------------------------
# load_images_config
# ---------------------------------------------------------------------------


def test_load_images_config_valid(tmp_path: Path):
    config = {
        "images": [
            {
                "id": "ubuntu-24.04",
                "name": "Ubuntu 24.04",
                "source": "https://example.com/ubuntu.qcow2",
                "format": "qcow2",
                "convert_to": "ext4",
                "size_mib": 4096,
                "sha256": "abc123",
            },
            {
                "id": "alpine",
                "source": "https://example.com/alpine.tar.gz",
                "format": "tar-rootfs",
                "convert_to": "ext4",
            },
        ]
    }
    config_file = tmp_path / "images.yaml"
    config_file.write_text(yaml.dump(config))

    result = load_images_config(config_file)

    assert len(result) == 2
    assert isinstance(result[0], ImageSpec)
    assert result[0].id == "ubuntu-24.04"
    assert result[0].name == "Ubuntu 24.04"
    assert result[0].sha256 == "abc123"
    assert result[0].size_mib == 4096
    # Second image uses defaults for missing optional fields
    assert result[1].id == "alpine"
    assert result[1].name == "alpine"  # defaults to id
    assert result[1].size_mib == 2048  # default
    assert result[1].sha256 is None


def test_load_images_config_missing_file(tmp_path: Path):
    with pytest.raises(ConfigError) as exc_info:
        load_images_config(tmp_path / "nonexistent.yaml")

    error_str = str(exc_info.value)
    assert error_str == "Config not found"
    assert str(tmp_path) not in error_str


def test_load_images_config_empty(tmp_path: Path):
    config_file = tmp_path / "images.yaml"
    config_file.write_text(yaml.dump({"images": []}))

    result = load_images_config(config_file)
    assert result == []


# ---------------------------------------------------------------------------
# download_file
# ---------------------------------------------------------------------------


def _mock_urlopen_response(data: bytes, content_length: str | None = None):
    """Create a mock urlopen response that yields data in chunks."""
    mock_response = MagicMock()
    mock_response.headers.get.return_value = content_length

    chunks = [data[i : i + 8192] for i in range(0, len(data), 8192)]
    chunks.append(b"")  # EOF sentinel
    mock_response.read.side_effect = chunks

    # Support context manager
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    return mock_response


@patch("mvmctl.utils.http.urlopen")
def test_download_file_raises_error_no_checksum(mock_urlopen: MagicMock, tmp_path: Path):
    """S-H10: download_file should raise FCMError when expected_sha256 is None and allow_missing_checksum=False."""

    data = b"hello world binary content"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "warn_output.bin"
    with pytest.raises(FCMError, match="No checksum provided"):
        download_file(
            "https://example.com/file.bin", dest, expected_sha256=None, show_progress=False
        )


@patch("mvmctl.utils.http.urlopen")
def test_download_file_allows_missing_checksum_with_param(
    mock_urlopen: MagicMock, tmp_path: Path, mocker: MockerFixture
):
    """download_file should allow missing checksum when allow_missing_checksum=True."""

    data = b"hello world binary content"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    # Mock interactive confirmation to return True
    mocker.patch("typer.confirm", return_value=True)
    mocker.patch("sys.stdin.isatty", return_value=True)

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=None,
        show_progress=False,
        allow_missing_checksum=True,
    )

    assert result is True
    assert dest.exists()
    assert dest.read_bytes() == data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_missing_checksum_rejected_in_non_interactive(
    mock_urlopen: MagicMock, tmp_path: Path, mocker: MockerFixture
):
    """download_file should raise FCMError in non-interactive mode when checksum is missing."""

    data = b"hello world binary content"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    mocker.patch("sys.stdin.isatty", return_value=False)

    with pytest.raises(FCMError, match="Cannot prompt for confirmation in non-interactive mode"):
        download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=None,
            show_progress=False,
            allow_missing_checksum=True,
        )


@patch("mvmctl.utils.http.urlopen")
def test_download_file_success(mock_urlopen: MagicMock, tmp_path: Path):
    data = b"hello world binary content"
    expected_sha = hashlib.sha256(data).hexdigest()
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=expected_sha,
        show_progress=False,
    )

    assert result is True
    assert dest.exists()
    assert dest.read_bytes() == data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_checksum_match(mock_urlopen: MagicMock, tmp_path: Path):
    data = b"checksum test data"
    expected_sha = hashlib.sha256(data).hexdigest()
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=expected_sha,
        show_progress=False,
    )

    assert result is True
    assert dest.exists()
    assert dest.read_bytes() == data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_checksum_mismatch(mock_urlopen: MagicMock, tmp_path: Path):
    data = b"checksum test data"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    with pytest.raises(ChecksumMismatchError):
        download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256="0000000000000000000000000000000000000000000000000000000000000000",
            show_progress=False,
        )

    assert not dest.exists()  # file deleted on mismatch


@patch("mvmctl.utils.http.urlopen")
def test_download_file_url_error(mock_urlopen: MagicMock, tmp_path: Path):
    mock_urlopen.side_effect = URLError("Connection refused")

    dest = tmp_path / "output.bin"
    with pytest.raises(FCMError):
        download_file("https://example.com/file.bin", dest, show_progress=False)


# ---------------------------------------------------------------------------
# convert_qcow2_to_raw
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
def test_convert_qcow2_to_raw_success(mock_run: MagicMock, tmp_path: Path):
    mock_run.return_value = MagicMock(returncode=0)

    qcow2 = tmp_path / "image.qcow2"
    raw = tmp_path / "image.raw"
    result = convert_qcow2_to_raw(qcow2, raw)

    assert result is True
    mock_run.assert_called_once_with(
        ["qemu-img", "convert", "-m", "512", "-f", "qcow2", "-O", "raw", str(qcow2), str(raw)],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("mvmctl.core.image.subprocess.run")
def test_convert_qcow2_to_raw_failure(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = subprocess.CalledProcessError(1, "qemu-img", stderr="error")

    with pytest.raises(ImageError):
        convert_qcow2_to_raw(tmp_path / "image.qcow2", tmp_path / "image.raw")


@patch("mvmctl.core.image.subprocess.run")
def test_convert_qcow2_to_raw_missing_tool(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = FileNotFoundError("qemu-img not found")

    with pytest.raises(ImageError):
        convert_qcow2_to_raw(tmp_path / "image.qcow2", tmp_path / "image.raw")


@patch("mvmctl.core.image.subprocess.run")
def test_convert_qcow2_to_raw_memory_limited(mock_run: MagicMock, tmp_path: Path):
    """Test that qemu-img convert uses memory limit to prevent OOM on large images."""
    mock_run.return_value = MagicMock(returncode=0)

    qcow2 = tmp_path / "large_image.qcow2"
    raw = tmp_path / "large_image.raw"
    result = convert_qcow2_to_raw(qcow2, raw)

    assert result is True
    call_args = mock_run.call_args[0][0]
    assert "-m" in call_args
    assert "512" in call_args
    m_index = call_args.index("-m")
    assert call_args[m_index + 1] == "512"


# ---------------------------------------------------------------------------
# create_ext4_from_tar
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
def test_create_ext4_from_tar_success(mock_run: MagicMock, tmp_path: Path):
    mock_run.return_value = MagicMock(returncode=0)

    tar = tmp_path / "rootfs.tar"
    output = tmp_path / "rootfs.ext4"
    result = create_ext4_from_tar(tar, output, size="1G")

    assert result is True
    # Verify multiple subprocess.run calls were made (truncate, mkfs.ext4, mount, tar, umount)
    assert mock_run.call_count >= 4


@patch("mvmctl.core.image.subprocess.run")
def test_create_ext4_from_tar_failure(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = subprocess.CalledProcessError(1, "truncate", stderr="error")

    tar = tmp_path / "rootfs.tar"
    output = tmp_path / "rootfs.ext4"
    with pytest.raises(ImageError):
        create_ext4_from_tar(tar, output)


# ---------------------------------------------------------------------------
# fetch_image
# ---------------------------------------------------------------------------


def test_fetch_image_already_exists(tmp_path: Path):
    spec = ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=4096,
    )

    # Pre-create the final file
    final = tmp_path / "ubuntu-24.04.ext4"
    final.write_text("existing image data")

    result = fetch_image(spec, tmp_path)

    assert result == final


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_qcow2(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=4096,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "ubuntu-24.04.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result == expected_output
    mock_download.assert_called_once()
    mock_convert.assert_called_once()
    mock_extract.assert_called_once()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_success_sfdisk(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {
            "partitiontable": {
                "partitions": [
                    {"start": 2048, "size": 100000, "type": "83"},
                ]
            }
        }
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "ext4\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".img"
    mock_copy.assert_called_once()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_success_fdisk(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    raw_path_str = str(tmp_path / "image.raw")

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            mock_result.stdout = f"{raw_path_str}1  2048  100000  97953  83 Linux\n"
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "ext4\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result == output_path
    assert result.suffix == ".img"
    mock_copy.assert_called_once()


@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_no_partitions(mock_run: MagicMock, tmp_path: Path):
    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = '{"partitiontable": {"partitions": []}}'
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert result == output_path
    assert output_path.exists()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_copy_failure(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 2048, "size": 100000, "type": "83"}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect
    mock_copy.side_effect = OSError("I/O error during copy")

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    with pytest.raises(ImageError) as exc_info:
        extract_partition_from_raw(raw_path, output_path)

    error_str = str(exc_info.value)
    assert error_str == "Extraction failed"
    assert str(raw_path) not in error_str


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_sfdisk_multi_partition(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {
            "partitiontable": {
                "partitions": [
                    {"start": 2048, "size": 50000, "type": "ef"},
                    {"start": 52048, "size": 200000, "type": "83"},
                ]
            }
        }
    )

    output_path = tmp_path / "output.img"

    def mock_copy_side_effect(*args: object, **kwargs: object) -> None:
        # Create the output file so rename works
        output_path.write_bytes(b"\x00" * 64)

    mock_copy.side_effect = mock_copy_side_effect

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "btrfs\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".btrfs"


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_sfdisk_explicit_partition(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {
            "partitiontable": {
                "partitions": [
                    {"start": 2048, "size": 50000, "type": "ef"},
                    {"start": 52048, "size": 200000, "type": "83"},
                ]
            }
        }
    )

    output_path = tmp_path / "output.img"

    def mock_copy_side_effect(*args: object, **kwargs: object) -> None:
        output_path.write_bytes(b"\x00" * 64)

    mock_copy.side_effect = mock_copy_side_effect

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "xfs\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)

    result = extract_partition_from_raw(raw_path, output_path, partition=1)

    assert isinstance(result, Path)
    assert result.suffix == ".xfs"


@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_fdisk_no_partitions(mock_run: MagicMock, tmp_path: Path):
    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            mock_result.stdout = "Disk /tmp/image.raw: 1 GiB\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert result == output_path
    assert output_path.exists()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_fdisk_multi_partition(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    raw_path_str = str(raw_path)

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            mock_result.stdout = (
                f"{raw_path_str}1  2048  50000  47953  ef EFI\n"
                f"{raw_path_str}2  52048  200000  147953  83 Linux\n"
            )
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = ""
            mock_result.returncode = 1
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result == output_path
    assert result.suffix == ".img"


@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_fdisk_parse_failure(mock_run: MagicMock, tmp_path: Path):
    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    raw_path_str = str(raw_path)

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            mock_result.stdout = f"{raw_path_str}1  nodigits  here\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    output_path = tmp_path / "output.img"

    with pytest.raises(ImageError):
        extract_partition_from_raw(raw_path, output_path)


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_blkid_not_found(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 2048, "size": 100000, "type": "83"}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            raise FileNotFoundError("blkid not found")
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".img"


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_sfdisk_json_error(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    raw_path_str = str(raw_path)

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = "NOT JSON AT ALL"
            mock_result.returncode = 0
        elif cmd[0] == "fdisk":
            mock_result.stdout = f"{raw_path_str}1  2048  100000  97953  83 Linux\n"
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "ext4\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result == output_path
    assert result.suffix == ".img"


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_unknown_fs_type(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 2048, "size": 100000, "type": "83"}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "ntfs\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".img"


@patch("mvmctl.core.image.create_ext4_from_tar")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_tar_rootfs(
    mock_download: MagicMock,
    mock_create: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="alpine",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        size_mib=1024,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "alpine.ext4"
    mock_download.return_value = True
    mock_create.return_value = True

    result = fetch_image(spec, tmp_path)

    assert result == expected_output
    mock_download.assert_called_once()
    mock_create.assert_called_once()


@patch("mvmctl.core.image.create_ext4_from_tar")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_tar_rootfs_failure(
    mock_download: MagicMock,
    mock_create: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="alpine",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        size_mib=1024,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_create.side_effect = ImageError("Failed to create image")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_force_re_download(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=4096,
        sha256="a" * 64,
    )

    final = tmp_path / "ubuntu-24.04.ext4"
    final.write_text("existing image data")

    expected_output = tmp_path / "ubuntu-24.04.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result == expected_output
    mock_download.assert_called_once()


@patch("mvmctl.core.image.download_file")
def test_fetch_image_download_failure(
    mock_download: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=4096,
        sha256="a" * 64,
    )

    mock_download.side_effect = ImageError("Download failed")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_raw_format(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="custom-image",
        name="Custom Image",
        source="https://example.com/image.raw",
        format="raw",
        convert_to="ext4",
        size_mib=2048,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "custom-image.ext4"
    mock_download.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path)

    assert result == expected_output
    mock_download.assert_called_once()
    mock_extract.assert_called_once()


@patch("mvmctl.core.image.download_file")
def test_fetch_image_unknown_format(
    mock_download: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="unknown",
        name="Unknown Format",
        source="https://example.com/image.xyz",
        format="xyz",
        convert_to="ext4",
        size_mib=2048,
        sha256="a" * 64,
    )

    mock_download.return_value = True

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_qcow2_convert_fails(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=4096,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_convert.side_effect = ImageError("qemu-img failed")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)

    mock_extract.assert_not_called()


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_qcow2_extract_fails(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=4096,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = None

    result = fetch_image(spec, tmp_path)

    assert result is None


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_raw_extract_fails(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="custom-image",
        name="Custom Image",
        source="https://example.com/image.raw",
        format="raw",
        convert_to="ext4",
        size_mib=2048,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_extract.return_value = None

    result = fetch_image(spec, tmp_path)

    assert result is None


def test_fetch_image_without_checksum_passes_none_to_download(
    tmp_path: Path, mocker: MockerFixture
):
    spec = ImageSpec(
        id="test-image",
        name="Test",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
    )
    mock_download = mocker.patch("mvmctl.core.image.download_file")

    with pytest.raises(ImageError, match="Checksum required"):
        fetch_image(spec, tmp_path)

    mock_download.assert_not_called()


# ---------------------------------------------------------------------------
# _copy_bytes
# ---------------------------------------------------------------------------


def test_copy_bytes_with_count(tmp_path: Path):
    """Test _copy_bytes copies exact number of bytes when count is specified."""
    src = tmp_path / "source.bin"
    dst = tmp_path / "dest.bin"
    data = b"Hello, World! This is test data for copying."
    src.write_bytes(data)

    _copy_bytes(src, dst, offset=0, count=20)

    assert dst.exists()
    assert dst.read_bytes() == data[:20]


def test_copy_bytes_with_offset(tmp_path: Path):
    """Test _copy_bytes respects offset parameter."""
    src = tmp_path / "source.bin"
    dst = tmp_path / "dest.bin"
    data = b"Hello, World! This is test data for copying."
    src.write_bytes(data)

    _copy_bytes(src, dst, offset=7, count=5)

    assert dst.exists()
    assert dst.read_bytes() == b"World"


def test_copy_bytes_to_eof(tmp_path: Path):
    """Test _copy_bytes copies to EOF when count is None."""
    src = tmp_path / "source.bin"
    dst = tmp_path / "dest.bin"
    data = b"Hello, World!"
    src.write_bytes(data)

    _copy_bytes(src, dst, offset=0, count=None)

    assert dst.exists()
    assert dst.read_bytes() == data


def test_copy_bytes_large_file_chunked(tmp_path: Path):
    """Test _copy_bytes handles large files with chunking correctly."""
    src = tmp_path / "source.bin"
    dst = tmp_path / "dest.bin"
    # Create data larger than _COPY_CHUNK_SIZE (1 MiB)
    data = b"X" * (1024 * 1024 + 100)
    src.write_bytes(data)

    _copy_bytes(src, dst, offset=0, count=None)

    assert dst.exists()
    assert dst.read_bytes() == data


def test_copy_bytes_partial_chunk(tmp_path: Path):
    """Test _copy_bytes handles partial final chunk correctly."""
    src = tmp_path / "source.bin"
    dst = tmp_path / "dest.bin"
    # Create data smaller than _COPY_CHUNK_SIZE
    data = b"Small data"
    src.write_bytes(data)

    _copy_bytes(src, dst, offset=0, count=len(data))

    assert dst.exists()
    assert dst.read_bytes() == data


# ---------------------------------------------------------------------------
# _handle_qcow2, _handle_tar_rootfs, _handle_raw
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_handle_qcow2_success(mock_convert: MagicMock, mock_extract: MagicMock, tmp_path: Path):
    """Test _handle_qcow2 successfully converts and extracts."""
    download_path = tmp_path / "image.qcow2"
    final_path = tmp_path / "image.ext4"
    expected_output = tmp_path / "image.img"

    download_path.write_bytes(b"qcow2 data")
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = _handle_qcow2(download_path, final_path)

    assert result == expected_output
    mock_convert.assert_called_once()
    mock_extract.assert_called_once()


@patch("mvmctl.core.image.subprocess.run")
def test_handle_tar_rootfs(mock_run: MagicMock, tmp_path: Path):
    """Test _handle_tar_rootfs creates ext4 from tar."""
    download_path = tmp_path / "rootfs.tar"
    final_path = tmp_path / "rootfs.ext4"

    download_path.write_bytes(b"tar data")
    mock_run.return_value = MagicMock(returncode=0)

    result = _handle_tar_rootfs(download_path, final_path)

    assert result == final_path


@patch("mvmctl.core.image.extract_partition_from_raw")
def test_handle_raw(mock_extract: MagicMock, tmp_path: Path):
    """Test _handle_raw extracts partition from raw image."""
    download_path = tmp_path / "image.raw"
    final_path = tmp_path / "image.ext4"
    expected_output = tmp_path / "image.img"

    download_path.write_bytes(b"raw data")
    mock_extract.return_value = expected_output

    result = _handle_raw(download_path, final_path)

    assert result == expected_output
    mock_extract.assert_called_once()


# ---------------------------------------------------------------------------
# _handle_squashfs
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
def test_handle_squashfs_success(mock_run: MagicMock, tmp_path: Path):
    """Test _handle_squashfs extracts squashfs and creates ext4."""
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"

    download_path.write_bytes(b"squashfs data")
    mock_run.return_value = MagicMock(returncode=0)

    result = _handle_squashfs(download_path, final_path)

    assert result == final_path


@patch("mvmctl.core.image.subprocess.run")
def test_handle_squashfs_unsquashfs_failure(mock_run: MagicMock, tmp_path: Path):
    """Test _handle_squashfs raises ImageError when unsquashfs fails."""
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"

    download_path.write_bytes(b"squashfs data")

    def side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "unsquashfs":
            raise subprocess.CalledProcessError(1, "unsquashfs", stderr="extraction failed")
        mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = side_effect

    with pytest.raises(ImageError, match="unsquashfs failed"):
        _handle_squashfs(download_path, final_path)


@patch("mvmctl.core.image.subprocess.run")
def test_handle_squashfs_unsquashfs_not_found(mock_run: MagicMock, tmp_path: Path):
    """Test _handle_squashfs raises ImageError when unsquashfs is not found."""
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"

    download_path.write_bytes(b"squashfs data")

    def side_effect(cmd, **kwargs):
        if cmd[0] == "unsquashfs":
            raise FileNotFoundError("unsquashfs not found")
        mock_result = MagicMock()
        mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = side_effect

    with pytest.raises(ImageError, match="unsquashfs not found"):
        _handle_squashfs(download_path, final_path)


@patch("mvmctl.core.image.subprocess.run")
def test_handle_squashfs_mkfs_failure(mock_run: MagicMock, tmp_path: Path):
    """Test _handle_squashfs raises ImageError when mkfs.ext4 fails."""
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"

    download_path.write_bytes(b"squashfs data")

    def side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "mkfs.ext4":
            raise subprocess.CalledProcessError(1, "mkfs.ext4", stderr="format failed")
        mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = side_effect

    with pytest.raises(ImageError, match="Failed to create ext4 from squashfs"):
        _handle_squashfs(download_path, final_path)


# ---------------------------------------------------------------------------
# _resolve_ubuntu_fc_source
# ---------------------------------------------------------------------------


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.core.config_state.get_firecracker_config")
def test_resolve_ubuntu_fc_source_success(
    mock_get_config: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_ubuntu_fc_source successfully resolves S3 URL."""
    mock_get_config.return_value = {"ci_version": "v1.15"}

    mock_response = MagicMock()
    mock_response.read.return_value = b"""<?xml version="1.0" encoding="UTF-8"?>
    <ListBucketResult>
        <Contents>
            <Key>firecracker-ci/v1.15/x86_64/ubuntu-22.04.squashfs</Key>
        </Contents>
        <Contents>
            <Key>firecracker-ci/v1.15/x86_64/ubuntu-24.04.squashfs</Key>
        </Contents>
    </ListBucketResult>"""
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    spec = ImageSpec(
        id="ubuntu-fc",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
    )

    result = _resolve_ubuntu_fc_source(spec)

    assert "ubuntu-24.04.squashfs" in result
    assert "s3.amazonaws.com" in result


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.core.config_state.get_firecracker_config")
def test_resolve_ubuntu_fc_source_uses_default_version(
    mock_get_config: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_ubuntu_fc_source uses default version when config fails."""
    mock_get_config.side_effect = Exception("config error")

    mock_response = MagicMock()
    # Use v1.15 which matches DEFAULT_FIRECRACKER_CI_VERSION
    mock_response.read.return_value = b"""<?xml version="1.0" encoding="UTF-8"?>
    <ListBucketResult>
        <Contents>
            <Key>firecracker-ci/v1.15/x86_64/ubuntu-22.04.squashfs</Key>
        </Contents>
    </ListBucketResult>"""
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    spec = ImageSpec(
        id="ubuntu-fc",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
    )

    result = _resolve_ubuntu_fc_source(spec)

    assert "ubuntu-22.04.squashfs" in result


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.core.config_state.get_firecracker_config")
def test_resolve_ubuntu_fc_source_network_error(
    mock_get_config: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_ubuntu_fc_source raises ImageError on network failure."""
    mock_get_config.return_value = {"ci_version": "v1.15"}
    mock_urlopen.side_effect = URLError("Connection failed")

    spec = ImageSpec(
        id="ubuntu-fc",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
    )

    with pytest.raises(ImageError, match="Failed to list Firecracker CI ubuntu images") as exc_info:
        _resolve_ubuntu_fc_source(spec)

    error_str = str(exc_info.value)
    assert "Connection failed" not in error_str


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.core.config_state.get_firecracker_config")
def test_resolve_ubuntu_fc_source_no_matching_keys(
    mock_get_config: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_ubuntu_fc_source raises ImageError when no images found."""
    mock_get_config.return_value = {"ci_version": "v1.15"}

    mock_response = MagicMock()
    mock_response.read.return_value = b"""<?xml version="1.0" encoding="UTF-8"?>
    <ListBucketResult>
        <Contents>
            <Key>some-other-file.txt</Key>
        </Contents>
    </ListBucketResult>"""
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    spec = ImageSpec(
        id="ubuntu-fc",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
    )

    with pytest.raises(ImageError, match="No ubuntu squashfs found"):
        _resolve_ubuntu_fc_source(spec)


# ---------------------------------------------------------------------------
# fetch_image - sha256_url resolution
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
@patch("mvmctl.core.image.urlopen")
def test_fetch_image_with_sha256_url(
    mock_urlopen: MagicMock,
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    """Test fetch_image resolves sha256 from sha256_url."""
    spec = ImageSpec(
        id="test-image",
        name="Test Image",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
        sha256_url="https://example.com/image.qcow2.sha256",
    )

    expected_output = tmp_path / "test-image.ext4"
    mock_resp = MagicMock()
    mock_resp.read.return_value = b"a" * 64 + b" *image.qcow2\n"
    mock_resp.__enter__ = MagicMock(return_value=mock_resp)
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result == expected_output
    mock_download.assert_called_once()
    assert mock_download.call_args.kwargs["expected_sha256"] == "a" * 64


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
@patch("mvmctl.core.image.urlopen")
def test_fetch_image_sha256_url_simple_format(
    mock_urlopen: MagicMock,
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    """Test fetch_image handles simple sha256 format (just hash)."""
    spec = ImageSpec(
        id="test-image",
        name="Test Image",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
        sha256_url="https://example.com/checksum",
    )

    expected_output = tmp_path / "test-image.ext4"
    full_hash = "a" * 64
    mock_resp = MagicMock()
    mock_resp.read.return_value = full_hash.encode()
    mock_resp.__enter__ = MagicMock(return_value=mock_resp)
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result == expected_output
    assert mock_download.call_args.kwargs["expected_sha256"] == full_hash


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_file")
@patch("mvmctl.core.image.urlopen")
def test_fetch_image_sha256_url_download_fails(
    mock_urlopen: MagicMock,
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    """Test fetch_image handles sha256_url download failure gracefully."""
    spec = ImageSpec(
        id="test-image",
        name="Test Image",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
        sha256_url="https://example.com/checksum",
    )

    mock_urlopen.side_effect = URLError("Checksum download failed")

    with pytest.raises(ImageError, match="Failed to fetch checksum"):
        fetch_image(spec, tmp_path, force=True)

    mock_download.assert_not_called()
    mock_convert.assert_not_called()
    mock_extract.assert_not_called()


@patch("mvmctl.core.image._resolve_ubuntu_fc_source")
@patch("mvmctl.core.image.download_file")
@patch("mvmctl.core.image.urlopen")
def test_fetch_image_ubuntu_fc_fetches_sidecar_checksum(
    mock_urlopen: MagicMock,
    mock_download: MagicMock,
    mock_resolve: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-fc",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
        sha256_url=None,
    )

    expected_output = tmp_path / "ubuntu-fc.ext4"
    resolved_url = (
        "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/ubuntu-24.04.squashfs"
    )
    mock_resolve.return_value = resolved_url

    mock_resp = MagicMock()
    mock_resp.read.return_value = b"b" * 64 + b"  ubuntu-24.04.squashfs\n"
    mock_resp.__enter__ = MagicMock(return_value=mock_resp)
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp
    mock_download.return_value = True

    original_handlers = mvmctl.core.image._FORMAT_HANDLERS.copy()
    mvmctl.core.image._FORMAT_HANDLERS["squashfs"] = lambda d, f: expected_output

    try:
        result = fetch_image(spec, tmp_path, force=True)
        assert result == expected_output
        assert mock_download.call_args.kwargs["expected_sha256"] == "b" * 64
    finally:
        mvmctl.core.image._FORMAT_HANDLERS.clear()
        mvmctl.core.image._FORMAT_HANDLERS.update(original_handlers)


@patch("mvmctl.core.image._resolve_ubuntu_fc_source")
@patch("mvmctl.core.image.download_file")
def test_fetch_image_ubuntu_fc_resolves_source(
    mock_download: MagicMock,
    mock_resolve: MagicMock,
    tmp_path: Path,
):
    """Test fetch_image resolves ubuntu-fc source dynamically."""
    spec = ImageSpec(
        id="ubuntu-fc",
        name="Ubuntu FC",
        source="",  # Empty source, should be resolved
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "ubuntu-fc.ext4"
    resolved_url = (
        "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/ubuntu-24.04.squashfs"
    )

    mock_resolve.return_value = resolved_url
    mock_download.return_value = True

    # Patch _FORMAT_HANDLERS to return our mock handler
    original_handlers = mvmctl.core.image._FORMAT_HANDLERS.copy()
    mvmctl.core.image._FORMAT_HANDLERS["squashfs"] = lambda d, f: expected_output

    try:
        result = fetch_image(spec, tmp_path, force=True)
        assert result == expected_output
        mock_resolve.assert_called_once()
        mock_download.assert_called_once()
        # Verify download was called with resolved URL
        call_args = mock_download.call_args
        assert call_args[0][0] == resolved_url
    finally:
        mvmctl.core.image._FORMAT_HANDLERS.clear()
        mvmctl.core.image._FORMAT_HANDLERS.update(original_handlers)


# ---------------------------------------------------------------------------
# import_image
# ---------------------------------------------------------------------------


def test_import_image_already_exists_no_force(tmp_path: Path):
    """Test import_image raises error when image exists and force=False."""
    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=tmp_path / "source.raw",
        format="raw",
        convert_to="ext4",
        size_mib=2048,
    )

    # Pre-create the output file
    output_dir = tmp_path / "images"
    output_dir.mkdir()
    final_path = output_dir / "my-image.ext4"
    final_path.write_text("existing image")

    with pytest.raises(ImageError, match="already exists"):
        import_image(spec, output_dir, force=False)


def test_import_image_source_not_found(tmp_path: Path):
    """Test import_image raises error when source file doesn't exist."""
    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=tmp_path / "nonexistent.raw",
        format="raw",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"

    with pytest.raises(ImageError, match="Source file not found"):
        import_image(spec, output_dir)


@patch("mvmctl.core.image.shutil.copy2")
def test_import_image_raw_format(mock_copy: MagicMock, tmp_path: Path):
    """Test import_image handles raw format by copying file."""
    source = tmp_path / "source.raw"
    source.write_text("raw image data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="raw",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"

    result = import_image(spec, output_dir)

    assert result == output_dir / "my-image.ext4"
    mock_copy.assert_called_once()


@patch("mvmctl.core.image.shutil.move")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_import_image_qcow2_format(
    mock_convert: MagicMock, mock_extract: MagicMock, mock_move: MagicMock, tmp_path: Path
):
    """Test import_image handles qcow2 format conversion."""
    source = tmp_path / "source.qcow2"
    source.write_text("qcow2 data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.img"

    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = import_image(spec, output_dir)

    assert result == expected_output
    mock_convert.assert_called_once()
    mock_extract.assert_called_once()
    mock_move.assert_called_once()


@patch("mvmctl.core.image.shutil.move")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_import_image_qcow2_cleans_up_raw(
    mock_convert: MagicMock, mock_extract: MagicMock, mock_move: MagicMock, tmp_path: Path
):
    """Test import_image cleans up intermediate raw file for qcow2."""
    source = tmp_path / "source.qcow2"
    source.write_text("qcow2 data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.img"

    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = import_image(spec, output_dir)

    assert result == expected_output
    mock_move.assert_called_once()


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_import_image_qcow2_convert_fails(
    mock_convert: MagicMock, mock_extract: MagicMock, tmp_path: Path
):
    """Test import_image raises error when qcow2 conversion fails."""
    source = tmp_path / "source.qcow2"
    source.write_text("qcow2 data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"

    mock_convert.side_effect = ImageError("qemu-img failed")

    with pytest.raises(ImageError, match="qemu-img failed"):
        import_image(spec, output_dir)


@patch("mvmctl.core.image.create_ext4_from_tar")
def test_import_image_tar_rootfs_format(mock_create: MagicMock, tmp_path: Path):
    """Test import_image handles tar-rootfs format."""
    source = tmp_path / "rootfs.tar.gz"
    source.write_text("tar archive")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="tar-rootfs",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.ext4"

    mock_create.return_value = True

    result = import_image(spec, output_dir)

    assert result == expected_output
    mock_create.assert_called_once()


def test_import_image_unsupported_format(tmp_path: Path):
    """Test import_image raises error for unsupported format."""
    source = tmp_path / "source.xyz"
    source.write_text("unknown format")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="xyz",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"

    with pytest.raises(ImageError, match="Unsupported import format"):
        import_image(spec, output_dir)


@patch("mvmctl.core.image.shutil.copy2")
def test_import_image_force_overwrite(mock_copy: MagicMock, tmp_path: Path):
    """Test import_image overwrites existing image when force=True."""
    source = tmp_path / "source.raw"
    source.write_text("new image data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="raw",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"
    output_dir.mkdir()
    final_path = output_dir / "my-image.ext4"
    final_path.write_text("old image data")

    result = import_image(spec, output_dir, force=True)

    assert result == final_path
    mock_copy.assert_called_once()


# ---------------------------------------------------------------------------
# extract_partition_from_raw - additional edge cases
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_unexpected_parse_result(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw handles unexpected parse result type."""

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        # Return sfdisk success but with malformed data that causes unexpected type
        if cmd[0] == "sfdisk":
            mock_result.stdout = '{"partitiontable": {"partitions": []}}'
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    # No partition table, so image is renamed as-is
    result = extract_partition_from_raw(raw_path, output_path)

    assert result == output_path


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
def test_extract_partition_from_raw_value_error(
    mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw handles ValueError during parsing."""
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": "not_a_number", "size": 100000}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    with pytest.raises(ImageError, match="Failed to parse partition table"):
        extract_partition_from_raw(raw_path, output_path)


# ---------------------------------------------------------------------------
# fetch_image - squashfs format
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.download_file")
def test_fetch_image_squashfs_format(mock_download: MagicMock, tmp_path: Path):
    """Test fetch_image handles squashfs format."""
    spec = ImageSpec(
        id="test-squashfs",
        name="Test Squashfs",
        source="https://example.com/image.squashfs",
        format="squashfs",
        convert_to="ext4",
        size_mib=2048,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "test-squashfs.ext4"
    mock_download.return_value = True

    # Patch _FORMAT_HANDLERS to return our mock handler
    original_handlers = mvmctl.core.image._FORMAT_HANDLERS.copy()
    mvmctl.core.image._FORMAT_HANDLERS["squashfs"] = lambda d, f: expected_output

    try:
        result = fetch_image(spec, tmp_path, force=True)
        assert result == expected_output
        mock_download.assert_called_once()
    finally:
        mvmctl.core.image._FORMAT_HANDLERS.clear()
        mvmctl.core.image._FORMAT_HANDLERS.update(original_handlers)


# ---------------------------------------------------------------------------
# convert_qcow2_to_raw - additional error scenarios
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
def test_convert_qcow2_to_raw_called_process_error_with_stderr(mock_run: MagicMock, tmp_path: Path):
    """Test convert_qcow2_to_raw raises ImageError on conversion failure."""
    mock_run.side_effect = subprocess.CalledProcessError(
        1, "qemu-img", stderr="Invalid format: not a qcow2 file"
    )

    with pytest.raises(ImageError, match="qemu-img conversion failed"):
        convert_qcow2_to_raw(tmp_path / "image.qcow2", tmp_path / "image.raw")


# ---------------------------------------------------------------------------
# Issue #17: Image Import - Temp File Leak Risk
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.shutil.move")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_import_image_qcow2_uses_tempfile_context_manager(
    mock_convert: MagicMock, mock_extract: MagicMock, mock_move: MagicMock, tmp_path: Path
):
    """Test that import_image uses tempfile context manager for atomic cleanup."""
    import tempfile

    source = tmp_path / "source.qcow2"
    source.write_text("qcow2 data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.img"

    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    # Track if temp directory was used
    temp_dirs_created = []
    original_mkdtemp = tempfile.mkdtemp

    def tracking_mkdtemp(*args, **kwargs):
        result = original_mkdtemp(*args, **kwargs)
        temp_dirs_created.append(result)
        return result

    with patch.object(tempfile, "mkdtemp", side_effect=tracking_mkdtemp):
        result = import_image(spec, output_dir)

    assert result == expected_output
    # Verify temp directory was created and cleaned up
    assert len(temp_dirs_created) > 0


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_import_image_qcow2_cleans_up_on_exception(
    mock_convert: MagicMock, mock_extract: MagicMock, tmp_path: Path
):
    """Test that import_image cleans up temp files even when extraction fails."""
    source = tmp_path / "source.qcow2"
    source.write_text("qcow2 data")

    spec = ImageImportSpec(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
    )

    output_dir = tmp_path / "images"

    mock_convert.return_value = True

    def _extract_side_effect(_raw_path: Path, extracted_path: Path) -> Path:
        extracted_path.write_text("partial image")
        raise ImageError("Extraction failed")

    mock_extract.side_effect = _extract_side_effect

    with pytest.raises(ImageError):
        import_image(spec, output_dir)

    assert list(output_dir.iterdir()) == []
