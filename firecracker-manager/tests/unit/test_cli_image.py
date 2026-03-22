import json
import tempfile
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from fcm.cli.image import app
from fcm.models.image import ImageSpec

runner = CliRunner()

_FAKE_IMAGES = [
    ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04 LTS",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
    ),
    ImageSpec(
        id="debian-12",
        name="Debian 12 Bookworm",
        source="https://example.com/debian.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
    ),
]


def test_list_images_table():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        with (
            patch("fcm.cli.image.load_images_config", return_value=_FAKE_IMAGES),
            patch("fcm.cli.image.get_images_dir", return_value=tmp_path),
        ):
            result = runner.invoke(app, ["list"])
    assert result.exit_code == 0
    assert "ubuntu-24.04" in result.output
    assert "debian-12" in result.output


def test_list_images_json():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        with (
            patch("fcm.cli.image.load_images_config", return_value=_FAKE_IMAGES),
            patch("fcm.cli.image.get_images_dir", return_value=tmp_path),
        ):
            result = runner.invoke(app, ["list", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    assert len(data) == 2
    ids = {item["id"] for item in data}
    assert "ubuntu-24.04" in ids
    assert "debian-12" in ids


def test_fetch_image_not_found():
    with patch("fcm.cli.image.load_images_config", return_value=[]):
        result = runner.invoke(app, ["fetch", "nonexistent"])
    assert result.exit_code == 1


def test_delete_image_not_found():
    with tempfile.TemporaryDirectory() as tmp:
        result = runner.invoke(app, ["delete", "--id", "nonexistent", "--images-dir", tmp])
    assert result.exit_code == 1


def test_delete_image_found():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        fake_image = tmp_path / "test.ext4"
        fake_image.write_text("fake image data")
        result = runner.invoke(
            app,
            ["delete", "--id", "test", "--images-dir", str(tmp_path), "--force"],
        )
    assert result.exit_code == 0
    assert not fake_image.exists()


def test_fetch_image_success():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        expected = tmp_path / "ubuntu-24.04.ext4"
        with (
            patch("fcm.cli.image.load_images_config", return_value=_FAKE_IMAGES),
            patch("fcm.cli.image.fetch_image", return_value=expected),
        ):
            result = runner.invoke(app, ["fetch", "ubuntu-24.04", "--out", str(tmp_path)])
    assert result.exit_code == 0


def test_fetch_image_failure():
    with (
        patch("fcm.cli.image.load_images_config", return_value=_FAKE_IMAGES),
        patch("fcm.cli.image.fetch_image", return_value=None),
    ):
        result = runner.invoke(app, ["fetch", "ubuntu-24.04"])
    assert result.exit_code == 1


def test_fetch_all_success():
    with (
        patch("fcm.cli.image.load_images_config", return_value=_FAKE_IMAGES),
        patch("fcm.cli.image.fetch_image", return_value=Path("/fake/output.ext4")),
    ):
        result = runner.invoke(app, ["fetch-all"])
    assert result.exit_code == 0
    assert "2/2" in result.output


def test_fetch_all_empty():
    with patch("fcm.cli.image.load_images_config", return_value=[]):
        result = runner.invoke(app, ["fetch-all"])
    assert result.exit_code == 1


def test_list_images_with_existing_file():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        (tmp_path / "ubuntu-24.04.ext4").write_text("data")
        with (
            patch("fcm.cli.image.load_images_config", return_value=_FAKE_IMAGES),
            patch("fcm.cli.image.get_images_dir", return_value=tmp_path),
        ):
            result = runner.invoke(app, ["list", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0
    assert "✓" in result.output


def test_convert_qcow2():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        src = tmp_path / "image.qcow2"
        src.write_text("fake qcow2")
        dst = tmp_path / "image.ext4"
        with (
            patch("fcm.core.image.convert_qcow2_to_raw", return_value=True),
            patch("fcm.core.image.extract_partition_from_raw", return_value=dst),
        ):
            result = runner.invoke(app, ["convert", "--src", str(src), "--dst", str(dst)])
    assert result.exit_code == 0


def test_convert_tar():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        src = tmp_path / "rootfs.tar.gz"
        src.write_text("fake tar")
        dst = tmp_path / "rootfs.ext4"
        with patch("fcm.core.image.create_ext4_from_tar", return_value=True):
            result = runner.invoke(app, ["convert", "--src", str(src), "--dst", str(dst)])
    assert result.exit_code == 0


def test_convert_raw():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        src = tmp_path / "image.raw"
        src.write_text("fake raw")
        dst = tmp_path / "output.ext4"
        with patch("fcm.core.image.extract_partition_from_raw", return_value=dst):
            result = runner.invoke(app, ["convert", "--src", str(src), "--dst", str(dst)])
    assert result.exit_code == 0


def test_convert_unknown_format():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        src = tmp_path / "image.xyz"
        src.write_text("fake")
        dst = tmp_path / "output.ext4"
        result = runner.invoke(app, ["convert", "--src", str(src), "--dst", str(dst)])
    assert result.exit_code == 1


def test_convert_source_not_found():
    with tempfile.TemporaryDirectory() as tmp:
        result = runner.invoke(
            app, ["convert", "--src", f"{tmp}/nope.qcow2", "--dst", f"{tmp}/out.ext4"]
        )
    assert result.exit_code == 1


def test_delete_image_multiple_formats():
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        (tmp_path / "multi.ext4").write_text("data")
        (tmp_path / "multi.raw").write_text("data")
        result = runner.invoke(
            app,
            ["delete", "--id", "multi", "--images-dir", str(tmp_path), "--force"],
        )
    assert result.exit_code == 0
