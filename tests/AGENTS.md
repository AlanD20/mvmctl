# tests/ — Test Suite

**Scope:** Unit and integration tests  
**Coverage Gate:** 80% branch coverage (`pyproject.toml --cov-fail-under=80`)  
**Rule:** Tests must NEVER require root, KVM, or real network stack

## STRUCTURE

```
tests/
├── conftest.py              # Root: _isolate_iptables_rules, _mock_sudo_cache (autouse)
├── unit/
│   ├── conftest.py          # Shared fixtures: isolate_config_and_cache (autouse), VM/network fixtures
│   ├── test_cli_*.py        # CLI layer tests (CliRunner, no subprocess) — 7 files
│   └── test_*.py            # Core/API unit tests — 34 other test_*.py (41 total in unit/)
├── integration/
│   ├── test_cli_smoke.py           # In-process CliRunner against `mvmctl.main.app` (NOT real subprocess)
│   ├── test_host_init_reset.py     # Host init/reset workflow (mocked subprocess)
│   ├── test_vm_lifecycle.py        # VM create/remove workflow (mocked subprocess)
│   └── test_network_workflow.py    # Network create/inspect/remove workflow
└── layer_compliance/
    ├── test_imports.py       # Enforces import boundaries (cli→api→core only)
    ├── test_constants.py    # Ensures constants.py is single source of truth
    └── test_privilege.py    # Verifies privilege checks in api/ layer
```

## KEY FIXTURES (conftest.py)

### Root Conftest (autouse — ALL tests)

Prevents real sudo/system calls across entire test suite.

```python
@pytest.fixture(autouse=True)
def _mock_sudo_cache():
    # Prevents real sudo -n/-v calls during tests
    # Blocks subprocess invokes to sudo, sudoedit, pkexec
```

### Unit Conftest (autouse — every unit test)

Isolates filesystem and environment per test.

```python
@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path / "config"))
    monkeypatch.setenv("MVM_CACHE_DIR",  str(tmp_path / "cache"))
    monkeypatch.setenv("MVM_STATE_DIR",  str(tmp_path / "state"))
```

Guarantees no test touches `~/.config/mvmctl/`, `~/.cache/`, or `~/.local/share/mvmctl/`.

### VM Fixtures

| Fixture | Type | Purpose |
|---------|------|---------|
| `vm_manager` | `VMManager(tmp_path)` | Real manager instance, isolated state |
| `sample_vm` | `VMInstance` | Valid VM with default config |
| `stopped_vm` | `VMInstance` | VM in stopped state (idempotent cleanup) |
| `running_vm` | `VMInstance` | VM in running state (mocks socket) |
| `error_vm` | `VMInstance` | VM in error state |

### Network Fixtures

```python
@pytest.fixture
def sample_network_config():
    return NetworkConfig(name="testnet", subnet="10.0.0.0/24", ...)
```

### Key Fixtures

| Fixture | Type | Purpose |
|---------|------|---------|
| `mock_keys_dir` | `Path` | Temporary `.ssh/` dir with mock keys |
| `sample_key_info` | `dict` | Valid key metadata for tests |

### Subprocess Mocks

| Fixture | Behavior |
|---------|----------|
| `mock_subprocess_run_success` | Returns `returncode=0, stdout="", stderr=""` |
| `mock_subprocess_run_failure` | Returns `returncode=1, stdout="", stderr="error"` |

**Note:** Mocks patch `subprocess.run` globally via `monkeypatch.setattr("subprocess.run", ...)` — not module-scoped.

## MOCKING PATTERNS

### pytest-mock (preferred)

For simple return-value patches and Spy calls.

```python
def test_list_vms_empty(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])
    # CLI command now returns empty list
```

```python
def test_create_vm_calls_api(mocker: MockerFixture):
    mock_api = mocker.patch("mvmctl.api.vm.create_vm")
    mock_api.return_value = VMInstance(name="test")
    # Verify API was called
    mock_api.assert_called_once()
```

### unittest.mock.patch

For subprocess/OS calls and complex mocks.

```python
@patch("mvmctl.core.host_setup.subprocess.run")
def test_host_init_calls_subprocess(mock_run):
    mock_run.return_value = MagicMock(returncode=0, stdout="")
    init_host()
    mock_run.assert_any_call(["sudo", ...], check=True, ...)
```

```python
@patch("mvmctl.core.firecracker.requests.Session")
def test_firecracker_client(mock_session):
    mock_session.return_value.get.return_value.json.return_value = {...}
```

### CLI Testing (CliRunner)

Always use CliRunner for CLI layer; never invoke real subprocess.

```python
from typer.testing import CliRunner

runner = CliRunner()

def test_rm_success():
    result = runner.invoke(app, ["rm", "--name", "myvm", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.stdout

def test_rm_not_found():
    result = runner.invoke(app, ["rm", "--name", "nonexistent"])
    assert result.exit_code == 2  # Click usage error
```

### Patching VMManager (hash-keyed API)

VMManager uses SHA256 hash keys; mock the return structure.

```python
def test_vm_lookup_by_name(mocker: MockerFixture):
    mock_mgr = mocker.MagicMock()
    sample = VMInstance(name="test", id="abc123def...")
    mock_mgr.get_by_name.return_value = [sample]
    mock_mgr.find_by_short_id.return_value = []
    mocker.patch("mvmctl.core.vm_manager.VMManager", return_value=mock_mgr)
```

### Patching Firecracker Socket

```python
def test_vm_start_socket_exists(mocker: MockerFixture):
    mock_exists = mocker.patch("pathlib.Path.exists", return_value=True)
    mocker.patch("mvmctl.core.firecracker.FirecrackerClient")
    # VM start proceeds to socket connection
```

### Patching Network Operations

```python
def test_network_create(mock_subprocess_run_success):
    # Uses shared fixture from conftest
    result = runner.invoke(app, ["network", "create", "mynet", "--subnet", "10.1.0.0/16"])
    assert result.exit_code == 0
```

## LAYER COMPLIANCE TESTS

`tests/layer_compliance/` enforces architectural boundaries.

### test_imports.py

Verifies import boundaries are not violated.

```python
def test_cli_does_not_import_core():
    # Ensures cli/ never imports from core/ directly
    # All CLI→Core calls go through api/ layer
```

```python
def test_api_imports_core():
    # api/ may import from core/ (this is correct)
    pass
```

### test_constants.py

Ensures constants.py is single source of truth.

```python
def test_no_hardcoded_defaults():
    # Scans source for forbidden patterns:
    # - Hardcoded paths
    # - Inline default values
    # - Magic numbers
```

```python
def test_env_var_coverage():
    # All user-facing config must have MVM_* env var
    # checked in constants.py or api/
```

### test_privilege.py

Verifies privilege checks exist in api/ layer, not in cli/.

```python
def test_binary_checks_in_api():
    # find_binary_path() privilege check must be in api/, not cli/
    pass
```

```python
def test_host_operations_require_privilege():
    # Host init/reset must call check_privileges() via api/
    pass
```

## TEST FILE SIZES

| File | Lines | Purpose |
|------|-------|---------|
| `test_image.py` | ~2032 | Image resolution, download, import, verify, remove |
| `test_host.py` | ~1849 | Host init, clean, reset, network setup/teardown |
| `test_network.py` | ~1233 | Network create, inspect, remove, TAP management |
| `test_vm_manager.py` | ~950+ | VM state CRUD, hash-keyed storage |
| `test_kernel.py` | ~800 | Kernel listing, resolution, activation |
| `test_firecracker.py` | ~700 | Firecracker client, socket ops, API calls |

**Legacy note:** Two separate kernel files: `test_kernel.py` (legacy) + `test_kernel_new.py` (new features)

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Real subprocess calls | `@patch("...subprocess.run")` |
| Real sudo invocations | Root conftest `_mock_sudo_cache` blocks; mock `ensure_default_network` in host tests |
| `tempfile.mkdtemp()` | `tmp_path` pytest fixture |
| Skip test for coverage | Fix it; coverage drop fails CI |
| Hardcoded `~/.cache/` paths | `monkeypatch.setenv("MVM_CACHE_DIR", ...)` |
| `type: ignore` in tests | Allowed for mocks; document reason |

## COMMANDS

```bash
# Fast run (stop on first failure)
uv run pytest tests/ -x -q

# Single file with verbose output
uv run pytest tests/unit/test_vm_manager.py -v

# Integration tests only
uv run pytest tests/integration/ -v

# Layer compliance tests
uv run pytest tests/layer_compliance/ -v

# Coverage report (fails if <80% branch)
uv run pytest tests/ --cov=mvmctl --cov-fail-under=80

# Run specific test class
uv run pytest tests/unit/test_vm_manager.py::TestVMManagerGetByName -v

# Run tests matching pattern
uv run pytest tests/ -k "test_create" -v

# With profiling (slow)
uv run pytest tests/ --profile

# With full traceback
uv run pytest tests/ --tb=long

# Parallel execution (requires pytest-xdist)
uv run pytest tests/ -n auto
```

## NOTES

- **48 total test files**: 41 unit + 4 integration + 3 layer_compliance
- mypy strict exempted for tests (`pyproject.toml` overrides: no `disallow_untyped_defs`)
- All tests run as non-root; no KVM access required
- Fixtures in `unit/conftest.py` auto-used for all unit tests
- CliRunner invoked against `mvmctl.main.app` (Click group), NOT Typer app
