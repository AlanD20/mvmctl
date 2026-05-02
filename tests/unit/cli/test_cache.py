"""Tests for CLI cache commands."""

from __future__ import annotations

from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.main import app
from mvmctl.models import CleanResult, PruneAllResult

runner = CliRunner()


class TestCacheInit:
    """Tests for 'cache init' command."""

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_init_success(self, mock_cache_op):
        mock_cache_op.init_all.return_value = {
            "images": "/tmp/cache/images",
            "kernels": "/tmp/cache/kernels",
        }
        result = runner.invoke(app, ["cache", "init"])
        assert result.exit_code == 0
        assert "initialized" in result.output.lower()

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_init_exception(self, mock_cache_op):
        mock_cache_op.init_all.side_effect = Exception("disk full")
        result = runner.invoke(app, ["cache", "init"])
        assert result.exit_code == 1
        assert "disk full" in result.output

    def test_init_help(self):
        result = runner.invoke(app, ["cache", "init", "--help"])
        assert result.exit_code == 0


class TestCachePrune:
    """Tests for 'cache prune' command."""

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_vm_success(self, mock_cache_op):
        mock_cache_op.prune_vms.return_value = ["vm1", "vm2"]
        result = runner.invoke(app, ["cache", "prune", "vm"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_vm_empty(self, mock_cache_op):
        mock_cache_op.prune_vms.return_value = []
        result = runner.invoke(app, ["cache", "prune", "vm"])
        assert result.exit_code == 0
        assert "No VMs to prune" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_network_success(self, mock_cache_op):
        mock_cache_op.prune_networks.return_value = ["net1"]
        result = runner.invoke(app, ["cache", "prune", "network"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_image_success(self, mock_cache_op):
        mock_cache_op.prune_images.return_value = ["img1"]
        result = runner.invoke(app, ["cache", "prune", "image"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_kernel_success(self, mock_cache_op):
        mock_cache_op.prune_kernels.return_value = ["krn1"]
        result = runner.invoke(app, ["cache", "prune", "kernel"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_binary_success(self, mock_cache_op):
        mock_cache_op.prune_binaries.return_value = ["bin1"]
        result = runner.invoke(app, ["cache", "prune", "binary"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_misc_success(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = {
            "appliance": True,
            "warm_images": False,
        }
        result = runner.invoke(app, ["cache", "prune", "misc"])
        assert result.exit_code == 0
        assert "appliance" in result.output.lower()

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_misc_empty(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = {
            "appliance": False,
            "warm_images": False,
        }
        result = runner.invoke(app, ["cache", "prune", "misc"])
        assert result.exit_code == 0
        assert "No misc cache" in result.output

    def test_prune_unknown_resource(self):
        result = runner.invoke(app, ["cache", "prune", "foobar"])
        assert result.exit_code == 1
        assert "Unknown resource" in result.output

    def test_prune_no_resource(self):
        result = runner.invoke(app, ["cache", "prune"])
        assert result.exit_code == 1
        assert "No resource specified" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_network_dry_run(self, mock_cache_op):
        mock_cache_op.prune_networks.return_value = ["net1"]
        result = runner.invoke(app, ["cache", "prune", "network", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_all_success(self, mock_cache_op):
        result = runner.invoke(app, ["cache", "prune", "--all", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_prune_all_dry_run(self, mock_cache_op):
        result = runner.invoke(app, ["cache", "prune", "--all", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output


class TestCacheClean:
    """Tests for 'cache clean' command."""

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_clean_dry_run(self, mock_cache_op):
        result = runner.invoke(app, ["cache", "clean", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_clean_success(self, mock_cache_op, tmp_path):
        mock_cache_op.clean.return_value = CleanResult(
            prune_result=PruneAllResult(
                pruned_ids=["vm1"],
                failed_ids=[],
                had_running_vms=False,
            ),
            cache_dir_removed=True,
            cache_dir=str(tmp_path / "cache"),
        )
        result = runner.invoke(app, ["cache", "clean", "--force"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.cli.cache.CacheOperation")
    def test_clean_with_failures(self, mock_cache_op, tmp_path):
        mock_cache_op.clean.return_value = CleanResult(
            prune_result=PruneAllResult(
                pruned_ids=["vm1"],
                failed_ids=["vm2"],
                had_running_vms=True,
            ),
            cache_dir_removed=False,
            cache_dir=str(tmp_path / "cache"),
        )
        result = runner.invoke(app, ["cache", "clean", "--force"])
        assert result.exit_code == 0
        assert "Failed to prune" in result.output


class TestCacheHelp:
    """Tests for cache command group help."""

    def test_cache_help(self):
        result = runner.invoke(app, ["cache", "--help"])
        assert result.exit_code == 0
        assert "Cache management" in result.output

    def test_cache_help_command(self):
        result = runner.invoke(app, ["cache", "help"])
        assert result.exit_code == 0
