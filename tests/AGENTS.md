# tests/ — Test Suite

**Scope:** Unit and integration tests  
**Coverage Gate:** 80% branch coverage (`pyproject.toml --cov-fail-under=80`)  
**Rule:** Tests must NEVER require root, KVM, or real network stack

## STRUCTURE

```
tests/
├── unit/
│   ├── conftest.py       # Shared fixtures (autouse isolation, VM/network fixtures)
│   ├── test_cli_*.py     # CLI layer tests (CliRunner, no subprocess)
│   ├── test_*.py         # Core/API unit tests (38 files total)
│   └── test_vm_lifecycle.py, test_vm_manager.py, test_host.py, test_image.py, ...
└── integration/
    └── test_cli_smoke.py # Invokes real `mvm --help`, `mvm --version`
```

## KEY FIXTURES (conftest.py)

**Autouse — runs before every test:**
```python
@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path / "config"))
    monkeypatch.setenv("MVM_CACHE_DIR",  str(tmp_path / "cache"))
```
Guarantees no test touches `~/.config/mvmctl/` or `~/.cache/`.

**Other fixtures:** `vm_manager`, `sample_vm`, `stopped_vm`, `running_vm`, `sample_network_config`, `mock_cache_dir`

## MOCKING PATTERNS

**pytest-mock** (preferred for simple patches):
```python
def test_foo(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])
```

**unittest.mock.patch** (for subprocess/OS calls):
```python
@patch("mvmctl.core.host_setup.subprocess.run")
def test_bar(mock_run):
    mock_run.return_value = MagicMock(returncode=0, stdout="1\n")
```

**CLI testing** (always use CliRunner, never subprocess):
```python
from typer.testing import CliRunner
runner = CliRunner()
result = runner.invoke(app, ["rm", "--name", "myvm", "--force"])
assert result.exit_code == 0
```

**Patching VMManager** (new hash-keyed API):
```python
mock_mgr = mocker.MagicMock()
mock_mgr.get_by_name.return_value = [vm]
mock_mgr.find_by_short_id.return_value = []
mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
```

**image_rm / metadata tests** — set `MVM_CACHE_DIR` via `monkeypatch.setenv`, write `metadata.json` manually in `tmp_path`.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Real subprocess calls | `@patch("...subprocess.run")` |
| `tempfile.mkdtemp()` | `tmp_path` pytest fixture |
| Skip test for coverage | Fix it; coverage drop fails CI |
| Hardcoded `~/.cache/` paths | `monkeypatch.setenv("MVM_CACHE_DIR", ...)` |

## COMMANDS

```bash
uv run pytest tests/ -x -q              # Fast run, stop on first failure
uv run pytest tests/unit/test_vm_manager.py -v   # Single file
uv run pytest tests/integration/ -v     # Smoke tests (no mocks needed)
```

## NOTES

- **38 unit test files** + 1 integration smoke test
- mypy strict exempted for tests (`pyproject.toml` overrides: no `disallow_untyped_defs`)
- `test_host.py` is 1,831 lines — largest test file; covers all host init paths
- `test_image.py` (969 lines), `test_kernel.py` (688 lines) are next-largest
- Two separate kernel test files: `test_kernel.py` (legacy) + `test_kernel_new.py` (new features)
