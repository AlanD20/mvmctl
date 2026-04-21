"""Tests for resize utilities."""

import json
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.resize import resize_rootfs


class TestResizeRootfs:
    def test_resize_image_not_found(self, tmp_path: Path):
        nonexistent = tmp_path / "nonexistent.img"
        with pytest.raises(MVMError, match="Image not found"):
            resize_rootfs(nonexistent, 1024 * 1024 * 1024)

    def test_resize_already_large_enough(self, tmp_path: Path):
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image data")
        mock_result = MagicMock()
        mock_result.stdout = json.dumps({"virtual-size": 2 * 1024 * 1024 * 1024})
        mock_result.stderr = ""
        with patch("subprocess.run", return_value=mock_result):
            resize_rootfs(image_path, 1024 * 1024 * 1024)

    def test_resize_success(self, tmp_path: Path):
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image data")
        info_result = MagicMock()
        info_result.stdout = json.dumps({"virtual-size": 512 * 1024 * 1024})
        info_result.stderr = ""
        resize_result = MagicMock()
        resize_result.stdout = ""
        resize_result.stderr = ""

        def mock_run(cmd, **kwargs):
            if cmd[1] == "info":
                return info_result
            elif cmd[1] == "resize":
                return resize_result
            return MagicMock()

        with patch("subprocess.run", side_effect=mock_run) as mock_subprocess:
            resize_rootfs(image_path, 1024 * 1024 * 1024)
            assert mock_subprocess.call_count == 2
            resize_call = mock_subprocess.call_args_list[1]
            assert resize_call[0][0][1] == "resize"
            assert str(image_path) in resize_call[0][0]

    def test_resize_qemu_img_info_fails(self, tmp_path: Path):
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image data")
        with patch(
            "subprocess.run",
            side_effect=subprocess.CalledProcessError(1, "qemu-img", stderr="error"),
        ):
            with pytest.raises(MVMError, match="Failed to get image info"):
                resize_rootfs(image_path, 1024 * 1024 * 1024)

    def test_resize_qemu_img_resize_fails(self, tmp_path: Path):
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image data")
        info_result = MagicMock()
        info_result.stdout = json.dumps({"virtual-size": 512 * 1024 * 1024})
        info_result.stderr = ""

        def mock_run(cmd, **kwargs):
            if cmd[1] == "info":
                return info_result
            elif cmd[1] == "resize":
                raise subprocess.CalledProcessError(1, "qemu-img", stderr="resize failed")
            return MagicMock()

        with patch("subprocess.run", side_effect=mock_run):
            with pytest.raises(MVMError, match="Failed to resize image"):
                resize_rootfs(image_path, 1024 * 1024 * 1024)

    def test_resize_invalid_json_response(self, tmp_path: Path):
        image_path = tmp_path / "test.img"
        image_path.write_bytes(b"dummy image data")
        mock_result = MagicMock()
        mock_result.stdout = "invalid json"
        mock_result.stderr = ""
        with patch("subprocess.run", return_value=mock_result):
            with pytest.raises(MVMError, match="Failed to get image info"):
                resize_rootfs(image_path, 1024 * 1024 * 1024)
