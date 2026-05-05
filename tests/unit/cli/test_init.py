"""Tests for CLI init command."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.api import InitResult, InitStepResult
from mvmctl.main import app

runner = CliRunner()


def _make_init_result(
    host_ready: bool = True,
) -> InitResult:
    steps = [
        InitStepResult(
            step="local_state", success=True, message="State loaded"
        ),
        InitStepResult(
            step="host",
            success=host_ready,
            message="Host ready" if host_ready else "Not ready",
        ),
        InitStepResult(step="cache", success=True, message="Cache initialized"),
        InitStepResult(step="binary", success=True, message="Binary found"),
    ]
    return InitResult(steps=steps, host_ready=host_ready)


class TestInit:
    """Tests for 'init' command."""

    @patch("mvmctl.cli.init.InitOperation")
    def test_init_success(self, mock_init_op):
        mock_init_op.run.return_value = _make_init_result(host_ready=True)
        result = runner.invoke(app, ["init", "--non-interactive"])
        assert result.exit_code == 0
        assert "all set" in result.output

    @patch("mvmctl.cli.init.InitOperation")
    def test_init_host_not_ready(self, mock_init_op):
        mock_init_op.run.return_value = _make_init_result(host_ready=False)
        result = runner.invoke(app, ["init", "--non-interactive"])
        assert result.exit_code == 1
        assert "incomplete" in result.output.lower()

    @patch("mvmctl.cli.init.InitOperation")
    def test_init_with_skip_host(self, mock_init_op):
        mock_init_op.run.return_value = _make_init_result(host_ready=True)
        result = runner.invoke(
            app, ["init", "--skip-host", "--non-interactive"]
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.init.InitOperation")
    def test_init_with_sudo_interaction(self, mock_init_op):
        """Test init path with sudo prompt interaction (non-interactive skips)."""
        needs_interaction = MagicMock()
        needs_interaction.code = "privilege.sudo_required"
        needs_interaction.context = {}

        result_with_needs = InitResult(
            steps=[],
            host_ready=False,
            needs_interaction=needs_interaction,
        )

        mock_init_op.run.side_effect = [
            result_with_needs,
            _make_init_result(host_ready=True),
        ]

        result = runner.invoke(app, ["init", "--non-interactive"])
        assert result.exit_code == 1

    def test_init_help(self):
        result = runner.invoke(app, ["init", "--help"])
        assert result.exit_code == 0
        assert "init" in result.output.lower()


class TestInitHelpers:
    """Tests for init helper functions."""

    def test_compose_all_changed(self):
        from mvmctl.cli.init import _compose_host_setup_message

        before = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        after = {
            "group_exists": True,
            "sudoers_exists": True,
            "user_in_group": True,
        }
        result = _compose_host_setup_message(before, after)
        assert "group created" in result
        assert "sudoers configured" in result
        assert "user added to group" in result

    def test_compose_none_changed(self):
        from mvmctl.cli.init import _compose_host_setup_message

        before = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        after = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        result = _compose_host_setup_message(before, after)
        assert result == "Host already configured"

    def test_compose_partial(self):
        from mvmctl.cli.init import _compose_host_setup_message

        before = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        after = {
            "group_exists": True,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        result = _compose_host_setup_message(before, after)
        assert "group created" in result
        assert "sudoers" not in result
        assert "user" not in result

    def test_compose_sudoers_only(self):
        from mvmctl.cli.init import _compose_host_setup_message

        before = {
            "group_exists": True,
            "sudoers_exists": False,
            "user_in_group": True,
        }
        after = {
            "group_exists": True,
            "sudoers_exists": True,
            "user_in_group": True,
        }
        result = _compose_host_setup_message(before, after)
        assert "sudoers configured" in result
        assert "group" not in result
        assert "user" not in result


class TestInitStepDisplay:
    """Tests for init step result display."""

    @patch("mvmctl.cli.init.InitOperation")
    def test_step_failure(self, mock_init_op):
        steps = [
            InitStepResult(
                step="local_state", success=True, message="State loaded"
            ),
            InitStepResult(
                step="host", success=False, message="Root privileges required"
            ),
            InitStepResult(
                step="cache", success=True, message="Cache initialized"
            ),
            InitStepResult(
                step="binary", success=True, message="Binary available"
            ),
        ]
        mock_init_op.run.return_value = InitResult(
            steps=steps, host_ready=False
        )
        result = runner.invoke(app, ["init", "--non-interactive"])
        assert result.exit_code == 1
        assert "sudoers / mvm group" in result.output
        assert "incomplete" in result.output.lower()

    @patch("mvmctl.cli.init.InitOperation")
    def test_missing_steps(self, mock_init_op):
        steps = [
            InitStepResult(step="local_state", success=True, message="Started"),
        ]
        mock_init_op.run.return_value = InitResult(
            steps=steps, host_ready=False
        )
        result = runner.invoke(app, ["init", "--non-interactive"])
        assert result.exit_code == 1
        assert "not checked" in result.output

    @patch("mvmctl.cli.init.InitOperation")
    def test_unknown_step_key(self, mock_init_op):
        steps = [
            InitStepResult(step="unknown_step", success=True, message="Done"),
        ]
        mock_init_op.run.return_value = InitResult(steps=steps, host_ready=True)
        result = runner.invoke(app, ["init", "--non-interactive"])
        assert result.exit_code == 0
        assert "unknown_step" in result.output


class TestInitInteractiveFlow:
    """Tests for init interactive flow paths."""

    @patch("mvmctl.cli.init._compose_host_setup_message")
    @patch("mvmctl.cli.init._run_with_sudo")
    @patch("mvmctl.cli.init._check_host_state")
    @patch("mvmctl.cli.init.typer.confirm")
    @patch("mvmctl.cli.init.InitOperation")
    def test_sudo_flow_approved(
        self,
        mock_init_op,
        mock_confirm,
        mock_check_state,
        mock_sudo,
        mock_compose,
    ):
        needs_sudo = MagicMock()
        needs_sudo.code = "privilege.sudo_required"
        needs_sudo.context = {}

        mock_init_op.run.side_effect = [
            InitResult(
                steps=[], host_ready=False, needs_interaction=needs_sudo
            ),
            InitResult(
                steps=[
                    InitStepResult(
                        step="local_state", success=True, message="State loaded"
                    )
                ],
                host_ready=True,
            ),
        ]
        mock_check_state.return_value = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        mock_confirm.return_value = True
        mock_sudo.return_value = MagicMock(returncode=0)
        mock_compose.return_value = "Host configured"

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 0
        assert mock_init_op.run.call_count == 2

    @patch("mvmctl.cli.init._check_host_state")
    @patch("mvmctl.cli.init.typer.confirm")
    @patch("mvmctl.cli.init.InitOperation")
    def test_sudo_flow_declined(
        self,
        mock_init_op,
        mock_confirm,
        mock_check_state,
    ):
        needs_sudo = MagicMock()
        needs_sudo.code = "privilege.sudo_required"
        needs_sudo.context = {}

        mock_init_op.run.return_value = InitResult(
            steps=[],
            host_ready=False,
            needs_interaction=needs_sudo,
        )
        mock_check_state.return_value = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        mock_confirm.return_value = False

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 1
        assert "skipped" in result.output

    @patch("mvmctl.cli.init._run_with_sudo")
    @patch("mvmctl.cli.init._check_host_state")
    @patch("mvmctl.cli.init.typer.confirm")
    @patch("mvmctl.cli.init.InitOperation")
    def test_sudo_flow_subprocess_fails(
        self,
        mock_init_op,
        mock_confirm,
        mock_check_state,
        mock_sudo,
    ):
        needs_sudo = MagicMock()
        needs_sudo.code = "privilege.sudo_required"
        needs_sudo.context = {}

        mock_init_op.run.return_value = InitResult(
            steps=[],
            host_ready=False,
            needs_interaction=needs_sudo,
        )
        mock_check_state.return_value = {
            "group_exists": False,
            "sudoers_exists": False,
            "user_in_group": False,
        }
        mock_confirm.return_value = True
        mock_sudo.return_value = MagicMock(returncode=1)

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 1
        assert "host init failed" in result.output

    @patch("mvmctl.cli.init.typer.confirm")
    @patch("mvmctl.cli.init.InitOperation")
    def test_binary_download_approved(
        self,
        mock_init_op,
        mock_confirm,
    ):
        needs_bin = MagicMock()
        needs_bin.code = "binary.confirm_download"
        needs_bin.context = {"latest_version": "1.15.0"}

        mock_init_op.run.side_effect = [
            InitResult(steps=[], host_ready=False, needs_interaction=needs_bin),
            InitResult(
                steps=[
                    InitStepResult(
                        step="binary", success=True, message="Downloaded"
                    )
                ],
                host_ready=True,
            ),
        ]
        mock_confirm.return_value = True

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 0
        assert mock_init_op.run.call_count == 2
        assert "downloading" in result.output

    @patch("mvmctl.cli.init.typer.confirm")
    @patch("mvmctl.cli.init.InitOperation")
    def test_binary_download_declined(
        self,
        mock_init_op,
        mock_confirm,
    ):
        needs_bin = MagicMock()
        needs_bin.code = "binary.confirm_download"
        needs_bin.context = {"latest_version": "1.15.0"}

        mock_init_op.run.return_value = InitResult(
            steps=[],
            host_ready=False,
            needs_interaction=needs_bin,
        )
        mock_confirm.return_value = False

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 1
        assert "skipped" in result.output

    @patch("mvmctl.cli.init.InitOperation")
    def test_binary_download_no_version(self, mock_init_op):
        needs_bin = MagicMock()
        needs_bin.code = "binary.confirm_download"
        needs_bin.context = {}  # No latest_version

        mock_init_op.run.return_value = InitResult(
            steps=[],
            host_ready=False,
            needs_interaction=needs_bin,
        )

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 1
        assert "no firecracker binary" in result.output.lower()
        assert "remote versions" in result.output

    @patch("mvmctl.cli.init.InitOperation")
    def test_unknown_interaction(self, mock_init_op):
        needs_unknown = MagicMock()
        needs_unknown.code = "some.random.code"
        needs_unknown.context = {}

        mock_init_op.run.return_value = InitResult(
            steps=[],
            host_ready=False,
            needs_interaction=needs_unknown,
        )

        result = runner.invoke(app, ["init"])
        assert result.exit_code == 1
        assert "unhandled interaction" in result.output.lower()
