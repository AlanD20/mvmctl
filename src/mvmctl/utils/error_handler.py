from typing import NoReturn

import typer

from mvmctl.exceptions import format_exception_debug
from mvmctl.utils.console import print_error
from mvmctl.utils.debug_state import is_debug_mode


def handle_mvm_error(exc: Exception, exit_code: int = 1) -> NoReturn:
    formatted = format_exception_debug(exc, is_debug_mode())
    print_error(formatted)
    raise typer.Exit(code=exit_code)
