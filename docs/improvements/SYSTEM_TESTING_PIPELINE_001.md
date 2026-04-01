# Pipeline-Based Integration Test Architecture

**Status:** Design specification  
**Created:** 2026-04-01  
**Target:** `tests/system/`  
**Purpose:** Black-box, user-perspective integration testing with pipeline orchestration

---

## Table of Contents

### Part 1: Pipeline Architecture

- [Problem Statement](#problem-statement) — Current test limitations and goals
- [Architecture Overview](#architecture-overview) — High-level pipeline design
- [Core Data Structures](#core-data-structures)
  - [PipelineStep](#1-pipelinestep) — Atomic test actions with metadata and timing
  - [PipelineContext](#2-pipelinecontext) — Shared state between steps
  - [PipelineGroup](#3-pipelinegroup) — Logical step organization
  - [PipelineDefinition](#4-pipelinedefinition) — Complete pipeline container
- [Execution Engine](#execution-engine) — PipelineRunner orchestration
- [Timing and Reporting](#timing-and-reporting)
  - [TimingCollector](#timingcollector) — Performance data capture
  - [ResultAggregator](#resultaggregator) — Report generation
- [Concrete Pipeline Definitions](#concrete-pipeline-definitions)
  - [Full VM Lifecycle Example](#example-full-vm-lifecycle-pipeline)
  - [Step Implementation Examples](#step-implementation-examples)
- [Configuration Approach](#configuration-approach)
  - [YAML Pipeline Definitions](#yaml-pipeline-definitions)
  - [Multi-Image Pipeline Matrix](#multi-image-pipeline-matrix)
- [Pytest Integration](#pytest-integration) — Bridge to pytest for CI
- [Execution Flow](#execution-flow) — Step-by-step execution diagram
- [Reporting Output](#reporting-output)
  - [Console Output (Rich)](#console-output-rich)
  - [JSON Report (for CI/CD)](#json-report-for-cicd)
- [File Structure](#file-structure) — Proposed directory layout
- [Design Decisions](#design-decisions) — Architecture rationale
- [Future Enhancements](#future-enhancements)
- [Migration Path](#migration-path) — Implementation phases
- [Summary](#summary) — Architecture benefits

### Part 2: Research Findings & Decisions

- [Fail-Fast vs Cleanup Strategy](#1-fail-fast-vs-cleanup-strategy) — Guaranteed cleanup approaches
- [Nested Virtualization for CI](#2-nested-virtualization-for-ci) — Firecracker inside Firecracker/QEMU feasibility
- [Minimum Compute Resources](#3-minimum-compute-resources) — Per-VM and scaling requirements
- [Python Testing vs Binary Testing](#4-python-testing-vs-binary-testing) — Hybrid testing strategy
- [Test Directory Naming](#5-test-directory-naming) — `tests/system/` vs alternatives
- [Complete Test Specification](#6-complete-test-specification) — All 61 tests documented
  - [Network Pipeline Tests (8)](#network-pipeline-tests-8-tests)
  - [Key Management Tests (7)](#key-management-pipeline-tests-7-tests)
  - [Image Pipeline Tests (15)](#image-pipeline-tests-15-tests)
  - [VM Lifecycle Tests (25)](#vm-lifecycle-pipeline-tests-25-tests)
  - [Full Journey Tests (8)](#full-journey-tests-8-tests)
- [Summary of Decisions](#7-summary-of-decisions) — Decision matrix

---

## PROBLEM STATEMENT

Current integration tests (`tests/integration/`) have several limitations:

1. **No timing visibility** — Cannot measure how long each step takes
2. **No multi-image support** — Tests run against single image configuration
3. **No step reordering** — Test order is hardcoded in test methods
4. **No result aggregation** — Results are scattered across pytest output
5. **No pipeline composition** — Cannot compose reusable test sequences
6. **No parallel execution** — All tests run sequentially via pytest

This design introduces a **pipeline-based architecture** that treats integration tests as composable, observable, and reorderable sequences of steps.

---

## ARCHITECTURE OVERVIEW

```
┌─────────────────────────────────────────────────────────────┐
│                    PipelineRunner                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         │
│  │  Group:     │  │  Group:     │  │  Group:     │         │
│  │  Keys       │  │  Images     │  │  VMs        │         │
│  │  ┌───────┐  │  │  ┌───────┐  │  │  ┌───────┐  │         │
│  │  │Step 1 │  │  │  │Step 1 │  │  │  │Step 1 │  │         │
│  │  │Step 2 │  │  │  │Step 2 │  │  │  │Step 2 │  │         │
│  │  └───────┘  │  │  └───────┘  │  │  └───────┘  │         │
│  └─────────────┘  └─────────────┘  └─────────────┘         │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              TimingCollector                        │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              ResultAggregator                       │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

---

## CORE DATA STRUCTURES

### 1. PipelineStep

Represents a single atomic test action with metadata, timing, and result tracking.

```python
from dataclasses import dataclass, field
from enum import Enum, auto
from pathlib import Path
from typing import Any, Callable, Optional
import time


class StepStatus(Enum):
    """Result status for a pipeline step."""
    PENDING = auto()
    RUNNING = auto()
    PASSED = auto()
    FAILED = auto()
    SKIPPED = auto()
    TIMEOUT = auto()


@dataclass
class StepResult:
    """Captures the outcome of a single step execution."""
    step_id: str
    status: StepStatus
    duration_ms: float
    error: Optional[Exception] = None
    output: Optional[str] = None
    artifacts: dict[str, Any] = field(default_factory=dict)
    timestamp: float = field(default_factory=time.monotonic)

    @property
    def duration_s(self) -> float:
        return self.duration_ms / 1000.0

    @property
    def is_success(self) -> bool:
        return self.status == StepStatus.PASSED

    @property
    def is_failure(self) -> bool:
        return self.status in (StepStatus.FAILED, StepStatus.TIMEOUT)


@dataclass
class PipelineStep:
    """A single executable step in a pipeline.

    Attributes:
        id: Unique identifier within the pipeline (e.g., "key.create.test")
        name: Human-readable description
        action: Callable that performs the step. Signature: (context: PipelineContext) -> dict
        depends_on: List of step IDs that must complete before this step
        timeout_s: Maximum execution time in seconds (0 = no timeout)
        skip_if: Optional callable that returns True to skip this step
        retry_count: Number of retries on failure (0 = no retries)
        tags: Categorization tags for filtering (e.g., ["smoke", "critical"])
        image_scope: Which images this step applies to (None = all)
    """
    id: str
    name: str
    action: Callable[["PipelineContext"], dict[str, Any]]
    depends_on: list[str] = field(default_factory=list)
    timeout_s: float = 0.0
    skip_if: Optional[Callable[["PipelineContext"], bool]] = None
    retry_count: int = 0
    tags: list[str] = field(default_factory=list)
    image_scope: Optional[list[str]] = None

    def execute(self, context: "PipelineContext") -> StepResult:
        """Execute this step with timing and error handling."""
        # Check skip condition
        if self.skip_if and self.skip_if(context):
            return StepResult(
                step_id=self.id,
                status=StepStatus.SKIPPED,
                duration_ms=0.0,
                output="Skipped by condition",
            )

        # Check dependencies
        for dep_id in self.depends_on:
            dep_result = context.get_result(dep_id)
            if dep_result and dep_result.is_failure:
                return StepResult(
                    step_id=self.id,
                    status=StepStatus.SKIPPED,
                    duration_ms=0.0,
                    output=f"Skipped: dependency '{dep_id}' failed",
                )

        # Execute with timing
        start = time.monotonic()
        try:
            output = self.action(context)
            duration_ms = (time.monotonic() - start) * 1000
            return StepResult(
                step_id=self.id,
                status=StepStatus.PASSED,
                duration_ms=duration_ms,
                output=str(output) if output else None,
                artifacts=output if isinstance(output, dict) else {},
            )
        except Exception as e:
            duration_ms = (time.monotonic() - start) * 1000
            return StepResult(
                step_id=self.id,
                status=StepStatus.FAILED,
                duration_ms=duration_ms,
                error=e,
            )
```

### 2. PipelineContext

Shared state passed between steps. Acts as a blackboard for inter-step communication.

```python
@dataclass
class PipelineContext:
    """Shared execution context for pipeline steps.

    Provides:
    - Shared state dictionary for inter-step communication
    - Result storage for completed steps
    - Configuration access (image names, VM names, etc.)
    - Temporary directory management
    """
    config: dict[str, Any]
    state: dict[str, Any] = field(default_factory=dict)
    results: dict[str, StepResult] = field(default_factory=dict)
    tmp_dir: Optional[Path] = None

    def get_result(self, step_id: str) -> Optional[StepResult]:
        """Get result from a previously executed step."""
        return self.results.get(step_id)

    def set_state(self, key: str, value: Any) -> None:
        """Store state for subsequent steps."""
        self.state[key] = value

    def get_state(self, key: str, default: Any = None) -> Any:
        """Retrieve state from previous steps."""
        return self.state.get(key, default)

    def get_artifact(self, step_id: str, key: str, default: Any = None) -> Any:
        """Get specific artifact from a step's result."""
        result = self.get_result(step_id)
        if result and result.artifacts:
            return result.artifacts.get(key, default)
        return default
```

### 3. PipelineGroup

Organizes related steps into logical groups (keys, images, networks, VMs).

```python
@dataclass
class PipelineGroup:
    """A named collection of pipeline steps that execute together.

    Groups provide:
    - Logical organization (keys, images, networks, VMs)
    - Group-level setup/teardown
    - Execution ordering within the group
    - Group-level timeout
    """
    name: str
    description: str
    steps: list[PipelineStep] = field(default_factory=list)
    setup: Optional[Callable[["PipelineContext"], None]] = None
    teardown: Optional[Callable[["PipelineContext"], None]] = None
    timeout_s: float = 0.0
    parallel: bool = False  # If True, steps can run in parallel

    def add_step(self, step: PipelineStep) -> "PipelineGroup":
        """Add a step to this group. Returns self for chaining."""
        self.steps.append(step)
        return self

    def get_step_ids(self) -> list[str]:
        """Return all step IDs in execution order."""
        return [s.id for s in self.steps]

    def validate(self) -> list[str]:
        """Validate group configuration. Returns list of errors."""
        errors = []
        step_ids = {s.id for s in self.steps}

        for step in self.steps:
            for dep in step.depends_on:
                if dep not in step_ids:
                    errors.append(
                        f"Step '{step.id}' depends on '{dep}' which is not in group '{self.name}'"
                    )

        # Check for circular dependencies
        if self._has_cycle():
            errors.append(f"Group '{self.name}' has circular dependencies")

        return errors

    def _has_cycle(self) -> bool:
        """Detect circular dependencies using DFS."""
        visited = set()
        rec_stack = set()

        def dfs(step_id: str) -> bool:
            visited.add(step_id)
            rec_stack.add(step_id)

            step = next((s for s in self.steps if s.id == step_id), None)
            if step:
                for dep in step.depends_on:
                    if dep not in visited:
                        if dfs(dep):
                            return True
                    elif dep in rec_stack:
                        return True

            rec_stack.discard(step_id)
            return False

        for step in self.steps:
            if step.id not in visited:
                if dfs(step.id):
                    return True
        return False
```

### 4. PipelineDefinition

Top-level container that composes groups into a complete test pipeline.

```python
@dataclass
class PipelineDefinition:
    """Complete pipeline definition composed of groups.

    Attributes:
        name: Pipeline identifier (e.g., "alpine-full-lifecycle")
        description: Human-readable description
        groups: Ordered list of groups to execute
        images: List of images to test against
        global_timeout_s: Maximum total execution time
        fail_fast: Stop on first failure
    """
    name: str
    description: str
    groups: list[PipelineGroup] = field(default_factory=list)
    images: list[str] = field(default_factory=lambda: ["alpine", "ubuntu-24.04"])
    global_timeout_s: float = 0.0
    fail_fast: bool = True

    def add_group(self, group: PipelineGroup) -> "PipelineDefinition":
        """Add a group to this pipeline. Returns self for chaining."""
        self.groups.append(group)
        return self

    def get_all_steps(self) -> list[PipelineStep]:
        """Flatten all steps across groups in execution order."""
        steps = []
        for group in self.groups:
            steps.extend(group.steps)
        return steps

    def validate(self) -> list[str]:
        """Validate entire pipeline. Returns list of errors."""
        errors = []
        for group in self.groups:
            errors.extend(group.validate())
        return errors
```

---

## EXECUTION ENGINE

### PipelineRunner

Orchestrates step execution with timing, retries, and parallel support.

```python
import asyncio
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime


class PipelineRunner:
    """Executes pipeline definitions with timing, retries, and reporting.

    Features:
    - Sequential and parallel step execution
    - Per-step and per-group timing
    - Automatic retries with backoff
    - Fail-fast and continue-on-error modes
    - Rich console output
    """

    def __init__(
        self,
        definition: PipelineDefinition,
        fail_fast: bool = True,
        max_workers: int = 4,
        console_output: bool = True,
    ):
        self.definition = definition
        self.fail_fast = fail_fast
        self.max_workers = max_workers
        self.console_output = console_output
        self.context = PipelineContext(config={"images": definition.images})
        self.timing = TimingCollector()
        self.aggregator = ResultAggregator()

    def run(self) -> PipelineReport:
        """Execute the entire pipeline and return a report."""
        pipeline_start = time.monotonic()

        self._print_header()

        for group in self.definition.groups:
            group_start = time.monotonic()
            self._print_group_header(group)

            # Run group setup
            if group.setup:
                group.setup(self.context)

            # Execute steps
            if group.parallel:
                self._run_parallel(group)
            else:
                self._run_sequential(group)

            # Run group teardown
            if group.teardown:
                group.teardown(self.context)

            group_duration = (time.monotonic() - group_start) * 1000
            self.timing.record_group(group.name, group_duration)
            self._print_group_summary(group, group_duration)

            # Fail fast check
            if self.fail_fast and self._group_has_failures(group):
                self._print_fail_fast()
                break

        pipeline_duration = (time.monotonic() - pipeline_start) * 1000
        self.timing.record_pipeline(pipeline_duration)

        report = self.aggregator.build_report(
            definition=self.definition,
            context=self.context,
            timing=self.timing,
            total_duration_ms=pipeline_duration,
        )

        self._print_final_report(report)
        return report

    def _run_sequential(self, group: PipelineGroup) -> None:
        """Execute steps in dependency order."""
        resolved_order = self._resolve_execution_order(group)

        for step in resolved_order:
            result = self._execute_with_retry(step)
            self.context.results[step.id] = result
            self.timing.record_step(step.id, result.duration_ms)
            self._print_step_result(step, result)

            if self.fail_fast and result.is_failure:
                break

    def _run_parallel(self, group: PipelineGroup) -> None:
        """Execute independent steps in parallel."""
        # Group steps by dependency level
        levels = self._group_by_dependency_level(group)

        for level in levels:
            with ThreadPoolExecutor(max_workers=self.max_workers) as executor:
                futures = {
                    executor.submit(self._execute_with_retry, step): step
                    for step in level
                }
                for future in futures:
                    step = futures[future]
                    result = future.result()
                    self.context.results[step.id] = result
                    self.timing.record_step(step.id, result.duration_ms)
                    self._print_step_result(step, result)

    def _execute_with_retry(self, step: PipelineStep) -> StepResult:
        """Execute a step with retry logic."""
        last_result = None
        for attempt in range(step.retry_count + 1):
            result = step.execute(self.context)
            last_result = result

            if result.is_success or result.status == StepStatus.SKIPPED:
                return result

            if attempt < step.retry_count:
                backoff = 0.5 * (2 ** attempt)  # Exponential backoff
                time.sleep(backoff)

        return last_result

    def _resolve_execution_order(self, group: PipelineGroup) -> list[PipelineStep]:
        """Topological sort of steps based on dependencies."""
        # Simple topological sort for DAG
        in_degree = {s.id: 0 for s in group.steps}
        graph = {s.id: [] for s in group.steps}

        for step in group.steps:
            for dep in step.depends_on:
                graph[dep].append(step.id)
                in_degree[step.id] += 1

        queue = [s.id for s in group.steps if in_degree[s.id] == 0]
        order = []

        while queue:
            node = queue.pop(0)
            order.append(node)
            for neighbor in graph[node]:
                in_degree[neighbor] -= 1
                if in_degree[neighbor] == 0:
                    queue.append(neighbor)

        step_map = {s.id: s for s in group.steps}
        return [step_map[id] for id in order if id in step_map]

    def _group_by_dependency_level(self, group: PipelineGroup) -> list[list[PipelineStep]]:
        """Group steps into levels for parallel execution."""
        levels = []
        executed = set()
        remaining = set(s.id for s in group.steps)
        step_map = {s.id: s for s in group.steps}

        while remaining:
            # Find steps whose dependencies are all executed
            ready = []
            for step_id in remaining:
                step = step_map[step_id]
                if all(dep in executed for dep in step.depends_on):
                    ready.append(step)

            if not ready:
                # Circular dependency detected
                break

            levels.append(ready)
            for step in ready:
                executed.add(step.id)
                remaining.discard(step.id)

        return levels
```

---

## TIMING AND REPORTING

### TimingCollector

Captures timing data at step, group, and pipeline levels.

```python
@dataclass
class StepTiming:
    step_id: str
    duration_ms: float
    start_time: float
    end_time: float


@dataclass
class GroupTiming:
    group_name: str
    duration_ms: float
    step_timings: list[StepTiming] = field(default_factory=list)


class TimingCollector:
    """Collects and aggregates timing data."""

    def __init__(self):
        self.step_timings: dict[str, StepTiming] = {}
        self.group_timings: list[GroupTiming] = []
        self.pipeline_duration_ms: float = 0.0

    def record_step(self, step_id: str, duration_ms: float) -> None:
        self.step_timings[step_id] = StepTiming(
            step_id=step_id,
            duration_ms=duration_ms,
            start_time=time.monotonic(),
            end_time=time.monotonic() + (duration_ms / 1000),
        )

    def record_group(self, group_name: str, duration_ms: float) -> None:
        steps = [
            t for t in self.step_timings.values()
            if t.step_id.startswith(f"{group_name}.")
        ]
        self.group_timings.append(GroupTiming(
            group_name=group_name,
            duration_ms=duration_ms,
            step_timings=steps,
        ))

    def record_pipeline(self, duration_ms: float) -> None:
        self.pipeline_duration_ms = duration_ms

    def get_slowest_steps(self, n: int = 10) -> list[StepTiming]:
        """Return the N slowest steps."""
        return sorted(
            self.step_timings.values(),
            key=lambda t: t.duration_ms,
            reverse=True,
        )[:n]

    def get_summary(self) -> dict[str, Any]:
        return {
            "pipeline_ms": self.pipeline_duration_ms,
            "groups": len(self.group_timings),
            "steps": len(self.step_timings),
            "slowest": [
                {"step": t.step_id, "ms": t.duration_ms}
                for t in self.get_slowest_steps(5)
            ],
        }
```

### ResultAggregator

Aggregates results into a comprehensive report.

```python
@dataclass
class PipelineReport:
    """Final report from pipeline execution."""
    pipeline_name: str
    status: str  # "passed", "failed", "partial"
    total_duration_ms: float
    groups: list[dict[str, Any]]
    steps: list[dict[str, Any]]
    timing_summary: dict[str, Any]
    errors: list[dict[str, str]]
    artifacts: dict[str, Any]

    @property
    def passed_count(self) -> int:
        return sum(1 for s in self.steps if s["status"] == "passed")

    @property
    def failed_count(self) -> int:
        return sum(1 for s in self.steps if s["status"] in ("failed", "timeout"))

    @property
    def skipped_count(self) -> int:
        return sum(1 for s in self.steps if s["status"] == "skipped")

    @property
    def success_rate(self) -> float:
        total = self.passed_count + self.failed_count
        return (self.passed_count / total * 100) if total > 0 else 0.0


class ResultAggregator:
    """Aggregates pipeline results into a comprehensive report."""

    def build_report(
        self,
        definition: PipelineDefinition,
        context: PipelineContext,
        timing: TimingCollector,
        total_duration_ms: float,
    ) -> PipelineReport:
        groups = []
        steps = []
        errors = []

        for group in definition.groups:
            group_steps = []
            for step in group.steps:
                result = context.results.get(step.id)
                if result:
                    step_data = {
                        "id": step.id,
                        "name": step.name,
                        "group": group.name,
                        "status": result.status.name.lower(),
                        "duration_ms": result.duration_ms,
                        "error": str(result.error) if result.error else None,
                    }
                    group_steps.append(step_data)
                    steps.append(step_data)

                    if result.is_failure:
                        errors.append({
                            "step": step.id,
                            "error": str(result.error),
                            "group": group.name,
                        })

            groups.append({
                "name": group.name,
                "steps": group_steps,
                "step_count": len(group_steps),
            })

        # Determine overall status
        if not errors:
            status = "passed"
        elif any(s["status"] == "passed" for s in steps):
            status = "partial"
        else:
            status = "failed"

        return PipelineReport(
            pipeline_name=definition.name,
            status=status,
            total_duration_ms=total_duration_ms,
            groups=groups,
            steps=steps,
            timing_summary=timing.get_summary(),
            errors=errors,
            artifacts=context.state.get("artifacts", {}),
        )
```

---

## CONCRETE PIPELINE DEFINITIONS

### Example: Full VM Lifecycle Pipeline

```python
def create_alpine_lifecycle_pipeline() -> PipelineDefinition:
    """Define a complete pipeline for testing Alpine VM lifecycle."""

    # Group 1: Key Management
    keys_group = PipelineGroup(
        name="keys",
        description="SSH key setup and validation",
        setup=lambda ctx: ctx.set_state("key_name", "pipeline-test-key"),
        teardown=lambda ctx: _cleanup_key(ctx),
    )
    keys_group.add_step(PipelineStep(
        id="keys.create",
        name="Create test SSH key",
        action=_step_create_key,
        tags=["setup", "critical"],
    ))
    keys_group.add_step(PipelineStep(
        id="keys.list",
        name="List keys and verify creation",
        action=_step_list_keys,
        depends_on=["keys.create"],
        tags=["verify"],
    ))
    keys_group.add_step(PipelineStep(
        id="keys.set_default",
        name="Set key as default",
        action=_step_set_default_key,
        depends_on=["keys.create"],
        tags=["setup"],
    ))

    # Group 2: Image Management
    images_group = PipelineGroup(
        name="images",
        description="Image fetch and validation",
    )
    images_group.add_step(PipelineStep(
        id="images.fetch",
        name="Fetch Alpine image",
        action=lambda ctx: _step_fetch_image(ctx, "alpine"),
        tags=["setup", "critical"],
    ))
    images_group.add_step(PipelineStep(
        id="images.verify",
        name="Verify image integrity",
        action=_step_verify_image,
        depends_on=["images.fetch"],
        tags=["verify"],
    ))
    images_group.add_step(PipelineStep(
        id="images.list",
        name="List images and verify presence",
        action=_step_list_images,
        depends_on=["images.fetch"],
        tags=["verify"],
    ))

    # Group 3: Network Setup
    networks_group = PipelineGroup(
        name="networks",
        description="Network creation and validation",
        setup=lambda ctx: ctx.set_state("network_name", "pipeline-test-net"),
        teardown=lambda ctx: _cleanup_network(ctx),
    )
    networks_group.add_step(PipelineStep(
        id="networks.create",
        name="Create test network",
        action=_step_create_network,
        tags=["setup"],
    ))
    networks_group.add_step(PipelineStep(
        id="networks.inspect",
        name="Inspect network configuration",
        action=_step_inspect_network,
        depends_on=["networks.create"],
        tags=["verify"],
    ))

    # Group 4: VM Lifecycle
    vms_group = PipelineGroup(
        name="vms",
        description="Full VM lifecycle testing",
        setup=lambda ctx: ctx.set_state("vm_name", "pipeline-test-vm"),
        teardown=lambda ctx: _cleanup_vm(ctx),
    )
    vms_group.add_step(PipelineStep(
        id="vms.create",
        name="Create VM with Alpine image",
        action=_step_create_vm,
        depends_on=["keys.create", "images.fetch", "networks.create"],
        tags=["critical"],
        timeout_s=120.0,
    ))
    vms_group.add_step(PipelineStep(
        id="vms.wait_ready",
        name="Wait for VM to be SSH-ready",
        action=_step_wait_vm_ready,
        depends_on=["vms.create"],
        tags=["wait"],
        timeout_s=60.0,
        retry_count=3,
    ))
    vms_group.add_step(PipelineStep(
        id="vms.ssh_test",
        name="Test SSH connectivity",
        action=_step_ssh_test,
        depends_on=["vms.wait_ready"],
        tags=["critical"],
    ))
    vms_group.add_step(PipelineStep(
        id="vms.console_test",
        name="Test console access",
        action=_step_console_test,
        depends_on=["vms.create"],
        tags=["verify"],
    ))
    vms_group.add_step(PipelineStep(
        id="vms.logs_test",
        name="Verify boot logs",
        action=_step_logs_test,
        depends_on=["vms.create"],
        tags=["verify"],
    ))
    vms_group.add_step(PipelineStep(
        id="vms.snapshot",
        name="Create VM snapshot",
        action=_step_snapshot_vm,
        depends_on=["vms.wait_ready"],
        tags=["advanced"],
    ))
    vms_group.add_step(PipelineStep(
        id="vms.remove",
        name="Remove VM",
        action=_step_remove_vm,
        depends_on=["vms.snapshot"],
        tags=["cleanup"],
    ))

    return PipelineDefinition(
        name="alpine-full-lifecycle",
        description="Complete lifecycle test for Alpine Linux VM",
        groups=[keys_group, images_group, networks_group, vms_group],
        images=["alpine"],
        fail_fast=True,
    )
```

### Step Implementation Examples

```python
def _step_create_key(ctx: PipelineContext) -> dict[str, Any]:
    """Create an SSH key using the CLI."""
    from typer.testing import CliRunner
    from mvmctl.main import app

    runner = CliRunner()
    key_name = ctx.get_state("key_name", "test-key")
    result = runner.invoke(app, ["key", "create", key_name])

    if result.exit_code != 0:
        raise RuntimeError(f"Key creation failed: {result.output}")

    return {"key_name": key_name, "output": result.output}


def _step_fetch_image(ctx: PipelineContext, image_name: str) -> dict[str, Any]:
    """Fetch an OS image."""
    from typer.testing import CliRunner
    from mvmctl.main import app

    runner = CliRunner()
    result = runner.invoke(app, ["image", "fetch", image_name])

    if result.exit_code != 0:
        raise RuntimeError(f"Image fetch failed: {result.output}")

    return {"image_name": image_name}


def _step_create_vm(ctx: PipelineContext) -> dict[str, Any]:
    """Create a VM using resolved dependencies."""
    from typer.testing import CliRunner
    from mvmctl.main import app

    runner = CliRunner()
    vm_name = ctx.get_state("vm_name", "test-vm")
    image_name = ctx.get_artifact("images.fetch", "image_name", "alpine")

    result = runner.invoke(app, [
        "vm", "create",
        "--name", vm_name,
        "--image", image_name,
    ])

    if result.exit_code != 0:
        raise RuntimeError(f"VM creation failed: {result.output}")

    return {"vm_name": vm_name}


def _step_wait_vm_ready(ctx: PipelineContext) -> dict[str, Any]:
    """Wait for VM to be ready for SSH."""
    import time
    from typer.testing import CliRunner
    from mvmctl.main import app

    runner = CliRunner()
    vm_name = ctx.get_state("vm_name", "test-vm")

    # Poll VM status until running
    for attempt in range(10):
        result = runner.invoke(app, ["vm", "ls", "--json"])
        if result.exit_code == 0 and vm_name in result.output:
            return {"ready": True, "attempts": attempt + 1}
        time.sleep(5)

    raise TimeoutError(f"VM {vm_name} did not become ready")
```

---

## CONFIGURATION APPROACH

### YAML Pipeline Definitions

Pipelines can be defined in YAML for easy reordering and composition:

```yaml
# pipelines/alpine-lifecycle.yaml
name: alpine-full-lifecycle
description: Complete lifecycle test for Alpine Linux VM
images:
  - alpine
fail_fast: true

groups:
  - name: keys
    description: SSH key management
    steps:
      - id: keys.create
        name: Create test SSH key
        action: steps.keys:create_key
        tags: [setup, critical]

      - id: keys.list
        name: List and verify keys
        action: steps.keys:list_keys
        depends_on: [keys.create]
        tags: [verify]

  - name: images
    description: Image management
    steps:
      - id: images.fetch
        name: Fetch Alpine image
        action: steps.images:fetch_image
        args:
          image_name: alpine
        tags: [setup, critical]

      - id: images.verify
        name: Verify image integrity
        action: steps.images:verify_image
        depends_on: [images.fetch]
        tags: [verify]

  - name: networks
    description: Network setup
    steps:
      - id: networks.create
        name: Create test network
        action: steps.networks:create_network
        tags: [setup]

      - id: networks.inspect
        name: Inspect network
        action: steps.networks:inspect_network
        depends_on: [networks.create]
        tags: [verify]

  - name: vms
    description: VM lifecycle
    steps:
      - id: vms.create
        name: Create VM
        action: steps.vms:create_vm
        depends_on: [keys.create, images.fetch, networks.create]
        timeout_s: 120
        tags: [critical]

      - id: vms.wait_ready
        name: Wait for SSH ready
        action: steps.vms:wait_ready
        depends_on: [vms.create]
        timeout_s: 60
        retry_count: 3
        tags: [wait]

      - id: vms.ssh_test
        name: Test SSH
        action: steps.vms:ssh_test
        depends_on: [vms.wait_ready]
        tags: [critical]

      - id: vms.remove
        name: Remove VM
        action: steps.vms:remove_vm
        depends_on: [vms.ssh_test]
        tags: [cleanup]
```

### Multi-Image Pipeline Matrix

```yaml
# pipelines/multi-image-matrix.yaml
name: multi-image-smoke
description: Smoke test across multiple OS images
images:
  - alpine
  - ubuntu-24.04
  - debian-12
  - archlinux
fail_fast: false

groups:
  - name: smoke
    description: Basic smoke tests per image
    parallel: true
    steps:
      - id: smoke.alpine
        name: Alpine smoke test
        action: steps.smoke:basic_vm_test
        image_scope: [alpine]

      - id: smoke.ubuntu
        name: Ubuntu smoke test
        action: steps.smoke:basic_vm_test
        image_scope: [ubuntu-24.04]

      - id: smoke.debian
        name: Debian smoke test
        action: steps.smoke:basic_vm_test
        image_scope: [debian-12]

      - id: smoke.arch
        name: Arch smoke test
        action: steps.smoke:basic_vm_test
        image_scope: [archlinux]
```

---

## PYTEST INTEGRATION

### Bridge to pytest

The pipeline system integrates with pytest for CI compatibility:

```python
# tests/integration/pipeline/test_pipeline_runner.py
"""Pytest wrapper for pipeline-based integration tests."""

import pytest
from typer.testing import CliRunner

from tests.integration.pipeline.definitions import (
    create_alpine_lifecycle_pipeline,
    create_multi_image_matrix,
)
from tests.integration.pipeline.runner import PipelineRunner


class TestPipelineAlpineLifecycle:
    """Run the Alpine full lifecycle pipeline."""

    def test_full_lifecycle(self, tmp_path, monkeypatch):
        """Execute complete Alpine VM lifecycle pipeline."""
        # Isolate config/cache
        monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path / "config"))
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path / "cache"))

        pipeline = create_alpine_lifecycle_pipeline()
        runner = PipelineRunner(pipeline, fail_fast=True)
        report = runner.run()

        # Assert overall success
        assert report.status == "passed", f"Pipeline failed: {report.errors}"
        assert report.success_rate == 100.0

        # Assert critical steps passed
        critical_steps = [s for s in report.steps if "critical" in s.get("tags", [])]
        for step in critical_steps:
            assert step["status"] == "passed", f"Critical step failed: {step['id']}"


class TestPipelineMultiImage:
    """Run multi-image smoke tests."""

    @pytest.mark.parametrize("image", ["alpine", "ubuntu-24.04", "debian-12"])
    def test_image_smoke(self, image, tmp_path, monkeypatch):
        """Smoke test for a specific image."""
        monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path / "config"))
        monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path / "cache"))

        pipeline = create_single_image_pipeline(image)
        runner = PipelineRunner(pipeline, fail_fast=True)
        report = runner.run()

        assert report.status == "passed"
```

---

## EXECUTION FLOW

```
User invokes: pytest tests/integration/pipeline/ -v

1. pytest discovers test classes
2. Test class instantiates PipelineDefinition
3. PipelineRunner created with definition
4. For each group in definition:
   a. Group setup executed
   b. Steps resolved in dependency order (topological sort)
   c. For each step:
      - Check skip conditions
      - Check dependency results
      - Execute action with timing
      - Retry on failure (if configured)
      - Store result in context
      - Print step result
   d. Group teardown executed
   e. Check fail-fast condition
5. ResultAggregator builds PipelineReport
6. Report printed to console
7. pytest asserts report status
```

---

## REPORTING OUTPUT

### Console Output (Rich)

```
═══════════════════════════════════════════════════════════
 Pipeline: alpine-full-lifecycle
 Images: alpine
 Started: 2026-04-01 10:30:00
═══════════════════════════════════════════════════════════

 Group: keys — SSH key setup and validation
 ─────────────────────────────────────────────────────────
   ✓ keys.create        Create test SSH key          1.2s
   ✓ keys.list          List and verify keys         0.3s
   ✓ keys.set_default   Set key as default           0.2s
   Group duration: 1.7s

 Group: images — Image fetch and validation
 ─────────────────────────────────────────────────────────
   ✓ images.fetch       Fetch Alpine image          12.4s
   ✓ images.verify      Verify image integrity       0.8s
   ✓ images.list        List images and verify       0.3s
   Group duration: 13.5s

 Group: networks — Network creation and validation
 ─────────────────────────────────────────────────────────
   ✓ networks.create    Create test network          2.1s
   ✓ networks.inspect   Inspect network config       0.5s
   Group duration: 2.6s

 Group: vms — Full VM lifecycle testing
 ─────────────────────────────────────────────────────────
   ✓ vms.create         Create VM with Alpine       15.3s
   ✓ vms.wait_ready     Wait for SSH ready           8.2s (3 retries)
   ✓ vms.ssh_test       Test SSH connectivity        1.1s
   ✓ vms.console_test   Test console access          0.9s
   ✓ vms.logs_test      Verify boot logs             0.4s
   ✓ vms.snapshot       Create VM snapshot           3.2s
   ✓ vms.remove         Remove VM                    1.8s
   Group duration: 30.9s

═══════════════════════════════════════════════════════════
 Pipeline Report
═══════════════════════════════════════════════════════════
 Status: PASSED
 Duration: 48.7s
 Steps: 15 passed, 0 failed, 0 skipped
 Success Rate: 100.0%

 Slowest Steps:
   1. images.fetch       12.4s
   2. vms.create         15.3s
   3. vms.wait_ready      8.2s
   4. vms.snapshot        3.2s
   5. networks.create     2.1s
═══════════════════════════════════════════════════════════
```

### JSON Report (for CI/CD)

```json
{
  "pipeline_name": "alpine-full-lifecycle",
  "status": "passed",
  "total_duration_ms": 48700,
  "groups": [
    {
      "name": "keys",
      "steps": 3,
      "duration_ms": 1700
    },
    {
      "name": "images",
      "steps": 3,
      "duration_ms": 13500
    },
    {
      "name": "networks",
      "steps": 2,
      "duration_ms": 2600
    },
    {
      "name": "vms",
      "steps": 7,
      "duration_ms": 30900
    }
  ],
  "steps": [
    {
      "id": "vms.create",
      "name": "Create VM with Alpine image",
      "status": "passed",
      "duration_ms": 15300,
      "error": null
    }
  ],
  "timing_summary": {
    "pipeline_ms": 48700,
    "groups": 4,
    "steps": 15,
    "slowest": [
      {"step": "vms.create", "ms": 15300},
      {"step": "images.fetch", "ms": 12400}
    ]
  },
  "errors": [],
  "artifacts": {}
}
```

---

## FILE STRUCTURE

```
tests/integration/pipeline/
├── __init__.py
├── models.py              # PipelineStep, PipelineGroup, PipelineDefinition, etc.
├── context.py             # PipelineContext
├── runner.py              # PipelineRunner
├── timing.py              # TimingCollector
├── aggregator.py          # ResultAggregator
├── reporter.py            # Console/JSON report formatting
├── steps/                 # Step implementations
│   ├── __init__.py
│   ├── keys.py            # Key management steps
│   ├── images.py          # Image management steps
│   ├── networks.py        # Network management steps
│   ├── vms.py             # VM lifecycle steps
│   └── smoke.py           # Basic smoke test steps
├── definitions.py         # Pipeline definition factories
├── yaml_loader.py         # YAML pipeline definition loader
├── conftest.py            # Pipeline-specific fixtures
└── test_pipeline_runner.py # Pytest integration tests
```

---

## DESIGN DECISIONS

### 1. Why dataclasses over Pydantic?
- Project already uses `@dataclass` extensively in `models/`
- No need for additional dependency
- Simpler serialization for this use case

### 2. Why topological sort for dependencies?
- Ensures correct execution order
- Detects circular dependencies early
- Enables parallel execution grouping

### 3. Why CliRunner for step actions?
- Maintains black-box testing approach
- Tests actual CLI behavior, not internal APIs
- Consistent with existing integration tests

### 4. Why fail-fast by default?
- Integration tests are expensive (real VMs, networks)
- Early failure saves time and resources
- Can be disabled for comprehensive reporting

### 5. Why YAML for pipeline definitions?
- Easy to reorder/rearrange without code changes
- Human-readable and version-controllable
- Enables non-developers to modify test sequences

### 6. Why not replace existing integration tests?
- Existing tests are valuable for specific workflow testing
- Pipeline tests complement, not replace, pytest tests
- Pipeline tests focus on end-to-end user journeys
- Existing tests focus on specific module interactions

---

## FUTURE ENHANCEMENTS

1. **Parallel image testing** — Run same pipeline against multiple images simultaneously
2. **Step caching** — Cache successful step results to skip on re-runs
3. **Pipeline diffing** — Compare results across runs to detect regressions
4. **Interactive mode** — Pause between steps for debugging
5. **Export to pytest** — Generate pytest test files from pipeline definitions
6. **CI integration** — Upload JSON reports to CI artifacts
7. **Step plugins** — Allow custom step implementations via entry points

---

## MIGRATION PATH

1. **Phase 1:** Create pipeline infrastructure (models, runner, timing)
2. **Phase 2:** Implement step library (keys, images, networks, VMs)
3. **Phase 3:** Define initial pipelines (Alpine lifecycle, multi-image smoke)
4. **Phase 4:** Integrate with pytest and CI
5. **Phase 5:** Migrate existing integration tests to pipeline format (optional)

---

## SUMMARY

This architecture provides:

- **Composable pipelines** — Groups and steps can be mixed and matched
- **Observable execution** — Per-step timing and detailed reporting
- **Flexible ordering** — Dependencies define execution order, not code structure
- **Multi-image support** — Same pipeline runs against different OS images
- **Black-box testing** — Uses CLI (CliRunner) to test from user perspective
- **CI compatible** — Integrates with pytest for existing CI infrastructure
- **YAML configurable** — Easy to modify test sequences without code changes
- **Parallel capable** — Independent steps can run concurrently
- **Retry support** — Flaky steps can be retried with exponential backoff

---

## RESEARCH FINDINGS & DECISIONS

### 1. Fail-Fast vs Cleanup Strategy

**Question:** How to ensure proper cleanup when fail-fast stops pipeline?

**Solution:** Guaranteed cleanup via `yield` fixtures + pre-test cleanup:

```python
@pytest.fixture
def isolated_network():
    """Creates network, guarantees cleanup even on failure."""
    network_name = f"test-net-{uuid4().hex[:6]}"
    try:
        runner.invoke(app, ["network", "create", network_name])
        yield network_name
    finally:
        # ALWAYS cleanup, even if test failed
        runner.invoke(app, ["network", "rm", network_name, "--force"])
```

**Additional safeguards:**
- Pre-test cleanup: Each test checks for orphaned resources before starting
- Post-session cleanup: `pytest_sessionfinish` hook removes all artifacts
- Resource tracking: Tests log created resources, cleanup verifies removal

---

### 2. Nested Virtualization for CI

**Research Question:** Can we use Firecracker inside Firecracker/QEMU for CI isolation?

**Findings:**

| Approach | Feasible? | Details |
|----------|-----------|---------|
| Firecracker inside Firecracker | ❌ **NO** | Firecracker is VMM, not hypervisor - cannot host VMs |
| Firecracker inside QEMU | ❌ **NO** | Firecracker requires real `/dev/kvm`, QEMU emulation too slow |
| Nested KVM (self-hosted) | ✅ **YES** | Requires `nested=1` kernel parameter |
| Google Cloud N2/C2 | ✅ **YES** | Enable nested virtualization via metadata |
| GitHub Actions hosted | ❌ **NO** | No KVM access on shared runners |

**Recommendation:** Keep mocked tests for PR CI. Real VM tests require self-hosted runners:

```bash
# Enable nested KVM on host
echo "options kvm_intel nested=1" | sudo tee /etc/modprobe.d/kvm-intel.conf
sudo modprobe -r kvm_intel && sudo modprobe kvm_intel
```

**CI Strategy:**
- **Tier 1 (PR CI):** Mocked tests on GitHub Actions hosted runners
- **Tier 2 (Nightly):** Real VM tests on self-hosted runners with KVM

---

### 3. Minimum Compute Resources

**Per-VM Requirements:**

| Component | Minimum | Notes |
|-----------|--------|-------|
| Guest RAM | 128 MiB | Firecracker hard minimum (exits below) |
| Host overhead | ~15 MiB per VM | Firecracker process memory |
| vCPU | 1 | Can oversubscribe 4:1 to 10:1 |
| Disk | 500 MiB | Rootfs + overhead |
| Network | 1 TAP + 2 iptables rules | Scales linearly |

**Scaling Calculations:**

| Scale | Memory | vCPUs | Disk | Host Recommendation |
|-------|--------|-------|------|---------------------|
| 1 VM | 263 MiB | 1 | 500 MiB | 1 GiB RAM, 2 cores |
| 10 VMs | 2.6 GiB | 10 | 5 GiB | 4-8 GiB RAM, 4-8 cores |
| 100 VMs | 26 GiB | 100 | 50 GiB | 32-64 GiB RAM, 16-32 cores |

**Critical Constraints:**
- Memory is primary bottleneck — guest RAM reserved upfront
- File descriptors: each Firecracker uses ~20 FDs; ensure `ulimit -n` ≥ 1024
- Network scales well — TAP and iptables handle thousands

**Resource Limits Per Step:**
```python
@pytest.mark.timeout(60)  # Max 60 seconds
@pytest.mark.max_memory("500MB")
def test_vm_create_alpine():
    ...
```

---

### 4. Python Testing vs Binary Testing

**Can Python tests reliably test compiled binary?** ✅ **YES**, with subprocess approach.

**Hybrid Testing Strategy:**

```python
# tests/conftest.py - Parameterized executable
@pytest.fixture(scope="session")
def mvm_executable():
    """Return mvm executable path. Override with MVM_BINARY env var."""
    return os.environ.get("MVM_BINARY", "uv run mvm")

# Test works with BOTH source and binary
def test_version(mvm_executable):
    result = subprocess.run([mvm_executable, "--version"], capture_output=True, text=True)
    assert result.returncode == 0
    assert "mvm" in result.stdout
```

**Run against source:**
```bash
pytest tests/system/
```

**Run against binary:**
```bash
MVM_BINARY=./dist/mvm pytest tests/system/
```

**False Positive Risks & Mitigations:**

| Risk | Severity | Mitigation |
|------|----------|------------|
| Subprocess env inheritance | Medium | Explicit `env={...}` in `subprocess.run()` |
| TTY/terminal differences | Low-Medium | Use `text=True`, test both TTY/non-TTY |
| Performance measurement | Medium | Don't compare source vs binary timings directly |
| Missing bundled assets | High | Test asset access specifically in binary mode |
| Dynamic import failures | High | Binary smoke tests catch dynamic import issues |

**Recommendation:**
- Keep `CliRunner` for fast unit tests (source only)
- Add `subprocess`-based tests for binary validation
- Same test code works for both via `MVM_BINARY` parameterization

---

### 5. Test Directory Naming

**Research Findings:**

| Convention | Used By | Meaning |
|------------|---------|---------|
| `tests/e2e/` | Django, FastAPI | Full user journey |
| `tests/functional/` | pyOpenSci | Workflow validation |
| `tests/system/` | **Kubernetes, Docker** | Tests against real infrastructure |
| `tests/integration/` | General | Integration between components |

**Current project structure:**
```
tests/
├── unit/              # Mocked, fast
├── integration/       # Mocked integration (existing)
├── layer_compliance/  # Architecture enforcement
└── ???               # NEW: Real system tests
```

**Recommendation: `tests/system/`** (NOT `tests/integration/pipeline/`)

**Rationale:**
1. Already have `tests/integration/` — it's mocked, would be confusing
2. "System tests" is industry standard for real-resource tests
3. Clear distinction: `integration/` = mocked, `system/` = real resources
4. CI-friendly: `pytest tests/system/` is explicit

**Proposed structure:**
```
tests/
├── unit/
├── integration/       # Keep existing mocked tests
├── layer_compliance/
└── system/          # NEW: Real system behavior tests
    ├── conftest.py
    ├── test_network.py
    ├── test_keys.py
    ├── test_images.py
    ├── test_vm_lifecycle.py
    └── test_full_journeys.py
```

---

### 6. Complete Test Specification

**Total: 61 tests across 5 categories**

#### Network Pipeline Tests (8 tests)
1. `test_network_create_with_default_cidr` — Verify default CIDR (10.0.0.0/24)
2. `test_network_create_with_custom_cidr` — Custom CIDR ranges (10.50.0.0/24, 172.16.0.0/16)
3. `test_network_listing_and_verification` — List networks with metadata
4. `test_ip_rule_verification_iptables` — Verify FORWARD rules
5. `test_nat_gateway_configuration` — Gateway IP and NAT enabled
6. `test_network_deletion_and_cleanup` — Remove network, verify cleanup
7. `test_duplicate_network_handling` — Fail gracefully on duplicate
8. `test_invalid_cidr_rejection` — Reject invalid CIDR formats

#### Key Management Pipeline Tests (7 tests)
1. `test_ssh_key_creation_rsa` — RSA key creation
2. `test_ssh_key_creation_ed25519` — Ed25519 key creation
3. `test_key_listing_and_metadata` — List with metadata
4. `test_set_default_key` — Change default key
5. `test_key_deletion` — Remove key and files
6. `test_multiple_key_handling` — 5 keys, switching defaults
7. `test_invalid_key_format_handling` — Reject invalid operations

#### Image Pipeline Tests (15 tests)
For each image (alpine-3.21, ubuntu-24.04-minimal, ubuntu-24.04, archlinux, debian-bookworm):
1. Image fetch/download
2. Image listing and verification
3. Image metadata inspection
4. Default image marking
5. Image removal and cleanup

Plus 5 general tests:
1. `test_duplicate_fetch_handling_cached` — Use cache on re-fetch
2. `test_force_refetch_image` — Force re-download
3. `test_image_fetch_invalid_name` — Fail on unknown image
4. `test_image_fetch_network_error` — Handle network errors
5. `test_image_fetch_checksum_verification` — Verify SHA256

#### VM Lifecycle Pipeline Tests (23 tests)
1. VM creation with each image (5 tests: alpine, ubuntu-minimal, ubuntu, arch, debian)
2. `test_vm_create_with_cloud_init_mode_off` — No cloud-init
3. `test_vm_create_with_cloud_init_mode_inject` — Inject mode
4. `test_vm_create_with_user_flag` — Non-root user creation
5. `test_vm_listing_and_status_verification` — List VMs with status
6. `test_ssh_connectivity_test` — SSH with key auth
7. `test_ssh_user_creation_verification` — SSH as created user
8. `test_tap_device_creation_verification` — TAP device created
9. `test_iptables_forward_rules_verification` — FORWARD rules
10. `test_vm_stop` — Stop VM (Firecracker pause/suspend)
11. `test_vm_reboot_via_ssh` — Reboot VM via `mvm ssh my-vm -c reboot` (Firecracker restarts)
12. `test_vm_deletion_and_cleanup` — Remove VM and cleanup
13. `test_orphaned_resource_detection` — Detect orphaned VMs
14. `test_multiple_vm_concurrent_creation` — Create 3 VMs
15. `test_vm_with_custom_resources` — Custom CPU/memory
16. `test_vm_logs_retrieval` — Get boot/OS logs
17. `test_vm_console_access` — Console connection
18. `test_vm_create_with_custom_network` — Non-default network
19. `test_vm_create_with_disk_size` — Custom disk size

#### Full Journey Tests (8 tests)
1. `test_complete_workflow_key_image_network_vm_ssh_cleanup` — End-to-end
2. `test_multi_vm_scenario_isolation` — 3 VMs with isolation
3. `test_resource_cleanup_after_failure` — Cleanup on failed test
4. `test_timing_under_5s_vm_creation` — Verify <5s target
5. `test_timing_under_5s_vm_teardown` — Verify <5s target
6. `test_full_journey_alpine` — Complete Alpine workflow
7. `test_full_journey_ubuntu_minimal` — Complete Ubuntu minimal
8. `test_full_journey_archlinux` — Complete Arch Linux workflow

**Each test includes:**
- Preconditions
- Detailed steps
- Expected outcomes
- Assertions (code examples)
- Cleanup requirements
- Estimated duration (1s-30s per test)
- Resource requirements (memory, CPU, disk)

---

### 7. Summary of Decisions

| Decision | Rationale |
|----------|-----------|
| **Cleanup** | Guaranteed via `yield` fixtures + pre/post cleanup hooks |
| **CI Environment** | Mocked tests on GitHub Actions; real VM tests on self-hosted KVM runners |
| **Resources** | 128 MiB min per VM; memory is bottleneck; plan for 4-8 GiB host for 10 VMs |
| **Testing Approach** | Hybrid: `CliRunner` for unit, `subprocess` for binary, parameterized via `MVM_BINARY` |
| **Test Location** | `tests/system/` — clear distinction from mocked `tests/integration/` |
| **Total Tests** | 61 tests across 5 categories covering all images and workflows
