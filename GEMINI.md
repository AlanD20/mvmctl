# mvmctl

## Project Overview
`mvmctl` (`mvm`) is a production-grade Python CLI application for managing the complete lifecycle of microVMs on Linux.

**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.

It handles everything from downloading official kernels and root filesystem images to setting up bridge networking, creating/destroying VMs, SSH access, log streaming, and cleanup. Detailed binary and system requirements are documented in [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md).

**Tech Stack:**
- **Language:** Python 3.13+
- **CLI Framework:** Click (LazyMVMGroup root group) with Typer for sub-commands, Rich
- **Package Management:** `uv`
- **Testing & Linting:** `pytest`, `ruff`, `mypy`

### IMPORTANT RULES
1. Always verify your understanding against actual code before making changes
2. Run CI checks before finishing: ruff, mypy, pytest

### User Confirmation Required

**NEVER implement changes immediately without user confirmation.**

Before making any code changes:
1. Present your proposed approach to the user
2. Explain what you intend to do and why
3. Wait for explicit user approval
4. Only proceed with implementation after receiving confirmation

This applies to all edits, fixes, features, and refactoring. No exceptions.

---

## Architecture
The project strictly adheres to a three-layer architecture: **CLI → API → Core**. Data flows sequentially: `User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/`. Runtime services in `services/` are spawned as subprocesses for console relay and cloud-init HTTP serving.

- **`cli/`**: Command definitions, argument parsing, and formatting output. No business logic. No database queries. Resolves defaults from `constants.py`.
- **`api/`**: Stable public Python API boundary. Performs privilege checks, resolves DB-backed defaults when CLI passes `None`, and orchestrates multiple core domains (the ONLY layer that imports across domains).
- **`core/`**: Isolated domain logic in subdirectories (e.g., `vm/`, `network/`, `host/`). Data-heavy domains follow a Controller, Service, Repository, and Resolver pattern. Simpler domains (cache, cloudinit, config, console, logs) may have fewer files. No cross-domain imports. No defaults. Returns data or raises typed exceptions (`MVMError`).
- **`models/`**: Pure `@dataclass` objects containing domain data (e.g., `VMInstanceItem`, `FirecrackerConfig`, `ImageSpec`). No side effects.
- **`utils/`**: Shared helpers (_disk, _io, _lazy_import, _system, _validators, auditlog, cli, common, crypto, fs, http, network, operation_utils, progress, template, timinglog, version, yaml) with no domain knowledge.
- **`db/`**: SQLite database with migration system (`migrations/001_initial_schema.sql`) for persistent state across 13 tables: images, kernels, binaries, volumes, networks, network_leases, vm_instances, host_state, host_state_changes, iptables_rules, nftables_rules, ssh_keys, user_settings.
- **`services/`**: Runtime subprocess services — `console_relay/` (PTY-to-vsock bridge), `nocloud_server/` (HTTP cloud-init datasource), and `loopmount/` (rootfs provisioning binary).

## Building and Running
The project uses `uv` for dependency management.

**Setup Development Environment:**
```bash
uv sync --group dev
```

**Run the CLI (Development):**
```bash
uv run mvm --help
```

**Building a Standalone Binary:**

Nuitka (Compiled binary — Recommended for releases):
```bash
uv sync --group dev --group build
python scripts/build_services.py
./dist/mvm --version
```

## Testing and Quality Gates
**ALL code changes MUST pass CI checks before completion.**

All checks are enforced in CI and must pass before opening a PR.

```bash
# Tests (Must mock all subprocess calls; no root/KVM/real network required; 80% branch coverage minimum)
uv run scripts/run_tests.py --pytest-extra "--cov=src/mvmctl -n auto --cov-fail-under=80"

# Linting & Formatting
uv run ruff check src/
uv run ruff format --check src/

# Type Checking (Strict Mode - no `type: ignore` allowed)
uv run mypy src/
```

**If checks fail:**
- Fix linting/formatting issues with `uv run ruff check src/ --fix` and `uv run ruff format src/`
- Fix type errors with proper type annotations
- Fix failing tests — NEVER delete tests to make them pass

*Note: A minimum of 80% branch coverage is strictly enforced.*

## Commit Authorship

See the Commit Authorship section in CLAUDE.md for details on the commit convention.

## Development Conventions
- **Defaults:** Never hardcode defaults in function parameters. All defaults live in `constants.py` under the `OVERRIDABLE_DEFAULTS` dict with category keys (e.g., `defaults.vm`, `defaults.network`). User-facing asset defaults are resolved from the SQLite database (`is_default` markers) and `MVM_*` environment variables.
- **Privilege Model:** `mvm host init` is run once to set up the host (mvm group, sudoers). Normal commands run rootless and validate privileges via the `mvm` group.
- **Asset ID System:** Downloaded or imported assets (images, kernels, networks, binaries, volumes) use a 64-character SHA256 hash as their persistent ID. VM IDs use a 32-character truncated SHA256 hash (to keep filesystem paths under the Unix domain socket path limit). The CLI displays and accepts the first 12 characters as a prefix.
- **Error Handling:** Avoid bare `except:` blocks. Catch specific domain exceptions derived from `exceptions.py`.
- **Error Code Format:** Every exception carries an optional `code: str | None` string for fine-grained programmatic branching. Format is dot-separated with domain prefix (e.g., `network.subnet.overlap`, `vm.create.binary_not_found`).
- **API Result Types:** The API layer returns `OperationResult[T]` (single result with status/code/message/item), `BatchResult[T]` (collection of results), or `NeedsInteraction` (requires user action like sudo prompt) for the CLI/TUI to consume.
- **AGENTS.md:** The only `AGENTS.md` file is at the project root (`AGENTS.md`). Per-folder AGENTS.md files in active source directories have been removed (the `legacy/` directory may still contain archived copies) — they caused agents to skip the root file. Use `CONTEXT.md` and `docs/adr/` for architecture context.
