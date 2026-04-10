# Unit Tests: Core Layer (`tests/unit/core/`)

## OVERVIEW
Isolated unit tests for `src/mvmctl/core/` business logic using extensive mocking of system calls, subprocesses, and networks.

## STRUCTURE
- `test_mvm_db_*.py`: CRUD operations and state persistence for VMs, assets, and host metadata.
- `test_network_*.py`: Logic for NAT/iptables rules, interface discovery, and IP lease management.
- `test_vm_monitor.py`: VM state reconciliation and health monitoring logic.
- `test_cache_manager.py`: Orchestration of image/kernel cache initialization and pruning.

## WHERE TO LOOK
| Module | Test File | Key Pattern |
|--------|-----------|-------------|
| `core/mvm_db.py` | `test_mvm_db_*.py` | Uses local `db` fixture for fresh SQLite migrations per test. |
| `core/network.py` | `test_network_nat.py` | Mocks `subprocess.run` to verify `iptables` rule construction. |
| `core/vm_lifecycle.py` | `test_vm_monitor.py` | Mocks `FirecrackerClient` context manager to simulate API states. |
| `core/network_manager.py` | `test_network_manager.py` | Tests IP availability and lease logic without real bridges. |

## CONVENTIONS
- **System Isolation**: ALL tests must mock `subprocess.run`, `os.kill`, and `shutil` to prevent real host modification.
- **Mocking**: Use `mocker` (pytest-mock) for module-level patches and `unittest.mock.patch` for localized OS-level behavior.
- **Data Factories**: Use `make_test_vmconfig` and `make_vm` fixtures from `tests/unit/conftest.py` for consistent domain objects.
- **Database**: Database tests MUST use the `db` fixture (isolated SQLite) and verify foreign key constraints.
- **Error States**: Every core module test must include a `pytest.raises` case for expected `MVMError` subclasses.

## NOTES
- **Relationship**: These tests verify the isolated "ingredients" of the architecture (see `core/AGENTS.md`).
- **Privileges**: Privilege checks are automatically mocked by root `conftest.py`; tests assume success unless testing `PrivilegeError`.
- **Paths**: Always use `tmp_path` or `make_test_paths()` for any file-based logic to maintain environment isolation.
