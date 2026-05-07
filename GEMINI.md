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

### ⚠️ IMPORTANT RULES
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
- **`core/`**: Isolated domain logic in subdirectories (e.g., `vm/`, `network/`, `host/`). Each domain has Controller, Service, Repository, and Resolver modules. No cross-domain imports. No defaults. Returns data or raises typed exceptions (`MVMError`).
- **`models/`**: Pure `@dataclass` objects containing domain data (e.g., `VMInstanceItem`, `FirecrackerConfig`, `ImageSpec`). No side effects.
- **`utils/`**: Shared helpers (fs, _system, http, network, crypto, template, yaml, _validators) with no domain knowledge.
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
python scripts/build_services.py --mvm
./dist/mvm --version
```

## Testing and Quality Gates
**ALL code changes MUST pass CI checks before completion.**

All checks are enforced in CI and must pass before opening a PR.

```bash
# Tests (Must mock all subprocess calls; no root/KVM/real network required; 80% branch coverage minimum)
uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80

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

## Commit Authorship (MANDATORY)

**DO NOT add `Co-authored-by` trailers unless the co-author actually contributed to that specific change.**

- Only add co-authors when they **directly contributed code, review, or significant input** to that specific commit
- Do NOT add co-authors as a blanket practice on every commit
- Do NOT add co-authors just because they are part of the project or team
- When in doubt, **omit the co-author trailer entirely**

**Correct:**
```
feat: add new VM snapshot feature

Co-authored-by: Alice <alice@example.com>  # Alice wrote part of this feature
```

**Incorrect:**
```
style: fix formatting

Co-authored-by: Adam <adam@example.com>  # WRONG - no contribution to this change
```

## Development Conventions
- **Defaults:** Never hardcode defaults in function parameters. All defaults live in `constants.py` under the `OVERRIDABLE_DEFAULTS` dict with category keys (e.g., `defaults.vm`, `defaults.network`). User-facing asset defaults are resolved from the SQLite database (`is_default` markers) and `MVM_*` environment variables.
- **Privilege Model:** `mvm host init` is run once to set up the host (mvm group, sudoers). Normal commands run rootless and validate privileges via the `mvm` group.
- **Asset ID System:** Downloaded or imported assets (images, kernels) use a full 64-character SHA256 hash as their persistent ID. The CLI displays and accepts the first 6 characters as a prefix.
- **Error Handling:** Avoid bare `except:` blocks. Catch specific domain exceptions derived from `exceptions.py`.
