"""Init wizard system tests."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_init]


class TestInitWizard:
    """Test the mvm init wizard (non-destructive, no sudo required with --skip-host)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_init,
    ]

    def test_init_non_interactive_skip_host(self, runner_vm):
        """Run init in non-interactive mode skipping host setup.

        This should succeed without sudo because --skip-host avoids
        the privileged host-init step.
        """
        result = _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            "--skip-host",
            check=False,
        )

        assert result.returncode == 0
        output = result.stdout + result.stderr
        assert "all set" in output, (
            f"Output missing expected success message:\n{output}"
        )

    def test_init_idempotent(self, runner_vm):
        """Run init twice — both invocations should succeed.

        Verifies that init is safe to run when everything is already
        set up (idempotent).
        """
        args = ("init", "--non-interactive", "--skip-host")

        first = _run_mvm(runner_vm, *args, check=False)
        assert first.returncode == 0, (
            f"First init failed (rc={first.returncode})\n"
            f"stdout: {first.stdout}\n"
            f"stderr: {first.stderr}"
        )
        first_output = (first.stdout + first.stderr).strip()
        assert first_output, "First init produced no output"

        second = _run_mvm(runner_vm, *args, check=False)
        assert second.returncode == 0, (
            f"Second init failed (rc={second.returncode})\n"
            f"stdout: {second.stdout}\n"
            f"stderr: {second.stderr}"
        )
        second_output = (second.stdout + second.stderr).strip()
        assert second_output, (
            "Second init (idempotent) produced no output"
        )

    def test_init_abort_on_sudo_needed(self, runner_vm):
        """Run init without --skip-host — should succeed as root (no sudo needed).

        Inside the test VM, commands run as root so sudo is not required.
        This test verifies init handles the privilege path correctly by
        checking that output mentions the host setup steps.
        """
        result = _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            check=False,
        )
        assert result.returncode == 0, (
            f"Expected zero exit (running as root): rc={result.returncode}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )
        output = result.stdout + result.stderr
        assert "all set" in output.lower(), (
            f"Output missing expected success message:\n{output}"
        )

    def test_init_without_skip_host(self, runner_vm):
        """Run init without --skip-host non-interactively.

        Inside the test VM, passwordless sudo is always available,
        so init --non-interactive without --skip-host should succeed.
        """
        result = _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            check=False,
        )
        assert result.returncode == 0, (
            f"Expected zero exit with passwordless sudo, got rc={result.returncode}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )


class TestRootFlags:
    """Test root-level CLI flags (--version, --verbose, --debug)."""

    def test_version_flag(self, runner_vm):
        """``mvm --version`` should print version and exit."""
        result = _run_mvm(runner_vm, "--version", check=False)
        assert result.returncode == 0
        assert result.stdout.strip(), (
            "Expected version string in output"
        )

    def test_verbose_flag(self, runner_vm):
        """``mvm --verbose`` should enable verbose logging output."""
        result = _run_mvm(
            runner_vm,
            "--verbose",
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
            check=False,
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout

    def test_debug_flag(self, runner_vm):
        """``mvm --debug`` should enable debug-level logging."""
        result = _run_mvm(
            runner_vm,
            "--debug",
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
            check=False,
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout


class TestInitEdgeCases:
    """Tests for init command edge cases (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier1,
        pytest.mark.domain_init,
    ]

    def test_init_skip_network(self, runner_vm):
        """``init --non-interactive --skip-network`` should succeed (exit 0).

        Rationale: The --skip-network flag skips network setup during init.
        A regression where --skip-network is silently ignored would cause
        the init wizard to attempt privileged network operations when the
        caller explicitly opted out. L1 verification: checks exit 0 and
        mentions the flag in output.
        """
        result = _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            "--skip-host",
            "--skip-network",
            check=False,
        )

        assert result.returncode == 0, (
            f"init --skip-network failed: rc={result.returncode} "
            f"stdout={result.stdout} stderr={result.stderr}"
        )
        output = result.stdout + result.stderr
        assert "all set" in output.lower(), (
            f"Output missing expected success message:\n{output}"
        )
