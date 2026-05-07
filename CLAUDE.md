# mvmctl — Claude Code Context

**Project:** Production-grade Python CLI for managing microVMs  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.  
**Stack:** Python 3.13, Click (LazyMVMGroup root), Typer (sub-apps), Rich, uv  
**CLI Entry:** `mvm` console script (defined in `pyproject.toml`)

> **Legacy bash scripts** are preserved in `legacy/` for reference.

### ⚠️ IMPORTANT RULES
1. Always verify your understanding against actual code before making changes
2. Run CI checks (`ruff check`, `mypy`, `pytest`) before finishing

### User Confirmation Required

**NEVER implement changes immediately without user confirmation.**

Before making any code changes:
1. Present your proposed approach to the user
2. Explain what you intend to do and why
3. Wait for explicit user approval
4. Only proceed with implementation after receiving confirmation

This applies to all edits, fixes, features, and refactoring. No exceptions.

---

## Quick Start

```bash
uv sync --group dev            # Install all deps
uv run pytest tests/ -x -q    # Run tests (stop at first failure)
uv run ruff check src/ && uv run mypy src/  # Lint + type check

# Build standalone binary (Nuitka — recommended)
uv sync --group dev --group build
python scripts/build_services.py                # Build everything
python scripts/build_services.py --mvm          # Build main mvm binary only
# Output: dist/mvm
```

## Project Structure

```
src/mvmctl/
├── main.py          # LazyMVMGroup (click.Group) — lazy-loads sub-apps via importlib
├── constants.py     # Single source of truth — CLI name, env prefix, all defaults
├── exceptions.py    # Custom exception hierarchy (MVMError → domain subclasses)
├── cli/             # Thin Typer command definitions (no business logic)
├── api/             # Stable public Python API; adds privilege checks before core
├── core/            # All business logic, subprocess, Firecracker interaction
├── models/          # Pure dataclasses (VMInstanceItem, FirecrackerConfig, ImageSpec, NetworkItem, etc.)
├── utils/           # Shared helpers: fs, _system, http, network, crypto, template, yaml, _validators, etc.
├── assets/          # Bundled YAML configs (images.yaml, kernels.yaml) + JSON templates (firecracker.template.json, cloud-init.template.yaml)
└── services/        # Runtime subprocess services (console_relay, nocloud_server, loopmount)
tests/               # 149 files across 4 subdirectories (111 unit + 17 integration + 14 system + 7 layer_compliance)
docs/                # API and release docs
legacy/              # Archived bash scripts (single-vm, multi-vm, assets)
pyproject.toml       # Build, ruff, mypy strict, pytest (80% branch coverage gate)
```

## Data Flow

```
User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

## Key Files

| Task | Location |
|------|----------|
| VM lifecycle | `src/mvmctl/core/vm/` (domain logic) + `src/mvmctl/api/vm_operations.py` (orchestration) |
| VM Firecracker API | `src/mvmctl/core/vm/_firecracker.py` |
| Network setup | `src/mvmctl/core/network/` (domain logic) + `src/mvmctl/api/network_operations.py` (orchestration) |
| Host init / privilege | `src/mvmctl/core/host/` (domain logic) + `src/mvmctl/api/host_operations.py` (orchestration) |
| Privilege checks | `src/mvmctl/core/host/_helper.py` (HostPrivilegeHelper) |
| Asset management | `src/mvmctl/core/_shared/_asset_manager.py` |
| CLI commands | `src/mvmctl/cli/` (thin Typer commands, registered in `main.py:_COMMAND_SPECS`) |
| Tests | `tests/integration/`, `tests/system/`, `tests/layer_compliance/` |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

## Configuration

- **Cache:** `~/.cache/mvmctl/` (`MVM_CACHE_DIR`)
- **Config:** `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`) — JSON, not YAML
- **Database:** `~/.cache/mvmctl/mvmdb.db` — SQLite DB (canonical asset state with `is_default` markers, VM records, network/lease state)
- **Env prefix:** `MVM_` (e.g. `MVM_CACHE_DIR`, `MVM_KERNEL`)
- **Priority:** constants.py fallbacks → SQLite DB (mvmdb.db) → config.json → MVM_* env vars → CLI flags

## Architecture Constraints

- **cli/** — arg parsing + output formatting ONLY; call `api/`; resolve defaults from `constants.py`; NO DB queries
- **api/** — privilege checks + DB resolution + sole orchestrator of core domains; stable public API with `__all__`; the ONLY layer that imports across core domains
- **core/** — isolated domain logic in subdirectories (vm/, network/, host/, etc.); no cross-domain imports; no defaults; raises typed exceptions
- **models/** — `@dataclass` only; no methods with side effects
- **utils/** — pure helpers with no domain knowledge

## Code Quality Gates (CI-enforced)

**ALL code changes MUST pass CI checks before completion.**

```bash
uv run ruff check src/          # Must be clean
uv run ruff format --check src/ # Must be clean
uv run mypy src/                # Strict mode — no type: ignore allowed
uv run pytest tests/ -q         # 80% branch coverage minimum
```

**If checks fail:**
- Fix linting/formatting issues with `uv run ruff check src/ --fix` and `uv run ruff format src/`
- Fix type errors with proper type annotations
- Fix failing tests — NEVER delete tests to make them pass

Tests must NOT require root, KVM, or real network. Mock all subprocess calls.

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

## Related Files

- `AGENTS.md` — Full architecture reference for AI agents
- `src/mvmctl/core/AGENTS.md` — Core module inventory
- `src/mvmctl/cli/AGENTS.md` — CLI wiring, Typer patterns
- `src/mvmctl/api/AGENTS.md` — API layer pattern
- `src/mvmctl/services/AGENTS.md` — Runtime services (console_relay, nocloud_server)
- `tests/AGENTS.md` — Test fixtures, mock conventions
