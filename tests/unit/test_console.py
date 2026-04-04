from mvmctl.utils.console import _PlainConsole, _strip_markup, format_timestamp


def test_strip_markup_removes_tags():
    assert _strip_markup("[green]hello[/green]") == "hello"
    assert _strip_markup("[bold]text[/bold]") == "text"
    assert _strip_markup("no tags here") == "no tags here"


def test_plain_console_print(capsys):
    c = _PlainConsole()
    c.print("hello", "world")
    captured = capsys.readouterr()
    assert "hello world" in captured.out


def test_plain_console_getattr_noop():
    c = _PlainConsole()
    noop = c.status
    noop("anything", key="value")


def test_format_timestamp_none():
    assert format_timestamp(None) == "-"


def test_format_timestamp_empty():
    assert format_timestamp("") == "-"


def test_format_timestamp_valid():
    result = format_timestamp("2024-01-15T10:30:00")
    assert result == "2024/01/15 10:30:00"


def test_format_timestamp_invalid():
    result = format_timestamp("not-a-date")
    assert result == "not-a-date"


def test_print_table_row_with_extra_cells(capsys):
    from mvmctl.utils.console import print_table

    print_table("T", ["Col1", "Col2"], [["a", "b", "EXTRA"]])
    captured = capsys.readouterr()
    assert "Col1" in captured.out
    assert "Col2" in captured.out
