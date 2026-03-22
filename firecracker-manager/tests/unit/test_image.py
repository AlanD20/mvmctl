"""Tests for image download and conversion utilities."""

import hashlib
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import yaml

from fcm.core.image import (
    convert_qcow2_to_raw,
    create_ext4_from_tar,
    download_file,
    extract_partition_from_raw,
    fetch_image,
    load_images_config,
)
from fcm.models.image import ImageSpec


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
    result = load_images_config(tmp_path / "nonexistent.yaml")
    assert result == []


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


@patch("fcm.core.image.urlopen")
def test_download_file_success(mock_urlopen: MagicMock, tmp_path: Path):
    data = b"hello world binary content"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    result = download_file("https://example.com/file.bin", dest, show_progress=False)

    assert result is True
    assert dest.exists()
    assert dest.read_bytes() == data


@patch("fcm.core.image.urlopen")
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


@patch("fcm.core.image.urlopen")
def test_download_file_checksum_mismatch(mock_urlopen: MagicMock, tmp_path: Path):
    data = b"checksum test data"
    mock_urlopen.return_value = _mock_urlopen_response(data)

    dest = tmp_path / "output.bin"
    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256="0000000000000000000000000000000000000000000000000000000000000000",
        show_progress=False,
    )

    assert result is False
    assert not dest.exists()  # file deleted on mismatch


@patch("fcm.core.image.urlopen")
def test_download_file_url_error(mock_urlopen: MagicMock, tmp_path: Path):
    mock_urlopen.side_effect = URLError("Connection refused")

    dest = tmp_path / "output.bin"
    result = download_file("https://example.com/file.bin", dest, show_progress=False)

    assert result is False


# ---------------------------------------------------------------------------
# convert_qcow2_to_raw
# ---------------------------------------------------------------------------


@patch("fcm.core.image.subprocess.run")
def test_convert_qcow2_to_raw_success(mock_run: MagicMock, tmp_path: Path):
    mock_run.return_value = MagicMock(returncode=0)

    qcow2 = tmp_path / "image.qcow2"
    raw = tmp_path / "image.raw"
    result = convert_qcow2_to_raw(qcow2, raw)

    assert result is True
    mock_run.assert_called_once_with(
        ["qemu-img", "convert", "-f", "qcow2", "-O", "raw", str(qcow2), str(raw)],
        capture_output=True,
        text=True,
        check=True,
    )


@patch("fcm.core.image.subprocess.run")
def test_convert_qcow2_to_raw_failure(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = subprocess.CalledProcessError(1, "qemu-img", stderr="error")

    result = convert_qcow2_to_raw(tmp_path / "image.qcow2", tmp_path / "image.raw")
    assert result is False


@patch("fcm.core.image.subprocess.run")
def test_convert_qcow2_to_raw_missing_tool(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = FileNotFoundError("qemu-img not found")

    result = convert_qcow2_to_raw(tmp_path / "image.qcow2", tmp_path / "image.raw")
    assert result is False


# ---------------------------------------------------------------------------
# create_ext4_from_tar
# ---------------------------------------------------------------------------


@patch("fcm.core.image.subprocess.run")
def test_create_ext4_from_tar_success(mock_run: MagicMock, tmp_path: Path):
    mock_run.return_value = MagicMock(returncode=0)

    tar = tmp_path / "rootfs.tar"
    output = tmp_path / "rootfs.ext4"
    result = create_ext4_from_tar(tar, output, size="1G")

    assert result is True
    # Verify multiple subprocess.run calls were made (truncate, mkfs.ext4, mount, tar, umount)
    assert mock_run.call_count >= 4


@patch("fcm.core.image.subprocess.run")
def test_create_ext4_from_tar_failure(mock_run: MagicMock, tmp_path: Path):
    mock_run.side_effect = subprocess.CalledProcessError(1, "truncate", stderr="error")

    tar = tmp_path / "rootfs.tar"
    output = tmp_path / "rootfs.ext4"
    result = create_ext4_from_tar(tar, output)

    assert result is False


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


@patch("fcm.core.image.extract_partition_from_raw")
@patch("fcm.core.image.convert_qcow2_to_raw")
@patch("fcm.core.image.download_file")
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


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_success_sfdisk(mock_run: MagicMock, tmp_path: Path):
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
        elif cmd[0] == "dd":
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

    assert result is not None
    assert result.suffix == ".img"


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_success_fdisk(mock_run: MagicMock, tmp_path: Path):
    raw_path_str = str(tmp_path / "image.raw")

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            raise FileNotFoundError("sfdisk not found")
        elif cmd[0] == "fdisk":
            mock_result.stdout = f"{raw_path_str}1  2048  100000  97953  83 Linux\n"
            mock_result.returncode = 0
        elif cmd[0] == "dd":
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

    assert result is not None


@patch("fcm.core.image.subprocess.run")
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


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_dd_failure(mock_run: MagicMock, tmp_path: Path):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 2048, "size": 100000, "type": "83"}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "dd":
            raise subprocess.CalledProcessError(1, "dd", stderr="dd failed")
        else:
            mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    raw_path = tmp_path / "image.raw"
    raw_path.write_bytes(b"\x00" * 1024)
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert result is None


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_sfdisk_multi_partition(mock_run: MagicMock, tmp_path: Path):
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

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "dd":
            # Create the output file so rename works
            output_path.write_bytes(b"\x00" * 64)
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
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path)

    assert result is not None
    assert result.suffix == ".btrfs"


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_sfdisk_explicit_partition(mock_run: MagicMock, tmp_path: Path):
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

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "dd":
            output_path.write_bytes(b"\x00" * 64)
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
    output_path = tmp_path / "output.img"

    result = extract_partition_from_raw(raw_path, output_path, partition=1)

    assert result is not None
    assert result.suffix == ".xfs"


@patch("fcm.core.image.subprocess.run")
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


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_fdisk_multi_partition(mock_run: MagicMock, tmp_path: Path):
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
        elif cmd[0] == "dd":
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

    assert result is not None


@patch("fcm.core.image.subprocess.run")
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

    result = extract_partition_from_raw(raw_path, output_path)

    assert result is None


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_blkid_not_found(mock_run: MagicMock, tmp_path: Path):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 2048, "size": 100000, "type": "83"}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "dd":
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

    assert result is not None
    assert result.suffix == ".img"


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_sfdisk_json_error(mock_run: MagicMock, tmp_path: Path):
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
        elif cmd[0] == "dd":
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

    assert result is not None


@patch("fcm.core.image.subprocess.run")
def test_extract_partition_from_raw_unknown_fs_type(mock_run: MagicMock, tmp_path: Path):
    import json

    sfdisk_output = json.dumps(
        {"partitiontable": {"partitions": [{"start": 2048, "size": 100000, "type": "83"}]}}
    )

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        if cmd[0] == "sfdisk":
            mock_result.stdout = sfdisk_output
            mock_result.returncode = 0
        elif cmd[0] == "dd":
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

    assert result is not None
    assert result.suffix == ".img"


@patch("fcm.core.image.create_ext4_from_tar")
@patch("fcm.core.image.download_file")
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
    )

    expected_output = tmp_path / "alpine.ext4"
    mock_download.return_value = True
    mock_create.return_value = True

    result = fetch_image(spec, tmp_path)

    assert result == expected_output
    mock_download.assert_called_once()
    mock_create.assert_called_once()


@patch("fcm.core.image.create_ext4_from_tar")
@patch("fcm.core.image.download_file")
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
    )

    mock_download.return_value = True
    mock_create.return_value = False

    result = fetch_image(spec, tmp_path)

    assert result is None


@patch("fcm.core.image.extract_partition_from_raw")
@patch("fcm.core.image.convert_qcow2_to_raw")
@patch("fcm.core.image.download_file")
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


@patch("fcm.core.image.download_file")
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
    )

    mock_download.return_value = False

    result = fetch_image(spec, tmp_path)

    assert result is None


@patch("fcm.core.image.extract_partition_from_raw")
@patch("fcm.core.image.download_file")
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
    )

    expected_output = tmp_path / "custom-image.ext4"
    mock_download.return_value = True
    mock_extract.return_value = expected_output

    result = fetch_image(spec, tmp_path)

    assert result == expected_output
    mock_download.assert_called_once()
    mock_extract.assert_called_once()


@patch("fcm.core.image.download_file")
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
    )

    mock_download.return_value = True

    result = fetch_image(spec, tmp_path)

    assert result is None


@patch("fcm.core.image.extract_partition_from_raw")
@patch("fcm.core.image.convert_qcow2_to_raw")
@patch("fcm.core.image.download_file")
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
    )

    mock_download.return_value = True
    mock_convert.return_value = False

    result = fetch_image(spec, tmp_path)

    assert result is None
    mock_extract.assert_not_called()


@patch("fcm.core.image.extract_partition_from_raw")
@patch("fcm.core.image.convert_qcow2_to_raw")
@patch("fcm.core.image.download_file")
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
    )

    mock_download.return_value = True
    mock_convert.return_value = True
    mock_extract.return_value = None

    result = fetch_image(spec, tmp_path)

    assert result is None


@patch("fcm.core.image.extract_partition_from_raw")
@patch("fcm.core.image.download_file")
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
    )

    mock_download.return_value = True
    mock_extract.return_value = None

    result = fetch_image(spec, tmp_path)

    assert result is None
