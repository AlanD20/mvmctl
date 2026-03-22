"""Tests for CLI kernel commands."""

import json
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from fcm.cli.kernel import app

runner = CliRunner()


def test_list_kernels_with_files(tmp_path: Path):
    """Test 'kernel list' shows vmlinux files in the directory."""
    kernel_file = tmp_path / "vmlinux"
    kernel_file.write_bytes(b"\x00" * 1024)

    result = runner.invoke(app, ["list", "--kernels-dir", str(tmp_path)])
    assert result.exit_code == 0
    assert "vmlinux" in result.output


def test_list_kernels_json(tmp_path: Path):
    """Test 'kernel list --json' outputs valid JSON."""
    kernel_file = tmp_path / "vmlinux"
    kernel_file.write_bytes(b"\x00" * 2048)

    result = runner.invoke(app, ["list", "--kernels-dir", str(tmp_path), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    assert len(data) == 1
    assert data[0]["name"] == "vmlinux"


def test_list_kernels_dir_not_found(tmp_path: Path):
    """Test 'kernel list' exits 1 when directory doesn't exist."""
    result = runner.invoke(app, ["list", "--kernels-dir", str(tmp_path / "nope")])
    assert result.exit_code == 1


def test_clean_nothing(tmp_path: Path):
    """Test 'kernel clean' prints nothing to clean when build dir absent."""
    result = runner.invoke(app, ["clean", "--build-dir", str(tmp_path / "nonexistent"), "--force"])
    assert result.exit_code == 0
    assert "Nothing to clean" in result.output


def test_clean_force(tmp_path: Path):
    """Test 'kernel clean --force' removes the build directory."""
    build_dir = tmp_path / "kernel-build"
    build_dir.mkdir()
    (build_dir / "Makefile").write_text("all:")

    result = runner.invoke(app, ["clean", "--build-dir", str(build_dir), "--force"])
    assert result.exit_code == 0
    assert not build_dir.exists()


def test_build_success(tmp_path: Path):
    out = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    with patch("fcm.cli.kernel.build_kernel_pipeline", return_value=True):
        result = runner.invoke(
            app,
            [
                "build",
                "--version",
                "6.1.102",
                "--out",
                str(out),
                "--build-dir",
                str(build_dir),
            ],
        )
    assert result.exit_code == 0


def test_build_failure(tmp_path: Path):
    out = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    with patch("fcm.cli.kernel.build_kernel_pipeline", return_value=False):
        result = runner.invoke(
            app,
            [
                "build",
                "--version",
                "6.1.102",
                "--out",
                str(out),
                "--build-dir",
                str(build_dir),
            ],
        )
    assert result.exit_code == 1


def test_clean_specific_version(tmp_path: Path):
    build_dir = tmp_path / "kernel-build"
    build_dir.mkdir()
    version_dir = build_dir / "linux-6.1.102"
    version_dir.mkdir()
    (version_dir / "Makefile").write_text("all:")

    result = runner.invoke(
        app,
        ["clean", "--version", "6.1.102", "--build-dir", str(build_dir), "--force"],
    )
    assert result.exit_code == 0
    assert not version_dir.exists()
    assert build_dir.exists()


def test_clean_specific_version_not_found(tmp_path: Path):
    build_dir = tmp_path / "kernel-build"
    build_dir.mkdir()

    result = runner.invoke(
        app,
        ["clean", "--version", "6.1.999", "--build-dir", str(build_dir), "--force"],
    )
    assert result.exit_code == 1
