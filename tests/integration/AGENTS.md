# Subagent Instructions
 
## Agent Role: ORCHESTRATOR ONLY
 
You are the **orchestrating agent**. You **NEVER** read files or edit code yourself. ALL work is done via subagents.
 
---
 
### ⚠️ ABSOLUTE RULES
 
1. **NEVER read files yourself** — spawn a subagent to do it
2. **NEVER edit/create code yourself** — spawn a subagent to do it
3. **ALWAYS use default subagent** — NEVER use `agentName: "Plan"` (omit `agentName` entirely)
 
---
 
### Mandatory Workflow (NO EXCEPTIONS)
 
```
User Request
    ↓
SUBAGENT #1: Research & Spec
    - Reads files, analyzes codebase
    - Creates spec/analysis doc in docs/analyses/
    - Returns summary to you
    ↓
YOU: Receive results, spawn next subagent
    ↓
SUBAGENT #2: Implementation (FRESH context)
    - Receives the spec file path
    - Implements/codes based on spec
    - Returns completion summary
```
 
---
 
### runSubagent Tool Usage
 
```
runSubagent(
  description: "3-5 word summary",  // REQUIRED
  prompt: "Detailed instructions"   // REQUIRED
)
```
 
**NEVER include `agentName`** — always use default subagent (has full read/write capability).
 
**If you get errors:**
- "disabled by user" → You may have included `agentName`. Remove it.
- "missing required property" → Include BOTH `description` and `prompt`
 
---
 
### Subagent Prompt Templates
 
**Research Subagent:**
```
Research [topic]. Analyze relevant files in the codebase.
Create a spec/analysis doc at: docs/analyses/[NAME].md
Return: summary of findings and the spec file path.
```
 
**Implementation Subagent:**
```
Read the spec at: docs/analyses/[NAME].md
Implement according to the spec.
Return: summary of changes made.
```
 
---
 
### What YOU Do (Orchestrator)
 
✅ Receive user requests  
✅ Spawn subagents with clear prompts  
✅ Pass spec paths between subagents  
✅ Run terminal commands  
 
### What YOU DON'T Do
 
❌ Read files (use subagent)  
❌ Edit/create code (use subagent)  
❌ Use `agentName: "Plan"` (always omit it)  
❌ "Quick look" at files before delegating

---

# tests/integration/ — Integration Test Suite

**Scope:** Integration tests that exercise multi-module workflows  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.  
**Parent:** See `tests/AGENTS.md` for fixtures, mocking patterns, and CliRunner conventions  
**Rule:** Test workflows, not individual functions; mock subprocess but test real orchestration

## STRUCTURE

```
tests/integration/
├── conftest.py                    # Integration-specific fixtures
├── test_cli_smoke.py              # Basic CLI invocation smoke tests
├── test_host_init_reset.py        # Host init/reset workflow
├── test_vm_lifecycle.py           # VM create/remove workflow
├── test_network_workflow.py       # Network create/inspect/remove
├── test_nocloud_net_lifecycle.py  # Nocloud-net HTTP server lifecycle
├── test_cloud_init_iso.py         # Cloud-init ISO generation
└── test_console_integration.py    # Console/pty-over-vsock integration
```

## TEST SCOPE

Integration tests differ from unit tests:

| Aspect | Unit Tests | Integration Tests |
|--------|-----------|---------------------|
| Scope | Single module/function | Multi-module workflows |
| Mocks | Heavy mocking of dependencies | Minimal mocks; test real orchestration |
| Subprocess | Fully mocked | Selectively mocked |
| State | Isolated fixtures | Persistent state across operations |

## KEY TEST FILES

### test_cli_smoke.py
- Verifies CLI loads without errors
- Tests `--version`, `--help` output
- Ensures no import/registration failures

### test_host_init_reset.py
- Host init workflow: KVM, sysctl, bridge, mvm group
- Host reset rollback: network + sysctl + sudoers cleanup
- Tests privilege escalation boundaries

### test_vm_lifecycle.py
- Full VM lifecycle: create → start → stop → remove
- Tests VM state transitions
- Cloud-init integration verification

### test_network_workflow.py
- Network create with IP lease tracking
- Bridge and TAP device setup
- NAT rule configuration
- Network removal cleanup

### test_nocloud_net_lifecycle.py
- Nocloud-net HTTP server start/stop
- Port allocation (8000-9000 range)
- Firewall rule management
- HTTP endpoint serving cloud-init data

### test_cloud_init_iso.py
- Cloud-init ISO generation with genisoimage
- User-data, meta-data, network-config injection
- ISO mounting and verification

### test_console_integration.py
- PTY-over-vsock console integration
- VM serial console access
- Console relay service integration

## CONVENTIONS

### subprocess Handling
Some subprocess calls are mocked, others run through:
```python
# Mock network setup (requires root)
@patch("mvmctl.core.network.subprocess.run")
def test_network_create(mock_run):
    mock_run.return_value = MagicMock(returncode=0)
    result = runner.invoke(app, ["network", "create", "testnet"])
```

### State Persistence
Integration tests may persist state across test methods:
```python
# VM created in test_1, removed in test_2
@pytest.fixture(scope="module")
def created_vm():
    vm = create_vm(name="test-integration")
    yield vm
    remove_vm(vm.name)
```

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Test single function | Test complete workflow |
| Mock everything | Mock only subprocess/sudo; test real orchestration |
| Use unit fixtures | Create integration-specific fixtures in conftest.py |
| Require root/KVM | Skip test with `@pytest.mark.skipif` if unavailable |
| Real network calls | Mock all HTTP downloads; use local test assets |

## COMMANDS

```bash
# Run all integration tests
uv run pytest tests/integration/ -v

# Run specific integration test
uv run pytest tests/integration/test_vm_lifecycle.py -v

# Integration tests with coverage
uv run pytest tests/integration/ --cov=mvmctl --cov-branch

# Stop on first failure
uv run pytest tests/integration/ -x -v
```

## NOTES

- **9 test files**: Covering host, VM, network, nocloud-net, cloud-init, console
- Tests are more coarse-grained than unit tests
- Some tests may require root for network operations (marked accordingly)
- Uses same mocking infrastructure as unit tests (see parent `tests/conftest.py`)
- CliRunner invoked against `mvmctl.main.app` with real subcommand loading
