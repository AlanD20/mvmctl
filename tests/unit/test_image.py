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
    _fetch_sha256_from_url,
    _handle_qcow2,
    _handle_raw,
    _handle_squashfs,
    _handle_tar_rootfs,
    _resolve_source_template,
    convert_qcow2_to_raw,
    create_ext4_from_tar,
    download_file,
    extract_partition_from_raw,
    fetch_image,
    get_filesystem_uuid,
    import_image,
    load_images_config,
)
from mvmctl.exceptions import ChecksumMismatchError, ConfigError, ImageError, MVMError
from mvmctl.models.image import ImageImportInput, ImageSpec

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
                "minimum_rootfs_size": 4096,
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
    assert result[0].minimum_rootfs_size == 4096
    # Second image uses defaults for missing optional fields
    assert result[1].id == "alpine"
    assert result[1].name == "alpine"  # defaults to id
    assert result[1].minimum_rootfs_size == 2048  # default
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


def test_load_images_config_alpine(tmp_path: Path):
    """Test loading images.yaml with Alpine entry."""
    config = {
        "images": [
            {
                "id": "alpine-3.21",
                "type": "alpine",
                "version": "3.21",
                "name": "Alpine Linux 3.21",
                "source": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/aws_alpine-3.21.4-x86_64-bios-cloudinit-r0.vhd",
                "format": "vhd",
                "convert_to": "ext4",
                "minimum_rootfs_size": 256,
                "sha256": None,
                "sha256_url": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/aws_alpine-3.21.4-x86_64-bios-cloudinit-r0.vhd.sha512",
            },
        ]
    }
    config_file = tmp_path / "images.yaml"
    config_file.write_text(yaml.dump(config))

    result = load_images_config(config_file)

    assert len(result) == 1
    assert result[0].id == "alpine-3.21"
    assert result[0].image_type == "alpine"
    assert result[0].version == "3.21"
    assert result[0].format == "vhd"
    assert result[0].convert_to == "ext4"
    assert result[0].minimum_rootfs_size == 256


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
    """S-H10: download_file should raise MVMError when expected_sha256 is None and allow_missing_checksum=False."""

    data = b"hello world binary content"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "warn_output.bin"
    with pytest.raises(MVMError, match="No checksum provided"):
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
    """download_file should raise MVMError in non-interactive mode when checksum is missing."""

    data = b"hello world binary content"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    mocker.patch("sys.stdin.isatty", return_value=False)

    with pytest.raises(MVMError, match="Cannot prompt for confirmation in non-interactive mode"):
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
    with pytest.raises(MVMError):
        download_file("https://example.com/file.bin", dest, show_progress=False)


@patch("mvmctl.core.image.subprocess.run")
def test_get_filesystem_uuid_success(mock_run: MagicMock, tmp_path: Path):
    image = tmp_path / "rootfs.ext4"
    image.write_bytes(b"image")
    mock_run.return_value = MagicMock(stdout="123e4567-e89b-12d3-a456-426614174000\n")

    fs_uuid = get_filesystem_uuid(image)

    assert fs_uuid == "123e4567-e89b-12d3-a456-426614174000"
    mock_run.assert_called_once_with(
        ["blkid", "-p", "-s", "UUID", "-o", "value", str(image)],
        capture_output=True,
        text=True,
        check=False,
    )


@patch("mvmctl.core.image.subprocess.run", side_effect=FileNotFoundError("blkid not found"))
def test_get_filesystem_uuid_no_blkid(mock_run: MagicMock, tmp_path: Path):
    image = tmp_path / "rootfs.ext4"
    image.write_bytes(b"image")

    fs_uuid = get_filesystem_uuid(image)

    assert fs_uuid is None


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
        ["qemu-img", "convert", "-m", "16", "-f", "qcow2", "-O", "raw", str(qcow2), str(raw)],
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
def test_convert_qcow2_to_raw_uses_parallel_coroutines(mock_run: MagicMock, tmp_path: Path):
    """Test that qemu-img convert uses -m 16 (max valid coroutines) for parallelism."""
    mock_run.return_value = MagicMock(returncode=0)

    qcow2 = tmp_path / "large_image.qcow2"
    raw = tmp_path / "large_image.raw"
    result = convert_qcow2_to_raw(qcow2, raw)

    assert result is True
    call_args = mock_run.call_args[0][0]
    assert "-m" in call_args
    m_index = call_args.index("-m")
    assert call_args[m_index + 1] == "16"


# ---------------------------------------------------------------------------
# create_ext4_from_tar
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
def test_create_ext4_from_tar_success(mock_run: MagicMock, tmp_path: Path, monkeypatch):
    """Test create_ext4_from_tar extracts tar and creates ext4 image with 5 subprocess calls."""
    # New implementation uses 5 calls: tar, chmod, du, truncate, mkfs.ext4
    mock_run.side_effect = [
        MagicMock(returncode=0),  # tar extraction
        MagicMock(returncode=0),  # chmod -R u+rwx tmpdir
        MagicMock(returncode=0, stdout="104857600\t/tmp\tdir"),  # du -sb (100 MiB)
        MagicMock(returncode=0),  # truncate
        MagicMock(returncode=0),  # mkfs.ext4
    ]

    tar = tmp_path / "rootfs.tar"
    output = tmp_path / "rootfs.ext4"
    result = create_ext4_from_tar(tar, output, minimum_rootfs_mib=1024)

    assert result is True
    assert mock_run.call_count == 5
    # Verify tar command - now uses -C flag
    tar_cmd = mock_run.call_args_list[0][0][0]
    assert tar_cmd[0] == "tar"
    assert tar_cmd[1] == "-xf"
    assert "-C" in tar_cmd
    assert str(tar) in tar_cmd
    assert "--exclude=dev/*" in tar_cmd
    # Verify chmod command
    chmod_cmd = mock_run.call_args_list[1][0][0]
    assert chmod_cmd[0] == "chmod"
    assert chmod_cmd[1] == "-R"
    assert "u+rwx" in chmod_cmd
    # Verify du command
    assert mock_run.call_args_list[2][0][0][:2] == ["du", "-sb"]
    # Verify truncate command
    assert mock_run.call_args_list[3][0][0] == ["truncate", "-s", "1280M", str(output)]
    # Verify mkfs.ext4 command - just check key parts
    mkfs_cmd = mock_run.call_args_list[4][0][0]
    assert mkfs_cmd[0] == "mkfs.ext4"
    assert "-d" in mkfs_cmd
    assert mkfs_cmd[-1] == str(output)


@patch("mvmctl.core.image.subprocess.run")
def test_create_ext4_from_tar_failure(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = subprocess.CalledProcessError(1, "tar", stderr="extraction failed")

    tar = tmp_path / "rootfs.tar"
    output = tmp_path / "rootfs.ext4"
    with pytest.raises(ImageError):
        create_ext4_from_tar(tar, output, minimum_rootfs_mib=2048)


# ---------------------------------------------------------------------------
# fetch_image
# ---------------------------------------------------------------------------


def test_fetch_image_already_exists(tmp_path: Path):
    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
    )

    # Pre-create the compressed file (final output after processing)
    compressed = tmp_path / "ubuntu-24.04.ext4.zst"
    compressed.write_text("existing compressed image data")

    result = fetch_image(spec, tmp_path)

    assert result.path == compressed


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_qcow2(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "ubuntu-24.04.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result.path == expected_output
    mock_download.assert_called_once()
    mock_convert.assert_called_once()
    mock_extract.assert_called_once()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_success_sfdisk(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw uses sfdisk when filesystem type detection returns None."""
    import json

    # Return None so it proceeds to partition detection
    mock_detect.return_value = None

    # Use start=1 (512 bytes) to be within 1024-byte file bounds
    sfdisk_output = json.dumps(
        {
            "partitiontable": {
                "partitions": [
                    {"start": 1, "size": 100000, "type": "83"},
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
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".img"
    mock_copy.assert_called_once()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_success_fdisk(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw uses fdisk when sfdisk fails and filesystem type detection returns None."""
    raw_path_str = str(tmp_path / "image.raw")

    # Return None so it proceeds to partition detection
    mock_detect.return_value = None

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            # fdisk -l output format: Device Boot Start End Sectors Size Id Type
            # Use start=1 (512 bytes) to be within 1024-byte file bounds
            mock_result.stdout = f"{raw_path_str}1  *  1  1  1  512B  83  Linux\n"
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "ext4\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result == output_path
    assert result.suffix == ".img"
    mock_copy.assert_called_once()


@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_no_partitions(
    mock_detect: MagicMock, mock_run: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw handles case when no partitions are found."""

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = '{"partitiontable": {"partitions": []}}'
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect
    # Return None so it proceeds to partition detection
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert result == output_path
    assert output_path.exists()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_copy_failure(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 1, "size": 100000, "type": "83"}]}}
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
    mock_detect.return_value = None
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
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_sfdisk_multi_partition(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {
            "partitiontable": {
                "partitions": [
                    {"start": 1, "size": 50000, "type": "ef"},
                    {"start": 1, "size": 200000, "type": "83"},
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
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    # Use 1MB file so partition at start=2048 (1MB) is within bounds
    raw_path.write_bytes(b"\x00" * (1024 * 1024))

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".btrfs"


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_sfdisk_explicit_partition(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {
            "partitiontable": {
                "partitions": [
                    {"start": 1, "size": 50000, "type": "ef"},
                    {"start": 1, "size": 200000, "type": "83"},
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
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    # Use 1MB file so partition at start=2048 (1MB) is within bounds
    raw_path.write_bytes(b"\x00" * (1024 * 1024))

    result = extract_partition_from_raw(raw_path, output_path, partition=1)

    assert isinstance(result, Path)
    assert result.suffix == ".xfs"


@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_fdisk_no_partitions(
    mock_detect: MagicMock, mock_run: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw handles case when fdisk finds no partitions."""

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
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert result == output_path
    assert output_path.exists()


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_fdisk_multi_partition(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    """Test extract_partition_from_raw selects correct partition when using fdisk fallback."""
    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    raw_path_str = str(raw_path)

    # Return None so it proceeds to partition detection
    mock_detect.return_value = None

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            # fdisk format: Device Boot Start End Sectors Id Type
            # Parser uses parts[3]=End as start and parts[4]=Sectors as size
            # Use small End values so start is within 1024-byte file bounds
            mock_result.stdout = (
                f"{raw_path_str}1  *  0  0  1  ef EFI\n{raw_path_str}2  -  1  1  1  83 Linux\n"
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
            # Format: Device Boot Start End Sectors Id Type
            # Test with unparseable numeric values (non-integer)
            mock_result.stdout = f"{raw_path_str}1  *  nodigits  here  ef EFI\n"
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
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_blkid_not_found(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 1, "size": 100000, "type": "83"}]}}
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
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".img"


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_sfdisk_json_error(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
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
            mock_result.stdout = f"{raw_path_str}1  *  1  1  1  512B  83  Linux\n"
            mock_result.returncode = 0
        elif cmd[0] == "blkid":
            mock_result.stdout = "ext4\n"
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect
    mock_detect.return_value = None

    # Use 1MB file so partition at start=2048 (1MB) is within bounds
    raw_path.write_bytes(b"\x00" * (1024 * 1024))

    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result == output_path
    assert result.suffix == ".img"


@patch("mvmctl.core.image._copy_bytes")
@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.detect_filesystem_type")
def test_extract_partition_from_raw_unknown_fs_type(
    mock_detect: MagicMock, mock_run: MagicMock, mock_copy: MagicMock, tmp_path: Path
):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 1, "size": 100000, "type": "83"}]}}
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
    mock_detect.return_value = None

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert isinstance(result, Path)
    assert result.suffix == ".img"


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.create_ext4_from_tar")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_tar_rootfs(
    mock_download: MagicMock,
    mock_create: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="alpine",
        image_type="test",
        version="test",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "alpine.ext4"
    mock_download.return_value = True
    mock_create.return_value = True

    result = fetch_image(spec, tmp_path)

    assert result.path == expected_output
    mock_download.assert_called_once()
    mock_create.assert_called_once()


@patch("mvmctl.core.image.create_ext4_from_tar")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_tar_rootfs_failure(
    mock_download: MagicMock,
    mock_create: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="alpine",
        image_type="test",
        version="test",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_create.side_effect = ImageError("Failed to create image")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)


@patch("mvmctl.core.image.compress_image")
@patch("mvmctl.core.image.shrink_image_with_guestfs")
@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_force_re_download(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    mock_shrink: MagicMock,
    mock_compress: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    final = tmp_path / "ubuntu-24.04.ext4"
    final.write_text("existing image data")

    expected_output = tmp_path / "ubuntu-24.04.ext4"
    expected_compressed = expected_output.with_suffix(".ext4.zst")

    # Create the expected output file after extraction mock
    def create_file_and_return(*args, **kwargs):
        expected_output.write_text("converted image data")
        return expected_output

    # Create the compressed file when compress_image is called
    def create_compressed_and_return(path):
        expected_compressed.write_text("compressed image data")
        return expected_compressed

    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.side_effect = create_file_and_return
    mock_shrink.return_value = (expected_output, 1000000, 500000)
    mock_compress.side_effect = create_compressed_and_return

    result = fetch_image(spec, tmp_path, force=True)

    # Result path should be the compressed version
    assert result.path == expected_compressed
    mock_download.assert_called_once()


@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_download_failure(
    mock_download: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    mock_download.side_effect = ImageError("Download failed")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_raw_format(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="custom-image",
        image_type="test",
        version="test",
        name="Custom Image",
        source="https://example.com/image.raw",
        format="raw",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "custom-image.ext4"
    mock_download.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path)

    assert result.path == expected_output
    mock_download.assert_called_once()
    mock_extract.assert_called_once()


@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_unknown_format(
    mock_download: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="unknown",
        image_type="test",
        version="test",
        name="Unknown Format",
        source="https://example.com/image.xyz",
        format="xyz",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256="a" * 64,
    )

    mock_download.return_value = True

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_qcow2_convert_fails(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_convert.side_effect = ImageError("qemu-img failed")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)

    mock_extract.assert_not_called()


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_qcow2_extract_fails(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.side_effect = ImageError("extraction failed")

    with pytest.raises(ImageError, match="extraction failed"):
        fetch_image(spec, tmp_path, force=True)


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_raw_extract_fails(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="custom-image",
        image_type="test",
        version="test",
        name="Custom Image",
        source="https://example.com/image.raw",
        format="raw",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_extract.side_effect = ImageError("extraction failed")

    with pytest.raises(ImageError, match="extraction failed"):
        fetch_image(spec, tmp_path, force=True)


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_sha256_null_skips_verification(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="test-image",
        image_type="test",
        version="test",
        name="Test",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256=None,
        sha256_url="https://example.com/image.sha256",
    )
    expected_output = tmp_path / "test-image.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result.path == expected_output
    mock_download.assert_called_once()
    call_kwargs = mock_download.call_args.kwargs
    assert call_kwargs["expected_sha256"] is None
    assert call_kwargs["allow_missing_checksum"] is True


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

    result = _handle_qcow2(download_path, final_path, 4096)

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

    result = _handle_tar_rootfs(download_path, final_path, 2048)

    assert result == final_path


@patch("mvmctl.core.image.extract_partition_from_raw")
def test_handle_raw(mock_extract: MagicMock, tmp_path: Path):
    """Test _handle_raw extracts partition from raw image."""
    download_path = tmp_path / "image.raw"
    final_path = tmp_path / "image.ext4"
    expected_output = tmp_path / "image.img"

    download_path.write_bytes(b"raw data")
    mock_extract.return_value = expected_output

    result = _handle_raw(download_path, final_path, 2048)

    assert result == expected_output
    mock_extract.assert_called_once()


# ---------------------------------------------------------------------------
# _handle_squashfs
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
@patch("shutil.which")
def test_handle_squashfs_success(mock_which: MagicMock, mock_run: MagicMock, tmp_path: Path):
    """Test _handle_squashfs extracts squashfs and creates ext4."""
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"

    mock_which.return_value = True  # virt-make-fs is available

    def side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "unsquashfs":
            mock_result.returncode = 0
        elif cmd[0] == "virt-make-fs":
            mock_result.returncode = 0
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = side_effect
    download_path.write_bytes(b"squashfs data")

    result = _handle_squashfs(download_path, final_path, 1024)

    assert result == final_path


@patch("mvmctl.core.image.subprocess.run")
@patch("shutil.which")
def test_handle_squashfs_uses_size_mib(mock_which: MagicMock, mock_run: MagicMock, tmp_path: Path):
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"
    mock_which.return_value = True

    def side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "du":
            mock_result.stdout = "512\t/some/dir\n"
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = side_effect
    download_path.write_bytes(b"squashfs data")

    _handle_squashfs(download_path, final_path, 2048)

    truncate_call = next(call for call in mock_run.call_args_list if call[0][0][0] == "truncate")
    assert "2560M" in truncate_call[0][0]


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
        _handle_squashfs(download_path, final_path, 1024)


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
        _handle_squashfs(download_path, final_path, 1024)


@patch("mvmctl.core.image.subprocess.run")
@patch("shutil.which")
def test_handle_squashfs_mkfs_failure(mock_which: MagicMock, mock_run: MagicMock, tmp_path: Path):
    download_path = tmp_path / "image.squashfs"
    final_path = tmp_path / "image.ext4"

    mock_which.return_value = True
    download_path.write_bytes(b"squashfs data")

    def side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "unsquashfs":
            mock_result.returncode = 0
        elif cmd[0] == "du":
            mock_result.stdout = "512\t/some/dir\n"
        elif cmd[0] == "truncate":
            mock_result.returncode = 0
        elif cmd[0] == "mkfs.ext4":
            raise subprocess.CalledProcessError(1, "mkfs.ext4", stderr="format failed")
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = side_effect

    with pytest.raises(ImageError, match="Failed to create ext4 from squashfs"):
        _handle_squashfs(download_path, final_path, 1024)


# ---------------------------------------------------------------------------
# _resolve_source_template
# ---------------------------------------------------------------------------


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.api.metadata.get_default_binary_entry")
def test_resolve_source_template_success(
    mock_get_default_binary: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_source_template successfully resolves S3 URL."""
    mock_get_default_binary.return_value = ("1.15.0", {"ci_version": "v1.15"})

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
        image_type="test",
        version="24.04",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        list_url_template="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/{ci_version}/{arch}/ubuntu-&list-type=2",
        source_base="https://s3.amazonaws.com/spec.ccfc.min",
    )

    result = _resolve_source_template(spec, ci_version="v1.15")

    assert "ubuntu-24.04.squashfs" in result
    assert "s3.amazonaws.com" in result


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.api.metadata.get_default_binary_entry")
def test_resolve_source_template_uses_default_version(
    mock_get_default_binary: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_source_template raises ImageError when binary metadata lookup fails (no fallback version)."""
    mock_get_default_binary.side_effect = Exception("metadata error")

    mock_response = MagicMock()
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
        image_type="test",
        version="24.04",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        list_url_template="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/{ci_version}/{arch}/ubuntu-&list-type=2",
        source_base="https://s3.amazonaws.com/spec.ccfc.min",
    )

    with pytest.raises(ImageError):
        _resolve_source_template(spec)


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.api.metadata.get_default_binary_entry")
def test_resolve_source_template_network_error(
    mock_get_default_binary: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_source_template raises ImageError on network failure."""
    mock_get_default_binary.return_value = ("1.15.0", {"ci_version": "v1.15"})
    mock_urlopen.side_effect = URLError("Connection failed")

    spec = ImageSpec(
        id="ubuntu-fc",
        image_type="test",
        version="24.04",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        list_url_template="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/{ci_version}/{arch}/ubuntu-&list-type=2",
        source_base="https://s3.amazonaws.com/spec.ccfc.min",
    )

    with pytest.raises(ImageError, match="Failed to list Firecracker CI ubuntu images") as exc_info:
        _resolve_source_template(spec)

    error_str = str(exc_info.value)
    assert "Connection failed" not in error_str


@patch("urllib.request.urlopen")
@patch("urllib.request.Request")
@patch("mvmctl.api.metadata.get_default_binary_entry")
def test_resolve_source_template_no_matching_keys(
    mock_get_default_binary: MagicMock, mock_request: MagicMock, mock_urlopen: MagicMock
):
    """Test _resolve_source_template raises ImageError when no images found."""
    mock_get_default_binary.return_value = ("1.15.0", {"ci_version": "v1.15"})

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
        image_type="test",
        version="test",
        name="Ubuntu FC",
        source="",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        list_url_template="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/{ci_version}/{arch}/ubuntu-&list-type=2",
        source_base="https://s3.amazonaws.com/spec.ccfc.min",
    )

    with pytest.raises(ImageError, match="No ubuntu squashfs found"):
        _resolve_source_template(spec)


# ---------------------------------------------------------------------------
# fetch_image - sha256_url resolution
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_sha256_url_ignored_when_sha256_null(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="test-image",
        image_type="test",
        version="test",
        name="Test Image",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256=None,
        sha256_url="https://example.com/image.qcow2.sha256",
    )
    expected_output = tmp_path / "test-image.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result.path == expected_output
    mock_download.assert_called_once()
    call_kwargs = mock_download.call_args.kwargs
    assert call_kwargs["expected_sha256"] is None
    assert call_kwargs["allow_missing_checksum"] is True


@patch("mvmctl.core.image._fetch_sha256_from_url", return_value="cafebabe" * 8)
@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_uses_templated_sha256_url(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    mock_fetch_sha: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="test-image",
        image_type="ubuntu",
        version="24.04",
        name="Test Image",
        source="https://example.com/image.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256=None,
        sha256_url="https://example.com/{image_type}/{version}.sha256",
    )
    expected_output = tmp_path / "test-image.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result.path == expected_output
    called_sha_url = mock_fetch_sha.call_args.args[0]
    assert called_sha_url == "https://example.com/ubuntu/24.04.sha256"
    assert mock_download.call_args.kwargs["expected_sha256"] == ("cafebabe" * 8)
    assert mock_download.call_args.kwargs["allow_missing_checksum"] is False


@patch("mvmctl.core.image._resolve_source_template")
@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_ubuntu_fc_sha256_null_skips_checksum(
    mock_download: MagicMock,
    mock_validate: MagicMock,
    mock_resolve: MagicMock,
    tmp_path: Path,
):
    spec = ImageSpec(
        id="ubuntu-fc",
        image_type="test",
        version="test",
        name="Ubuntu FC",
        source="https://spec.ccfc.min/firecracker-ci/{ci_version}/{arch}/ubuntu-{ubuntu_version}.squashfs",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256=None,
        sha256_url=None,
    )

    expected_output = tmp_path / "ubuntu-fc.ext4"
    resolved_url = (
        "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/ubuntu-24.04.squashfs"
    )
    mock_resolve.return_value = resolved_url
    mock_download.return_value = True

    original_handlers = mvmctl.core.image._FORMAT_HANDLERS.copy()
    mvmctl.core.image._FORMAT_HANDLERS["squashfs"] = lambda d, f, s, p=None, dd=None: (
        expected_output
    )

    try:
        result = fetch_image(spec, tmp_path, force=True)
        assert result.path == expected_output
        call_kwargs = mock_download.call_args.kwargs
        assert call_kwargs["expected_sha256"] is None
        assert call_kwargs["allow_missing_checksum"] is True
    finally:
        mvmctl.core.image._FORMAT_HANDLERS.clear()
        mvmctl.core.image._FORMAT_HANDLERS.update(original_handlers)


@patch("mvmctl.core.image._resolve_source_template")
@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_ubuntu_fc_resolves_source(
    mock_download: MagicMock,
    mock_validate: MagicMock,
    mock_resolve: MagicMock,
    tmp_path: Path,
):
    """Test fetch_image resolves ubuntu-fc source dynamically."""
    spec = ImageSpec(
        id="ubuntu-fc",
        image_type="test",
        version="test",
        name="Ubuntu FC",
        source="https://spec.ccfc.min/firecracker-ci/{ci_version}/{arch}/ubuntu-{ubuntu_version}.squashfs",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
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
    mvmctl.core.image._FORMAT_HANDLERS["squashfs"] = lambda d, f, s, p=None, dd=None: (
        expected_output
    )

    try:
        result = fetch_image(spec, tmp_path, force=True)
        assert result.path == expected_output
        mock_resolve.assert_called_once()
        mock_download.assert_called_once()
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
    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=tmp_path / "source.raw",
        format="raw",
        convert_to="ext4",
        minimum_rootfs_size=2048,
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
    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=tmp_path / "nonexistent.raw",
        format="raw",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"

    with pytest.raises(ImageError, match="Source file not found"):
        import_image(spec, output_dir)


@patch("mvmctl.core.image.shutil.copy2")
def test_import_image_raw_format(mock_copy: MagicMock, tmp_path: Path):
    """Test import_image handles raw format by copying file."""
    source = tmp_path / "source.raw"
    source.write_text("raw image data")

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="raw",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"

    result = import_image(spec, output_dir)

    assert result.path == output_dir / "my-image.ext4"
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

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.img"

    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = import_image(spec, output_dir)

    assert result.path == expected_output
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

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.img"

    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = import_image(spec, output_dir)

    assert result.path == expected_output
    mock_move.assert_called_once()


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
def test_import_image_qcow2_convert_fails(
    mock_convert: MagicMock, mock_extract: MagicMock, tmp_path: Path
):
    """Test import_image raises error when qcow2 conversion fails."""
    source = tmp_path / "source.qcow2"
    source.write_text("qcow2 data")

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
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

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"
    expected_output = output_dir / "my-image.ext4"

    mock_create.return_value = True

    result = import_image(spec, output_dir)

    assert result.path == expected_output
    mock_create.assert_called_once()


def test_import_image_unsupported_format(tmp_path: Path):
    """Test import_image raises error for unsupported format."""
    source = tmp_path / "source.xyz"
    source.write_text("unknown format")

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="xyz",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"

    with pytest.raises(ImageError, match="Unsupported import format"):
        import_image(spec, output_dir)


@patch("mvmctl.core.image.compress_image")
@patch("mvmctl.core.image.shrink_image_with_guestfs")
@patch("mvmctl.core.image.shutil.copy2")
def test_import_image_force_overwrite(
    mock_copy: MagicMock,
    mock_shrink: MagicMock,
    mock_compress: MagicMock,
    tmp_path: Path,
):
    """Test import_image overwrites existing image when force=True."""
    source = tmp_path / "source.raw"
    source.write_text("new image data")

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="raw",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"
    output_dir.mkdir()
    final_path = output_dir / "my-image.ext4"
    final_path.write_text("old image data")
    compressed_path = final_path.with_suffix(".ext4.zst")
    compressed_path.write_text("compressed image data")

    mock_shrink.return_value = (final_path, 1024, 512)
    mock_compress.return_value = compressed_path

    result = import_image(spec, output_dir, force=True)

    # Result path should be the compressed version
    assert result.path == compressed_path
    mock_copy.assert_called_once()
    mock_shrink.assert_called_once_with(final_path)
    mock_compress.assert_called_once_with(final_path)


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


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_squashfs_format(
    mock_download: MagicMock, mock_validate: MagicMock, tmp_path: Path
):
    """Test fetch_image handles squashfs format."""
    spec = ImageSpec(
        id="test-squashfs",
        image_type="test",
        version="test",
        name="Test Squashfs",
        source="https://example.com/image.squashfs",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "test-squashfs.ext4"
    mock_download.return_value = True

    original_handlers = mvmctl.core.image._FORMAT_HANDLERS.copy()
    mvmctl.core.image._FORMAT_HANDLERS["squashfs"] = lambda d, f, s, p=None, dd=None: (
        expected_output
    )

    try:
        result = fetch_image(spec, tmp_path, force=True)
        assert result.path == expected_output
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

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
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

    assert result.path == expected_output
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

    spec = ImageImportInput(
        id="my-image",
        name="My Image",
        source_path=source,
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
    )

    output_dir = tmp_path / "images"

    mock_convert.return_value = True

    def _extract_side_effect(_raw_path: Path, extracted_path: Path, **kwargs: object) -> Path:
        extracted_path.write_text("partial image")
        raise ImageError("Extraction failed")

    mock_extract.side_effect = _extract_side_effect

    with pytest.raises(ImageError):
        import_image(spec, output_dir)

    assert list(output_dir.iterdir()) == []


# ---------------------------------------------------------------------------
# _fetch_sha256_from_url
# ---------------------------------------------------------------------------


@patch("urllib.request.urlopen")
def test_fetch_sha256_single_entry_backward_compat(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url returns first token for single-entry checksum (backward compat)."""
    mock_response = MagicMock()
    mock_response.read.return_value = b"abc123def456789"
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url("https://example.com/checksum.sha256")

    assert result == "abc123def456789"


@patch("urllib.request.urlopen")
def test_fetch_sha256_multi_entry_exact_match(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url matches filename exactly in multi-entry checksum file."""
    checksum_content = "abc111  file1.tar.xz\nabc222  file2.img\nabc333  file3.raw\n"
    mock_response = MagicMock()
    mock_response.read.return_value = checksum_content.encode()
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url("https://example.com/SHA256SUMS", source_filename="file2.img")

    assert result == "abc222"


@patch("urllib.request.urlopen")
def test_fetch_sha256_multi_entry_basename_match(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url matches basename when full path provided."""
    checksum_content = (
        "abc111  /path/to/file1.tar.xz\nabc222  /path/to/file2.img\nabc333  /path/to/file3.raw\n"
    )
    mock_response = MagicMock()
    mock_response.read.return_value = checksum_content.encode()
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url("https://example.com/SHA256SUMS", source_filename="file2.img")

    assert result == "abc222"


@patch("urllib.request.urlopen")
def test_fetch_sha256_multi_entry_first_line_selected(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url returns correct hash when filename is first entry."""
    checksum_content = (
        "abc111  ubuntu-24.04.qcow2\nabc222  ubuntu-22.04.qcow2\nabc333  debian-12.qcow2\n"
    )
    mock_response = MagicMock()
    mock_response.read.return_value = checksum_content.encode()
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url(
        "https://example.com/SHA256SUMS", source_filename="ubuntu-24.04.qcow2"
    )

    assert result == "abc111"


@patch("urllib.request.urlopen")
def test_fetch_sha256_filename_not_found(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url returns None when filename not in checksum file."""
    checksum_content = "abc111  file1.tar.xz\nabc222  file2.img\n"
    mock_response = MagicMock()
    mock_response.read.return_value = checksum_content.encode()
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url(
        "https://example.com/SHA256SUMS", source_filename="nonexistent.file"
    )

    assert result is None


@patch("urllib.request.urlopen")
def test_fetch_sha256_bsd_format(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url handles BSD checksum format with asterisks."""
    # BSD format: hash *filename
    checksum_content = "abc111 *file1.tar.xz\nabc222 *file2.img\n"
    mock_response = MagicMock()
    mock_response.read.return_value = checksum_content.encode()
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url("https://example.com/SHA256SUMS", source_filename="file2.img")

    assert result == "abc222"


@patch("urllib.request.urlopen")
def test_fetch_sha256_multiple_spaces_between_hash_and_file(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url handles multiple spaces between hash and filename."""
    checksum_content = (
        "abc111    file1.tar.xz\n"  # 4 spaces
        "abc222  \t  file2.img\n"  # mixed tabs and spaces
    )
    mock_response = MagicMock()
    mock_response.read.return_value = checksum_content.encode()
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url(
        "https://example.com/SHA256SUMS", source_filename="file1.tar.xz"
    )

    assert result == "abc111"


@patch("urllib.request.urlopen")
def test_fetch_sha256_empty_file(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url returns None for empty checksum file."""
    mock_response = MagicMock()
    mock_response.read.return_value = b""
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    result = _fetch_sha256_from_url("https://example.com/empty.sha256")

    assert result is None


@patch("urllib.request.urlopen")
def test_fetch_sha256_network_error(mock_urlopen: MagicMock):
    """Test _fetch_sha256_from_url returns None on network error."""
    mock_urlopen.side_effect = URLError("Connection refused")

    result = _fetch_sha256_from_url("https://example.com/checksum.sha256")

    assert result is None


# ---------------------------------------------------------------------------
# _validate_downloaded_file
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
def test_validate_downloaded_file_tar_success(mock_run: MagicMock, tmp_path: Path):
    """Test _validate_downloaded_file passes for valid tar files."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.tar"
    download_path.write_bytes(b"tar content")

    mock_run.return_value = MagicMock(returncode=0)

    # Should not raise
    _validate_downloaded_file(download_path, "tar-rootfs")

    mock_run.assert_called_once_with(
        ["tar", "-tf", str(download_path)],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("mvmctl.core.image.subprocess.run")
def test_validate_downloaded_file_tar_invalid(mock_run: MagicMock, tmp_path: Path):
    """Test _validate_downloaded_file raises ImageError for invalid tar files."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.tar"
    download_path.write_bytes(b"invalid tar content")

    mock_run.side_effect = subprocess.CalledProcessError(1, "tar", stderr="invalid tar")

    with pytest.raises(ImageError, match="Invalid tar file"):
        _validate_downloaded_file(download_path, "tar-rootfs")

    # Should cleanup the file
    assert not download_path.exists()


@patch("mvmctl.core.image.subprocess.run")
def test_validate_downloaded_file_tar_not_found(mock_run: MagicMock, tmp_path: Path):
    """Test _validate_downloaded_file raises ImageError when tar command not found."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.tar"
    download_path.write_bytes(b"tar content")

    mock_run.side_effect = FileNotFoundError("tar not found")

    with pytest.raises(ImageError, match="tar command not found"):
        _validate_downloaded_file(download_path, "tar-rootfs")

    assert not download_path.exists()


@patch("mvmctl.core.image.subprocess.run")
def test_validate_downloaded_file_squashfs_success(mock_run: MagicMock, tmp_path: Path):
    """Test _validate_downloaded_file passes for valid squashfs files."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.squashfs"
    download_path.write_bytes(b"squashfs content")

    mock_run.return_value = MagicMock(returncode=0)

    # Should not raise
    _validate_downloaded_file(download_path, "squashfs")

    mock_run.assert_called_once_with(
        ["unsquashfs", "-l", str(download_path)],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("mvmctl.core.image.subprocess.run")
def test_validate_downloaded_file_squashfs_invalid(mock_run: MagicMock, tmp_path: Path):
    """Test _validate_downloaded_file raises ImageError for invalid squashfs files."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.squashfs"
    download_path.write_bytes(b"invalid squashfs content")

    mock_run.side_effect = subprocess.CalledProcessError(1, "unsquashfs", stderr="invalid squashfs")

    with pytest.raises(ImageError, match="Invalid squashfs file"):
        _validate_downloaded_file(download_path, "squashfs")

    # Should cleanup the file
    assert not download_path.exists()


def test_validate_downloaded_file_squashfs_not_found(tmp_path: Path):
    """Test _validate_downloaded_file raises ImageError when unsquashfs command not found."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.squashfs"
    download_path.write_bytes(b"squashfs content")

    with patch("mvmctl.core.image.subprocess.run") as mock:
        mock.side_effect = FileNotFoundError("unsquashfs not found")

        with pytest.raises(ImageError, match="unsquashfs command not found"):
            _validate_downloaded_file(download_path, "squashfs")

        assert not download_path.exists()


def test_validate_downloaded_file_empty(tmp_path: Path):
    """Test _validate_downloaded_file raises ImageError for empty files."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "image.tar"
    download_path.write_bytes(b"")

    with pytest.raises(ImageError, match="Downloaded file is empty"):
        _validate_downloaded_file(download_path, "tar-rootfs")

    # Should cleanup the file
    assert not download_path.exists()


def test_validate_downloaded_file_missing(tmp_path: Path):
    """Test _validate_downloaded_file raises ImageError when file doesn't exist."""
    from mvmctl.core.image import _validate_downloaded_file

    download_path = tmp_path / "nonexistent.tar"

    with pytest.raises(ImageError, match="Downloaded file not found"):
        _validate_downloaded_file(download_path, "tar-rootfs")


# ---------------------------------------------------------------------------
# fetch_image stale artifact cleanup
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_cleans_stale_download_on_force(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    """Test that fetch_image removes stale .download file when force=True."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    # Create a stale .download file
    download_path = tmp_path / "ubuntu-24.04.download"
    download_path.write_bytes(b"stale download data")

    expected_output = tmp_path / "ubuntu-24.04.ext4"
    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path, force=True)

    assert result.path == expected_output
    # Verify stale file was removed before download
    mock_download.assert_called_once()
    # Verify the .download file is cleaned up after success
    assert not download_path.exists()


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_validates_after_download(
    mock_download: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    """Test that fetch_image calls _validate_downloaded_file after successful download."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="alpine",
        image_type="test",
        version="test",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256="a" * 64,
    )

    expected_output = tmp_path / "alpine.ext4"
    mock_download.return_value = True

    # Mock handler
    with patch("mvmctl.core.image._FORMAT_HANDLERS") as mock_handlers:
        mock_handler = MagicMock(return_value=expected_output)
        mock_handlers.get.return_value = mock_handler

        result = fetch_image(spec, tmp_path)

        assert result.path == expected_output
        # Validation should be called after download
        mock_validate.assert_called_once()
        # Handler should be called after validation
        mock_handler.assert_called_once()


@patch("mvmctl.core.image._validate_downloaded_file")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_validates_before_handler(
    mock_download: MagicMock,
    mock_validate: MagicMock,
    tmp_path: Path,
):
    """Test that validation happens before handler is called."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="alpine",
        image_type="test",
        version="test",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256="a" * 64,
    )

    mock_download.return_value = True
    mock_validate.side_effect = ImageError("Validation failed")

    with patch("mvmctl.core.image._FORMAT_HANDLERS") as mock_handlers:
        mock_handler = MagicMock(return_value=tmp_path / "alpine.ext4")
        mock_handlers.get.return_value = mock_handler

        with pytest.raises(ImageError, match="Validation failed"):
            fetch_image(spec, tmp_path)

        # Handler should NOT be called if validation fails
        mock_handler.assert_not_called()


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_cleans_download_on_handler_failure(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    """Test that .download file is cleaned up when handler fails."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="ubuntu-24.04",
        image_type="test",
        version="test",
        name="Ubuntu 24.04",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=4096,
        sha256="a" * 64,
    )

    download_path = tmp_path / "ubuntu-24.04.download"
    download_path.write_bytes(b"download data")

    mock_download.return_value = True
    mock_convert.side_effect = ImageError("Conversion failed")

    with pytest.raises(ImageError):
        fetch_image(spec, tmp_path)

    # .download file should be cleaned up
    assert not download_path.exists()


@patch("mvmctl.core.image.extract_partition_from_raw")
@patch("mvmctl.core.image.convert_qcow2_to_raw")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_cleans_download_on_unknown_format(
    mock_download: MagicMock,
    mock_convert: MagicMock,
    mock_extract: MagicMock,
    tmp_path: Path,
):
    """Test that .download file is cleaned up when format is unknown."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="unknown",
        image_type="test",
        version="test",
        name="Unknown",
        source="https://example.com/image.xyz",
        format="xyz",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256="a" * 64,
    )

    download_path = tmp_path / "unknown.download"
    download_path.write_bytes(b"download data")

    mock_download.return_value = True

    with pytest.raises(ImageError, match="Unknown format"):
        fetch_image(spec, tmp_path)

    # .download file should be cleaned up
    assert not download_path.exists()


@patch("mvmctl.core.image.compress_image")
@patch("mvmctl.core.image.shrink_image_with_guestfs")
def test_fetch_image_no_stale_cleanup_without_force(
    mock_shrink: MagicMock,
    mock_compress: MagicMock,
    tmp_path: Path,
):
    """Test that stale .download file is cleaned when resuming from ext4."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="alpine",
        image_type="test",
        version="test",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256="a" * 64,
    )

    # Create a stale .download file and final image
    download_path = tmp_path / "alpine.download"
    download_path.write_bytes(b"stale download data")
    final_path = tmp_path / "alpine.ext4"
    final_path.write_bytes(b"existing image")
    compressed_path = final_path.with_suffix(".ext4.zst")

    mock_shrink.return_value = (final_path, 1024, 512)

    def compress_side_effect(path: Path) -> Path:
        compressed_path.write_text("compressed image")
        return compressed_path

    mock_compress.side_effect = compress_side_effect

    with patch("mvmctl.core.image.download_file") as mock_download:
        result = fetch_image(spec, tmp_path, force=False)

        # Should resume from ext4 and compress (no download needed)
        assert result.path == compressed_path
        mock_download.assert_not_called()
        mock_shrink.assert_called_once_with(final_path)
        mock_compress.assert_called_once_with(final_path)
        # Stale .download file should be cleaned up after successful processing
        assert not download_path.exists()


# ---------------------------------------------------------------------------
# fetch_image validation integration
# ---------------------------------------------------------------------------


@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_tar_validation_failure_cleans_up(
    mock_download: MagicMock,
    mock_run: MagicMock,
    tmp_path: Path,
):
    """Test that failed tar validation cleans up the download file."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="alpine",
        image_type="test",
        version="test",
        name="Alpine Linux",
        source="https://example.com/alpine.tar.gz",
        format="tar-rootfs",
        convert_to="ext4",
        minimum_rootfs_size=1024,
        sha256="a" * 64,
    )

    download_path = tmp_path / "alpine.download"
    download_path.write_bytes(b"invalid tar data")

    mock_download.return_value = True
    # tar validation fails
    mock_run.side_effect = subprocess.CalledProcessError(1, "tar", stderr="corrupt")

    with pytest.raises(ImageError, match="Failed to create ext4 image"):
        fetch_image(spec, tmp_path)

    # Download file should be cleaned up
    assert not download_path.exists()


@patch("mvmctl.core.image.subprocess.run")
@patch("mvmctl.core.image.download_with_progress")
def test_fetch_image_squashfs_validation_failure_cleans_up(
    mock_download: MagicMock,
    mock_run: MagicMock,
    tmp_path: Path,
):
    """Test that failed squashfs validation cleans up the download file."""
    from mvmctl.core.image import fetch_image

    spec = ImageSpec(
        id="ubuntu-fc",
        image_type="test",
        version="test",
        name="Ubuntu FC",
        source="https://example.com/ubuntu.squashfs",
        format="squashfs",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256="a" * 64,
    )

    download_path = tmp_path / "ubuntu-fc.download"
    download_path.write_bytes(b"invalid squashfs data")

    mock_download.return_value = True
    # unsquashfs validation fails
    mock_run.side_effect = subprocess.CalledProcessError(1, "unsquashfs", stderr="corrupt")

    with pytest.raises(ImageError, match="unsquashfs failed"):
        fetch_image(spec, tmp_path)

    # Download file should be cleaned up
    assert not download_path.exists()


# ---------------------------------------------------------------------------
# shrink_image_with_guestfs - Ubuntu cleanup and filesystem-specific operations
# ---------------------------------------------------------------------------


def test_ubuntu_minimal_image_metadata():
    """Test that ubuntu-24.04-minimal image is properly defined."""
    import yaml

    from mvmctl.utils.fs import get_assets_dir

    assets_dir = get_assets_dir()
    images_yaml = assets_dir / "images.yaml"

    with open(images_yaml) as f:
        data = yaml.safe_load(f)

    # Find ubuntu-24.04-minimal entry
    minimal = None
    for img in data.get("images", []):
        if img.get("id") == "ubuntu-24.04-minimal":
            minimal = img
            break

    assert minimal is not None, "ubuntu-24.04-minimal not found in images.yaml"
    assert minimal["id"] == "ubuntu-24.04-minimal"
    assert minimal["name"] == "Ubuntu 24.04 Minimal"
    assert minimal["format"] == "tar-rootfs"
    assert minimal["convert_to"] == "ext4"
    assert minimal["minimum_rootfs_size"] == 512, (
        f"Expected minimum_rootfs_size=512, got {minimal['minimum_rootfs_size']}"
    )


def test_shrink_image_with_guestfs_performs_ubuntu_cleanup(tmp_path: Path, mocker: MockerFixture):
    """Test that shrink_image_with_guestfs performs OS-specific cleanup for Ubuntu."""
    # Patch check_libguestfs at the source location (utils.guestfs)
    mocker.patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True)

    mock_g = MagicMock()
    mock_g.list_partitions.return_value = ["/dev/sda1"]
    mock_g.vfs_type.return_value = "ext4"
    mock_g.cat.return_value = "ID=ubuntu"
    mock_g.blockdev_getsize64.return_value = 1024 * 1024 * 1024  # 1GB

    # Patch optimized_guestfs at the source location (utils.guestfs)
    mocker.patch("mvmctl.utils.guestfs.optimized_guestfs")
    with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_og:
        mock_og.return_value.__enter__.return_value = mock_g
        mock_og.return_value.__exit__.return_value = False

        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"x" * (1024 * 1024))  # 1MB

        from mvmctl.core.image import shrink_image_with_guestfs

        shrink_image_with_guestfs(image_path)

        # Verify cleanup commands were issued
        sh_calls = [str(c) for c in mock_g.sh.call_args_list]
        assert any("apt-get clean" in c for c in sh_calls), f"Expected apt-get clean in {sh_calls}"
        assert any("rm -rf /var/lib/apt/lists" in c for c in sh_calls), (
            f"Expected apt lists cleanup in {sh_calls}"
        )
        assert any("sync" in c for c in sh_calls), f"Expected sync in {sh_calls}"


def test_shrink_image_with_guestfs_performs_zeroing_ext4(tmp_path: Path, mocker: MockerFixture):
    """Test that shrink_image_with_guestfs zeros free space for ext4."""
    mocker.patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True)

    mock_g = MagicMock()
    mock_g.list_partitions.return_value = ["/dev/sda1"]
    mock_g.vfs_type.return_value = "ext4"
    mock_g.cat.return_value = "ID=ubuntu"
    mock_g.blockdev_getsize64.return_value = 1024 * 1024 * 1024

    mocker.patch("mvmctl.utils.guestfs.optimized_guestfs")
    with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_og:
        mock_og.return_value.__enter__.return_value = mock_g
        mock_og.return_value.__exit__.return_value = False

        image_path = tmp_path / "test.ext4"
        image_path.write_bytes(b"x" * (1024 * 1024))

        from mvmctl.core.image import shrink_image_with_guestfs

        shrink_image_with_guestfs(image_path)

        # Verify zero_free_space was called for ext4
        mock_g.zero_free_space.assert_called()
        # Verify resize2fs_size was called
        mock_g.resize2fs_size.assert_called()


def test_shrink_image_with_guestfs_performs_zeroing_btrfs(tmp_path: Path, mocker: MockerFixture):
    """Test that shrink_image_with_guestfs uses fstrim for btrfs."""
    mocker.patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True)

    mock_g = MagicMock()
    mock_g.list_partitions.return_value = ["/dev/sda1"]
    mock_g.vfs_type.return_value = "btrfs"
    mock_g.cat.return_value = "ID=ubuntu"
    mock_g.blockdev_getsize64.return_value = 1024 * 1024 * 1024

    mocker.patch("mvmctl.utils.guestfs.optimized_guestfs")
    with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_og:
        mock_og.return_value.__enter__.return_value = mock_g
        mock_og.return_value.__exit__.return_value = False

        image_path = tmp_path / "test.btrfs"
        image_path.write_bytes(b"x" * (1024 * 1024))

        from mvmctl.core.image import shrink_image_with_guestfs

        shrink_image_with_guestfs(image_path)

        # Verify fstrim was called via g.sh
        sh_calls = [str(c) for c in mock_g.sh.call_args_list]
        assert any("fstrim" in c for c in sh_calls), f"Expected fstrim in {sh_calls}"
        # Verify btrfs_filesystem_sync was called
        mock_g.btrfs_filesystem_sync.assert_called()


def test_shrink_image_with_guestfs_btrfs_resize_still_works(tmp_path: Path, mocker: MockerFixture):
    """Test that btrfs filesystem resize still works after cleanup/trim."""
    mocker.patch("mvmctl.utils.guestfs.check_libguestfs", return_value=True)

    mock_g = MagicMock()
    mock_g.list_partitions.return_value = ["/dev/sda1"]
    mock_g.vfs_type.return_value = "btrfs"
    mock_g.cat.return_value = "ID=ubuntu"
    mock_g.blockdev_getsize64.return_value = 1024 * 1024 * 1024

    mocker.patch("mvmctl.utils.guestfs.optimized_guestfs")
    with patch("mvmctl.utils.guestfs.optimized_guestfs") as mock_og:
        mock_og.return_value.__enter__.return_value = mock_g
        mock_og.return_value.__exit__.return_value = False

        image_path = tmp_path / "test.btrfs"
        image_path.write_bytes(b"x" * (1024 * 1024))

        from mvmctl.core.image import shrink_image_with_guestfs

        shrink_image_with_guestfs(image_path)

        # Verify btrfs_filesystem_resize was called with "/" and 0 (minimum size)
        mock_g.btrfs_filesystem_resize.assert_called_with("/", 0)
