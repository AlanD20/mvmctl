"""Tests for CLI cache commands."""

from __future__ import annotations

from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.main import app
from mvmctl.models import CleanResult, PruneAllResult
from mvmctl.models.result import OperationResult

runner = CliRunner()


class TestCacheInit:
    """Tests for 'cache init' command."""

    @patch("mvmctl.api.CacheOperation")
    def test_init_success(self, mock_cache_op):
        mock_cache_op.init_all.return_value = OperationResult(
            status="success",
            code="cache.initialized",
            message="Cache initialized",
            item={
                "images": "/tmp/cache/images",
                "kernels": "/tmp/cache/kernels",
            },
        )
        result = runner.invoke(app, ["cache", "init"])
        assert result.exit_code == 0
        assert "initialized" in result.output.lower()

    @patch("mvmctl.api.CacheOperation")
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

    @patch("mvmctl.api.CacheOperation")
    def test_prune_vm_success(self, mock_cache_op):
        mock_cache_op.prune_vms.return_value = OperationResult(
            status="success", code="cache.pruned", item=["vm1", "vm2"]
        )
        result = runner.invoke(app, ["cache", "prune", "vm"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_vm_empty(self, mock_cache_op):
        mock_cache_op.prune_vms.return_value = OperationResult(
            status="success", code="cache.pruned", item=[]
        )
        result = runner.invoke(app, ["cache", "prune", "vm"])
        assert result.exit_code == 0
        assert "No VMs to prune" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_network_success(self, mock_cache_op):
        mock_cache_op.prune_networks.return_value = OperationResult(
            status="success", code="cache.pruned", item=["net1"]
        )
        result = runner.invoke(app, ["cache", "prune", "network"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_image_success(self, mock_cache_op):
        mock_cache_op.prune_images.return_value = OperationResult(
            status="success", code="cache.pruned", item=["img1"]
        )
        result = runner.invoke(app, ["cache", "prune", "image"])
        assert result.exit_code == 0

    @patch("mvmctl.api.CacheOperation")
    def test_prune_kernel_success(self, mock_cache_op):
        mock_cache_op.prune_kernels.return_value = OperationResult(
            status="success", code="cache.pruned", item=["krn1"]
        )
        result = runner.invoke(app, ["cache", "prune", "kernel"])
        assert result.exit_code == 0

    @patch("mvmctl.api.CacheOperation")
    def test_prune_binary_success(self, mock_cache_op):
        mock_cache_op.prune_binaries.return_value = OperationResult(
            status="success", code="cache.pruned", item=["bin1"]
        )
        result = runner.invoke(app, ["cache", "prune", "binary"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_misc_success(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item={
                "appliance": True,
                "warm_images": False,
            },
        )
        result = runner.invoke(app, ["cache", "prune", "misc"])
        assert result.exit_code == 0
        assert "appliance" in result.output.lower()

    @patch("mvmctl.api.CacheOperation")
    def test_prune_misc_empty(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item={
                "appliance": False,
                "warm_images": False,
            },
        )
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

    @patch("mvmctl.api.CacheOperation")
    def test_prune_network_dry_run(self, mock_cache_op):
        mock_cache_op.prune_networks.return_value = OperationResult(
            status="success", code="cache.pruned", item=["net1"]
        )
        result = runner.invoke(app, ["cache", "prune", "network", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_success(self, mock_cache_op):
        mock_cache_op.prune_all.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item=PruneAllResult(
                pruned_ids=[], failed_ids=[], had_running_vms=False
            ),
        )
        result = runner.invoke(app, ["cache", "prune", "--all", "--force"])
        assert result.exit_code == 0

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_dry_run(self, mock_cache_op):
        mock_cache_op.prune_all.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item=PruneAllResult(
                pruned_ids=[], failed_ids=[], had_running_vms=False
            ),
        )
        result = runner.invoke(app, ["cache", "prune", "--all", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output


class TestCacheClean:
    """Tests for 'cache clean' command."""

    @patch("mvmctl.api.CacheOperation")
    def test_clean_dry_run(self, mock_cache_op):
        mock_cache_op.clean.return_value = OperationResult(
            status="success",
            code="cache.cleaned",
            item=CleanResult(
                prune_result=PruneAllResult(
                    pruned_ids=[], failed_ids=[], had_running_vms=False
                ),
                cache_dir_removed=False,
                cache_dir="",
            ),
        )
        result = runner.invoke(app, ["cache", "clean", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_clean_success(self, mock_cache_op, tmp_path):
        mock_cache_op.clean.return_value = OperationResult(
            status="success",
            code="cache.cleaned",
            item=CleanResult(
                prune_result=PruneAllResult(
                    pruned_ids=["vm1"],
                    failed_ids=[],
                    had_running_vms=False,
                ),
                cache_dir_removed=True,
                cache_dir=str(tmp_path / "cache"),
            ),
        )
        result = runner.invoke(app, ["cache", "clean", "--force"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_clean_with_failures(self, mock_cache_op, tmp_path):
        mock_cache_op.clean.return_value = OperationResult(
            status="success",
            code="cache.cleaned",
            item=CleanResult(
                prune_result=PruneAllResult(
                    pruned_ids=["vm1"],
                    failed_ids=["vm2"],
                    had_running_vms=True,
                ),
                cache_dir_removed=False,
                cache_dir=str(tmp_path / "cache"),
            ),
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


class TestCachePruneResourceEdgeCases:
    """Tests for 'cache prune' edge cases per resource type."""

    @patch("mvmctl.api.CacheOperation")
    def test_prune_vm_error(self, mock_cache_op):
        mock_cache_op.prune_vms.return_value = OperationResult(
            status="error", code="cache.error", message="Prune VMs failed"
        )
        result = runner.invoke(app, ["cache", "prune", "vm"])
        assert result.exit_code == 1
        assert "Prune VMs failed" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_network_empty(self, mock_cache_op):
        mock_cache_op.prune_networks.return_value = OperationResult(
            status="success", code="cache.pruned", item=[]
        )
        result = runner.invoke(app, ["cache", "prune", "network"])
        assert result.exit_code == 0
        assert "No networks" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_network_error(self, mock_cache_op):
        mock_cache_op.prune_networks.return_value = OperationResult(
            status="error", code="cache.error", message="Network error"
        )
        result = runner.invoke(app, ["cache", "prune", "network"])
        assert result.exit_code == 1
        assert "Network error" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_image_empty(self, mock_cache_op):
        mock_cache_op.prune_images.return_value = OperationResult(
            status="success", code="cache.pruned", item=[]
        )
        result = runner.invoke(app, ["cache", "prune", "image"])
        assert result.exit_code == 0
        assert "No images" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_image_error(self, mock_cache_op):
        mock_cache_op.prune_images.return_value = OperationResult(
            status="error", code="cache.error", message="Image error"
        )
        result = runner.invoke(app, ["cache", "prune", "image"])
        assert result.exit_code == 1
        assert "Image error" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_image_dry_run(self, mock_cache_op):
        mock_cache_op.prune_images.return_value = OperationResult(
            status="success", code="cache.pruned", item=["img1"]
        )
        result = runner.invoke(app, ["cache", "prune", "image", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_kernel_empty(self, mock_cache_op):
        mock_cache_op.prune_kernels.return_value = OperationResult(
            status="success", code="cache.pruned", item=[]
        )
        result = runner.invoke(app, ["cache", "prune", "kernel"])
        assert result.exit_code == 0
        assert "No kernels" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_kernel_error(self, mock_cache_op):
        mock_cache_op.prune_kernels.return_value = OperationResult(
            status="error", code="cache.error", message="Kernel error"
        )
        result = runner.invoke(app, ["cache", "prune", "kernel"])
        assert result.exit_code == 1
        assert "Kernel error" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_kernel_dry_run(self, mock_cache_op):
        mock_cache_op.prune_kernels.return_value = OperationResult(
            status="success", code="cache.pruned", item=["krn1"]
        )
        result = runner.invoke(app, ["cache", "prune", "kernel", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_binary_empty(self, mock_cache_op):
        mock_cache_op.prune_binaries.return_value = OperationResult(
            status="success", code="cache.pruned", item=[]
        )
        result = runner.invoke(app, ["cache", "prune", "binary"])
        assert result.exit_code == 0
        assert "No binaries" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_binary_error(self, mock_cache_op):
        mock_cache_op.prune_binaries.return_value = OperationResult(
            status="error", code="cache.error", message="Binary error"
        )
        result = runner.invoke(app, ["cache", "prune", "binary"])
        assert result.exit_code == 1
        assert "Binary error" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_binary_dry_run(self, mock_cache_op):
        mock_cache_op.prune_binaries.return_value = OperationResult(
            status="success", code="cache.pruned", item=["bin1"]
        )
        result = runner.invoke(app, ["cache", "prune", "binary", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output


class TestCachePruneMiscEdgeCases:
    """Tests for 'cache prune misc' edge cases."""

    @patch("mvmctl.api.CacheOperation")
    def test_prune_misc_warm_images_only(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item={"appliance": False, "warm_images": True},
        )
        result = runner.invoke(app, ["cache", "prune", "misc"])
        assert result.exit_code == 0
        assert "warm images" in result.output.lower()

    @patch("mvmctl.api.CacheOperation")
    def test_prune_misc_dry_run(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item={"appliance": True, "warm_images": True},
        )
        result = runner.invoke(app, ["cache", "prune", "misc", "--dry-run"])
        assert result.exit_code == 0
        assert "DRY RUN" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_misc_error(self, mock_cache_op):
        mock_cache_op.prune_misc.return_value = OperationResult(
            status="error", code="cache.error", message="Misc error"
        )
        result = runner.invoke(app, ["cache", "prune", "misc"])
        assert result.exit_code == 1
        assert "Misc error" in result.output


class TestCachePruneAllEdgeCases:
    """Tests for 'cache prune --all' edge cases."""

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_with_pruned_ids(self, mock_cache_op):
        mock_cache_op.prune_all.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item=PruneAllResult(
                pruned_ids=["vm1", "vm2"], failed_ids=[], had_running_vms=False
            ),
        )
        result = runner.invoke(app, ["cache", "prune", "--all", "--force"])
        assert result.exit_code == 0
        assert "Pruned" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_with_failed_ids(self, mock_cache_op):
        mock_cache_op.prune_all.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item=PruneAllResult(
                pruned_ids=["vm1"], failed_ids=["vm2"], had_running_vms=False
            ),
        )
        result = runner.invoke(app, ["cache", "prune", "--all", "--force"])
        assert result.exit_code == 0
        assert "Failed to prune" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_with_running_vms(self, mock_cache_op):
        mock_cache_op.prune_all.return_value = OperationResult(
            status="success",
            code="cache.pruned",
            item=PruneAllResult(
                pruned_ids=[], failed_ids=[], had_running_vms=True
            ),
        )
        result = runner.invoke(app, ["cache", "prune", "--all", "--force"])
        assert result.exit_code == 0
        assert "running or starting" in result.output.lower()

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_error(self, mock_cache_op):
        mock_cache_op.prune_all.return_value = OperationResult(
            status="error", code="cache.error", message="Prune all failed"
        )
        result = runner.invoke(app, ["cache", "prune", "--all", "--force"])
        assert result.exit_code == 1
        assert "Prune all failed" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_prune_all_aborted(self, mock_cache_op):
        result = runner.invoke(app, ["cache", "prune", "--all"], input="n\n")
        assert result.exit_code == 0
        assert "Aborted" in result.output


class TestCacheCleanEdgeCases:
    """Tests for 'cache clean' edge cases."""

    @patch("mvmctl.api.CacheOperation")
    def test_clean_error(self, mock_cache_op):
        mock_cache_op.clean.return_value = OperationResult(
            status="error", code="cache.error", message="Clean failed"
        )
        result = runner.invoke(app, ["cache", "clean", "--force"])
        assert result.exit_code == 1
        assert "Clean failed" in result.output

    @patch("mvmctl.api.CacheOperation")
    def test_clean_cache_dir_not_removed(self, mock_cache_op):
        mock_cache_op.clean.return_value = OperationResult(
            status="success",
            code="cache.cleaned",
            item=CleanResult(
                prune_result=PruneAllResult(
                    pruned_ids=["vm1"],
                    failed_ids=[],
                    had_running_vms=False,
                ),
                cache_dir_removed=False,
                cache_dir="/tmp/cache",
            ),
        )
        result = runner.invoke(app, ["cache", "clean", "--force"])
        assert result.exit_code == 0
        assert "Cache directory was already empty" in result.output
