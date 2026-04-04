"""Tests for api/init.py."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.api.init import build_default_kernel, init_database, run_host_init_escalated
from mvmctl.exceptions import HostError, KernelError


class TestInitDatabase:
    """Tests for init_database()."""

    def test_creates_db_and_runs_migrations(self):
        """init_database should create MVMDatabase and run migrations."""
        with patch("mvmctl.core.mvm_db.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()
            mock_db_class.return_value = mock_db
            mock_db.migrate.return_value = 2

            init_database()

            mock_db_class.assert_called_once()
            mock_db.migrate.assert_called_once()


class TestRunHostInitEscalated:
    """Tests for run_host_init_escalated()."""

    def test_success_with_mvm_in_path(self):
        """Should return 0 on success when mvm is in PATH."""
        with patch("mvmctl.api.init.shutil.which", return_value="/usr/bin/mvm"):
            with patch("mvmctl.api.init.subprocess.run") as mock_run:
                with patch.dict("os.environ", {}, clear=True):
                    mock_run.return_value = MagicMock(returncode=0)
                    result = run_host_init_escalated()
                    assert result == 0
                    call_args = mock_run.call_args[0][0]
                    assert call_args[0] == "sudo"
                    assert call_args[1] == "-E"
                    assert call_args[2] == "/usr/bin/mvm"
                    assert call_args[3:] == ["host", "init"]

    def test_sets_escalated_env(self):
        """Should set MVM_ESCALATED=1 in subprocess environment."""
        with patch("mvmctl.api.init.shutil.which", return_value="/usr/bin/mvm"):
            with patch("mvmctl.api.init.subprocess.run") as mock_run:
                with patch.dict("os.environ", {}, clear=True):
                    mock_run.return_value = MagicMock(returncode=0)
                    run_host_init_escalated()
                    call_kwargs = mock_run.call_args[1]
                    assert call_kwargs["env"]["MVM_ESCALATED"] == "1"

    def test_failure_raises_host_error(self):
        """Should raise HostError on subprocess exception."""
        with patch("mvmctl.api.init.shutil.which", return_value="/usr/bin/mvm"):
            with patch("mvmctl.api.init.subprocess.run") as mock_run:
                with patch.dict("os.environ", {}, clear=True):
                    mock_run.side_effect = OSError("sudo not found")
                    with pytest.raises(HostError, match="Failed to run host init"):
                        run_host_init_escalated()

    def test_falls_back_to_sys_argv(self):
        """Should fall back to sys.argv[0] when mvm not in PATH."""
        with patch("mvmctl.api.init.shutil.which", return_value=None):
            with patch("mvmctl.api.init.subprocess.run") as mock_run:
                with patch.dict("os.environ", {}, clear=True):
                    mock_run.return_value = MagicMock(returncode=0)
                    import mvmctl.api.init as init_mod

                    orig_argv = init_mod.sys.argv
                    init_mod.sys.argv = ["/path/to/mvm", "init"]
                    try:
                        run_host_init_escalated()
                        call_args = mock_run.call_args[0][0]
                        assert call_args[2] == "/path/to/mvm"
                    finally:
                        init_mod.sys.argv = orig_argv

    def test_returns_nonzero_exit_code(self):
        """Should return the subprocess exit code on failure."""
        with patch("mvmctl.api.init.shutil.which", return_value="/usr/bin/mvm"):
            with patch("mvmctl.api.init.subprocess.run") as mock_run:
                with patch.dict("os.environ", {}, clear=True):
                    mock_run.return_value = MagicMock(returncode=1)
                    result = run_host_init_escalated()
                    assert result == 1


class TestBuildDefaultKernel:
    """Tests for build_default_kernel()."""

    def test_builds_kernel_without_official_spec(self):
        """Should build kernel when no official spec exists."""
        with patch("mvmctl.api.init.get_kernels_dir") as mock_kernels:
            with patch("mvmctl.api.init.load_kernel_spec") as mock_load:
                with patch("mvmctl.api.init.build_kernel_pipeline") as mock_build:
                    with patch("mvmctl.api.init.get_cache_dir") as mock_cache:
                        mock_kernels.return_value = Path("/tmp/kernels")
                        mock_cache.return_value = Path("/tmp/cache")
                        mock_load.side_effect = KernelError("not found")

                        result = build_default_kernel()

                        assert result == Path("/tmp/kernels/vmlinux")
                        mock_build.assert_called_once()
                        call_kwargs = mock_build.call_args[1]
                        assert call_kwargs["kernel_spec"] is None

    def test_builds_kernel_with_official_spec(self):
        """Should pass kernel_spec when official spec exists."""
        with patch("mvmctl.api.init.get_kernels_dir") as mock_kernels:
            with patch("mvmctl.api.init.load_kernel_spec") as mock_load:
                with patch("mvmctl.api.init.build_kernel_pipeline") as mock_build:
                    with patch("mvmctl.api.init.get_cache_dir") as mock_cache:
                        mock_kernels.return_value = Path("/tmp/kernels")
                        mock_cache.return_value = Path("/tmp/cache")
                        mock_spec = MagicMock()
                        mock_spec.kernel_type = "official"
                        mock_load.return_value = mock_spec

                        result = build_default_kernel()

                        assert result == Path("/tmp/kernels/vmlinux")
                        call_kwargs = mock_build.call_args[1]
                        assert call_kwargs["kernel_spec"] == mock_spec

    def test_creates_kernels_dir(self):
        """Should create kernels directory if it doesn't exist."""
        with patch("mvmctl.api.init.get_kernels_dir") as mock_kernels:
            with patch("mvmctl.api.init.load_kernel_spec") as mock_load:
                with patch("mvmctl.api.init.build_kernel_pipeline"):
                    with patch("mvmctl.api.init.get_cache_dir") as mock_cache:
                        mock_kernels.return_value = Path("/tmp/kernels")
                        mock_cache.return_value = Path("/tmp/cache")
                        mock_load.side_effect = KernelError("not found")

                        build_default_kernel()

                        mock_kernels.assert_called_once()
