# pyright: reportMissingImports=false
"""Tests for the standalone logs CLI command."""

from pytest_mock import MockerFixture
from typer.testing import CliRunner

from mvmctl.cli.logs import app
from mvmctl.exceptions import MVMError

runner = CliRunner()


def test_logs_success(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.logs.get_logs", return_value=["line 1\n", "line 2\n"])
    result = runner.invoke(app, ["--name", "myvm"])
    assert result.exit_code == 0


def test_logs_failure(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.logs.get_logs", side_effect=MVMError("Log error"))
    result = runner.invoke(app, ["--name", "badvm"])
    assert result.exit_code == 1
