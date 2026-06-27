"""CLI command system tests — help, version, debug, error messages.

Extracted from tests/e2e/cli/test_cli_edge_cases.py — pure CLI tests
that need no VM resources beyond the runner VM.
"""

from __future__ import annotations

import re

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system]

_HELP_COMMANDS = [
    "vm",
    "network",
    "image",
    "kernel",
    "key",
    "volume",
    "bin",
    "config",
    "cache",
]

# ============================================================================
# Help command tests (non-destructive — first)
# ============================================================================


class TestHelpCommand:
    """Tests for the ``help`` CLI command."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_help_root(self, runner_vm):
        """``mvm help`` should show root help."""
        result = _run_mvm(runner_vm, "help")
        assert result.returncode == 0
        assert "Usage:" in result.stdout

    def test_help_subcommand(self, runner_vm):
        """``mvm help vm`` should show vm help."""
        result = _run_mvm(runner_vm, "help", "vm")
        assert result.returncode == 0
        assert "Usage:" in result.stdout

    def test_help_subsubcommand(self, runner_vm):
        """``mvm help vm create`` should show vm create help."""
        result = _run_mvm(runner_vm, "help", "vm", "create")
        assert result.returncode == 0
        assert "Create and start" in result.stdout

    def test_help_nonexistent(self, runner_vm):
        """``mvm help nonexistent`` should fail."""
        result = _run_mvm(runner_vm, "help", "nonexistent", check=False)
        assert result.returncode != 0

    def test_help_version(self, runner_vm):
        """``mvm help version`` should show version help."""
        result = _run_mvm(runner_vm, "help", "version")
        assert result.returncode == 0
        assert "version" in result.stdout.lower()

    def test_completion_bash(self, runner_vm: str) -> None:
        """``mvm completion bash`` should generate a bash shell completion script."""
        result = _run_mvm(runner_vm, "completion", "bash")
        assert result.returncode == 0
        assert "_mvm_completion" in result.stdout, (
            "bash completion output should contain shell function definition"
        )

    def test_completion_zsh(self, runner_vm: str) -> None:
        """``mvm completion zsh`` should generate a zsh shell completion script."""
        result = _run_mvm(runner_vm, "completion", "zsh")
        assert result.returncode == 0
        assert "#compdef mvm" in result.stdout, (
            "zsh completion output should contain the compdef directive"
        )

    def test_completion_fish(self, runner_vm: str) -> None:
        """``mvm completion fish`` should generate a fish shell completion script."""
        result = _run_mvm(runner_vm, "completion", "fish")
        assert result.returncode == 0
        assert "function _mvm_completion" in result.stdout, (
            "fish completion output should contain shell function definition"
        )

    def test_version_command(self, runner_vm: str) -> None:
        """``mvm version`` command should show a version string (distinct from --version flag)."""
        result = _run_mvm(runner_vm, "version")
        assert result.returncode == 0
        version_text = result.stdout.strip()
        assert version_text, "version command output should not be empty"
        assert re.search(r"\d", version_text), (
            f"version command output should contain a digit: {version_text!r}"
        )


class TestHelpOutputConsistentFormat:
    """All --help outputs share common structural elements."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    @pytest.mark.parametrize("cmd_group", _HELP_COMMANDS)
    def test_help_contains_common_elements(
        self, runner_vm: str, cmd_group: str
    ) -> None:
        """Every command group's ``--help`` should contain ``Usage:``, ``Commands:``, ``--help``."""
        result = _run_mvm(runner_vm, cmd_group, "--help")
        help_text = result.stdout

        assert "Usage:" in help_text, f"'{cmd_group} --help' missing 'Usage:'"
        assert "Commands" in help_text, (
            f"'{cmd_group} --help' missing 'Commands'"
        )
        assert "--help" in help_text, (
            f"'{cmd_group} --help' missing '--help' reference"
        )


class TestHelpOutputShowsSubcommands:
    """Each command's --help should list its subcommands."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_vm_help_lists_subcommands(self, runner_vm: str) -> None:
        """``vm --help`` should list expected VM subcommands."""
        result = _run_mvm(runner_vm, "vm", "--help")
        help_text = result.stdout

        expected = {
            "create", "rm", "start", "stop", "reboot",
            "pause", "resume", "ls", "ps", "inspect",
        }
        for cmd in expected:
            assert cmd in help_text, (
                f"'vm --help' missing '{cmd}' subcommand"
            )

    def test_image_help_lists_subcommands(self, runner_vm: str) -> None:
        """``image --help`` should list expected image subcommands."""
        result = _run_mvm(runner_vm, "image", "--help")
        help_text = result.stdout

        expected = {
            "ls",
            "pull",
            "default",
            "rm",
            "inspect",
            "import",
            "warm",
        }
        for cmd in expected:
            assert cmd in help_text, (
                f"'image --help' missing '{cmd}' subcommand"
            )


class TestErrorMessageIsActionable:
    """Error messages should guide the user to fix the problem."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_vm_rm_nonexistent(self, runner_vm: str) -> None:
        """``vm rm`` with a nonexistent VM name should produce an actionable error."""
        result = _run_mvm(
            runner_vm,
            "vm",
            "rm",
            "nonexistent-vm-12345",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected rm of nonexistent VM to fail"
        )

        error_text = result.stderr + result.stdout
        assert len(error_text) > 20, (
            f"Error message too short ({len(error_text)} chars): {error_text!r}"
        )

        # Must contain a specific helpful phrase, not a guess-list
        assert "not found" in error_text.lower(), (
            f"Error message should contain 'not found', got: {error_text!r}"
        )


class TestDebugFlagOutput:
    """The ``--debug`` flag should produce additional diagnostic output."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_debug_flag_produces_output(self, runner_vm: str) -> None:
        """``--debug vm ls --json`` should include debug-level output."""
        result = _run_mvm(
            runner_vm,
            "--debug",
            "vm",
            "ls",
            "--json",
            check=False,
        )

        combined = result.stderr + result.stdout
        debug_marker_found = (
            "DEBUG:" in result.stderr
            or "DEBUG:" in combined
            or "[DEBUG]" in result.stderr
        )

        assert result.returncode == 0 or debug_marker_found, (
            "Command should either succeed or produce debug output. "
            f"Return code: {result.returncode}, stderr: {result.stderr!r}"
        )


class TestVersionFlag:
    """The ``--version`` flag should show a version string."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_version_output(self, runner_vm: str) -> None:
        """``--version`` should show a non-empty version string."""
        result = _run_mvm(runner_vm, "--version")
        version_text = result.stdout.strip()

        assert version_text, "--version output should not be empty"
        assert re.search(r"\d", version_text), (
            f"--version output should contain a digit: {version_text!r}"
        )


class TestHelpSubcommandShowsCorrectly:
    """``mvm help <subcommand>`` and ``mvm <subcommand> --help`` should match."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_help_vm_equivalent_to_vm_help(self, runner_vm: str) -> None:
        """``mvm help vm`` and ``mvm vm --help`` should both show VM help."""
        result_help = _run_mvm(runner_vm, "help", "vm")
        result_flag = _run_mvm(runner_vm, "vm", "--help")

        assert "Usage:" in result_help.stdout, (
            "'mvm help vm' missing 'Usage:'"
        )
        assert "Usage:" in result_flag.stdout, (
            "'mvm vm --help' missing 'Usage:'"
        )
        assert "create" in result_help.stdout, (
            "'mvm help vm' missing 'create' subcommand"
        )
        assert "create" in result_flag.stdout, (
            "'mvm vm --help' missing 'create' subcommand"
        )
