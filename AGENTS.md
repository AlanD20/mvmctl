# mvmctl

**Scope:** Production-grade Go CLI for managing Firecracker microVMs.
**Stack:** Go 1.26.3, Cobra (CLI), sqlx (SQLite), slog (logging).
**Entry:** `cmd/mvm/main.go` -> `app.Initialize()` -> `cli.NewRootCmd(op)` -> `ExecuteContext()`.

## Where to find context

This is the **only** AGENTS.md in the project. Per-folder AGENTS.md files have been removed.

1. **`CONTEXT.md`** — Domain language, conventions, patterns, and architecture rules. Start here for every task.
2. **`docs/adr/`** — Architecture Decision Records for hard-to-reverse decisions made with real trade-offs.
3. **`docs/STANDARDS.md`** — Go coding standards, conventions, and architectural rules.

Individual agent instructions live in `.opencode/agent/`:
- `architect.md` — Planner, analyzer, delegator. NEVER writes code.
- `engineer.md` — Go engineer. Writes and refactors Go code following idiomatic patterns. Handles porting and post-port refinement.
- `qa-engineer.md` — QA engineer. Owns system tests, coverage audit, and release verification. Never writes production code.

## Agent boundaries

- **`engineer` agent**: Go engineer — handles all Go source code (`cmd/`, `internal/`, `pkg/`, `go.mod`, etc.).
- **`qa-engineer` agent**: QA engineer — owns all test and release processes. Never writes production Go code.
- **`architect` agent**: Plans, analyzes, delegates. NEVER writes code. OWNS all documentation (CONTEXT.md, AGENTS.md, docs/, .opencode/). May spawn `explore` for research.

## CI commands

```bash
go build ./...
go vet ./...
go test ./...
```

## Critical rules (violation = critical failure)

- Core domains NEVER import other core/* packages. Only `internal/lib/model/` is shared across domains.
- Controller = state management per entity (start/stop/pause/resume/snapshot). No remove(), no create().
- Service does NOT validate caller input. Caller validates, receiver trusts.
- ALL subprocess calls through `system.RunCmdOpts` / `system.RunCmd` — no raw `os/exec`. Documented exceptions (see CONTEXT.md "Subprocess invocation"): Firecracker spawn (pass_fds), kernel build (log streaming), tar-pipe in cp (two-child pipe chain), and service subprocesses (loopmount provisioner, console relay, nocloudnet server).
- Context propagation: every repository method, every infrastructure function with side effects takes `ctx context.Context` as its first parameter.
- The API layer (`pkg/api/`) is the SOLE orchestrator of multiple core domains.
- Validation lives in API `pkg/api/inputs/` `*Input` / `*Request` structs, not in Service/Controller.
- `No reflect`, no `goto` — banned unless approved via ADR.
- Error handling uses `pkg/errs.DomainError` — single error type with Code + Class. No multiple error types.
