"""Tests for rootfs resize utilities."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.resize import resize_rootfs


class TestResizeRootfs:
    """Tests for resize_rootfs function."""

    def test_resize_success(self, tmp_path: Path) -> None:
        """Test successful resize operation."""
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image content")

        # Mock qemu-img info output
        mock_info_result = MagicMock()
        mock_info_result.stdout = '{"virtual-size": 1073741824}'  # 1GB
        mock_info_result.returncode = 0

        # Mock qemu-img resize output
        mock_resize_result = MagicMock()
        mock_resize_result.returncode = 0

        with patch(
            "mvmctl.utils.resize.subprocess.run",
            side_effect=[mock_info_result, mock_resize_result],
        ) as mock_run:
            resize_rootfs(image_path, 2 * 1024**3)  # Target 2GB

            # Check that both commands were called
            assert mock_run.call_count == 2

            # First call should be qemu-img info
            first_call = mock_run.call_args_list[0]
            assert first_call[0][0] == ["qemu-img", "info", "--output=json", str(image_path)]

            # Second call should be qemu-img resize
            second_call = mock_run.call_args_list[1]
            assert second_call[0][0][0] == "qemu-img"
            assert second_call[0][0][1] == "resize"
            assert second_call[0][0][2] == str(image_path)

    def test_resize_no_change_if_large_enough(self, tmp_path: Path) -> None:
        """Test that resize is skipped if image is already large enough."""
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image content")

        # Mock qemu-img info output - image is already 2GB
        mock_info_result = MagicMock()
        mock_info_result.stdout = '{"virtual-size": 2147483648}'  # 2GB
        mock_info_result.returncode = 0

        with patch(
            "mvmctl.utils.resize.subprocess.run",
            return_value=mock_info_result,
        ) as mock_run:
            resize_rootfs(image_path, 1024**3)  # Target 1GB

            # Only one call should be made (info, not resize)
            assert mock_run.call_count == 1
            mock_run.assert_called_once_with(
                ["qemu-img", "info", "--output=json", str(image_path)],
                capture_output=True,
                text=True,
                check=True,
            )

    def test_resize_image_not_found(self, tmp_path: Path) -> None:
        """Test that MVMError is raised if image doesn't exist."""
        nonexistent_path = tmp_path / "nonexistent.img"

        with pytest.raises(MVMError, match="Image not found"):
            resize_rootfs(nonexistent_path, 1024**3)

    def test_resize_qemu_img_info_fails(self, tmp_path: Path) -> None:
        """Test error handling when qemu-img info fails."""
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image content")

        with patch(
            "mvmctl.utils.resize.subprocess.run",
            side_effect=subprocess.CalledProcessError(1, "qemu-img", stderr="error"),
        ):
            with pytest.raises(MVMError, match="Failed to get image info"):
                resize_rootfs(image_path, 2 * 1024**3)

    def test_resize_qemu_img_resize_fails(self, tmp_path: Path) -> None:
        """Test error handling when qemu-img resize fails."""
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image content")

        # Mock qemu-img info output
        mock_info_result = MagicMock()
        mock_info_result.stdout = '{"virtual-size": 1073741824}'  # 1GB
        mock_info_result.returncode = 0

        with patch(
            "mvmctl.utils.resize.subprocess.run",
            side_effect=[
                mock_info_result,
                subprocess.CalledProcessError(1, "qemu-img", stderr="resize failed"),
            ],
        ):
            with pytest.raises(MVMError, match="Failed to resize image"):
                resize_rootfs(image_path, 2 * 1024**3)

    def test_resize_json_decode_error(self, tmp_path: Path) -> None:
        """Test error handling when qemu-img info returns invalid JSON."""
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image content")

        # Mock qemu-img info output with invalid JSON
        mock_info_result = MagicMock()
        mock_info_result.stdout = "invalid json"
        mock_info_result.returncode = 0

        with patch(
            "mvmctl.utils.resize.subprocess.run",
            return_value=mock_info_result,
        ):
            with pytest.raises(MVMError, match="Failed to get image info"):
                resize_rootfs(image_path, 2 * 1024**3)


import subprocess  # noqa: E402
