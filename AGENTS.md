# mvmctl

**Scope:** Production-grade Go CLI for managing Firecracker microVMs.
**Stack:** Go 1.26.3, Cobra (CLI), sqlx (SQLite), slog (logging).
**Entry:** `cmd/mvm/main.go` -> `app.Initialize()` -> `cli.NewRootCmd(op)` -> `ExecuteContext()`.

## Where to find context

This is the **only** AGENTS.md in the project. Per-folder AGENTS.md files have been removed.

1. **`CONTEXT.md`** — Domain language, conventions, patterns, and architecture rules. Start here for every task.
2. **`docs/adr/`** — Architecture Decision Records for hard-to-reverse decisions made with real trade-offs.
3. **`docs/STANDARDS.md`** — Go coding standards, conventions, and architectural rules.
4. **`docs/system-test-architecture.md`** — System test architecture, tier classification, fixture scoping, known-limitation patterns. The primary reference for writing or modifying system tests.

Individual agent instructions live in `.opencode/agent/`:
- `architect.md` — Planner, analyzer, delegator. NEVER writes code.
- `engineer.md` — Go engineer. Writes and refactors Go code following idiomatic patterns. Handles porting and post-port refinement.
- `qa-engineer.md` — QA engineer. Owns system tests, coverage audit, and release verification. Never writes production code.

## Agent boundaries

- **`engineer` agent**: Go engineer — handles all Go source code (`cmd/`, `internal/`, `pkg/`, `go.mod`, etc.). Owns L0 (pure function) and L1 (hermetic) Go tests — table-driven tests, in-memory repos, `FakeRunner`, `go test ./...`.
- **`qa-engineer` agent**: QA engineer — owns all test and release processes. Owns L2 (Python runner VM) e2e tests in `tests/e2e/`, coverage audit, and release verification. Never writes production Go code.
- **`architect` agent**: Plans, analyzes, delegates. NEVER writes code. OWNS all documentation (CONTEXT.md, AGENTS.md, docs/, .opencode/). May spawn `explore` for research.

## CI standards (mirrors `.github/workflows/ci.yml`)

Every agent MUST verify these pass before declaring a task complete.
The CI pipeline enforces them; deviating locally means the PR fails.

1. **Tidy** — `go mod tidy && git diff --exit-code` (no dirty go.mod/go.sum)
2. **Format** — `test -z "$(gofmt -l .)"` (gofmt compliance, entire tree)
3. **Line length** — `golines --max-len=120 --list-files ./internal/ ./pkg/ ./cmd/`
   (120-char limit on Go source)
4. **Generate** — `go generate ./internal/service/vsockagent/...` (embed placeholders)
5. **Vet** — `go vet ./...` (zero static-analysis warnings)
6. **Test** — `go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic`

```bash
go mod tidy && git diff --exit-code
test -z "$(gofmt -l .)"
golines --max-len=120 --list-files ./internal/ ./pkg/ ./cmd/ 2>&1 | grep . && echo "violations found" && exit 1 || true
go generate ./internal/service/vsockagent/...
go vet ./...
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
```

## Plan approval protocol

The architect MUST verify these before approving any implementation plan:

1. **Patterns cross-check** — Read the EXISTING interface/pattern the proposal touches. Does the new method match the naming and shape of existing methods? (e.g., `Backend.SetupSSH(ctx, user, keys)` → new method should be `InjectVsockAgent(ctx, binary, port, token)`, not `ApplyOps(ctx, ops)`)
2. **Analogous precedent** — Find 2-3 existing code paths that solve the same KIND of problem. If the existing Backend methods are all named typed methods, a generic `[]Operation` passthrough is a red flag.
3. **Layer check** — Does the proposed code belong in the layer it's placed in? (e.g., CID retry loops belong in `internal/core/*/service.go`, not in `pkg/api/*.go`)
4. **Reject generic extension points** — "This is extensible for future use" is a smell. Prefer typed named methods that describe exactly what they do. Add new methods when new needs arise, not generic hooks.
5. **Architect reads the diff** — After every implementation phase, review the actual diff for the key changes (interfaces, structs, imports). Do not rely solely on subagent reviews.

## Critical rules (violation = critical failure)

- Core domains NEVER import other core/* packages. Only `internal/lib/model/` is shared across domains.
- Controller = state management per entity (start/stop/pause/resume/snapshot). No remove(), no create().
- Service does NOT validate caller input. Caller validates, receiver trusts.
- ALL subprocess calls through `system.RunCmdOpts` / `system.RunCmd` — no raw `os/exec`. Documented exceptions (see CONTEXT.md "Subprocess invocation"): Firecracker spawn (pass_fds), kernel build (log streaming), tar-pipe in cp (two-child pipe chain), and service subprocesses (loopmount provisioner, console relay, nocloudnet server).
- Context propagation: every repository method, every infrastructure function with side effects takes `ctx context.Context` as its first parameter.
- The API layer (`pkg/api/`) is the SOLE orchestrator of multiple core domains.
- Validation lives in API `pkg/api/inputs/` `*Input` structs with `Validate()`/`Resolve()`, not in Service/Controller.
- `No reflect`, no `goto` — banned unless approved via ADR.
- Error handling uses `pkg/errs.DomainError` — single error type with Code + Class. No multiple error types.
