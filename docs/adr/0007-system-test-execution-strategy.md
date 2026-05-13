# System Test Execution Strategy — Per-File, Never Batch

System tests (`tests/system/`) are black-box CLI subprocess tests that operate against real system infrastructure — real kernels, images, bridges, iptables rules, and SQLite state. They are the primary release gate: a domain is not production-ready until its system tests pass on real hardware.

## Context

System tests are expensive and stateful. A single test file can take 30s–10m depending on the assets it needs (image downloads, kernel builds, VM boot time). Running all files in a single `pytest tests/system/` invocation causes:

1. **Session-scoped state sharing**: `prepare_system_env` (session-scoped per subdirectory) pulls assets once per session. If one file's test modifies shared state (default image, default network, cached binary), subsequent files inherit polluted state.
2. **Cross-file state pollution**: A VM left running by `test_vm_lifecycle.py` causes `test_host.py::TestHostCleanSafety` to fail (host clean is blocked by running VMs). A removed binary in one file causes `test_bin.py` tests in the same session to fail.
3. **Long wall-clock time**: A single session running all 19 system test files takes 20–45 minutes. A single failure at minute 30 requires re-running the entire session. This is unacceptable for CI iteration speed.

## Decision

System tests MUST be executed **per-file**, never as a single batch:

```bash
# Correct: per-file
pytest tests/system/network/test_network.py -n 0
pytest tests/system/vm/test_vm_lifecycle.py -n 0

# WRONG: undefined behavior, state pollution
pytest tests/system/
```

Running individual test classes (not full files) within a file is also safe, provided the class is self-contained with its own fixture setup/teardown.

## Additional Rules

### No conftest sudo
The conftest must NEVER call `sudo` directly. The mvm application handles privilege escalation internally via `run_cmd()`/`stream_cmd()`. The `_verify_system_test_iptables` fixture was removed (per this ADR) because it bypassed this pattern by calling `sudo iptables ...` directly. System tests that need privileged operations (VM creation, kernel build, host init) must fail naturally through the application's own error handling, not through a separate conftest check.

### Passwordless sudo required for VM tests
System tests that create VMs, build kernels, or run `host clean`/`host reset` require passwordless sudo configured via mvm group membership (set up by `sudo mvm host init`). Tests that do not need privileged operations (bin, config, cache, keys, logs, network, ssh, etc.) work without it.

### Heavy asset downloads are session-level
Images, kernels, and binaries are pulled once per test file via the session fixture `prepare_system_env` (defined in each subdirectory's `conftest.py`). Each file incurs the download cost if the assets aren't already cached. This is intentional — it keeps each file independently runnable and avoids cross-file state dependencies.

## Status

Accepted.

## Consequences

- **CI pipeline**: Must run system test files individually, collecting results from each. A dedicated CI step or orchestration script is required — a single `pytest tests/system/` call is forbidden. The `Taskfile.yml` implements this via phased execution (domain tests parallel, VM tests serial/parallel, cache cleanup last).
- **Local development**: Developers run individual files relevant to their change. Running all files is done infrequently (pre-release).
- **Faster feedback loop**: A single file failure doesn't block other files. Developers can fix one file and re-run it without re-running everything.
- **Redundant downloads**: Each file independently verifies that required assets exist, with a small overhead (~0.5s per file for `kernel ls --json` + `image ls --json` + `bin ls --json`). This is acceptable for isolation.
