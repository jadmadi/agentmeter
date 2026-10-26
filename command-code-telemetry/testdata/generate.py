#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Generate the Command Code parity fixture tree.

The fixture is deliberately small but exercises every loader and report
branch: sidecars, hidden directories, nested directories, missing session
headers, ordered message entries, parent timing (positive, negative, missing),
duplicates, noOutput, missing timestamps, hooks with and without tool windows,
compactions, model changes, file snapshots, branches, tool/thinking/text
blocks, web tools, every timing bucket, fits (n >= 30), price fits, semantics
heuristics, project-label disambiguation and collision, empty lines and
invalid UTF-8.

Run from this directory:

    TZ=UTC python3 generate.py

It rewrites fixture/ and expected/telemetry-data.json (the Python builder's
output with meta.generatedAt removed).
"""

from __future__ import annotations

import datetime as dt
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
FIXTURE = HERE / "fixture"
EXPECTED = HERE / "expected" / "telemetry-data.json"
TOOL = HERE.parent
PY = TOOL / "py" / "telemetry.py"

MODEL_FAST = "TestCorp/Fast-1"
MODEL_SLOW = "OtherLab/Slow-2"

# Price model the Fast-1 rows follow exactly: $/1M tokens by category.
FRESH_RATE = 1.0
CACHE_RATE = 0.1
WRITE_RATE = 1.2
OUTPUT_RATE = 3.0

_parent_counter = 0


def price(input_tokens: int, cache_read: int, cache_write: int, output_tokens: int) -> float:
    fresh = max(0, input_tokens - cache_read - cache_write)
    return (
        fresh * FRESH_RATE
        + cache_read * CACHE_RATE
        + cache_write * WRITE_RATE
        + output_tokens * OUTPUT_RATE
    ) / 1e6


def stamp(day: int, hour: int, minute: int, second: int, millis: int = 0) -> dt.datetime:
    return dt.datetime(2026, 9, day, hour, minute, second, millis * 1000, tzinfo=dt.timezone.utc)


def iso(value: dt.datetime) -> str:
    return value.strftime("%Y-%m-%dT%H:%M:%S.") + f"{value.microsecond // 1000:03d}Z"


def session_record(session_id: str, cwd: str) -> dict:
    return {
        "type": "session",
        "version": 3,
        "id": session_id,
        "timestamp": iso(stamp(1, 7, 59, 0)),
        "cwd": cwd,
    }


def user_record(message_id: str, when: dt.datetime, branch: str | None = None) -> dict:
    record = {
        "type": "message",
        "id": message_id,
        "timestamp": iso(when),
        "message": {"role": "user", "content": [{"type": "text", "text": "go"}]},
    }
    if branch:
        record["gitBranch"] = branch
    return record


def parent_record(before: dt.datetime, seconds: float) -> dict:
    """A uniquely identified prompt record `seconds` before `before`."""
    global _parent_counter
    _parent_counter += 1
    start = before - dt.timedelta(seconds=seconds)
    return {
        "type": "message",
        "id": f"parent-{_parent_counter:04d}",
        "timestamp": iso(start),
        "message": {"role": "user", "content": [{"type": "text", "text": "prompt"}]},
    }


def reply(
    message_id: str,
    parent_id: str | None,
    when: dt.datetime | None,
    *,
    model: str,
    out: int,
    inp: int,
    cache_read: int,
    cache_write: int,
    cost: float,
    blocks: list[dict] | None = None,
    effort: str | None = "high",
) -> dict:
    record = {
        "type": "message",
        "id": message_id,
        "message": {
            "role": "assistant",
            "content": blocks if blocks is not None else [{"type": "text", "text": "ok"}],
        },
        "usage": {
            "inputTokens": inp,
            "outputTokens": out,
            "cacheReadTokens": cache_read,
            "cacheWriteTokens": cache_write,
            "costUsd": cost,
        },
        "model": model,
    }
    if parent_id is not None:
        record["parentId"] = parent_id
    if when is not None:
        record["timestamp"] = iso(when)
    if effort is not None:
        record["effort"] = effort
    return record


def timed_row(
    message_id: str,
    when: dt.datetime,
    seconds: float,
    *,
    model: str,
    out: int,
    inp: int,
    cache_read: int,
    cache_write: int,
    blocks: list[dict] | None = None,
    effort: str | None = "high",
) -> list[dict]:
    """A prompt/reply pair whose duration is exactly `seconds`."""
    parent = parent_record(when, seconds)
    record = reply(
        message_id,
        parent["id"],
        when,
        model=model,
        out=out,
        inp=inp,
        cache_read=cache_read,
        cache_write=cache_write,
        cost=price(inp, cache_read, cache_write, out),
        blocks=blocks,
        effort=effort,
    )
    return [parent, record]


def write(path: Path, lines: list) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = b""
    for line in lines:
        if isinstance(line, bytes):
            payload += line
        elif isinstance(line, str):
            payload += line.encode("utf-8")
        else:
            payload += json.dumps(line).encode("utf-8")
        payload += b"\n"
    path.write_bytes(payload)


def build_session_a() -> list[str | bytes]:
    lines: list[str | bytes] = [json.dumps(session_record("sess-alpha", "/work/alpha"))]
    lines.append(json.dumps(user_record("user-a-0", stamp(1, 7, 59, 30), branch="main")))
    lines.append(json.dumps(user_record("user-a-0b", stamp(1, 7, 59, 45), branch="feature/hooks")))

    # 32 timed rows: a clean linear duration model (duration fit) and an exact
    # price model (price fit). Half on day 1, half on day 2.
    for index in range(32):
        if index < 16:
            when = stamp(1, 8, 0, 0) + dt.timedelta(minutes=5 * index)
        else:
            when = stamp(2, 9, 0, 0) + dt.timedelta(minutes=5 * (index - 16))
        out_tokens = 100 + index * 100
        seconds = 2.0 + out_tokens / 50.0
        input_tokens = 5000 + index * 100
        cache_read = 4000
        cache_write = 500 if index % 7 == 0 else 0
        if index % 3 == 0:
            blocks = [{"type": "text", "text": "answer"}]
        elif index % 3 == 1:
            blocks = [{"type": "tool_use", "id": f"t{index}", "name": "read_file", "input": {}}]
        else:
            blocks = [
                {"type": "tool_use", "id": f"g{index}", "name": "grep", "input": {}},
                {"type": "tool_use", "id": f"b{index}", "name": "bash", "input": {}},
            ]
        if index % 4 == 0:
            blocks = [{"type": "thinking", "thinking": "hmm", "signature": ""}] + blocks
        lines.extend(timed_row(
            f"a-{index}",
            when,
            seconds,
            model=MODEL_FAST,
            out=out_tokens,
            inp=input_tokens,
            cache_read=cache_read,
            cache_write=cache_write,
            blocks=blocks,
            effort="high" if index % 2 == 0 else None,
        ))

    # Hooks: paired window, unpaired pre, unpaired post, unknown exit, unnamed.
    lines.append(json.dumps({
        "eventName": "PreToolUse", "toolUseId": "tu1", "toolName": "read_file",
        "hookName": "guard", "hookType": "pre", "duration": 12, "exitCode": 0,
        "timestamp": iso(stamp(1, 8, 10, 0)),
    }))
    lines.append(json.dumps({
        "eventName": "PostToolUse", "toolUseId": "tu1", "toolName": "read_file",
        "hookName": "guard", "hookType": "post", "duration": 40, "exitCode": 2,
        "blocked": True, "timestamp": iso(stamp(1, 8, 10, 3)),
    }))
    lines.append(json.dumps({
        "eventName": "PreToolUse", "toolUseId": "tu2", "toolName": "bash",
        "hookName": "guard", "hookType": "pre", "duration": 9, "exitCode": 0,
        "timestamp": iso(stamp(1, 8, 20, 0)),
    }))
    lines.append(json.dumps({
        "eventName": "PostToolUse", "toolUseId": "tu3", "toolName": "grep",
        "hookName": "guard", "hookType": "post", "duration": 21, "exitCode": 0,
        "timestamp": iso(stamp(1, 8, 30, 1)),
    }))
    lines.append(json.dumps({
        "eventName": "SessionStart", "hookName": "timeout-hook", "duration": 1500,
        "timestamp": iso(stamp(1, 7, 59, 1)),
    }))
    lines.append(json.dumps({
        "eventName": "SessionStop", "hookName": "", "duration": "slow",
        "timestamp": iso(stamp(1, 9, 30, 0)),
    }))

    # Events: compaction, model change (both shapes), file snapshot.
    lines.append(json.dumps({
        "type": "compaction", "timestamp": iso(stamp(1, 9, 0, 0)),
        "sessionId": "sess-alpha", "tokensBefore": 150000,
    }))
    lines.append(json.dumps({
        "type": "model_change", "timestamp": iso(stamp(1, 9, 5, 0)),
        "sessionId": "sess-alpha", "model": MODEL_SLOW,
    }))
    lines.append(json.dumps({
        "type": "model_change", "timestamp": iso(stamp(1, 9, 6, 0)),
        "sessionId": "sess-alpha", "to": MODEL_FAST,
    }))
    lines.append(json.dumps({"type": "file-history-snapshot", "timestamp": iso(stamp(1, 9, 7, 0))}))

    # Ledger edges.
    lines.append(json.dumps(reply(
        "a-noout", "user-a-0", stamp(1, 9, 10, 0), model=MODEL_FAST, out=0,
        inp=1000, cache_read=0, cache_write=0, cost=0.002,
    )))
    lines.append(json.dumps(reply(
        "a-nots", "user-a-0", None, model=MODEL_FAST, out=500,
        inp=2000, cache_read=0, cache_write=0, cost=price(2000, 0, 0, 500),
    )))

    # Timing edges use the same price model, so the price fit stays exact.
    lines.append(json.dumps(reply(
        "a-noparent", None, stamp(1, 9, 12, 0), model=MODEL_FAST, out=400,
        inp=1500, cache_read=0, cache_write=0, cost=price(1500, 0, 0, 400),
    )))
    future = parent_record(stamp(1, 9, 20, 0), 0)
    future["timestamp"] = iso(stamp(1, 9, 20, 0))
    lines.append(json.dumps(future))
    lines.append(json.dumps(reply(
        "a-negparent", future["id"], stamp(1, 9, 15, 0), model=MODEL_FAST, out=450,
        inp=1500, cache_read=0, cache_write=0, cost=price(1500, 0, 0, 450),
    )))
    lines.extend(timed_row(
        "a-web", stamp(1, 10, 0, 0), 5.0, model=MODEL_FAST, out=250, inp=900,
        cache_read=0, cache_write=0,
        blocks=[{"type": "tool_use", "id": "w1", "name": "web_search", "input": {}}],
    ))
    lines.extend(timed_row(
        "a-tooshort", stamp(1, 10, 5, 0), 1.0, model=MODEL_FAST, out=10, inp=500,
        cache_read=0, cache_write=0,
    ))
    lines.extend(timed_row(
        "a-shortdur", stamp(1, 10, 10, 0), 0.2, model=MODEL_FAST, out=100, inp=700,
        cache_read=0, cache_write=0,
    ))
    lines.extend(timed_row(
        "a-fast", stamp(1, 10, 15, 0), 5.0, model=MODEL_FAST, out=3000, inp=9000,
        cache_read=2000, cache_write=0,
    ))
    lines.extend(timed_row(
        "a-slow", stamp(1, 10, 20, 0), 50.0, model=MODEL_FAST, out=100, inp=600,
        cache_read=0, cache_write=0,
    ))
    lines.extend(timed_row(
        "a-long", stamp(1, 10, 25, 0), 300.0, model=MODEL_FAST, out=10000, inp=20000,
        cache_read=5000, cache_write=0,
    ))
    lines.extend(timed_row(
        "a-nomodel", stamp(1, 10, 30, 0), 3.0, model="", out=30, inp=300,
        cache_read=0, cache_write=0,
    ))
    # Duplicate of session-b's copy: the later copy becomes a ledger row.
    lines.extend(timed_row(
        "dup-1", stamp(1, 10, 35, 0), 8.0, model=MODEL_FAST, out=500, inp=2500,
        cache_read=100, cache_write=50,
    ))

    # Not JSON, blank, invalid UTF-8: all skipped.
    lines.append(b'{"type":"message","id":"bad-utf8","message":{"role":"assistant","content":"\xff\xfe"}}')
    lines.append("")
    lines.append("{not json")
    lines.append(b"\xff\xfe\x00garbage")
    lines.append(json.dumps(user_record("user-a-last", stamp(1, 10, 40, 0))))
    return lines


def main() -> int:
    if FIXTURE.exists():
        shutil.rmtree(FIXTURE)
    EXPECTED.parent.mkdir(parents=True, exist_ok=True)

    # ---- session-a: the main corpus.
    write(FIXTURE / "alpha" / "session-a.jsonl", build_session_a())
    write(FIXTURE / "alpha" / "session-a.meta.json", [
        json.dumps({"title": "Alpha ✓ session", "entrypoint": "print", "model": MODEL_FAST}),
    ])

    # ---- session-b: the first dup-1 copy plus a small model.
    lines: list[str | bytes] = [json.dumps(session_record("sess-beta", "/work/beta/app"))]
    lines.append(json.dumps(user_record("user-b-0", stamp(3, 10, 0, 0), branch="work")))
    lines.extend(timed_row(
        "dup-1", stamp(3, 10, 0, 10), 8.0, model=MODEL_FAST, out=500, inp=2500,
        cache_read=100, cache_write=50,
    ))
    for index in range(6):
        blocks = [{"type": "thinking", "thinking": "plan", "signature": ""}]
        if index % 2:
            blocks = blocks + [
                {"type": "tool_use", "id": f"tls{index}", "name": "list_files", "input": {}},
            ]
        parent = parent_record(stamp(3, 10, 1, index * 9), 15.0)
        lines.append(json.dumps(parent))
        lines.append(json.dumps(reply(
            f"b-{index}", parent["id"], stamp(3, 10, 1, index * 9),
            model=MODEL_SLOW, out=300, inp=1000, cache_read=2000, cache_write=0,
            cost=(1000 * 0.5 + 2000 * 0.05 + 300 * 1.0) / 1e6, blocks=blocks, effort="low",
        )))
    write(FIXTURE / "alpha" / "deep" / "session-b.jsonl", lines)
    write(FIXTURE / "alpha" / "deep" / "session-b.meta.json", [
        json.dumps({"title": "Nested", "entrypoint": "interactive"}),
    ])

    # ---- session-c/d/e: project label disambiguation and unknown projects.
    lines = [json.dumps(session_record("sess-gamma", "/work/gamma/app"))]
    lines.append(json.dumps(user_record("user-c-0", stamp(2, 12, 0, 0), branch="main")))
    lines.append(json.dumps(reply(
        "c-0", "user-c-0", stamp(2, 12, 0, 5), model=MODEL_SLOW, out=800,
        inp=3000, cache_read=1000, cache_write=0, cost=0.02,
    )))
    write(FIXTURE / "beta" / "app" / "session-c.jsonl", lines)

    lines = [json.dumps(session_record("sess-delta", "/work/trailing/"))]
    lines.append(json.dumps(user_record("user-d-0", stamp(4, 14, 0, 0))))
    lines.append(json.dumps(reply(
        "d-0", "user-d-0", stamp(4, 14, 0, 5), model=MODEL_SLOW, out=120,
        inp=800, cache_read=0, cache_write=0, cost=0.001,
    )))
    lines.extend(timed_row(
        "dup-1", stamp(4, 14, 0, 10), 8.0, model=MODEL_FAST, out=500, inp=2500,
        cache_read=100, cache_write=50,
    ))
    write(FIXTURE / "gamma" / "session-d.jsonl", lines)

    lines = []
    lines.append(json.dumps(user_record("user-e-0", stamp(5, 16, 0, 0))))
    parent = parent_record(stamp(5, 16, 0, 5), 4.0)
    lines.append(json.dumps(parent))
    lines.append(json.dumps(reply(
        "e-0", parent["id"], stamp(5, 16, 0, 5), model=MODEL_FAST, out=200,
        inp=1200, cache_read=0, cache_write=0, cost=price(1200, 0, 0, 200),
        blocks=[{"type": "tool_use", "id": "x1", "name": "read_file", "input": {}}],
    )))
    parent = parent_record(stamp(5, 16, 0, 15), 12.0)
    lines.append(json.dumps(parent))
    lines.append(json.dumps(reply(
        "e-1", parent["id"], stamp(5, 16, 0, 15), model=MODEL_FAST, out=600,
        inp=2200, cache_read=0, cache_write=0, cost=price(2200, 0, 0, 600),
    )))
    write(FIXTURE / "nosession" / "session-e.jsonl", lines)

    # ---- label collision: same basename and same parent name.
    for index, cwd in enumerate(("/another/work/app", "/other/work/app")):
        name = f"session-{'fg'[index]}"
        tag = name[-1]
        when = stamp(6, 18, 0, 0) + dt.timedelta(minutes=index)
        lines = [json.dumps(session_record(f"sess-{tag}", cwd))]
        lines.append(json.dumps(user_record(f"user-{tag}-0", when)))
        lines.extend(timed_row(
            f"{tag}-0", when + dt.timedelta(seconds=5), 4.0, model=MODEL_FAST,
            out=400, inp=1600, cache_read=0, cache_write=0,
        ))
        write(FIXTURE / "labels" / name / f"{name}.jsonl", lines)

    # ---- ignored: hidden directories, empty file, uppercase extension.
    write(FIXTURE / "alpha" / ".hidden" / "session-hidden.jsonl", [
        json.dumps(session_record("sess-hidden", "/work/hidden")),
        json.dumps(reply(
            "hidden-0", None, stamp(1, 10, 0, 0), model=MODEL_FAST, out=100,
            inp=100, cache_read=0, cache_write=0, cost=0.1,
        )),
    ])
    write(FIXTURE / ".hidden-top" / "session-hidden2.jsonl", [
        json.dumps(session_record("sess-hidden2", "/work/hidden2")),
    ])
    write(FIXTURE / "empty" / "session-empty.jsonl", [])
    write(FIXTURE / "empty" / "session-empty.meta.json", [json.dumps({"title": "Empty"})])
    write(FIXTURE / "empty" / "session-empty.JSONL", [
        json.dumps(session_record("sess-upper", "/work/upper")),
    ])

    # ---- expected report from the Python builder.
    out = HERE / "expected" / ".build"
    if out.exists():
        shutil.rmtree(out)
    env = dict(os.environ, TZ="UTC")
    subprocess.run(
        [sys.executable, str(PY), "--root", str(FIXTURE), "--out", str(out)],
        check=True, env=env, cwd=str(TOOL / "py"), capture_output=True,
    )
    data = json.loads((out / "telemetry-data.json").read_text(encoding="utf-8"))
    data["meta"].pop("generatedAt", None)
    # The checkout path is not reproducible; both sides normalize it.
    data["meta"]["rootLabel"] = "<fixture>"
    EXPECTED.write_text(json.dumps(data, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    shutil.rmtree(out)
    print(f"wrote {FIXTURE} and {EXPECTED}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
