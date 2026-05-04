"""Tests for CLI image commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app
from mvmctl.models import ImageItem, ImageSpec
from mvmctl.models.result import (
    BatchResult,
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)

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
        mock_img_op.fetch.return_value = OperationResult(
            status="success",
            code="image.acquired",
            item=img,
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
        assert "fetched successfully" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_fetch_with_force(self, mock_img_op, tmp_path):
        img = _make_image("Ubuntu 24.04")
        mock_img_op.fetch.return_value = OperationResult(
            status="success",
            code="image.acquired",
            item=img,
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
        mock_img_op.fetch.return_value = OperationResult(
            status="error",
            code="image.not_found",
            message="Failed to download image 'nonexistent'",
        )
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
        mock_img_op.remove.return_value = BatchResult(items=[OperationResult(status="success", code="image.removed", message="Image removed")])
        result = runner.invoke(app, ["image", "rm", "abc123"])
        assert result.exit_code == 0
        assert "Removed" in result.output

    @patch("mvmctl.cli.image.ImageOperation")
    def test_rm_multiple(self, mock_img_op):
        mock_img_op.remove.return_value = BatchResult(items=[OperationResult(status="success", code="image.removed", message="Image removed")])
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
        mock_img_op.set_default.return_value = OperationResult(status='success', code='image.default_set', message='Default image set')
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
        mock_img_op.import_.return_value = OperationResult(
            status="success",
            code="image.imported",
            item=img,
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
        mock_img_op.import_.return_value = OperationResult(
            status="success",
            code="image.imported",
            item=img,
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
        mock_img_op.warm.return_value = OperationResult(status="success", code="image.warmed", item=[warm_path])
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


class TestImageLsExtended:
    """Extended tests for 'image ls' — uncovered paths."""

    def test_ls_remote_json(self, mocker):
        """Should render remote images as JSON."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.list_.return_value = [
            ImageSpec(
                id="ubuntu-24.04",
                image_type="ubuntu",
                version="24.04",
                name="Ubuntu 24.04 LTS",
                source="https://example.com/ubuntu.qcow2",
                format="qcow2",
            ),
        ]
        result = runner.invoke(app, ["image", "ls", "--remote", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1
        assert data[0]["id"] == "ubuntu-24.04"

    def test_ls_remote_size_none(self, mocker):
        """Should show '-' for missing remote image size."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.list_.return_value = [
            ImageSpec(
                id="ubuntu-24.04",
                image_type="ubuntu",
                version="24.04",
                name="Ubuntu 24.04 LTS",
                source="https://example.com/ubuntu.qcow2",
                format="qcow2",
                size=None,
            ),
        ]
        result = runner.invoke(app, ["image", "ls", "--remote"])
        assert result.exit_code == 0
        assert "-" in result.output

    def test_ls_error(self, mocker):
        """Should handle API error."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.list_.side_effect = MVMError("Database connection failed")
        result = runner.invoke(app, ["image", "ls"])
        assert result.exit_code == 1
        assert "Database connection failed" in result.output


class TestImageFetchExtended:
    """Extended tests for 'image fetch' — uncovered paths."""

    def test_fetch_needs_interaction(self, mocker):
        """Should prompt when NeedsInteraction is returned."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.fetch.return_value = NeedsInteraction(
            code="sudo.required",
            message="Sudo access required to continue",
            input_type="sudo",
            context={"command": "sudo mvm host init"},
        )
        result = runner.invoke(app, ["image", "fetch", "ubuntu-24.04"])
        assert result.exit_code == 0
        assert "Sudo access required" in result.output

    def test_fetch_error_result(self, mocker):
        """Should fail cleanly on error OperationResult."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.fetch.return_value = OperationResult(
            status="error",
            code="image.download_failed",
            message="Network timeout during download",
        )
        result = runner.invoke(app, ["image", "fetch", "ubuntu-24.04"])
        assert result.exit_code == 1
        assert "Network timeout" in result.output

    def test_fetch_with_set_default(self, mocker):
        """Should confirm default image when --set-default."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Ubuntu 24.04")
        mock.fetch.return_value = OperationResult(
            status="success",
            code="image.acquired",
            item=img,
        )
        result = runner.invoke(
            app, ["image", "fetch", "ubuntu-24.04", "--set-default"]
        )
        assert result.exit_code == 0
        assert "Default image set" in result.output

    def test_fetch_progress_callback(self, mocker):
        """Should handle progress events with and without messages."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Ubuntu 24.04")

        def mock_fetch(fetch_input, on_progress=None):
            if on_progress:
                on_progress(ProgressEvent(phase="download", status="running", message="Downloading..."))
                on_progress(ProgressEvent(phase="download", status="running", message=None))
                on_progress(ProgressEvent(phase="complete", status="complete", message="Done"))
            return OperationResult(status="success", code="image.acquired", item=img)

        mock.fetch.side_effect = mock_fetch
        result = runner.invoke(app, ["image", "fetch", "ubuntu-24.04"])
        assert result.exit_code == 0

    def test_fetch_error_no_message(self, mocker):
        """Should use fallback message when result has no message."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.fetch.return_value = OperationResult(
            status="error",
            code="image.download_failed",
        )
        result = runner.invoke(app, ["image", "fetch", "nonexistent"])
        assert result.exit_code == 1
        assert "Failed to download" in result.output


class TestImageSetDefaultExtended:
    """Extended tests for 'image set-default' — uncovered paths."""

    def test_set_default_error_result(self, mocker):
        """Should fail on error OperationResult (not exception)."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.set_default.return_value = OperationResult(
            status="error",
            code="image.not_found",
            message="Image ID prefix 'badid' not found",
        )
        result = runner.invoke(app, ["image", "set-default", "badid"])
        assert result.exit_code == 1
        assert "not found" in result.output

    def test_set_default_error_no_message(self, mocker):
        """Should use fallback message when result has no message."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.set_default.return_value = OperationResult(
            status="error",
            code="image.default_failed",
        )
        result = runner.invoke(app, ["image", "set-default", "badid"])
        assert result.exit_code == 1
        assert "Failed to set default image" in result.output


class TestImageRemoveExtended:
    """Extended tests for 'image rm' — uncovered paths."""

    def test_rm_mixed_results(self, mocker):
        """Should handle batch with successes and failures."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img1 = _make_image("Ubuntu 24.04", image_id="img-ok-" + "x" * 55)
        mock.remove.return_value = BatchResult(items=[
            OperationResult(status="success", code="image.removed", message="Removed", item=img1),
            OperationResult(status="error", code="image.not_found", message="Image not found", item=None),
        ])
        result = runner.invoke(app, ["image", "rm", "ok123", "bad456"])
        assert result.exit_code == 0
        assert "Removed" in result.output
        assert "Image not found" in result.output

    def test_rm_with_force(self, mocker):
        """Should pass --force to API."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Ubuntu 24.04")
        mock.remove.return_value = BatchResult(items=[
            OperationResult(status="success", code="image.removed", message="Removed", item=img),
        ])
        result = runner.invoke(app, ["image", "rm", "--force", "abc123"])
        assert result.exit_code == 0
        mock.remove.assert_called_once()
        args = mock.remove.call_args[0]
        assert args[1] is True


class TestImageInspectExtended:
    """Extended tests for 'image inspect' — uncovered paths."""

    def test_inspect_tree(self, mocker):
        """Should render tree format output."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Ubuntu 24.04")
        mock.inspect.return_value = img
        result = runner.invoke(app, ["image", "inspect", "abc123", "--tree"])
        assert result.exit_code == 0
        assert "Ubuntu 24.04" in result.output

    def test_inspect_missing_image_marker(self, mocker):
        """Should show '(missing)' for images not on disk."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.inspect.return_value = _make_image("Ubuntu 24.04", is_present=False)
        result = runner.invoke(app, ["image", "inspect", "abc123"])
        assert result.exit_code == 0
        assert "(missing)" in result.output

    def test_inspect_tree_missing(self, mocker):
        """Should show missing marker in tree format."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.inspect.return_value = _make_image("Ubuntu 24.04", is_present=False)
        result = runner.invoke(app, ["image", "inspect", "abc123", "--tree"])
        assert result.exit_code == 0
        assert "(missing)" in result.output

    def test_inspect_dict_json(self, mocker):
        """Should handle dict return with --json."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.inspect.return_value = {"os_slug": "ubuntu-24.04", "os_name": "Ubuntu"}
        result = runner.invoke(app, ["image", "inspect", "abc123", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["os_slug"] == "ubuntu-24.04"

    def test_inspect_with_fs_uuid(self, mocker):
        """Should show fs_uuid when present."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Ubuntu 24.04")
        img.fs_uuid = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
        mock.inspect.return_value = img
        result = runner.invoke(app, ["image", "inspect", "abc123"])
        assert result.exit_code == 0
        assert "a1b2c3d4" in result.output

    def test_inspect_no_compression_data(self, mocker):
        """Should show '-' for missing compression fields."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image(
            "Ubuntu 24.04",
        )
        img.compressed_size = None
        img.compression_ratio = None
        img.compressed_format = None
        img.original_size = 0
        mock.inspect.return_value = img
        result = runner.invoke(app, ["image", "inspect", "abc123"])
        assert result.exit_code == 0
        assert "-" in result.output

    def test_inspect_tree_no_compression(self, mocker):
        """Should show '-' for compression fields in tree format."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Ubuntu 24.04")
        img.compressed_size = None
        img.compression_ratio = None
        img.compressed_format = None
        img.original_size = 0
        mock.inspect.return_value = img
        result = runner.invoke(app, ["image", "inspect", "abc123", "--tree"])
        assert result.exit_code == 0
        assert "-" in result.output


class TestImageImportExtended:
    """Extended tests for 'image import' — uncovered paths."""

    def test_import_auto_detect_fails(self, mocker, tmp_path):
        """Should fail when format cannot be auto-detected."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        source = tmp_path / "image.unknown_ext"
        source.write_bytes(b"data")
        result = runner.invoke(
            app, ["image", "import", "Test OS", str(source)]
        )
        assert result.exit_code == 1
        assert "auto-detect" in result.output.lower()

    def test_import_error_result(self, mocker, tmp_path):
        """Should fail on error OperationResult."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.import_.return_value = OperationResult(
            status="error",
            code="image.import_failed",
            message="Import process failed",
        )
        source = tmp_path / "source.qcow2"
        source.write_bytes(b"data")
        result = runner.invoke(
            app, ["image", "import", "Test OS", str(source)]
        )
        assert result.exit_code == 1
        assert "Import process failed" in result.output

    def test_import_error_no_message(self, mocker, tmp_path):
        """Should use fallback message on error result."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.import_.return_value = OperationResult(
            status="error",
            code="image.import_failed",
        )
        source = tmp_path / "source.qcow2"
        source.write_bytes(b"data")
        result = runner.invoke(
            app, ["image", "import", "Test OS", str(source)]
        )
        assert result.exit_code == 1
        assert "Failed to import image" in result.output

    def test_import_set_default(self, mocker, tmp_path):
        """Should confirm default image when --set-default."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Imported OS", "imported-os")
        mock.import_.return_value = OperationResult(
            status="success",
            code="image.imported",
            item=img,
        )
        source = tmp_path / "source.qcow2"
        source.write_bytes(b"data")
        result = runner.invoke(
            app, ["image", "import", "Test OS", str(source), "--set-default"]
        )
        assert result.exit_code == 0
        assert "Default image set" in result.output

    def test_import_progress_callback(self, mocker, tmp_path):
        """Should handle progress events during import."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Imported OS", "imported-os")

        def mock_import(spec, on_progress=None):
            if on_progress:
                on_progress(ProgressEvent(phase="import", status="running", message="Importing..."))
            return OperationResult(status="success", code="image.imported", item=img)

        mock.import_.side_effect = mock_import
        source = tmp_path / "source.qcow2"
        source.write_bytes(b"data")
        result = runner.invoke(
            app, ["image", "import", "Test OS", str(source)]
        )
        assert result.exit_code == 0

    def test_import_with_disable_detector(self, mocker, tmp_path):
        """Should pass disable-detector to API."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        img = _make_image("Imported OS", "imported-os")
        mock.import_.return_value = OperationResult(
            status="success",
            code="image.imported",
            item=img,
        )
        source = tmp_path / "source.qcow2"
        source.write_bytes(b"data")
        result = runner.invoke(
            app, ["image", "import", "Test OS", str(source), "--disable-detector", "type,label"]
        )
        assert result.exit_code == 0


class TestImageWarmExtended:
    """Extended tests for 'image warm' — uncovered paths."""

    def test_warm_error_result(self, mocker):
        """Should fail on error OperationResult."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        mock.warm.return_value = OperationResult(
            status="error",
            code="image.warm_failed",
            message="Image not found",
        )
        result = runner.invoke(app, ["image", "warm", "badid"])
        assert result.exit_code == 1
        assert "Image not found" in result.output

    def test_warm_progress_callback(self, mocker, tmp_path):
        """Should handle progress events during warm."""
        mock = mocker.patch("mvmctl.cli.image.ImageOperation")
        warm_path = tmp_path / "warm" / "ubuntu-24.04.ext4"

        def mock_warm(vm_input, on_progress=None):
            if on_progress:
                on_progress(ProgressEvent(phase="warm", status="running", message="Warming..."))
            return OperationResult(
                status="success",
                code="image.warmed",
                item=[warm_path],
            )

        mock.warm.side_effect = mock_warm
        warm_path.parent.mkdir(parents=True)
        warm_path.write_bytes(b"\x00" * 1024)

        result = runner.invoke(app, ["image", "warm", "ubuntu-24.04"])
        assert result.exit_code == 0
        assert "warmed" in result.output.lower()
