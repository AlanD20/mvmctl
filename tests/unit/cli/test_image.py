"""Tests for CLI image commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app
from mvmctl.models import ImageItem

runner = CliRunner()


def _make_image(
    name: str = "Ubuntu 24.04 LTS",
    os_slug: str = "ubuntu-24.04",
    is_default: bool = False,
    is_present: bool = True,
    image_id: str | None = None,
) -> ImageItem:
    return ImageItem(
        id=image_id or f"img-{os_slug}-" + "x" * 55,
        os_slug=os_slug,
        os_name=name,
        arch="x86_64",
        path=f"images/{os_slug}.ext4",
        fs_type="ext4",
        minimum_rootfs_size_mib=2048,
        original_size=1024,
        is_default=is_default,
        is_present=is_present,
        pulled_at="2026-01-01T12:00:00+00:00",
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        compressed_size=500,
        compression_ratio=2.0,
        compressed_format="zst",
    )


class TestImageLs:
    """Tests for 'image ls' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_ls_empty(self, mock_img_op):
        mock_img_op.list_.return_value = []
        result = runner.invoke(app, ["image", "ls"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.image.ImageOperation")
    def test_ls_with_images(self, mock_img_op):
        mock_img_op.list_.return_value = [
            _make_image("Ubuntu 24.04"),
            _make_image(
                "Debian 12", "debian-12", image_id="img-debian-12-" + "x" * 55
            ),
        ]
        result = runner.invoke(app, ["image", "ls"])
        assert result.exit_code == 0
        assert "Ubuntu 24.04" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_ls_json(self, mock_img_op):
        mock_img_op.list_.return_value = [_make_image("Ubuntu 24.04")]
        result = runner.invoke(app, ["image", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) >= 1

    @patch("mvmctl.cli.image.ImageOperation")
    def test_ls_remote(self, mock_img_op):
        from mvmctl.models import ImageSpec

        mock_img_op.list_.return_value = [
            ImageSpec(
                id="ubuntu-24.04",
                image_type="ubuntu",
                version="24.04",
                name="Ubuntu 24.04 LTS",
                source="https://example.com/ubuntu.qcow2",
                format="qcow2",
            ),
        ]
        result = runner.invoke(app, ["image", "ls", "--remote"])
        assert result.exit_code == 0
        assert "ubuntu-24.04" in result.output

    def test_ls_help(self):
        result = runner.invoke(app, ["image", "ls", "--help"])
        assert result.exit_code == 0


class TestImageFetch:
    """Tests for 'image fetch' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_fetch_success(self, mock_img_op):
        img = _make_image("Ubuntu 24.04")
        mock_img_op.fetch.return_value = MagicMock(
            result=img,
            full_hash="a" * 64,
        )
        result = runner.invoke(
            app,
            [
                "image",
                "fetch",
                "ubuntu-24.04",
            ],
        )
        assert result.exit_code == 0
        assert "Image ready" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_fetch_with_force(self, mock_img_op, tmp_path):
        img = _make_image("Ubuntu 24.04")
        mock_img_op.fetch.return_value = MagicMock(
            result=img,
            full_hash="a" * 64,
        )
        result = runner.invoke(
            app,
            [
                "image",
                "fetch",
                "ubuntu-24.04",
                "--force",
            ],
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.image.ImageOperation")
    def test_fetch_not_found(self, mock_img_op):
        mock_img_op.fetch.return_value = None
        result = runner.invoke(app, ["image", "fetch", "nonexistent"])
        assert result.exit_code == 1
        assert "Failed to download" in result.output

    def test_fetch_help(self):
        result = runner.invoke(app, ["image", "fetch", "--help"])
        assert result.exit_code == 0


class TestImageRemove:
    """Tests for 'image rm' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_rm_success(self, mock_img_op):
        mock_img_op.remove.return_value = None
        result = runner.invoke(app, ["image", "rm", "abc123"])
        assert result.exit_code == 0
        assert "Removed" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_rm_multiple(self, mock_img_op):
        mock_img_op.remove.return_value = None
        result = runner.invoke(app, ["image", "rm", "abc123", "def456"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.image.ImageOperation")
    def test_rm_no_ids(self, mock_img_op):
        result = runner.invoke(app, ["image", "rm"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.image.ImageOperation")
    def test_rm_not_found(self, mock_img_op):
        mock_img_op.remove.side_effect = MVMError("not found")
        result = runner.invoke(app, ["image", "rm", "badid"])
        assert result.exit_code == 1

    def test_rm_help(self):
        result = runner.invoke(app, ["image", "rm", "--help"])
        assert result.exit_code == 0


class TestImageSetDefault:
    """Tests for 'image set-default' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_set_default_success(self, mock_img_op):
        mock_img_op.set_default.return_value = None
        result = runner.invoke(app, ["image", "set-default", "abc123"])
        assert result.exit_code == 0
        assert "Default image set" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_set_default_not_found(self, mock_img_op):
        mock_img_op.set_default.side_effect = MVMError("not found")
        result = runner.invoke(app, ["image", "set-default", "badid"])
        assert result.exit_code == 1

    def test_set_default_help(self):
        result = runner.invoke(app, ["image", "set-default", "--help"])
        assert result.exit_code == 0


class TestImageImport:
    """Tests for 'image import' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_import_success(self, mock_img_op, tmp_path):
        img = _make_image("Imported OS", "imported-os")
        mock_img_op.import_.return_value = MagicMock(
            result=img,
            full_hash="f" * 64,
        )
        source = tmp_path / "source.qcow2"
        source.write_bytes(b"qcow2")
        result = runner.invoke(
            app,
            [
                "image",
                "import",
                "Imported OS",
                str(source),
            ],
        )
        assert result.exit_code == 0
        assert "imported" in result.output.lower()

    @patch("mvmctl.cli.image.ImageOperation")
    def test_import_file_not_found(self, mock_img_op, tmp_path):
        result = runner.invoke(
            app,
            [
                "image",
                "import",
                "Imported OS",
                "/nonexistent/file.qcow2",
            ],
        )
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    @patch("mvmctl.cli.image.ImageOperation")
    def test_import_with_format(self, mock_img_op, tmp_path):
        img = _make_image("Imported OS", "imported-os")
        mock_img_op.import_.return_value = MagicMock(
            result=img,
            full_hash="f" * 64,
        )
        source = tmp_path / "image.raw"
        source.write_bytes(b"raw")
        result = runner.invoke(
            app,
            [
                "image",
                "import",
                "Imported OS",
                str(source),
                "--format",
                "raw",
            ],
        )
        assert result.exit_code == 0

    def test_import_help(self):
        result = runner.invoke(app, ["image", "import", "--help"])
        assert result.exit_code == 0


class TestImageInspect:
    """Tests for 'image inspect' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_inspect_success(self, mock_img_op):
        mock_img_op.inspect.return_value = _make_image("Ubuntu 24.04")
        result = runner.invoke(app, ["image", "inspect", "abc123"])
        assert result.exit_code == 0
        assert "Ubuntu 24.04" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_inspect_json(self, mock_img_op):
        mock_img_op.inspect.return_value = {
            "os_slug": "ubuntu-24.04",
            "os_name": "Ubuntu 24.04 LTS",
        }
        result = runner.invoke(app, ["image", "inspect", "abc123", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["os_slug"] == "ubuntu-24.04"

    @patch("mvmctl.cli.image.ImageOperation")
    def test_inspect_not_found(self, mock_img_op):
        mock_img_op.inspect.side_effect = MVMError("not found")
        result = runner.invoke(app, ["image", "inspect", "badid"])
        assert result.exit_code == 1

    def test_inspect_help(self):
        result = runner.invoke(app, ["image", "inspect", "--help"])
        assert result.exit_code == 0


class TestImageWarm:
    """Tests for 'image warm' command."""

    @patch("mvmctl.cli.image.ImageOperation")
    def test_warm_success(self, mock_img_op, tmp_path):
        warm_path = tmp_path / "warm" / "ubuntu-24.04.ext4"
        warm_path.parent.mkdir(parents=True)
        warm_path.write_bytes(b"\x00" * 1024)
        mock_img_op.warm.return_value = [warm_path]
        result = runner.invoke(app, ["image", "warm", "ubuntu-24.04"])
        assert result.exit_code == 0
        assert "warmed" in result.output.lower()

    @patch("mvmctl.cli.image.ImageOperation")
    def test_warm_not_found(self, mock_img_op):
        mock_img_op.warm.side_effect = MVMError("not found")
        result = runner.invoke(app, ["image", "warm", "badid"])
        assert result.exit_code == 1

    def test_warm_help(self):
        result = runner.invoke(app, ["image", "warm", "--help"])
        assert result.exit_code == 0


class TestImageHelp:
    """Tests for image command group help."""

    def test_image_help(self):
        result = runner.invoke(app, ["image", "--help"])
        assert result.exit_code == 0
        assert "Image management" in result.output
