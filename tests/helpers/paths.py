from pathlib import Path
from typing import NamedTuple


class TestPaths(NamedTuple):
    root: Path
    config: Path
    cache: Path
    temp: Path


def make_test_paths(tmp_path: Path) -> TestPaths:
    return TestPaths(
        root=tmp_path,
        config=tmp_path / "config",
        cache=tmp_path / "cache",
        temp=tmp_path / "temp",
    )
