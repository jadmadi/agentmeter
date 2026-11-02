# Security policy

## Supported versions

The latest tagged release and the `main` branch receive security fixes.
Older releases are supported on a best-effort basis.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting:

https://github.com/jadmadi/agentmeter/security/advisories/new

Do not open a public issue for a suspected vulnerability. Include the version
or commit, OS/architecture, a minimal reproducer, and the impact you observed.

## Scope

AgentMeter parses local session stores written by Command Code, OpenCode and
Muse, then emits local HTML/JSON reports. Relevant areas include:

- parsing untrusted or malformed session logs and SQLite stores
- report HTML/JS output and path handling
- CLI argument handling and file writes
- anything that could read or expose prompts, tool arguments or file contents

AgentMeter does not upload telemetry and does not ship secrets. Vulnerabilities
in the underlying agents or third-party dependencies are usually best reported
upstream, but tell us if AgentMeter's use of them creates a local risk.

## Response

We will acknowledge a report, triage it, and coordinate a fix and disclosure.
Credit is offered unless you prefer to remain anonymous.
