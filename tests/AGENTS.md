# tests/ — Test Suite

**Scope:** Unit and integration tests  
**Coverage Gate:** 80% branch coverage (`pyproject.toml --cov-fail-under=80`)  
**Rule:** Tests must NEVER require root, KVM, or real network stack

## STRUCTURE

```
tests/
├── conftest.py          # Root: _isolate_iptables_rules, _mock_sudo_cache (autouse)
├── unit/
│   ├── conftest.py      # Shared fixtures: isolate_config_and_cache (autouse), VM/network fixtures
│   ├── test_cli_*.py    # CLI layer tests (CliRunner, no subprocess) — 7 files
│   └── test_*.py        # Core/API unit tests — 34 other test_*.py (41 total in unit/)
└── integration/
    ├── test_cli_smoke.py       # In-process CliRunner against `mvmctl.main.app` (NOT real subprocess)
    ├── test_host_init_reset.py # Host init/reset workflow (mocked subprocess)
    ├── test_vm_lifecycle.py    # VM create/remove workflow (mocked subprocess)
    └── test_network_workflow.py # Network create/inspect/remove workflow
```

## KEY FIXTURES (conftest.py)

**Root conftest (autouse — ALL tests):**
```python
@pytest.fixture(autouse=True)
def _mock_sudo_cache():
    # Prevents real sudo -n/-v calls during tests
```

**Unit conftest (autouse — every unit test):**
```python
@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path / "config"))
    monkeypatch.setenv("MVM_CACHE_DIR",  str(tmp_path / "cache"))
```
Guarantees no test touches `~/.config/mvmctl/` or `~/.cache/`.

**Other unit fixtures:** `vm_manager` (real `VMManager(tmp_path)`), `sample_vm`, `stopped_vm`, `running_vm`, `error_vm`, `sample_network_config`, `mock_cache_dir`, `mock_keys_dir`, `sample_key_info`, `mock_subprocess_run_success`, `mock_subprocess_run_failure`

**Note:** `mock_subprocess_run_*` patch `subprocess.run` globally via `monkeypatch.setattr("subprocess.run", ...)` — not module-scoped.

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

**Patching VMManager** (hash-keyed API):
```python
mock_mgr = mocker.MagicMock()
mock_mgr.get_by_name.return_value = [vm]
mock_mgr.find_by_short_id.return_value = []
mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
```

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Real subprocess calls | `@patch("...subprocess.run")` |
| Real sudo invocations | Root conftest `_mock_sudo_cache` blocks; mock `ensure_default_network` in host tests |
| `tempfile.mkdtemp()` | `tmp_path` pytest fixture |
| Skip test for coverage | Fix it; coverage drop fails CI |
| Hardcoded `~/.cache/` paths | `monkeypatch.setenv("MVM_CACHE_DIR", ...)` |

## COMMANDS

```bash
uv run pytest tests/ -x -q              # Fast run, stop on first failure
uv run pytest tests/unit/test_vm_manager.py -v   # Single file
uv run pytest tests/integration/ -v     # Integration tests
```

## NOTES

- **41 unit** + **4 integration** `test_*.py` (**45** total under `tests/`)
- mypy strict exempted for tests (`pyproject.toml` overrides: no `disallow_untyped_defs`)
- `test_host.py` is 1,849 lines — largest test file; covers all host init/clean/reset paths
- `test_image.py` (2,032 lines), `test_network.py` (1,233 lines) are next-largest
- Two separate kernel test files: `test_kernel.py` (legacy) + `test_kernel_new.py` (new features)
