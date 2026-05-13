# mvmctl

**Scope:** Production-grade Python CLI for managing Firecracker microVMs.
**Stack:** Python 3.13, Click (root), Typer (sub-apps), Rich, uv.
**Entry:** `mvm` console script → `main.py:LazyMVMGroup` (Click group, NOT Typer root app).

## Where to find context

This is the **only** AGENTS.md in the project. Per-folder AGENTS.md files have been removed — they caused agents to skip this file and miss the two primary context sources:

1. **`CONTEXT.md`** — Domain language, conventions, patterns, and architecture rules. Start here for every task.
2. **`docs/adr/`** — Architecture Decision Records for hard-to-reverse decisions made with real trade-offs.

Individual agent instructions live in `.opencode/agent/`:
- `architect.md` — Planner, analyzer, delegator. NEVER writes code.
- `engineer.md` — Production code engineer. Handles everything except `tests/`.
- `qa-engineer.md` — Test owner. Handles only `tests/`.

## Agent boundaries (ABSOLUTE)

- **`engineer` agent**: Handles **everything except `tests/`** — src/mvmctl/, scripts/, benchmarks/, docs/, stubs/, pyproject.toml, etc.
- **`qa-engineer` agent**: Handles **only `tests/`** — unit, integration, system, layer_compliance.
- **`architect` agent**: Plans, analyzes, delegates. NEVER writes code. May spawn `explore` for research.

## CI commands (agents MUST run these before finishing)

```bash
uv sync --group dev
uv run ruff check src/ && uv run ruff format --check src/
uv run mypy src/
uv run pytest tests/ -q -n auto -x
```

## Critical rules (violation = critical failure)

- Core domains NEVER import from other core domains. Only `_shared` is allowed.
- Controller = state management per entity (start/stop/pause/resume). No remove(), no create().
- Service does NOT validate caller input. Caller validates, receiver trusts.
- ALL subprocess calls through `run_cmd()` / `stream_cmd()` — no raw `subprocess.run()`.
- Lazy imports (PEP 562) in ALL `__init__.py` — no eager imports.
- `from __future__ import annotations` in every `.py` file under `src/mvmctl/`.
- The API layer is the SOLE orchestrator of multiple core domains.
- Validation lives in API `*Input` / `*Request` classes, not in Service/Controller.
