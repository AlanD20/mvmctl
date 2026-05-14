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
uv run scripts/run_tests.py --ci
```

## Running a specific test class with pytest

Use `::` syntax to target a single class instead of running the whole file:

```
pytest tests/path/to/test_foo.py::TestBar
```

Concrete examples:
- `pytest tests/unit/test_main.py::TestMainHelp`
- `pytest tests/unit/test_main.py::TestMainSubcommands`
- `pytest tests/unit/models/test_vm.py::TestVMStatus`
- `pytest tests/unit/cli/test_network.py::TestNetworkLs`

Drill further into a specific method: `pytest tests/unit/test_main.py::TestMainHelp::test_help`

Key points:
- The `::` separator drills into file → class (and optionally → method).
- pytest does NOT default to `-x` (stop on first failure), so all tests in the class run.
- Add `-v` for verbose output: `pytest -v tests/unit/test_main.py::TestMainHelp`
- Add `-x` to stop on first failure within the class.
- Combine with `-k` for additional filtering if needed.

## SUDO & UV PATH

- **Always use `uv`** (resolved via PATH). Never use bare `uv` with sudo in an unactivated shell.
- For one-time setup via uv (requires sudo):
  `sudo uv run mvm host init`
- For one-time setup via built binary:
  `sudo ~/.local/bin/mvm host init`
- The built binary **MUST** be copied to `~/.local/bin/mvm` — that is the only path
  where `sudo` will work with the binary
- For running system tests: `sg mvm -c 'uv run scripts/run_tests.py --system --domain <domain>'`
- For running a single test file: `sg mvm -c 'uv run scripts/run_tests.py --system --test tests/system/<domain>/test_xxx.py'`
- For running mvm commands: `sg mvm -c 'uv run mvm <command>'`
- DO NOT use sudo for regular mvm commands (vm create, network create, etc.)
- Only use sudo when actually needed: `host init`, `host clean`, `host reset`
- `sudo` is allowed for: `mvm init`, `mvm host init`, `mvm host clean`, `mvm host reset`
- For verbose or debug output, use the `--verbose` or `--debug` CLI flags instead of `MVM_LOG_LEVEL=DEBUG`:
  ```bash
  sg mvm -c 'uv run mvm --debug vm create --name test-vm'
  sg mvm -c 'uv run mvm --verbose vm ls'
  ```
  The `--debug` flag sets log level to DEBUG; `--verbose` sets it to INFO. Both are available on every command via the root `mvm` group.

## Critical rules (violation = critical failure)

- Core domains NEVER import from other core domains. Only `_shared` is allowed.
- Controller = state management per entity (start/stop/pause/resume). No remove(), no create().
- Service does NOT validate caller input. Caller validates, receiver trusts.
- ALL subprocess calls through `run_cmd()` / `stream_cmd()` — no raw `subprocess.run()`.
- Lazy imports (PEP 562) in ALL `__init__.py` — no eager imports.
- `from __future__ import annotations` in every `.py` file under `src/mvmctl/`.
- The API layer is the SOLE orchestrator of multiple core domains.
- Validation lives in API `*Input` / `*Request` classes, not in Service/Controller.
