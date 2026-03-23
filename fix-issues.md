# Agent Task: Full-Scale Audit & Parallel Implementation

## Objective
Your task is to perform a comprehensive audit and fix-cycle. You must reconcile the requirements in **`python-cli-entry.md`** with the issues identified in the **`.full-review/OPEN-ISSUES-TRACKER.md`** file. All fixes must be implemented in the source code and tracked in **`.full-review/OPEN-ISSUES-TRACKER.md`**.

## Resources & Context
- **Core Specification:** `python-cli-entry.md` (and all referenced implementation phases).
- **Audit Context:** The **`.full-review/OPEN-ISSUES-TRACKER.ms`** file contains everything you need. You are required to **read all the content of the file** to understand the full scope of required fixes, edge cases, and architectural concerns.
- **State Tracking:** `.full-review/OPEN-ISSUES-TRACKER.md`.

## Mandatory Workflow

### 1. Ingestion & Analysis
Before taking action, you must read the issue tracker in the `.full-review/OPEN-ISSUES-TRACKER.md` file. This file contains the complete audit results. synthesize the information across the entire file to build your execution plan.

### 2. Verification (Code vs. Audit)
Do not trust the current status of any issue.
- **Action:** For every issue described in the `.full-review/OPEN-ISSUES-TRACKER.md` file, inspect the actual Python source code.
- **Validation:** Confirm if the implementation matches the `python-cli-entry.md` spec. If the code is missing or logic is flawed, it is "Pending."

### 3. Parallel Execution & Delegation
You are authorized to **spawn sub-agents** to accelerate the process.
- **Tasking:** Delegate specific files or implementation phases to sub-agents.
- **Oversight:** You are the lead agent. You must review and validate all sub-agent contributions before they are finalized.

### 4. Implementation & Correction
Fix or implement any requirement that is missing or broken. All code must adhere to the patterns established in the project entry documents.

### 5. Consolidated State Update
Maintain **`.full-review/OPEN-ISSUES-TRACKER.md`** as the single source of truth for the entire project. 
- **Format:** List every requirement checked, its final status (`Verified`, `Fixed`, or `Pending`), and a concise technical log of the work performed.

## Guardrails
- **TOTAL CONTEXT:** Everything you need is in `.full-review/OPEN-ISSUES-TRACKER.md`. Read it all.
- **DO NOT ASSUME:** If you cannot find the code for a requirement, it is not implemented.
- **PARALLELISM:** Use sub-agents to fix multiple modules simultaneously.
- **SINGLE LOG:** No other tracking files should be created. Use only `.full-review/OPEN-ISSUES-TRACKER.md`.
