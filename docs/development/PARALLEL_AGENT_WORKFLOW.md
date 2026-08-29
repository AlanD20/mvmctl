# Parallel Agent Workflow

Use this protocol only when the user authorizes parallel agents or the active task is explicitly divided across
agents. It governs delegation and integration; `AGENTS.md`, `CONTEXT.md`, accepted ADRs, `tasks/plan.md`,
`tasks/todo.md`, commits, and live repository state remain authoritative.

## Head-agent loop

1. Inspect the main worktree, branch, recent commits, active plan, dirty files, worktrees, and live agent handles.
   Completion criterion: every proposed assignment has a current dependency, an exclusive owner, and a concrete
   output that advances the active goal.
2. Assign independent work only. Use one writer in a shared worktree or one isolated worktree per concurrent writer.
   Read-only reviewers may inspect a stable commit while a writer owns later uncommitted changes. Completion criterion:
   no two writers own overlapping files or the same effect boundary.
3. Give each agent a bounded work packet containing the worktree, fixed commit or scope, required project references,
   exact deliverable, prohibited side effects, verification gate, and stop condition. Completion criterion: the packet
   can be accepted or rejected from repository evidence without relying on the agent's narrative.
4. Do useful local integration work while agents run. Check live state after each bounded local step, on every agent
   message, and before opening another assignment. During a long command, retain and poll its real process handle;
   do not create replacement work from a timeout alone.
5. Review ready commits, tests first. Inspect the full diff, interfaces, imports, effect boundaries, failure paths,
   ownership transfer, and cleanup. Run one focused evidence pass. Completion criterion: the commit is approved or has
   one severity-ranked correction request with exact evidence.
6. Integrate one approved slice at a time. Re-run focused checks on the integration branch, then record user-visible
   behavior and remaining limitations in a separate documentation commit. Completion criterion: the main branch is a
   working save point and unrelated user changes are unchanged.

## Usefulness monitor

An agent is useful only when its live handle and repository evidence agree. Acceptable evidence is a new scoped diff,
commit, test result, reproduced blocker, or decision-changing review finding.

- Inspect the live agent tree and its assigned worktree together. Agent listings can omit a still-running worker.
  Before reassigning an existing worktree, explicitly stop or interrupt every previously named owner and confirm its
  prior status; when that cannot be confirmed, give the replacement a fresh worktree.
- A writer with no new evidence and no confirmed live command receives one concise status request. If the next check
  is still unchanged, stop it and reuse the slot for a non-overlapping dependency.
- Stop an agent immediately when it duplicates another owner, edits outside its assignment, repeats an unchanged
  command, broadens scope, or continues reviewing after a release blocker is established.
- Do not spawn reviewers for a moving uncommitted target. Review a fixed commit; send one correction round to its
  owner; perform final sign-off after the correction.
- Prefer three productive streams over a larger idle swarm. Add a stream only when its output can integrate without
  waiting on another active writer.

## Circuit breaker

Each review gets one static pass and one focused verification pass. Stop at the first Critical blocker. The owner gets
one bounded correction round; a repeated blocker returns the slice to dependency planning instead of another review
loop. Broad CI waits for a coherent integration checkpoint.

Never rerun an unchanged failing command. Change the hypothesis, command, code, or environment first. A quota,
missing generated asset, sandbox denial, or expired observation is evidence to classify, not a reason to spin.

## Integration sign-off

The head agent signs off; agent self-approval is insufficient. Before cherry-picking or committing, prove:

- the diff matches the assigned requirement and accepted ADRs;
- tests exercise the claimed behavior and important failure paths;
- focused format, vet, and test gates pass for Go changes;
- privileged effects consume typed, validated authority and clean up partial progress;
- the commit remains a working save point rather than activating a half-wired runtime path;
- no user-owned staged, unstaged, or untracked file is included;
- documentation states what is active, what is only a foundation, and what remains blocked.

Full CI and system qualification remain release gates; a focused sign-off never substitutes for them.

## Compaction recovery

After context compaction or leadership handoff:

1. Re-read `AGENTS.md` and this file.
2. Read the active entries in `tasks/plan.md` and `tasks/todo.md`, then inspect the current branch, log, status, and
   worktree list.
3. Query live agent handles and reconcile them with every named writer in the compacted summary. An absent handle is
   inconclusive: explicitly stop or interrupt the previous owner before reusing its worktree.
4. For each remaining live writer, inspect its current diff or commit and confirm exclusive ownership before sending
   follow-up work. For each running command, resume its existing handle.
5. State the next integration checkpoint and continue from repository evidence. Do not recreate completed work or
   start a broad audit merely because conversational detail was compacted.

Keep prompts and reports evidence-dense: paths, commit hashes, commands, findings, and remaining dependency. Avoid
duplicated context, speculative fan-out, repeated summaries, and review narration that does not change the next action.
