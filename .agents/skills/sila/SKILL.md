---
name: sila
description: "Cross-harness continuity engine for AI coding agents. Use whenever you need to recall prior cross-project fixes for an error or package, search past sessions before grep or web search, record decisions, lessons, cases, or handoffs, prime a session, verify facts, run the quality gate, inspect goals, route specialist work to personas or skills, or check symbol blast radius before a rename or delete."
---

# Sila (صِلَة) — Cross-Harness Continuity Engine

## Overview

`sila` is the cross-harness continuity engine for AI coding agents. It consolidates
knowledge from heterogeneous AI CLI tools (Antigravity, Claude Code, Codex, Devin,
OpenCode, MiMoCode, Command Code) into one durable, disk-grounded SQLite store. Sila reads
content and truth, not mere telemetry.

Call Sila at these moments; do not wait to be asked.

## The 9-Verb Continuity Loop

### 1. Prime at turn 0

```bash
sila prime
sila hook session-start --json
```

MCP: `prime_context`, `salam`, `get_status`. The brief carries the active handoff, open
items, key decisions, lessons, cases, and the project runbook. Call it once at the start
of a session, not after every prompt.

### 2. Recall before guessing

```bash
sila recall "<error, symptom, package, or API>"
```

MCP: `recall`, `omni_search`, `search_docs`, `get_message_context`. Use `recall` when a
command fails, a package misbehaves, or an API is unfamiliar. Use `omni_search` when the
question is "where did we research X?" or "why did we choose Y?".

### 3. Record a decision

```bash
sila memo "<directive>" --files=<path>
```

MCP: `save_memo`. Call it immediately when an architectural decision, invariant, or
rejected alternative is established.

### 4. Record a solved problem

```bash
sila lesson "<symptom>" --cause="<cause>" --fix="<fix>" --files=<path>
```

MCP: `save_lesson`. Call it the moment a fix is proven, before the context is lost.

### 5. Record a reusable pattern

```bash
sila case "<intent>" --approach="<approach>" --invariant="<invariant>" --files=<path>
```

MCP: `save_case`. Use it for a module seam, design pattern, or structural rule that future
work should reuse.

### 6. Verify grounding

```bash
sila facts verify
sila verify
```

MCP: `get_project_facts`, `reconcile_drift`. Run before certification or
handoff; a memory store that cites phantom files is worse than no memory.

### 7. Gate before closure

```bash
sila gate --pre-commit
sila gate
```

MCP: `run_gate_pipeline`. The gate checks tests, formatting, changelog, architecture,
charters, provenance, and memory writes where applicable.

### 8. Handoff at session end

```bash
sila handoff "<summary>" --open="<next item>"
```

MCP: `save_handoff`, `get_latest_handoff`. The session-end hook writes an automatic
`sila-hook` handoff when none exists; an explicit handoff is still better because it names
the decisions and open items.

### 9. Sync the record

```bash
sila sync
```

MCP: `record_feedback`, `audit_goals`. Sync ingests new harness activity and refreshes the
cross-session record. Run it at session end or before a long handoff.

## Pre-Grep Archaeology

Before raw `grep`, `find`, or `git log`, search the continuity store:

```bash
sila search "<query>" --omni
sila search expand <session-id>
sila doc search "<query>"
sila doc search --pkg <package>
```

MCP: `omni_search`, `get_message_context`, `search_docs`, `get_doc`. Workspace search
(`fffind`, `ffgrep`, ripgrep) finds code in the current tree; Sila finds prior sessions,
decisions, lessons, and documentation across harnesses. Use both, Sila first for history.

## Temporal Activity

When asked what changed recently or what is in flight:

```bash
sila standup "last 24h"
sila digest --activity --since=24h
sila recap --since=7d
```

MCP: `get_activity_digest`, `get_status`. These are source-attributed and read-only.

## Specialist Workflows

```bash
sila guide <domain>                  # Living domain standards and guardrails
sila learn add --domain=<domain> --title=<title> --symptom=<symptom> --fix=<fix>
sila learn distil --save
sila persona list                    # Persona catalogue with evaluation rubrics
sila hire --goal=<slug>              # Recommend a specialist squad for a goal
sila hire assess
sila audit                           # Security, compliance, and quality audits
sila skill search <query>            # Discover and load reusable skills
sila skill load <name>
```

MCP: `get_operational_guidebook`, `save_domain_learning`, `consult_personas`, `audit_goal`,
`list_skills`, `skill_search`, `skill_load`, `route_workflow`.

## Symbol Blast Radius and Notes

Before renaming or deleting a symbol, check callers and contracts:

```bash
sila edit-check <symbol>
sila safe-delete <symbol>
sila note "<warning>" --symbol=<pkg::Sym>
sila recall --symbol=<pkg::Sym>
```

MCP: `check_symbol_blast_radius`, `save_memo`, `recall`. A `safe-delete` that would leave
live callers exits non-zero; fix the callers first.

## Goals, Roadmap, and Evidence

```bash
sila goals
sila goals ready
sila goals claim <slug> --agent=<name>
sila goals focus <slug> --session=$SESSION_ID
sila goals report <slug> "<note>"
sila goals certify <slug> --evidence=goals/<slug>/evidence.md
```

MCP: `list_goals`, `claim_goal`, `touch_goal`, `report_goal`, `certify_goal`,
`audit_goals`, `audit_goal`. A goal closes only with a `Verdict: PASS` evidence file and a
goal-attributed commit; docs-only and test-only commits do not close a goal.

## Quality Gates and Hygiene

```bash
sila gate --pre-commit
sila gate pipeline
sila doctor
sila heal --dry-run
```

MCP: `run_gate_pipeline`, `reconcile_drift`, `get_status`. The pre-commit gate is fast;
the pipeline runs isolated worktree verification before a push.

## First Contact and Onboarding

```bash
sila salam
sila intro
sila onboard
sila onboard check --strict
```

MCP: `salam`, `prime_context`. `salam` is the first command in a new repository; `onboard`
connects a legacy project and `check` names the exact fix for every gap.

## Feedback

If Sila itself gets in the way or misses something:

```bash
sila feedback "<what happened>" --type=issue|tip|idea
```

MCP: `record_feedback`. Keep working; the feedback is recorded for the next session.
