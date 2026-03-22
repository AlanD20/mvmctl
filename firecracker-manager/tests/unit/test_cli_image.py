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
