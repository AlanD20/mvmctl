# tests/unit/ — Unit Test Suite

**Scope:** 54+ test files covering all CLI, API, core, utils, models, services, and DB modules
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Parent:** See `tests/AGENTS.md` for fixtures, mocking patterns, and CliRunner conventions — not repeated here

## FILE → SOURCE MAPPING

### CLI Layer (7 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_cli_vm.py` | `cli/vm.py` | CliRunner + mocker.patch; short-ID vs name resolution |
| `test_cli_asset.py` | `cli/bin.py` | kernel/image/bin commands; patches `core.metadata` directly (known layer violation) |
| `test_cli_host.py` | `cli/host.py` | host init/clean/reset; mocked subprocess |
| `test_cli_network.py` | `cli/network.py` | network create/rm/ls; patches `api.network.*` |
| `test_cli_key.py` | `cli/key.py` | key add/create/rm; patches `api.keys.*` |
| `test_cli_config.py` | `cli/config.py` | config get/set/show; patches `api.config.*` |
| `test_cli_configure.py` | `cli/init.py` | wizard steps; mocked binary/kernel/image flows |

### API Layer (6 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_api_vms.py` | `api/vms.py` | Verifies `cleanup_vms` is the only vm op with privilege check |
| `test_api_network.py` | `api/network.py` | Verifies privilege check on create/remove; not on list/inspect |
| `test_api_assets.py` | `api/assets.py` | Verifies direct pass-through to core (no privilege wrap) |
| `test_api_host.py` | `api/host.py` | Host init/reset/status/clean |
| `test_api_init.py` | `api/init.py` | Init/onboarding API |
| `test_api_keys.py` | `api/keys.py` | SSH key operations |

### Core Layer (22 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_image.py` | `core/image.py` | ~2032 lines — image resolution, download, import, conversion, remove |
| `test_host.py` | `core/host_setup.py` + `core/host.py` | ~1849 lines — init, clean, reset, iptables, sysctl |
| `test_network.py` | `core/network.py` | ~1233 lines — bridge, TAP, NAT, iptables chains |
| `test_vm_manager.py` | `core/vm_manager.py` | ~950 lines — hash-keyed CRUD, name vs short-ID lookup |
| `test_kernel.py` | `core/kernel.py` | ~800 lines — legacy (complete coverage) |
| `test_kernel_new.py` | `core/kernel.py` | — new feature tests; do NOT delete `test_kernel.py` |
| `test_firecracker.py` | `core/firecracker.py` | ~700 lines — socket, HTTP API, client lifecycle |
| `test_firecracker_client.py` | `core/firecracker.py` | — FirecrackerClient unit tests |
| `test_vm_lifecycle.py` | `core/vm_lifecycle.py` | — create/remove orchestration |
| `test_vm_lifecycle_helpers.py` | `core/vm_lifecycle.py` | — `_resolve_image_path`, `generate_vm_id` |
| `test_network_manager.py` | `core/network_manager.py` | — named networks, IP leases |
| `test_metadata.py` | `core/metadata.py` | — MetadataCache, locking, short-ID lookup |
| `test_config_gen.py` | `core/config_gen.py` | — ConfigGenerator, template rendering |
| `test_config.py` | `core/config.py` | — YAML loading, MVMConfig dataclass |
| `test_config_state.py` | `core/config_state.py` | — config.json persistence, default accessors |
| `test_binary_manager.py` | `core/binary_manager.py` | — fetch, set-default, version management; SQLite-first canonical tests |
| `test_cloud_init.py` | `core/cloud_init.py` | — ISO creation, user-data injection |
| `test_host_privileges.py` | `core/host_privilege.py` | — group membership, sudoers check |
| `test_key_manager.py` | `core/key_manager.py` | — import, generate, list, remove |
| `test_logs.py` | `core/logs.py` | — log path resolution, follow mode |
| `test_ssh.py` | `core/ssh.py` | — key resolution, command building |
| `test_user_config.py` | `core/user_config.py` | — config get/set helpers |

### DB Layer (5 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_initial_schema.py` | `db/migrations/001_initial_schema.sql` | Schema validation |
| `test_migration_runner.py` | `db/migrations/runner.py` | Migration application, db_migrations tracking |
| `test_models.py` | `db/models.py` | ORM dataclass validation |
| `test_db_integration.py` | `db/` + `core/mvm_db.py` | Integration of DB layer |
| `test_mvm_db_assets.py` | `core/mvm_db.py` | Asset-related DB operations |

### Utils Layer (6 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_audit.py` | `utils/audit.py` | Tests private `_audit_logger` and `_get_audit_log_path` directly |
| `test_constants.py` | `constants.py` | Verifies `FALLBACK_*` / `DEFAULT_*` completeness |
| `test_fs.py` | `utils/fs.py` | Path resolution, SUDO_USER bridging |
| `test_http.py` | `utils/http.py` | Resumable download, SHA256, missing checksum handling |
| `test_process.py` | `utils/process.py` | `run_cmd` / `stream_cmd` — only consumer in test suite |
| `test_validation.py` | `utils/validation.py` | Name regex, boot arg rejection, IP validation |

### Root (3 files)

| Test file | Source |
|-----------|--------|
| `test_main.py` | `main.py` — LazyMVMGroup loading, `_reconcile_networks`, root commands |
| `test_vm_config_file.py` | `models/vm_config_file.py` — `--import-config` / `--output-config` |
| `test_security.py` | Cross-cutting: checksum handling, privilege escalation boundaries |

## NOTES

- **Two kernel test files coexist**: `test_kernel.py` (full legacy coverage) + `test_kernel_new.py` (new features). Do not merge or delete either.
- **VMManager mocking**: Always mock both `get_by_name()` and `find_by_id_prefix()` together — `vm rm` tries ID prefix first, then falls back to name.
- **`test_security.py`**: Not tied to a single source file — validates security properties across modules.
- **`conftest.py`** (132 lines) — provides VM fixtures (`sample_vm`, `running_vm`, `stopped_vm`, `error_vm`), network fixtures, key fixtures, and subprocess mock fixtures; autouse isolation via parent `tests/conftest.py`.

### Services Layer (tests/unit/services/)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `services/console_relay/test_manager.py` | `services/console_relay/manager.py` | ConsoleManager start/stop/is_running; PID file lifecycle |
| `services/console_relay/test_process.py` | `services/console_relay/process.py` | PTY relay main() entry point; argparse; vsock connection mocks |
| `services/nocloud_server/test_process.py` | `services/nocloud_server/process.py` | HTTP server main() entry point; port binding; config-dir serving |

Note: `nocloud_server/test_manager.py` does not exist yet — NocloudServerManager lifecycle is currently tested via integration tests.

### Models Layer (tests/unit/models/)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `models/test_vm.py` | `models/vm.py` | VMStatus (7 values), VMConfig, VMInstance field validation |
| `models/test_cloud_init.py` | `models/cloud_init.py` | CloudInitMode (4 values), CloudInitStatus (4 values), CloudInitConfig |
