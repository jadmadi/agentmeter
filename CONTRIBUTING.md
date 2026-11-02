# Contributing

## License

By contributing, you agree that your contributions are licensed under the
GNU Affero General Public License v3.0 only, matching the project
(inbound = outbound). There is no CLA.

Add `SPDX-License-Identifier: AGPL-3.0-only` to every new `.go`, `.py`,
`.sh` and `.html` file. `scripts/check-licenses.sh` enforces this.

## Before a pull request

- Run `./check.sh`.
- Add tests or fixtures for behavior changes.
- Use synthetic fixtures only. Never commit real session data, prompts,
  titles, project paths, API keys or other personal telemetry. The `data/`
  directory is generated and gitignored.
- If you add or update a Go dependency, update
  `third_party/licenses/`, `third_party/licenses/modules.txt` and
  `THIRD_PARTY_NOTICES.md`, then re-run `./check.sh`.
- Keep the visible license/source footer in the four HTML report shells.

## Scope

AgentMeter is local-first and read-only. It must not upload telemetry, and it
must not read or emit prompts, assistant text, tool arguments or file
contents. Changes that weaken those guarantees need an explicit design
discussion first.

## Pull requests

Keep changes focused, describe the behavior and tests, and call out any
schema or contract change. Contract changes also need `CONTRACT.md` updates
and fixture coverage.
