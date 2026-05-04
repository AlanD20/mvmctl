# tests/system/ — System Test Suite

**Scope:** Black-box CLI integration tests via subprocess
**Status:** Requires KVM, mvm group membership, and real hardware
**Rule:** NO imports from `mvmctl.*` — tests invoke `mvm` as a user would

## STRUCTURE

```
tests/system/
├── conftest.py              # Fixtures: mvm_binary, created_vm, created_network, created_key
├── test_bin.py              # Firecracker binary tests
├── test_cache.py            # Cache management tests
├── test_config.py          # Configuration tests
├── test_console.py         # Console access tests
├── test_full_journeys.py    # End-to-end journey tests
├── test_host.py             # Host configuration tests
├── test_images.py           # Image management tests
├── test_init.py            # Init tests
├── test_kernel.py           # Kernel tests
├── test_keys.py             # SSH key tests
├── test_logs.py            # Log tests
├── test_network.py          # Network CRUD tests
├── test_ssh.py             # SSH config tests
├── test_vm_lifecycle.py     # VM lifecycle tests
└── __init__.py
```

## MARKERS

| Marker | Purpose |
|--------|---------|
| `system` | Every test in this directory |
| `requires_kvm` | Tests that create actual VMs |
| `requires_network` | Tests that create networks |
| `slow` | Tests taking >30 seconds |
| `shared_vm` | Uses module-scoped lifecycle_vm fixture |
| `independent_vm` | Creates its own VM per test |

## RUNNING

```bash
# All system tests (requires KVM + mvm group)
uv run pytest tests/system/ -v

# Fast subset only (no downloads, no KVM)
uv run pytest tests/system/ -m "system and not slow and not requires_kvm"

# Default CI (system tests excluded automatically)
uv run pytest tests/
```

## PREREQUISITES

1. KVM available: /dev/kvm must exist
2. mvm group: user must be in mvm group (run `sudo mvm host init`)
3. Images pre-cached: `mvm image fetch alpine-3.21`
4. Default kernel set: `mvm kernel fetch --type firecracker`

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| `from mvmctl import anything` | `subprocess.run(["mvm", ...])` only |
| Hardcoded VM names | `unique_vm_name` fixture |
| No cleanup on failure | Use `created_vm`/`created_network`/`created_key` fixtures |
| Real env vars in test process | `_restore_real_dirs` autouse fixture handles this |

## NOTES

- **14 test files**: Covering bin, cache, config, console, full_journeys, host, images, init, kernel, keys, logs, network, ssh, and VM lifecycle
- Requires real system environment (KVM, mvm group, network privileges)
- Completely black-box testing via CLI binary calls
