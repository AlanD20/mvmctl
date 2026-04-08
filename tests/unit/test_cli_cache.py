from unittest.mock import patch

from typer.testing import CliRunner

from mvmctl.cli.cache import cache_app as app

runner = CliRunner()


def test_help_cmd():
    result = runner.invoke(app, ["help"])
    assert result.exit_code == 0


def test_cache_prune_image_error():
    with patch("mvmctl.cli.cache.cache_api.prune_images", side_effect=Exception("disk full")):
        result = runner.invoke(app, ["prune", "image"])
        assert result.exit_code == 1
        assert "disk full" in result.output


def test_cache_prune_kernel_error():
    with patch("mvmctl.cli.cache.cache_api.prune_kernels", side_effect=Exception("io error")):
        result = runner.invoke(app, ["prune", "kernel"])
        assert result.exit_code == 1
        assert "io error" in result.output


def test_cache_prune_vm_error():
    with patch("mvmctl.cli.cache.cache_api.prune_vms", side_effect=Exception("vm locked")):
        result = runner.invoke(app, ["prune", "vm"])
        assert result.exit_code == 1
        assert "vm locked" in result.output


def test_cache_prune_network_error():
    with patch("mvmctl.cli.cache.cache_api.prune_networks", side_effect=Exception("net error")):
        result = runner.invoke(app, ["prune", "network"])
        assert result.exit_code == 1
        assert "net error" in result.output


def test_cache_prune_unknown_resource():
    result = runner.invoke(app, ["prune", "foobar"])
    assert result.exit_code == 1
    assert "Unknown resource" in result.output


def test_cache_prune_no_resource():
    result = runner.invoke(app, ["prune"])
    assert result.exit_code == 1
    assert "No resource specified" in result.output


def test_cache_prune_image_success_empty():
    with patch("mvmctl.cli.cache.cache_api.prune_images", return_value=[]):
        result = runner.invoke(app, ["prune", "image"])
        assert result.exit_code == 0
        assert "No images to prune" in result.output


def test_cache_prune_kernel_success_empty():
    with patch("mvmctl.cli.cache.cache_api.prune_kernels", return_value=[]):
        result = runner.invoke(app, ["prune", "kernel"])
        assert result.exit_code == 0
        assert "No kernels to prune" in result.output


def test_cache_init_success_with_null_path():
    with patch(
        "mvmctl.cli.cache.cache_api.init_all",
        return_value={"images": None, "kernels": "/cache/kernels"},
    ):
        result = runner.invoke(app, ["init"])
        assert result.exit_code == 0
        assert "initialized" in result.output


def test_cache_init_exception():
    with patch("mvmctl.cli.cache.cache_api.init_all", side_effect=Exception("disk full")):
        result = runner.invoke(app, ["init"])
        assert result.exit_code == 1
        assert "disk full" in result.output
