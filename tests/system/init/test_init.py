"""Init wizard system tests."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_init]


class TestInitWizard:
    """Test the mvm init wizard (non-destructive, no sudo required with --skip-host)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_init,
    ]

    def test_init_non_interactive_skip_host(self, mvm_binary):
        """Run init in non-interactive mode skipping host setup.

        This should succeed without sudo because --skip-host avoids
        the privileged host-init step.
        """
        # Rationale: Only needs CLI invocation with --skip-host flag.
        # No expensive resources needed — testing the init wizard's
        # non-interactive path with host setup skipped.
        result = _run_mvm(
            mvm_binary,
            "init",
            "--non-interactive",
            "--skip-host",
            check=False,
        )

        assert result.returncode == 0
        output = result.stdout + result.stderr
        assert any(
            phrase in output for phrase in ("all set", "ready", "Setup Wizard")
        ), f"Output missing expected phrases:\n{output}"

    def test_init_idempotent(self, mvm_binary):
        """Run init twice — both invocations should succeed.

        Verifies that init is safe to run when everything is already
        set up (idempotent).
        """
        # Rationale: Only needs CLI invocation. Verifies idempotency
        # of the init wizard — no expensive resources needed.
        args = ("init", "--non-interactive", "--skip-host")

        first = _run_mvm(mvm_binary, *args, check=False)
        assert first.returncode == 0, (
            f"First init failed (rc={first.returncode})\n"
            f"stdout: {first.stdout}\n"
            f"stderr: {first.stderr}"
        )

        second = _run_mvm(mvm_binary, *args, check=False)
        assert second.returncode == 0, (
            f"Second init failed (rc={second.returncode})\n"
            f"stdout: {second.stdout}\n"
            f"stderr: {second.stderr}"
        )

    def test_init_abort_on_sudo_needed(self, mvm_binary):
        """Run init without --skip-host non-interactively.

        Host setup requires sudo which cannot be obtained in
        non-interactive mode.  The CLI should exit cleanly
        with a useful error message rather than hanging.
        """
        # Rationale: Only needs CLI invocation with intentional missing
        # --skip-host to verify graceful error handling. No resources needed.
        result = _run_mvm(
            mvm_binary,
            "init",
            "--non-interactive",
            check=False,
        )

        # Must exit non-zero — host init cannot proceed without sudo.
        assert result.returncode != 0, (
            f"Expected non-zero exit, got rc={result.returncode}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )

        output = result.stdout + result.stderr
        lower = output.lower()
        assert any(
            keyword in lower
            for keyword in ("sudo", "root", "host init", "privilege")
        ), f"Missing error guidance in output:\n{output}"


class TestRootFlags:
    """Test root-level CLI flags (--version, --verbose, --debug)."""

    def test_version_flag(self, mvm_binary):
        """``mvm --version`` should print version and exit."""
        # Rationale: Only needs CLI invocation. No resources needed —
        # testing that the --version flag returns a non-empty string.
        result = _run_mvm(mvm_binary, "--version", check=False)
        assert result.returncode == 0
        assert result.stdout.strip(), "Expected version string in output"

    def test_verbose_flag(self, mvm_binary):
        """``mvm --verbose`` should enable verbose logging output."""
        # Rationale: Only needs CLI invocation with --verbose flag.
        # No resources needed — testing that verbose mode doesn't break config get.
        result = _run_mvm(
            mvm_binary,
            "--verbose",
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
            check=False,
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout

    def test_debug_flag(self, mvm_binary):
        """``mvm --debug`` should enable debug-level logging."""
        # Rationale: Only needs CLI invocation with --debug flag.
        # No resources needed — testing that debug mode doesn't break config get.
        result = _run_mvm(
            mvm_binary,
            "--debug",
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
            check=False,
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout
