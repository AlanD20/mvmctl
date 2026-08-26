# mvmctl

**Scope:** Production-grade Go CLI for managing Firecracker microVMs.
**Stack:** Go 1.26.3, Cobra, sqlx with SQLite, and slog.
**Entry:** `cmd/mvm/main.go` -> `app.Initialize()` -> `cli.NewRootCmd(op)` -> `ExecuteContext()`.

## Instruction order

This file is the source of truth for active project work. Read only the references that the task needs:

1. Read `CONTEXT.md` for domain language, architecture, state, privilege, or lifecycle work.
2. Read the applicable record in `docs/adr/` before changing a recorded decision.
3. Read `docs/STANDARDS.md` before writing or reviewing Go code.
4. Read `docs/development/HOW_AGENTS_WRITE_UNIT_TESTS.md` before writing or changing Go tests.
5. Read `docs/system-test-architecture.md` and `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` before writing or
   changing system tests.
6. Read `docs/development/HOW_TO_RUN_SYSTEM_TESTS.md` before executing system tests or release qualification.

The `AGENTS.md` files under `legacy/` describe archived implementations. They apply only while inspecting that archived
code and never override this file for active work.

## Roles and ownership

- **`engineer`** owns production Go code, L0 and L1 Go tests, and production or build tooling. L0 and L1 tests include
  table-driven tests, in-memory repositories, `FakeRunner`, and `go test ./...`.
- **`qa-engineer`** owns Python system tests, `scripts/run-system-tests.py`, its Python tests, coverage audits, and
  release verification. The OpenCode configuration key for this role is `qa`. QA may run and diagnose Go tests but
  does not write production Go code.
- **`architect`** owns plans, ADRs, `CONTEXT.md`, `AGENTS.md`, `.opencode/`, and documentation. The architect analyzes,
  delegates, and reviews diffs but never writes production code, tests, or executable scripts. The architect may spawn
  `explore` for read-only research.

Ownership follows purpose. A Python system-test runner belongs to QA. A production or build script belongs to the
engineer. A Markdown file belongs to the architect.

## Approval and workspace safety

- Treat questions as read-only requests. Start implementation only after the user explicitly approves it.
- Preserve unrelated staged, unstaged, and untracked files. Never clean, stash, reset, restore, or overwrite them to
  make a check pass.
- Build candidates under `dist/`, the worktree, or a task-specific temporary directory.
- Never install or copy a candidate over `/usr/local/bin/mvm` or `~/.local/bin/mvm` unless the current task explicitly
  authorizes host installation. Never change sudoers as an incidental test step.
- Keep the installed outer controller separate from the release candidate. Install and exercise candidates inside
  disposable runner VMs as described by the system-test guides.
- Run Tier 3 or `--host-direct` only when the current task explicitly authorizes mutation of a clean qualification host.
- Set `MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror` for asset-consuming test workflows. Do not download an asset that the
  mirror already contains.

## Pstack use

Pstack is a workflow aid. This file, `CONTEXT.md`, and accepted ADRs remain authoritative.

- Use focused pstack skills such as `how`, `why`, `interrogate`, `technical-writing`, and `unslop` when their trigger
  matches. Use `tdd` only when the user explicitly asks for test-driven development or a failing test first.
- The pstack `architect` skill is a design review, not the project `architect` role. When the project architect invokes
  it, stop after the design and approval checkpoint. Delegate implementation to the owning engineer after approval.
- Use `swarm` for read-only audits or work divided by exclusive file ownership. Use one writer at a time in a shared
  worktree. Give concurrent writers separate worktrees and review every diff before integration.
- Keep `tasks/plan.md`, `tasks/todo.md`, ADRs, and commits as the project record. Do not create a second pstack
  orchestration ledger for ordinary release work.
- Use only model identifiers available in the current runtime. On Codex, inherit the parent model when no pstack model
  map exists. Never pass a `claude-*` model identifier to Codex.
- Use pstack Orchestrate only when the user explicitly requests a multi-day, multi-PR program that needs it.

## Verification

Run checks in proportion to the changed surface. Go changes require focused format, vet, and test checks before their
commit. System-test changes require collection plus the smallest affected orchestrator or runner test. Documentation
changes require targeted searches, link and path checks, and a diff review.

The integration owner runs the complete CI sequence on a clean integration worktree before declaring the PR or release
ready. An agent working beside unrelated changes must not alter those changes to make `git diff --exit-code` pass.

The CI sequence mirrors `.github/workflows/ci.yml`:

```bash
go mod tidy && git diff --exit-code
test -z "$(gofmt -l .)"
golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/ 2>&1 | grep . && echo "violations found" && exit 1 || true
go generate ./internal/service/agent/...
go vet ./...
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
```

## Plan approval protocol

The architect verifies these points before approving an implementation plan:

1. **Patterns cross-check.** Read the existing interface or pattern that the proposal changes. Match its naming and
   shape. For example, extend typed `Backend.SetupSSH(ctx, user, keys)` methods with another typed method, not a generic
   operation passthrough.
2. **Analogous precedent.** Find two or three existing paths that solve the same kind of problem.
3. **Layer check.** Place the behavior in the layer that owns it. For example, CID retry loops belong in
   `internal/core/*/service.go`, not `pkg/api/*.go`.
4. **Reject generic extension points.** Add a named method for an observed need. Do not add hooks for hypothetical use.
5. **Architect reads the diff.** Review changed interfaces, structs, imports, and effect boundaries directly. Do not
   rely only on an implementation report.

## Critical architecture rules

- Core domains never import another `internal/core/*` domain. Only `internal/lib/model/` is shared across domains.
- A Controller manages state for one existing entity. It does not create or remove entities.
- A Service trusts validated caller input. It does not repeat API input validation.
- All subprocess calls use `system.DefaultRunner.Run()` or `system.DefaultRunner.Stream()` with `system.RunCmdOpts`.
  `CONTEXT.md` documents the narrow exceptions for Jailer, SSH probes, loopmount, vsock sessions, archive pipes, and the
  runner or spawn boundary.
- Every repository method and every side-effecting infrastructure function takes `context.Context` first.
- `pkg/api/` is the only layer that orchestrates multiple core domains.
- Public validation lives in `pkg/api/inputs/` `*Input` types with `Validate()` or `Resolve()`.
- `reflect` and `goto` require an approved ADR.
- Errors use the single `pkg/errs.DomainError` type with a code and class.
