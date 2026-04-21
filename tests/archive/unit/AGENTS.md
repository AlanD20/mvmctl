# tests/unit/ — Unit Test Suite

**Scope:** 100 test files covering all CLI, API, core, utils, models, services, and DB modules
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Parent:** See `tests/AGENTS.md` for fixtures, mocking patterns, and CliRunner conventions — not repeated here

## FILE → SOURCE MAPPING

### CLI Layer (15 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_cli_vm.py` | `cli/vm.py` | CliRunner + mocker.patch; short-ID vs name resolution |
| `test_cli_bin.py` | `cli/bin.py` | kernel/image/bin commands; patches `core.metadata` directly (known layer violation) |
| `test_cli_host.py` | `cli/host.py` | host init/clean/reset; mocked subprocess |
| `test_cli_network.py` | `cli/network.py` | network create/rm/ls; patches `api.network.*` |
| `test_cli_key.py` | `cli/key.py` | key add/create/rm; patches `api.keys.*` |
| `test_cli_config.py` | `cli/config.py` | config get/set/show; patches `api.config.*` |
| `test_cli_init.py` | `cli/init.py` | wizard steps; mocked binary/kernel/image flows |
| `test_cli_cache.py` | `cli/cache.py` | Cache management commands |
| `test_cli_console.py` | `cli/console.py` | Console access commands |
| `test_cli_helpers.py` | `cli/helpers.py` | CLI helper functions |
| `test_cli_logs.py` | `cli/logs.py` | Log viewing commands |
| `test_cli_ssh.py` | `cli/ssh.py` | SSH command tests |
| `cli/test_cache.py` | `cli/cache.py` | Additional cache tests |
| `cli/test_console.py` | `cli/console.py` | Additional console tests |
| `test_main.py` | `main.py` | LazyMVMGroup loading, `_reconcile_networks`, root commands |

### API Layer (10 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_api_vms.py` | `api/vms.py` | Verifies `cleanup_vms` is the only vm op with privilege check |
| `test_api_network.py` | `api/network.py` | Verifies privilege check on create/remove; not on list/inspect |
| `test_api_assets.py` | `api/assets.py` | Verifies direct pass-through to core (no privilege wrap) |
| `test_api_host.py` | `api/host.py` | Host init/reset/status/clean |
| `test_api_init.py` | `api/init.py` | Init/onboarding API |
| `test_api_keys.py` | `api/keys.py` | SSH key operations |
| `test_api_cache.py` | `api/cache.py` | Cache API tests |
| `test_api_config.py` | `api/config.py` | Config API tests |
| `test_api_console.py` | `api/console.py` | Console API tests |
| `test_api_vm_config.py` | `api/vm_config.py` | VM config API tests |

### Core Layer (35 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_image.py` | `core/image.py` | ~2032 lines — image resolution, download, import, conversion, remove |
| `test_host.py` | `core/host_setup.py` + `core/host.py` | ~1849 lines — init, clean, reset, iptables, sysctl |
| `test_network.py` | `core/network.py` | ~1233 lines — bridge, TAP, NAT, iptables chains |
| `test_vm_manager.py" | `core/vm_manager.py` | ~950 lines — hash-keyed CRUD, name vs short-ID lookup |
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
| `test_console_core.py` | `core/console.py` | Core console functionality |
| `test_debug_mode.py` | `core/debug.py` | Debug mode functionality |
| `test_disk_size.py` | `core/disk_size.py` | Disk size handling |
| `test_firewall.py` | `core/firewall.py` | Firewall management |
| `test_partition_detection.py` | `core/partition_detection.py` | Partition detection |
| `test_rootfs_injector.py` | `core/rootfs_injector.py` | Rootfs injection |
| `test_vm_process.py` | `core/vm_process.py` | VM process management |
| `core/test_cache_manager.py` | `core/cache_manager.py` | Cache manager |
| `core/test_mvm_db_assets.py` | `core/mvm_db.py` | DB asset operations |
| `core/test_mvm_db_host.py` | `core/mvm_db.py` | DB host operations |
| `core/test_mvm_db_vms.py` | `core/mvm_db.py` | DB VM operations |
| `core/test_network_interfaces.py` | `core/network.py` | Network interfaces |
| `core/test_network_nat.py` | `core/network.py` | NAT operations |

### DB Layer (8 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_initial_schema.py` | `db/migrations/001_initial_schema.sql` | Schema validation |
| `test_migration_runner.py` | `db/migrations/runner.py` | Migration application, db_migrations tracking |
| `test_models.py` | `db/models.py` | ORM dataclass validation |
| `test_db_integration.py` | `db/` + `core/mvm_db.py` | Integration of DB layer |
| `test_mvm_db_assets.py` | `core/mvm_db.py` | Asset-related DB operations |
| `db/test_db_integration.py` | `db/` | Redundant DB integration |
| `db/test_initial_schema.py` | `db/` | Redundant schema validation |
| `db/test_models.py` | `db/` | Redundant model validation |

### Utils Layer (12 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_audit.py` | `utils/audit.py` | Tests private `_audit_logger` and `_get_audit_log_path` directly |
| `test_constants.py` | `constants.py` | Verifies `FALLBACK_*` / `DEFAULT_*` completeness |
| `test_fs.py" | `utils/fs.py` | Path resolution, SUDO_USER bridging |
| `test_http.py` | `utils/http.py` | Resumable download, SHA256, missing checksum handling |
| `test_process.py` | `utils/process.py` | `run_cmd` / `stream_cmd` — only consumer in test suite |
| `test_validation.py` | `utils/validation.py` | Name regex, boot arg rejection, IP validation |
| `test_utils_guestfs.py` | `utils/guestfs.py` | Guestfs utilities |
| `test_utils_resize.py` | `utils/resize.py` | Resize utilities |
| `test_utils_template.py` | `utils/template.py` | Template utilities |
| `test_utils_time.py` | `utils/time.py` | Time utilities |
| `test_utils_yaml.py` | `utils/yaml.py` | YAML utilities |
| `utils/test_error_handler.py` | `utils/error_handler.py` | Error handling |

### Services Layer (4 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `services/console_relay/test_manager.py` | `services/console_relay/manager.py` | ConsoleManager start/stop/is_running; PID file lifecycle |
| `services/console_relay/test_process.py` | `services/console_relay/process.py` | PTY relay main() entry point; argparse; vsock connection mocks |
| `services/nocloud_server/test_process.py` | `services/nocloud_server/process.py` | HTTP server main() entry point; port binding; config-dir serving |
| `test_services/test_nocloud_server_manager.py` | `services/nocloud_server/manager.py` | Nocloud server manager tests |

### Models Layer (3 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `models/test_vm.py` | `models/vm.py` | VMStatus (7 values), VMConfig, VMInstance field validation |
| `models/test_cloud_init.py` | `models/cloud_init.py` | CloudInitMode (4 values), CloudInitStatus (4 values), CloudInitConfig |
| `models/test_vm_config.py` | `models/vm_config.py` | VM config model tests |

### Root & Security (8 files)

| Test file | Source | Notes |
|-----------|--------|-------|
| `test_vm_config_file.py` | `models/vm_config_file.py` | `--import-config` / `--output-config` |
| `test_security.py` | Cross-cutting | checksum handling, privilege escalation boundaries |
| `test_constants.py` | `constants.py` | Redundant constants validation |
| `test_image.py` | `core/image.py` | Redundant image management |
| `test_kernel.py` | `core/kernel.py` | Redundant kernel management |
| `test_network.py` | `core/network.py` | Redundant network management |
| `test_progress.py` | `utils/progress.py` | Progress reporting |
| `test_validation.py` | `utils/validation.py` | Redundant validation |

## NOTES

- **Two kernel test files coexist**: `test_kernel.py` (full legacy coverage) + `test_kernel_new.py` (new features). Do not merge or delete either.
- **VMManager mocking**: Always mock both `get_by_name()` and `find_by_id_prefix()` together — `vm rm` tries ID prefix first, then falls back to name.
- **`test_security.py`**: Not tied to a single source file — validates security properties across modules.
- **`conftest.py`** (277 lines) — provides VM fixtures (`sample_vm`, `running_vm`, `stopped_vm`, `error_vm`), network fixtures, key fixtures, and subprocess mock fixtures; autouse isolation via parent `tests/conftest.py`.

