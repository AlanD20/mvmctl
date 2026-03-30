import typer
import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.debug_state import is_debug_mode, set_debug_mode
from mvmctl.utils.error_handler import handle_mvm_error


def test_handle_mvm_error_non_debug(capsys: pytest.CaptureFixture[str]) -> None:
    set_debug_mode(False)
    with pytest.raises(typer.Exit):
        handle_mvm_error(MVMError("test error"))
    captured = capsys.readouterr()
    assert "test error" in captured.out or "test error" in captured.err


def test_handle_mvm_error_debug_includes_traceback(capsys: pytest.CaptureFixture[str]) -> None:
    set_debug_mode(True)
    try:
        raise MVMError("debug error")
    except MVMError as e:
        exc = e
    with pytest.raises(typer.Exit):
        handle_mvm_error(exc)
    set_debug_mode(False)
    captured = capsys.readouterr()
    assert "debug error" in captured.out or "debug error" in captured.err


def test_handle_mvm_error_exits_with_code_1() -> None:
    set_debug_mode(False)
    with pytest.raises(typer.Exit) as exc_info:
        handle_mvm_error(MVMError("error"))
    assert exc_info.value.exit_code == 1


def test_debug_state_set_and_get() -> None:
    set_debug_mode(True)
    assert is_debug_mode() is True
    set_debug_mode(False)
    assert is_debug_mode() is False
