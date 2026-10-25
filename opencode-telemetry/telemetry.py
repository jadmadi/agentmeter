#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""
OpenCode session telemetry.

Reconstructs per-step throughput, token/cache usage, cost, reasoning, tools,
sessions and time patterns from the OpenCode SQLite store. No prompts,
assistant text, tool arguments or file contents are read or emitted.

OpenCode records a much richer timing model than an append-only transcript:

    time.created    request sent
    time.streamed   model finished streaming (tools start here)
    time.completed  step finished, including tool execution

So the LLM latency is `streamed - created`, the tool/commit tail is
`completed - streamed`, and reasoning blocks carry their own start/end times.
Tokens are normalized: `input` is fresh input, `cache.read`/`cache.write` are
separate, and `output` excludes `reasoning`. Recorded cost fits
`output + reasoning` at the output rate.

Outputs:
    data/telemetry-data.json   machine-readable aggregate
    data/telemetry-data.js     same data as `window.OPENCODE_TELEMETRY`

Usage:
    python3 telemetry.py                  # build the data
    python3 telemetry.py --serve          # build, serve and open the report
    python3 telemetry.py --models deepseek,qwen --since 2026-09-01
"""

from __future__ import annotations

import argparse
import bisect
import collections
import datetime as dt
import hashlib
import json
import math
import os
import sqlite3
import statistics
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from telemetry_common import (  # noqa: E402
    SCHEMA_VERSION,
    TIMING_REASONS,
    bootstrap_median_ci,
    close,
    linear_fit,
    median_or_none,
    percentile,
    r1,
    r2,
    robust_z,
    safe_div,
    solve_linear,
    standardize,
    validate_common,
)

def default_db_path() -> str:
    candidates = [
        Path.home() / ".local/share/opencode/opencode.db",
        Path.home() / "Library/Application Support/opencode/opencode.db",
        Path(os.environ.get("APPDATA", "")) / "opencode" / "opencode.db",
    ]
    for candidate in candidates:
        if candidate.exists():
            return str(candidate)
    return str(candidates[0])


DEFAULT_DB = default_db_path()
DEFAULT_THRESHOLDS = {
    "minSeconds": 0.5,
    "maxSeconds": 300.0,
    "minTokens": 20,
    "minRate": 3.0,
    "maxRate": 400.0,
}
THRESHOLDS = dict(DEFAULT_THRESHOLDS)

# ---------------------------------------------------------------- utilities


def local_tz_label() -> str:
    now = dt.datetime.now().astimezone()
    offset = now.utcoffset() or dt.timedelta(0)
    minutes = int(offset.total_seconds() // 60)
    sign = "+" if minutes >= 0 else "-"
    return f"UTC{sign}{abs(minutes) // 60:02d}:{abs(minutes) % 60:02d}"


def connect_ro(path: str) -> sqlite3.Connection:
    db_path = Path(path).expanduser()
    if not db_path.exists():
        raise SystemExit(
            f"OpenCode database not found at {db_path}. "
            "Pass --db <path> or set the correct OpenCode data directory."
        )
    uri = db_path.resolve().as_uri() + "?mode=ro"
    try:
        conn = sqlite3.connect(uri, uri=True, timeout=15)
    except sqlite3.OperationalError as exc:
        raise SystemExit(
            f"Could not open OpenCode database at {db_path}: {exc}. "
            "Pass --db <path> if it lives elsewhere."
        ) from exc
    conn.row_factory = sqlite3.Row
    tables = {
        row[0]
        for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
    }
    missing = {"session_v2", "session_message"} - tables
    if missing:
        conn.close()
        raise SystemExit(
            "OpenCode database schema not recognized: missing "
            + ", ".join(sorted(missing))
            + ". This report supports the session_v2/session_message store."
        )
    return conn


def short_model(provider: str, model_id: str) -> str:
    return model_id.split("/")[-1] if model_id else provider


# ---------------------------------------------------------------- loading


DEDUP_DROP_KEYS = {
    "id",
    "parentID",
    "sessionID",
    "sessionId",
    "messageID",
    "callID",
    "snapshot",
    "path",
    "time_created",
    "time_updated",
}


def normalize_payload(value):
    """Return a fork-comparable payload with ids and location fields removed."""
    if isinstance(value, dict):
        return {
            key: normalize_payload(item)
            for key, item in value.items()
            if key not in DEDUP_DROP_KEYS
        }
    if isinstance(value, list):
        return [normalize_payload(item) for item in value]
    return value


def merge_intervals(intervals: list[tuple[int, int]]) -> tuple[float, float]:
    """Return (union_seconds, span_seconds) for millisecond intervals."""
    if not intervals:
        return 0.0, 0.0
    ordered = sorted(intervals)
    total = 0
    cur_start, cur_end = ordered[0]
    for start, end in ordered[1:]:
        if start <= cur_end:
            cur_end = max(cur_end, end)
        else:
            total += cur_end - cur_start
            cur_start, cur_end = start, end
    total += cur_end - cur_start
    span = max(end for _, end in ordered) - min(start for start, _ in ordered)
    return total / 1000.0, span / 1000.0


def allocate_tool_time(calls: list[dict]) -> list[float]:
    """Allocate non-overlapping tool time so per-tool shares are additive.

    Non-overlapping calls keep their own runtime. When calls overlap (parallel
    tools), the union duration is split proportionally to each call's runtime,
    so the allocations for a step sum to the step's tool union, never more.
    """
    allocations = [0.0] * len(calls)
    intervals = []
    durations = []
    indexes = []
    for index, call in enumerate(calls):
        ran = call.get("ran")
        done = call.get("completed")
        if ran and done and done > ran:
            intervals.append((ran, done))
            durations.append((done - ran) / 1000.0)
            indexes.append(index)
    if not intervals:
        return allocations
    union, _ = merge_intervals(intervals)
    total_runtime = sum(durations)
    if total_runtime <= 0:
        return allocations
    scale = min(1.0, union / total_runtime) if total_runtime else 0.0
    for index, duration in zip(indexes, durations):
        allocations[index] = duration * scale
    return allocations


def load_rows(
    db_path: str,
    model_filters: list[str],
    since: str | None,
    until: str | None,
    include_titles: bool,
    full_paths: bool,
):
    conn = connect_ro(db_path)
    sessions_meta: dict[str, dict] = {}
    for row in conn.execute(
        "SELECT id, title, slug, directory, parent_id, fork_session_id, version, time_created FROM session_v2"
    ):
        sessions_meta[row["id"]] = {
            "title": row["title"],
            "slug": row["slug"],
            "directory": row["directory"],
            "parent": row["parent_id"],
            "fork": row["fork_session_id"],
            "version": row["version"],
            "created": row["time_created"],
        }

    rows: list[dict] = []
    ledger: list[dict] = []
    seen: dict[tuple, set[str]] = {}
    sessions: set[str] = set()
    duplicates = 0
    duplicate_sessions = collections.Counter()
    duplicate_cost = 0.0
    duplicate_tokens = 0
    collisions = 0
    empty_steps = 0
    error_steps = 0
    scanned = 0
    considered = 0
    no_created = 0
    max_created = 0
    timing = {
        "plausible": 0, "noTiming": 0, "tooShort": 0, "tooShortDuration": 0,
        "tooLong": 0, "slowRate": 0, "fastRate": 0,
    }
    events = {
        "types": collections.Counter(),
        "finishes": collections.Counter(),
        "toolErrors": collections.Counter(),
        "toolCalls": collections.Counter(),
        "versions": collections.Counter(),
        "compactions": 0,
        "compactionEvents": [],
        "modelSwitches": 0,
        "modelSwitchEvents": [],
        "agentSwitches": 0,
        "idle": 0,
        "shell": 0,
        "errors": 0,
        "noStream": 0,
        "negativeOverhead": 0,
    }
    sessions_total = len(sessions_meta)
    sessions_root = 0
    sessions_subagent = 0
    for meta in sessions_meta.values():
        if meta["parent"]:
            sessions_subagent += 1
        else:
            sessions_root += 1
        events["versions"][meta["version"] or "unknown"] += 1

    cursor = conn.execute(
        "SELECT m.id, m.session_id, m.time_created, m.data "
        "FROM session_message m LEFT JOIN session_v2 s ON s.id = m.session_id "
        "WHERE m.type = 'assistant' "
        "ORDER BY (s.fork_session_id IS NOT NULL) ASC, m.time_created, m.id"
    )
    for rec in cursor:
        scanned += 1
        try:
            d = json.loads(rec["data"])
        except (ValueError, TypeError):
            continue

        model = d.get("model") or {}
        provider = model.get("providerID") or ""
        model_id = model.get("id") or ""
        model_key = f"{provider}/{model_id}" if provider else model_id
        if not model_key:
            model_key = "(unknown)"
        if model_filters and not any(
            term.lower() in model_key.lower() or term.lower() in model_id.lower()
            for term in model_filters
        ):
            continue

        tokens = d.get("tokens") or {}
        output = int(tokens.get("output") or 0)
        reasoning = int(tokens.get("reasoning") or 0)
        cache = tokens.get("cache") or {}
        cache_read = int(cache.get("read") or 0)
        cache_write = int(cache.get("write") or 0)
        fresh_input = int(tokens.get("input") or 0)
        generated = output + reasoning

        t = d.get("time") or {}
        created = int(t.get("created") or 0)
        streamed = int(t.get("streamed") or 0)
        completed = int(t.get("completed") or 0)
        if created > max_created:
            max_created = created
        if not created:
            no_created += 1
            continue
        ts = dt.datetime.fromtimestamp(created / 1000, tz=dt.timezone.utc).astimezone()
        if since or until:
            day = ts.date().isoformat()
            if since and day < since:
                continue
            if until and day > until:
                continue
        considered += 1

        blocks = d.get("content") or []
        tool_names: list[str] = []
        tool_details: list[dict] = []
        tool_windows: list[tuple[int, int]] = []
        first_block_created = None
        n_reasoning = 0
        n_text = 0
        for block in blocks:
            if not isinstance(block, dict):
                continue
            kind = block.get("type")
            if kind == "tool":
                name = block.get("name") or "(unnamed)"
                tool_names.append(name)
                bt = block.get("time") or {}
                ran = int(bt.get("ran") or 0)
                done = int(bt.get("completed") or 0)
                block_created = int(bt.get("created") or 0)
                runtime = (done - ran) / 1000.0 if ran and done and done > ran else None
                state = block.get("state") or {}
                tool_details.append(
                    {
                        "name": name,
                        "ran": ran,
                        "completed": done,
                        "runtime": runtime,
                        "error": state.get("status") == "error",
                    }
                )
                if ran and done and done > ran:
                    tool_windows.append((ran, done))
                if block_created:
                    first_block_created = block_created if first_block_created is None else min(first_block_created, block_created)
            elif kind == "reasoning":
                n_reasoning += 1
                bt = block.get("time") or {}
                block_created = int(bt.get("created") or 0)
                if block_created:
                    first_block_created = block_created if first_block_created is None else min(first_block_created, block_created)
            elif kind == "text":
                n_text += 1

        # Generation ends when the stream ends. If OpenCode did not record
        # `streamed`, only trust `completed` for tool-free steps.
        if streamed and streamed > created:
            llm_end = streamed
        elif completed and completed > created and not tool_names:
            llm_end = completed
        else:
            llm_end = 0
        llm_seconds = (llm_end - created) / 1000 if llm_end else None
        total_seconds = (completed - created) / 1000 if completed > created else None
        tool_seconds, tool_span = merge_intervals(tool_windows)
        tool_gap = max(0.0, tool_span - tool_seconds) if tool_windows else 0.0
        overhead_seconds = None
        if total_seconds is not None and llm_seconds is not None:
            raw_overhead = total_seconds - llm_seconds - tool_seconds
            if raw_overhead < 0:
                events["negativeOverhead"] += 1
                overhead_seconds = 0.0
            else:
                overhead_seconds = raw_overhead

        # TTFT: first reasoning/tool block after the request starts. Text-only
        # replies have no timestamps in the store, so TTFT stays unknown.
        ttft = None
        decode_seconds = None
        decode_rate = None
        if first_block_created and created and llm_seconds:
            candidate = (first_block_created - created) / 1000.0
            if 0.05 <= candidate < llm_seconds:
                ttft = candidate
                decode_seconds = llm_seconds - candidate
                if decode_seconds >= 0.1:
                    decode_rate = generated / decode_seconds

        fingerprint = (created, model_key, fresh_input, output, reasoning, cache_read, cache_write)
        normalized = json.dumps(
            normalize_payload(d), sort_keys=True, separators=(",", ":"), ensure_ascii=False
        )
        signature = hashlib.sha256(normalized.encode()).hexdigest()
        signatures = seen.setdefault(fingerprint, set())
        if signature in signatures:
            duplicates += 1
            duplicate_sessions[rec["session_id"]] += 1
            duplicate_cost += float(d.get("cost") or 0.0)
            duplicate_tokens += generated
            continue
        if signatures:
            # Same timing/token fingerprint but different payload: not a copy.
            collisions += 1
        signatures.add(signature)

        meta = sessions_meta.get(rec["session_id"]) or {}
        directory = meta.get("directory") or "(unknown)"
        project_path = directory
        project_label = directory if full_paths else (os.path.basename(directory.rstrip("/")) or directory)
        parent = bool(meta.get("parent"))
        title = None
        if include_titles:
            title = meta.get("title") or meta.get("slug")
        finish = d.get("finish") or "unknown"

        base = {
            "id": rec["id"],
            "model": model_key,
            "provider": provider or "unknown",
            "short": short_model(provider, model_id),
            "variant": model.get("variant") or "default",
            "agent": d.get("agent") or "unknown",
            "finish": finish,
            "ts": ts,
            "out": output,
            "reas": reasoning,
            "gen": generated,
            "inp": fresh_input,
            "cacheRead": cache_read,
            "cacheWrite": cache_write,
            "cost": float(d.get("cost") or 0.0),
            "tool": bool(tool_names),
            "tools": tool_names,
            "toolDetails": tool_details,
            "primaryTool": tool_names[0] if tool_names else "(final answer)",
            "nTools": len(tool_names),
            "nThinking": n_reasoning,
            "nText": n_text,
            "thinking": n_reasoning > 0,
            "dt": llm_seconds,
            "totalDt": total_seconds,
            "toolDt": tool_seconds if tool_windows else None,
            "toolSpanDt": tool_span if tool_windows else None,
            "toolGapDt": tool_gap if tool_windows else None,
            "overheadDt": overhead_seconds,
            "ttft": ttft,
            "decodeDt": decode_seconds,
            "decodeRate": decode_rate,
            "sessionId": rec["session_id"],
            "sessionTitle": title,
            "project": project_path,
            "projectLabel": project_label,
            "entry": "subagent" if parent else "root",
            "version": meta.get("version"),
            "streamed": bool(streamed),
        }

        # Assistant health follows the selected rows and the error ledger.
        events["finishes"][finish] += 1
        if finish == "error":
            events["errors"] += 1
        if not streamed:
            events["noStream"] += 1
        for tool in tool_details:
            events["toolCalls"][tool["name"]] += 1
            if tool["error"]:
                events["toolErrors"][tool["name"]] += 1

        if generated <= 0 or finish == "error":
            ledger.append(
                {
                    **base,
                    "reason": (
                        "both" if generated <= 0 and finish == "error"
                        else "error" if finish == "error"
                        else "empty"
                    ),
                    "empty": generated <= 0,
                    "error": finish == "error",
                }
            )
            if generated <= 0:
                empty_steps += 1
            if finish == "error":
                error_steps += 1
            continue

        rows.append(base)
        sessions.add(rec["session_id"])

    # Non-assistant event types are store-wide; assistant health above follows
    # the selected window.
    for row in conn.execute(
        "SELECT type, COUNT(*) AS n FROM session_message WHERE type NOT IN ('assistant', 'user') GROUP BY type"
    ):
        events["types"][row["type"]] = row["n"]
    for row in conn.execute(
        "SELECT session_id, time_created, type, data FROM session_message "
        "WHERE type IN ('compaction', 'model-switched') ORDER BY time_created"
    ):
        event_ts = (
            dt.datetime.fromtimestamp(
                int(row["time_created"]) / 1000, tz=dt.timezone.utc
            ).astimezone()
            if row["time_created"] else None
        )
        payload = {}
        if row["data"]:
            try:
                payload = json.loads(row["data"])
            except (ValueError, TypeError):
                payload = {}
        if row["type"] == "compaction":
            events["compactionEvents"].append(
                {
                    "ts": event_ts,
                    "sessionId": row["session_id"],
                    "tokensBefore": payload.get("tokensBefore") or payload.get("tokens"),
                }
            )
        elif row["type"] == "model-switched":
            events["modelSwitchEvents"].append(
                {
                    "ts": event_ts,
                    "sessionId": row["session_id"],
                    "model": payload.get("model") or payload.get("to") or payload.get("id"),
                }
            )
    events["compactions"] = events["types"].get("compaction", 0)
    events["modelSwitches"] = events["types"].get("model-switched", 0)
    events["agentSwitches"] = events["types"].get("agent-switched", 0)
    events["idle"] = events["types"].get("idle", 0)
    events["shell"] = events["types"].get("shell", 0)
    conn.close()

    return {
        "rows": rows,
        "ledger": ledger,
        "sessions": sessions,
        "sessionsTotal": sessions_total,
        "sessionsRoot": sessions_root,
        "sessionsSubagent": sessions_subagent,
        "sessionsMeta": sessions_meta,
        "duplicates": duplicates,
        "duplicateSessions": duplicate_sessions,
        "duplicateCost": duplicate_cost,
        "duplicateTokens": duplicate_tokens,
        "maxCreatedMs": max_created,
        "collisions": collisions,
        "emptySteps": empty_steps,
        "errorSteps": error_steps,
        "scanned": scanned,
        "considered": considered,
        "noCreated": no_created,
        "events": events,
    }

# ---------------------------------------------------------------- analysis


def is_timed(row: dict) -> bool:
    d = row.get("dt")
    if d is None or d <= 0:
        return False
    rate = row["gen"] / d
    t = THRESHOLDS
    return (
        t["minSeconds"] <= d <= t["maxSeconds"]
        and row["gen"] >= t["minTokens"]
        and t["minRate"] <= rate <= t["maxRate"]
    )


def timing_bucket(row: dict) -> str:
    d = row.get("dt")
    if d is None or d <= 0:
        return "noTiming"
    t = THRESHOLDS
    if row["gen"] < t["minTokens"]:
        return "tooShort"
    if d < t["minSeconds"]:
        return "tooShortDuration"
    if d > t["maxSeconds"]:
        return "tooLong"
    rate = row["gen"] / d
    if rate < t["minRate"]:
        return "slowRate"
    if rate > t["maxRate"]:
        return "fastRate"
    return "plausible"


def summarize(rows: list[dict]) -> dict:
    if not rows:
        return {
            "n": 0, "tokens": 0, "visible": 0, "reasoning": 0, "seconds": 0,
            "weighted": None, "visibleWeighted": None, "median": None,
            "visibleMedian": None, "p10": None, "p25": None, "p75": None,
            "p90": None, "p99": None, "max": None,
            "ttftMedian": None, "ttftN": 0, "decodeMedian": None, "decodeN": 0,
        }
    rates = sorted(row["gen"] / row["dt"] for row in rows)
    visible_rates = sorted(row["out"] / row["dt"] for row in rows)
    ttfts = sorted(row["ttft"] for row in rows if row.get("ttft") is not None)
    decodes = sorted(row["decodeRate"] for row in rows if row.get("decodeRate") is not None)
    tokens = sum(row["gen"] for row in rows)
    visible = sum(row["out"] for row in rows)
    reasoning = sum(row["reas"] for row in rows)
    seconds = sum(row["dt"] for row in rows)
    return {
        "n": len(rows),
        "tokens": tokens,
        "visible": visible,
        "reasoning": reasoning,
        "seconds": round(seconds, 3),
        "ttftMedian": r1(statistics.median(ttfts)) if ttfts else None,
        "ttftN": len(ttfts),
        "decodeMedian": r1(statistics.median(decodes)) if decodes else None,
        "decodeN": len(decodes),
        "weighted": round(tokens / seconds, 1) if seconds else None,
        "visibleWeighted": round(visible / seconds, 1) if seconds else None,
        "median": r1(statistics.median(rates)),
        "visibleMedian": r1(statistics.median(visible_rates)),
        "p10": r1(percentile(rates, 10)),
        "p25": r1(percentile(rates, 25)),
        "p75": r1(percentile(rates, 75)),
        "p90": r1(percentile(rates, 90)),
        "p99": r1(percentile(rates, 99)),
        "max": r1(rates[-1]),
    }


def fit_rate(rows: list[dict]) -> dict | None:
    """Regress duration on generated tokens with a non-negative intercept."""
    fit = linear_fit(
        [row["gen"] for row in rows],
        [row["dt"] for row in rows],
        min_n=30,
    )
    if fit:
        fit["usable"] = fit["n"] >= 30 and (fit["r2"] or 0) >= 0.50
    return fit


def fit_prices(rows: list[dict]) -> dict | None:
    """Fit recorded cost against fresh input, cache read/write and generated tokens.

    OpenCode normalizes tokens: `input` excludes cache reads/writes, and
    `reasoning` is separate from `output`. The recorded cost fits
    `output + reasoning` at the output rate, so the last feature is the
    generated total. Returns $ per 1M tokens.
    """
    rs = [row for row in rows if row["cost"] > 0]
    if len(rs) < 30:
        return None

    def features(row):
        return [
            row["inp"] / 1e6,
            row["cacheRead"] / 1e6,
            row["cacheWrite"] / 1e6,
            row["gen"] / 1e6,
        ]

    sample = [features(row) for row in rs]
    active = [i for i in range(4) if any(f[i] > 0 for f in sample)]
    if not active:
        return None
    n = len(active)
    matrix = [[0.0] * n for _ in range(n)]
    rhs = [0.0] * n
    for feats, row in zip(sample, rs):
        vals = [feats[i] for i in active]
        for i in range(n):
            rhs[i] += vals[i] * row["cost"]
            for j in range(n):
                matrix[i][j] += vals[i] * vals[j]
    coeffs = solve_linear(matrix, rhs)
    if not coeffs:
        return None
    rates = [0.0] * 4
    for idx, value in zip(active, coeffs):
        rates[idx] = value
    if any(value < -0.01 for value in rates):
        return None
    rates = [max(0.0, value) for value in rates]
    input_rate, cache_rate, write_rate = rates[0], rates[1], rates[2]
    if cache_rate > input_rate * 1.001:
        return None
    if write_rate > max(input_rate * 3, 0.01):
        return None
    mean = sum(row["cost"] for row in rs) / len(rs)
    ss_res = sum((row["cost"] - sum(f[i] * rates[i] for i in range(4))) ** 2 for f, row in zip(sample, rs))
    ss_tot = sum((row["cost"] - mean) ** 2 for row in rs)
    r_squared = 1 - ss_res / ss_tot if ss_tot else None
    if r_squared is not None and r_squared < 0.85:
        return None
    return {
        "inputRate": round(rates[0], 4),
        "cacheReadRate": round(rates[1], 4),
        "cacheWriteRate": round(rates[2], 4),
        "outputRate": round(rates[3], 4),
        "r2": round(r_squared, 4) if r_squared is not None else None,
        "n": len(rs),
    }


def cost_summary(model: str, short: str, rows: list[dict], price_fit: dict | None) -> dict:
    inp = sum(row["inp"] for row in rows)
    cache_read = sum(row["cacheRead"] for row in rows)
    cache_write = sum(row["cacheWrite"] for row in rows)
    visible = sum(row["out"] for row in rows)
    generated = sum(row["gen"] for row in rows)
    reasoning = sum(row["reas"] for row in rows)
    recorded = math.fsum(row["cost"] for row in rows)
    total_input = inp + cache_read + cache_write
    if price_fit:
        fresh_cost = inp / 1e6 * price_fit["inputRate"]
        cache_read_cost = cache_read / 1e6 * price_fit["cacheReadRate"]
        cache_write_cost = cache_write / 1e6 * price_fit["cacheWriteRate"]
        output_cost = generated / 1e6 * price_fit["outputRate"]
        token_cost = fresh_cost + cache_read_cost + cache_write_cost + output_cost
        cache_read_savings = cache_read * (price_fit["inputRate"] - price_fit["cacheReadRate"]) / 1e6
        cache_write_penalty = cache_write * (price_fit["cacheWriteRate"] - price_fit["inputRate"]) / 1e6
        cache_savings = cache_read_savings - cache_write_penalty
        effective_input = (fresh_cost + cache_read_cost + cache_write_cost) / (total_input / 1e6) if total_input else None
    else:
        fresh_cost = cache_read_cost = cache_write_cost = output_cost = None
        token_cost = recorded
        cache_savings = None
        cache_read_savings = None
        cache_write_penalty = None
        effective_input = None
    return {
        "model": model,
        "short": short,
        "steps": len(rows),
        "outputTokens": visible,
        "visibleOutputTokens": visible,
        "reasoningTokens": reasoning,
        "generatedTokens": generated,
        "reasoningShare": round(reasoning / generated, 4) if generated else None,
        "inputTokens": total_input,
        "inputRawTokens": inp,
        "freshInputTokens": inp,
        "inputTotal": total_input,
        "cacheReadTokens": cache_read,
        "cacheWriteTokens": cache_write,
        "cacheHitRate": round(cache_read / total_input, 4) if total_input else None,
        "cacheWriteShare": round(cache_write / total_input, 4) if total_input else None,
        "inputIncludesCache": False,
        "inputSemanticsSource": "normalized",
        "recordedCost": round(recorded, 6),
        "tokenCost": round(token_cost, 6),
        "freshCost": round(fresh_cost, 6) if fresh_cost is not None else None,
        "cacheReadCost": round(cache_read_cost, 6) if cache_read_cost is not None else None,
        "cacheWriteCost": round(cache_write_cost, 6) if cache_write_cost is not None else None,
        "outputCost": round(output_cost, 6) if output_cost is not None else None,
        "unattributedCost": round(recorded, 6) if not price_fit else 0.0,
        "cacheSavings": round(cache_savings, 6) if cache_savings is not None else None,
        "cacheReadSavings": round(cache_read_savings, 6) if cache_read_savings is not None else None,
        "cacheWritePenalty": round(cache_write_penalty, 6) if cache_write_penalty is not None else None,
        "effectiveInputPrice": round(effective_input, 4) if effective_input is not None else None,
        "freshInputRate": price_fit["inputRate"] if price_fit else None,
        "cacheRate": price_fit["cacheReadRate"] if price_fit else None,
        "cacheWriteRate": price_fit["cacheWriteRate"] if price_fit else None,
        "outputRate": price_fit["outputRate"] if price_fit else None,
        "priceR2": price_fit["r2"] if price_fit else None,
        "costPerStep": round(recorded / len(rows), 4) if rows else None,
        "costPerMillionGenerated": round(recorded / generated * 1e6, 2) if generated else None,
        "costPerMillionOutput": round(recorded / generated * 1e6, 2) if generated else None,
    }


def reasoning_summary(rows: list[dict]) -> dict:
    timed = [row for row in rows if is_timed(row)]
    with_reasoning = [row for row in timed if row["reas"] > 0]
    timed_gen = sum(row["gen"] for row in timed)
    timed_reas = sum(row["reas"] for row in timed)
    token_share = timed_reas / timed_gen if timed_gen else None
    return {
        "steps": len(with_reasoning),
        "shareSteps": round(len(with_reasoning) / len(timed), 4) if timed else None,
        "tokenShare": round(token_share, 4) if token_share is not None else None,
        "medianReasonTokens": r1(median_or_none([row["reas"] for row in with_reasoning])),
        "medianOutputTokens": r1(median_or_none([row["out"] for row in with_reasoning])),
    }


def runtime_summary(rows: list[dict], tools: list[dict] | None = None) -> dict:
    """Separate model time from tool execution using OpenCode's own clocks.

    Tool time is the union of the step's tool intervals, so gaps and permission
    waits are not counted as tool execution. Overhead is total - LLM - union.
    """
    with_tools = [
        row for row in rows
        if row["toolDt"] is not None
        and row["totalDt"] is not None
        and 0 <= row["toolDt"] <= row["totalDt"] <= 3600
    ]
    if not with_tools:
        return {
            "steps": 0, "medianToolSeconds": None, "p90ToolSeconds": None,
            "medianStepSeconds": None, "medianShare": None, "weightedShare": None,
            "modelTimeShare": None, "medianOverheadSeconds": None,
            "p90OverheadSeconds": None, "overheadShare": None,
            "medianGapSeconds": None, "p90GapSeconds": None, "byTool": [],
        }
    tool_seconds = sorted(row["toolDt"] for row in with_tools)
    step_seconds = [row["totalDt"] for row in with_tools]
    shares = [row["toolDt"] / row["totalDt"] for row in with_tools if row["totalDt"] > 0]
    complete = [row for row in with_tools if row["dt"] is not None]
    overheads = sorted(
        row["overheadDt"] for row in with_tools
        if row["overheadDt"] is not None and row["dt"] is not None
    )
    gaps = sorted(row["toolGapDt"] for row in with_tools if row["toolGapDt"] is not None)

    tool_step_time = sum(row["totalDt"] for row in with_tools)
    call_rows = []
    for row in with_tools:
        for call in row.get("toolDetails") or []:
            if call.get("runtime") is not None:
                call_rows.append(
                    {
                        "name": call["name"],
                        "runtime": call["runtime"],
                        "allocated": call.get("allocated") or 0.0,
                        "stepSeconds": row["totalDt"],
                        "stepId": row["id"],
                    }
                )
    by_tool = []
    for name in sorted({item["name"] for item in call_rows}):
        rs = [item for item in call_rows if item["name"] == name]
        runtimes = sorted(item["runtime"] for item in rs if item["runtime"] > 0)
        step_totals = {item["stepId"]: item["stepSeconds"] for item in rs}
        allocated = sum(item["allocated"] for item in rs)
        by_tool.append(
            {
                "tool": name,
                "calls": len(rs),
                "medianToolSeconds": r2(statistics.median(runtimes)) if runtimes else None,
                "p90ToolSeconds": r2(percentile(runtimes, 90)) if runtimes else None,
                "medianStepSeconds": r2(statistics.median(list(step_totals.values()))),
                "allocatedSeconds": round(allocated, 3),
                "weightedShare": allocated / tool_step_time if tool_step_time else None,
            }
        )
    by_tool.sort(key=lambda item: -item["calls"])
    return {
        "steps": len(with_tools),
        "medianToolSeconds": r2(statistics.median(tool_seconds)),
        "p90ToolSeconds": r2(percentile(tool_seconds, 90)),
        "medianStepSeconds": r2(statistics.median(step_seconds)),
        "medianShare": round(statistics.median(shares), 4) if shares else None,
        "weightedShare": round(sum(row["toolDt"] for row in complete) / sum(row["totalDt"] for row in complete), 4)
        if complete else None,
        "modelTimeShare": round(sum(row["dt"] for row in complete) / sum(row["totalDt"] for row in complete), 4)
        if complete else None,
        "medianOverheadSeconds": r2(statistics.median(overheads)) if overheads else None,
        "p90OverheadSeconds": r2(percentile(overheads, 90)) if overheads else None,
        "overheadShare": round(sum(overheads) / sum(row["totalDt"] for row in complete), 4)
        if complete and overheads else None,
        "medianGapSeconds": r2(statistics.median(gaps)) if gaps else None,
        "p90GapSeconds": r2(percentile(gaps, 90)) if gaps else None,
        "toolStepSeconds": round(tool_step_time, 3),
        "byToolShareSum": round(sum(item["weightedShare"] or 0 for item in by_tool), 6),
        "byTool": by_tool,
    }


def expected_ratio(row: dict, fits: dict, global_fits: dict) -> float | None:
    kind = "tool" if row["tool"] else "text"
    candidates = (
        fits.get((row["model"], kind)),
        fits.get((row["model"], "all")),
        global_fits.get(kind),
    )
    fit = next((item for item in candidates if item and item.get("usable")), None)
    if not fit or not row.get("dt"):
        return None
    expected = fit["fixedSeconds"] + row["gen"] / fit["pureRate"]
    if expected <= 0:
        return None
    return expected / row["dt"]  # >1 means faster than expected


def bucketize(rows: list[dict], buckets: list[tuple[str, int, int]]) -> list[dict]:
    out = []
    for label, lo, hi in buckets:
        rs = [row for row in rows if lo <= row["gen"] < hi]
        if not rs:
            continue
        rates = sorted(row["gen"] / row["dt"] for row in rs)
        tokens = sum(row["gen"] for row in rs)
        seconds = sum(row["dt"] for row in rs)
        out.append(
            {
                "label": label,
                "n": len(rs),
                "tokens": tokens,
                "weighted": round(tokens / seconds, 1),
                "median": r1(statistics.median(rates)),
                "p90": r1(percentile(rates, 90)),
            }
        )
    return out


def histogram(rows: list[dict], edges: list[int]) -> list[int]:
    counts = [0] * (len(edges) - 1)
    for row in rows:
        value = row["gen"] / row["dt"]
        idx = bisect.bisect_right(edges, value) - 1
        idx = max(0, min(idx, len(counts) - 1))
        counts[idx] += 1
    return counts


def time_group(rows: list[dict], key) -> dict:
    grouped = collections.defaultdict(list)
    for row in rows:
        grouped[key(row)].append(row)
    return grouped


def compaction_impact(rows, events, context_fn, rate_fn):
    by_session = collections.defaultdict(list)
    for row in rows:
        by_session[row["sessionId"]].append(row)
    for session_rows in by_session.values():
        session_rows.sort(key=lambda row: row["ts"])
    pairs = []
    for event in events:
        session = event.get("sessionId")
        ts = event.get("ts")
        if not session or not ts:
            continue
        session_rows = by_session.get(session, [])
        before = [
            row for row in session_rows if row["ts"] < ts and is_timed(row)
        ]
        after = [
            row for row in session_rows if row["ts"] > ts and is_timed(row)
        ]
        if not before or not after:
            continue
        old, new = before[-1], after[0]
        pairs.append(
            {
                "contextBefore": context_fn(old),
                "contextAfter": context_fn(new),
                "rateBefore": rate_fn(old),
                "rateAfter": rate_fn(new),
                "costBefore": old["cost"],
                "costAfter": new["cost"],
            }
        )

    def median_of(key):
        values = [pair[key] for pair in pairs if pair[key] is not None]
        return r2(statistics.median(values)) if values else None

    deltas = [
        (pair["rateAfter"] - pair["rateBefore"]) / pair["rateBefore"] * 100
        for pair in pairs if pair["rateBefore"]
    ]
    context_deltas = [
        pair["contextAfter"] - pair["contextBefore"] for pair in pairs
    ]
    return {
        "events": len(events),
        "paired": len(pairs),
        "medianContextBefore": median_of("contextBefore"),
        "medianContextAfter": median_of("contextAfter"),
        "medianContextDelta": r2(statistics.median(context_deltas)) if context_deltas else None,
        "medianRateBefore": median_of("rateBefore"),
        "medianRateAfter": median_of("rateAfter"),
        "medianRateDeltaPct": r1(statistics.median(deltas)) if deltas else None,
        "medianCostBefore": median_of("costBefore"),
        "medianCostAfter": median_of("costAfter"),
    }


def pace_index(values, pace_base):
    vals = [v for v in values if v is not None]
    if not vals or not pace_base:
        return None
    return round((statistics.median(vals) / pace_base) * 100, 1)


# ---------------------------------------------------------------- build


def build(
    db_path: str,
    model_filters: list[str],
    since: str | None,
    until: str | None,
    include_titles: bool,
    full_paths: bool,
    thresholds: dict | None = None,
    baseline: str | None = None,
) -> dict:
    global THRESHOLDS
    THRESHOLDS = {**DEFAULT_THRESHOLDS, **(thresholds or {})}
    loaded = load_rows(db_path, model_filters, since, until, include_titles, full_paths)
    rows = loaded["rows"]
    ledger = loaded["ledger"]
    timed = [row for row in rows if is_timed(row)]
    text = [row for row in timed if not row["tool"]]

    for row in rows:
        row["timingBucket"] = timing_bucket(row)
        row["timing"] = row["timingBucket"]

    project_paths = sorted({row["project"] for row in rows})
    base_counts = collections.Counter(
        os.path.basename(path.rstrip("/")) or path for path in project_paths
    )
    project_labels = {}
    used_labels = set()
    for path in project_paths:
        base = os.path.basename(path.rstrip("/")) or path
        if full_paths:
            label = path
        elif base_counts[base] == 1:
            label = base
        else:
            parent = os.path.basename(os.path.dirname(path.rstrip("/"))) or ""
            label = f"{base} · {parent}" if parent else base
        if label in used_labels:
            label = path
        used_labels.add(label)
        project_labels[path] = label
    for row in rows:
        row["projectLabel"] = project_labels[row["project"]]

    ledger_by_model = collections.defaultdict(list)
    ledger_by_project = collections.defaultdict(list)
    for entry in ledger:
        ledger_by_model[entry["model"]].append(entry)
        ledger_by_project[entry["project"]].append(entry)

    def stable_seed(text: str) -> int:
        return sum((index + 1) * ord(ch) for index, ch in enumerate(text)) % 1_000_000

    models = sorted({row["model"] for row in rows})
    model_meta = {}
    for row in rows:
        model_meta.setdefault(row["model"], {"short": row["short"], "provider": row["provider"]})

    fits = {}
    regressions = []
    for model in models:
        rs = [row for row in timed if row["model"] == model]
        fit_all = fit_rate(rs)
        fit_text = fit_rate([row for row in rs if not row["tool"]])
        fit_tool = fit_rate([row for row in rs if row["tool"]])
        for kind, fit in (("all", fit_all), ("text", fit_text), ("tool", fit_tool)):
            if fit:
                fits[(model, kind)] = fit
        regressions.append(
            {
                "model": model,
                "short": model_meta[model]["short"],
                "all": fit_all,
                "text": fit_text,
                "tool": fit_tool,
            }
        )

    global_fits = {
        "text": fit_rate(text),
        "tool": fit_rate([row for row in timed if row["tool"]]),
    }
    for row in timed:
        row["ratio"] = expected_ratio(row, fits, global_fits)

    baseline_from = baseline_to = None
    if baseline:
        if ".." not in baseline:
            raise SystemExit("--baseline must be YYYY-MM-DD..YYYY-MM-DD")
        baseline_from, baseline_to = baseline.split("..", 1)
    baseline_rows = [
        row for row in timed
        if baseline_from and baseline_to
        and baseline_from <= row["ts"].date().isoformat() <= baseline_to
    ]
    base_source = baseline_rows if (baseline_from and baseline_to) else timed
    ratio_values = [row["ratio"] for row in base_source if row["ratio"] is not None]
    pace_base = statistics.median(ratio_values) if ratio_values else None

    # ---- per model
    by_model = []
    for model in models:
        rs = [row for row in rows if row["model"] == model]
        ts = [row for row in timed if row["model"] == model]
        summary = summarize(ts)
        fit = fits.get((model, "all")) or fits.get((model, "text")) or {}
        reasoning = reasoning_summary(rs)
        model_gen = sum(row["gen"] for row in rs)
        model_reas = sum(row["reas"] for row in rs)
        cache_read = sum(row["cacheRead"] for row in rs)
        cache_write = sum(row["cacheWrite"] for row in rs)
        fresh = sum(row["inp"] for row in rs)
        total_in = fresh + cache_read + cache_write
        model_timing = collections.Counter(row["timingBucket"] for row in rs)
        model_ledger = ledger_by_model.get(model, [])
        error_steps = sum(
            1 for entry in model_ledger if entry["reason"] in ("error", "both")
        )
        empty_steps = sum(
            1 for entry in model_ledger if entry["reason"] in ("empty", "both")
        )
        error_cost = sum(
            entry["cost"] for entry in model_ledger
            if entry["reason"] in ("error", "both")
        )
        rates = [row["gen"] / row["dt"] for row in ts]
        by_model.append(
            {
                "model": model,
                "short": model_meta[model]["short"],
                "provider": model_meta[model]["provider"],
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": sum(row["out"] for row in rs),
                "visibleOutputTokens": sum(row["out"] for row in rs),
                "reasoningTokens": model_reas,
                "generatedTokens": model_gen,
                "reasoningShare": round(model_reas / model_gen, 4) if model_gen else None,
                "reasoningSteps": reasoning["steps"],
                "inputTokens": total_in,
                "inputRawTokens": fresh,
                "freshInputTokens": fresh,
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheWriteTokens": cache_write,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "inputIncludesCache": False,
                "inputSemanticsSource": "normalized",
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "timedN": summary["n"],
                "rated": len(ts) >= 30,
                "lowSample": len(ts) < 30,
                "medianCI": bootstrap_median_ci(rates, seed=stable_seed(model)),
                "weighted": summary["weighted"],
                "median": summary["median"],
                "visibleMedian": summary["visibleMedian"],
                "p90": summary["p90"],
                "ttftMedian": summary["ttftMedian"],
                "ttftN": summary["ttftN"],
                "decodeMedian": summary["decodeMedian"],
                "decodeN": summary["decodeN"],
                "pureRate": fit.get("pureRate"),
                "fixedSeconds": fit.get("fixedSeconds"),
                "r2": fit.get("r2"),
                "fitUsable": bool(fit.get("usable")),
                "timing": {
                    key: model_timing.get(key, 0)
                    for key in TIMING_REASONS
                },
                "rejected": len(rs) - model_timing.get("plausible", 0),
                "errorSteps": error_steps,
                "emptySteps": empty_steps,
                "ledgerSteps": len(model_ledger),
                "errorCostUsd": round(error_cost, 6),
                "errorRate": round(error_steps / (len(rs) + error_steps), 4)
                if (len(rs) + error_steps) else None,
            }
        )

    size_defs = [
        ("20-100", 20, 100), ("100-300", 100, 300), ("300-1k", 300, 1000),
        ("1k-5k", 1000, 5000), ("5k+", 5000, 10**12),
    ]

    def size_bucket(value):
        for label, low, high in size_defs:
            if low <= value < high:
                return label
        return None

    global_strata = collections.Counter()
    for row in timed:
        bucket = size_bucket(row["gen"])
        if bucket:
            global_strata[("tool" if row["tool"] else "text", bucket)] += 1
    for item in by_model:
        model = item["model"]
        strata = collections.defaultdict(list)
        for row in timed:
            if row["model"] != model:
                continue
            bucket = size_bucket(row["gen"])
            if bucket:
                strata[("tool" if row["tool"] else "text", bucket)].append(
                    row["gen"] / row["dt"]
                )
        medians = {
            key: statistics.median(values)
            for key, values in strata.items()
            if len(values) >= 5
        }
        value, covered = standardize(medians, global_strata)
        item["standardizedRate"] = r1(value) if value is not None else None
        item["standardizedCoverage"] = covered
        item["standardizedN"] = sum(
            len(values) for key, values in strata.items() if key in medians
        )

    # ---- cache & price
    price_fits = {}
    for model in models:
        price_fit = fit_prices([row for row in rows if row["model"] == model])
        if price_fit:
            price_fits[model] = price_fit

    cost_by_model = [
        cost_summary(
            model,
            model_meta[model]["short"],
            [row for row in rows if row["model"] == model],
            price_fits.get(model),
        )
        for model in models
    ]
    total_input = sum(item["inputTotal"] for item in cost_by_model)
    total_raw_input = sum(item["inputRawTokens"] for item in cost_by_model)
    total_fresh_input = sum(item["freshInputTokens"] for item in cost_by_model)
    total_cache_read = sum(row["cacheRead"] for row in rows)
    total_cache_write = sum(row["cacheWrite"] for row in rows)
    total_output = sum(row["out"] for row in rows)
    total_reasoning = sum(row["reas"] for row in rows)
    total_generated = sum(row["gen"] for row in rows)
    total_cost = math.fsum(row["cost"] for row in rows)
    error_cost = math.fsum(entry["cost"] for entry in ledger)
    undated_error_cost = math.fsum(
        entry["cost"] for entry in ledger if entry["ts"] is None
    )
    total_spend = total_cost + error_cost
    unattributed_cost = math.fsum(
        item["recordedCost"] for item in cost_by_model if item["freshCost"] is None
    )
    fitted_cache_read = sum(
        item["cacheReadTokens"]
        for item in cost_by_model
        if (item["freshInputRate"] or 0) > 0
    )
    fitted_models = sum(
        1 for item in cost_by_model if (item["freshInputRate"] or 0) > 0
    )
    cache = {
        "inputTokens": total_input,
        "inputRawTokens": total_raw_input,
        "freshInputTokens": total_fresh_input,
        "cacheReadTokens": total_cache_read,
        "cacheWriteTokens": total_cache_write,
        "outputTokens": total_output,
        "visibleOutputTokens": total_output,
        "reasoningTokens": total_reasoning,
        "generatedTokens": total_generated,
        "hitRate": round(total_cache_read / total_input, 4) if total_input else None,
        "writeShare": round(total_cache_write / total_input, 4) if total_input else None,
        "savings": round(math.fsum(item["cacheSavings"] or 0 for item in cost_by_model), 6),
        "readSavings": round(math.fsum(item["cacheReadSavings"] or 0 for item in cost_by_model), 6),
        "writePenalty": round(math.fsum(item["cacheWritePenalty"] or 0 for item in cost_by_model), 6),
        "readCost": round(math.fsum(item["cacheReadCost"] or 0 for item in cost_by_model), 6),
        "writeCost": round(math.fsum(item["cacheWriteCost"] or 0 for item in cost_by_model), 6),
        "tokenCost": round(math.fsum(item["tokenCost"] for item in cost_by_model), 6),
        "fittedModels": fitted_models,
        "unfittedModels": len(models) - fitted_models,
        "savingsCoverage": round(fitted_cache_read / total_cache_read, 4)
        if total_cache_read else None,
    }
    cost = {
        "recorded": round(total_cost, 6),
        "errorCostUsd": round(error_cost, 6),
        "undatedErrorCostUsd": round(undated_error_cost, 6),
        "totalSpendUsd": round(total_spend, 6),
        "tokenCost": round(math.fsum(item["tokenCost"] for item in cost_by_model), 6),
        "freshCost": round(math.fsum(item["freshCost"] or 0 for item in cost_by_model), 6),
        "cacheReadCost": round(math.fsum(item["cacheReadCost"] or 0 for item in cost_by_model), 6),
        "cacheWriteCost": round(math.fsum(item["cacheWriteCost"] or 0 for item in cost_by_model), 6),
        "outputCost": round(math.fsum(item["outputCost"] or 0 for item in cost_by_model), 6),
        "unattributedCost": round(unattributed_cost, 6),
        "perStep": round(total_cost / len(rows), 4) if rows else None,
        "perMillionGenerated": round(total_cost / total_generated * 1e6, 2)
        if total_generated else None,
        "perMillionOutput": round(total_cost / total_generated * 1e6, 2)
        if total_generated else None,
        "byModel": cost_by_model,
    }

    # ---- tool mix: attribute every call and allocate non-overlapping time
    for row in rows:
        calls = row.get("toolDetails") or []
        allocations = allocate_tool_time(calls)
        for call, allocation in zip(calls, allocations):
            call["allocated"] = allocation
    tool_step_time = sum(
        row["totalDt"] for row in rows
        if row.get("toolDetails") and row["totalDt"] is not None
    )
    tool_stats = collections.defaultdict(
        lambda: {
            "calls": 0, "steps": set(), "generated": 0.0, "visible": 0.0,
            "reasoning": 0.0, "cost": 0.0, "tokensPerCall": [], "runtimes": [],
            "errors": 0, "timedCalls": 0, "timedGenerated": 0.0, "timedSeconds": 0.0,
            "rates": [], "timeAllocated": 0.0,
        }
    )
    total_calls = 0
    for row in rows:
        calls = row["toolDetails"] or [
            {"name": "(final answer)", "runtime": None, "error": False, "allocated": 0.0}
        ]
        n = len(calls)
        total_calls += n
        row_timed = is_timed(row)
        for call in calls:
            stat = tool_stats[call["name"]]
            stat["calls"] += 1
            stat["steps"].add(row["id"])
            stat["generated"] += row["gen"] / n
            stat["visible"] += row["out"] / n
            stat["reasoning"] += row["reas"] / n
            stat["cost"] += row["cost"] / n
            stat["tokensPerCall"].append(row["gen"] / n)
            stat["timeAllocated"] += call.get("allocated", 0.0)
            if call.get("runtime") is not None:
                stat["runtimes"].append(call["runtime"])
            if call.get("error"):
                stat["errors"] += 1
            if row_timed:
                stat["timedCalls"] += 1
                stat["timedGenerated"] += row["gen"] / n
                stat["timedSeconds"] += row["dt"] / n
                stat["rates"].append(row["gen"] / row["dt"])
    tools = []
    for name, stat in tool_stats.items():
        runtimes = sorted(stat["runtimes"])
        tools.append(
            {
                "tool": name,
                "calls": stat["calls"],
                "stepsWithTool": len(stat["steps"]),
                "shareCalls": stat["calls"] / total_calls if total_calls else None,
                "generatedTokens": round(stat["generated"], 1),
                "visibleTokens": round(stat["visible"], 1),
                "visibleOutputTokens": round(stat["visible"], 1),
                "reasoningTokens": round(stat["reasoning"], 1),
                "medianTokensPerCall": r1(statistics.median(stat["tokensPerCall"])),
                "timedCalls": stat["timedCalls"],
                "weighted": round(stat["timedGenerated"] / stat["timedSeconds"], 1) if stat["timedSeconds"] else None,
                "medianTps": r1(statistics.median(stat["rates"])) if stat["rates"] else None,
                "medianRuntime": r2(statistics.median(runtimes)) if runtimes else None,
                "p90Runtime": r2(percentile(runtimes, 90)) if runtimes else None,
                "timeAllocatedSeconds": round(stat["timeAllocated"], 3),
                "timeShare": stat["timeAllocated"] / tool_step_time if tool_step_time else None,
                "timeShareBasis": "tool-step-time",
                "errors": stat["errors"],
                "errRate": round(stat["errors"] / stat["calls"], 4) if stat["calls"] else None,
                "costUsd": round(stat["cost"], 6),
                "costPerCall": round(stat["cost"] / stat["calls"], 4) if stat["calls"] else None,
                "attribution": "equal-share",
            }
        )
    tools.sort(key=lambda item: -item["calls"])

    # ---- behavior slices
    def behavior_slice(rs: list[dict]) -> dict:
        tr = [row for row in rs if is_timed(row)]
        return {
            "steps": len(rs),
            "share": round(len(rs) / len(rows), 4) if rows else None,
            "generatedTokens": sum(row["gen"] for row in rs),
            "medianOut": r1(statistics.median([row["gen"] for row in rs])) if rs else None,
            "timedN": len(tr),
            "weighted": round(sum(row["gen"] for row in tr) / sum(row["dt"] for row in tr), 1) if tr else None,
            "medianTps": r1(statistics.median([row["gen"] / row["dt"] for row in tr])) if tr else None,
        }

    thinking = {
        "with": behavior_slice([row for row in rows if row["thinking"]]),
        "without": behavior_slice([row for row in rows if not row["thinking"]]),
    }

    agent_groups = collections.defaultdict(list)
    for row in rows:
        agent_groups[row["agent"]].append(row)
    agents = []
    for name, rs in agent_groups.items():
        item = behavior_slice(rs)
        item["agent"] = name
        agents.append(item)
    agents.sort(key=lambda item: -item["steps"])

    variant_groups = collections.defaultdict(list)
    for row in rows:
        variant_groups[row["variant"]].append(row)
    variants = []
    for name, rs in variant_groups.items():
        item = behavior_slice(rs)
        item["variant"] = name
        variants.append(item)
    variants.sort(key=lambda item: -item["steps"])

    entry_groups = collections.defaultdict(list)
    for row in rows:
        entry_groups[row["entry"]].append(row)
    entrypoints = []
    for name, rs in entry_groups.items():
        item = behavior_slice(rs)
        item["entrypoint"] = name
        item["sessions"] = len({row["sessionId"] for row in rs})
        entrypoints.append(item)
    entrypoints.sort(key=lambda item: -item["steps"])

    project_groups = collections.defaultdict(list)
    for row in rows:
        project_groups[row["project"]].append(row)
    projects = []
    for path, rs in project_groups.items():
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs)
        project_ledger = ledger_by_project.get(path, [])
        error_steps = sum(
            1 for entry in project_ledger if entry["reason"] in ("error", "both")
        )
        error_cost = sum(
            entry["cost"] for entry in project_ledger
            if entry["reason"] in ("error", "both")
        )
        projects.append(
            {
                "name": project_labels.get(path, path),
                "path": path,
                "steps": len(rs),
                "sessions": len({row["sessionId"] for row in rs}),
                "generatedTokens": sum(row["gen"] for row in rs),
                "outputTokens": sum(row["out"] for row in rs),
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "errorSteps": error_steps,
                "errorCostUsd": round(error_cost, 6),
                "errorRate": round(error_steps / (len(rs) + error_steps), 4)
                if (len(rs) + error_steps) else None,
            }
        )
    projects.sort(key=lambda item: -item["generatedTokens"])
    runtime = runtime_summary(rows, tools)
    error_cost = math.fsum(entry["cost"] for entry in ledger)
    error_seconds = math.fsum(entry["totalDt"] or 0 for entry in ledger)

    # ---- health / coverage
    event_versions = loaded["events"]["versions"]
    session_health = {
        "events": [
            {"type": kind, "count": count}
            for kind, count in loaded["events"]["types"].most_common()
        ],
        "finishes": [
            {"finish": kind, "count": count}
            for kind, count in loaded["events"]["finishes"].most_common()
        ],
        "toolErrors": [
            {"tool": name, "count": count}
            for name, count in loaded["events"]["toolErrors"].most_common()
        ],
        "toolCalls": sum(loaded["events"]["toolCalls"].values()),
        "errors": loaded["events"]["errors"],
        "noStream": loaded["events"]["noStream"],
        "compactions": loaded["events"]["compactions"],
        "modelSwitches": loaded["events"]["modelSwitches"],
        "agentSwitches": loaded["events"]["agentSwitches"],
        "idle": loaded["events"]["idle"],
        "shell": loaded["events"]["shell"],
        "sessionsTotal": loaded["sessionsTotal"],
        "sessionsRoot": loaded["sessionsRoot"],
        "sessionsSubagent": loaded["sessionsSubagent"],
        "sessionsInWindow": len(loaded["sessions"]),
        "errorSteps": loaded["events"]["errors"],
        "emptySteps": loaded["emptySteps"],
        "ledgerSteps": len(ledger),
        "errorCostUsd": round(error_cost, 6),
        "errorSeconds": round(error_seconds, 3),
        "skippedCostUsd": round(error_cost, 6),
        "duplicateSteps": loaded["duplicates"],
        "duplicateCostUsd": round(loaded["duplicateCost"], 6),
        "duplicateTokens": loaded["duplicateTokens"],
        "negativeOverhead": loaded["events"]["negativeOverhead"],
        "timing": {
            key: sum(1 for row in rows if row["timingBucket"] == key)
            for key in TIMING_REASONS
        },
        "versions": [
            {"version": version, "sessions": count}
            for version, count in event_versions.most_common()
        ],
    }
    timing_counts = collections.Counter(row["timingBucket"] for row in rows)
    coverage = {
        "timing": timing_counts.get("plausible", 0) > 0,
        "timingBreakdown": True,
        "streamed": any(row["dt"] is not None for row in rows),
        "completed": any(row["totalDt"] is not None for row in rows),
        "toolRuntime": any(row["toolDt"] is not None for row in rows),
        "toolTimeAllocation": True,
        "reasoning": total_reasoning > 0,
        "cacheRead": total_cache_read > 0,
        "cacheWrite": total_cache_write > 0,
        "titles": any(row.get("sessionTitle") for row in rows),
        "parentSessions": loaded["sessionsSubagent"] > 0,
        "cost": total_cost > 0,
        "variants": any(row["variant"] != "default" for row in rows),
        "errors": loaded["events"]["errors"] > 0,
        "errorLedger": bool(ledger),
        "skipLedger": bool(ledger),
        "ttft": any(row["ttft"] is not None for row in rows),
        "overhead": any(row["overheadDt"] is not None for row in rows),
        "projectPaths": True,
        "inputSemantics": True,
    }
    timing_info = {
        "scanned": loaded["scanned"],
        "considered": loaded["considered"],
        "noCreated": loaded["noCreated"],
        "empty": loaded["emptySteps"],
        "errorSteps": loaded["errorSteps"],
        "ledger": len(ledger),
        "rows": len(rows),
        "plausible": timing_counts.get("plausible", 0),
        "noTiming": timing_counts.get("noTiming", 0),
        "tooShort": timing_counts.get("tooShort", 0),
        "tooShortDuration": timing_counts.get("tooShortDuration", 0),
        "tooLong": timing_counts.get("tooLong", 0),
        "stalled": timing_counts.get("tooLong", 0),
        "slowRate": timing_counts.get("slowRate", 0),
        "fastRate": timing_counts.get("fastRate", 0),
        "outOfRange": timing_counts.get("tooShortDuration", 0) + timing_counts.get("tooLong", 0),
        "implausibleRate": timing_counts.get("slowRate", 0) + timing_counts.get("fastRate", 0),
        "duplicates": loaded["duplicates"],
        "fingerprintCollisions": loaded["collisions"],
        "thresholds": dict(THRESHOLDS),
    }

    # ---- compact per-step rows and session index
    tool_name_set = set()
    for row in rows:
        if row["toolDetails"]:
            tool_name_set.update(call["name"] for call in row["toolDetails"])
        else:
            tool_name_set.add("(final answer)")
    tool_names = sorted(tool_name_set)
    agent_names = sorted({row["agent"] for row in rows})
    variant_names = sorted({row["variant"] for row in rows})
    project_names = sorted({row["project"] for row in rows})
    project_labels_out = [project_labels.get(path, path) for path in project_names]
    entrypoint_names = sorted({row["entry"] for row in rows})
    session_ids = sorted({row["sessionId"] for row in rows})
    finish_names = sorted({row["finish"] for row in rows})
    session_index = {sid: idx for idx, sid in enumerate(session_ids)}
    tool_index = {name: idx for idx, name in enumerate(tool_names)}
    agent_index = {name: idx for idx, name in enumerate(agent_names)}
    variant_index = {name: idx for idx, name in enumerate(variant_names)}
    project_index = {name: idx for idx, name in enumerate(project_names)}
    entrypoint_index = {name: idx for idx, name in enumerate(entrypoint_names)}
    model_index = {name: idx for idx, name in enumerate(models)}
    finish_index = {name: idx for idx, name in enumerate(finish_names)}
    timing_index = {name: idx for idx, name in enumerate(TIMING_REASONS)}
    compaction_by_session = collections.Counter(
        event["sessionId"]
        for event in loaded["events"]["compactionEvents"]
        if event["sessionId"]
    )

    steps_out = []
    for row in rows:
        steps_out.append(
            [
                int(row["ts"].timestamp()) if row["ts"] else 0,
                model_index[row["model"]],
                tool_index[row["primaryTool"]],
                row["out"],
                round(row["dt"], 3) if row["dt"] is not None else -1,
                row["inp"],
                row["cacheRead"],
                row["cacheWrite"],
                row["cost"],
                agent_index[row["agent"]],
                1 if row["thinking"] else 0,
                project_index[row["project"]],
                entrypoint_index[row["entry"]],
                session_index[row["sessionId"]],
                row["nTools"],
                row["nThinking"],
                row["nText"],
                row["reas"],
                round(row["totalDt"], 3) if row["totalDt"] is not None else -1,
                round(row["toolDt"], 3) if row["toolDt"] is not None else -1,
                row["inp"] + row["cacheRead"] + row["cacheWrite"],
                variant_index[row["variant"]],
                finish_index[row["finish"]],
                round(row["ttft"], 3) if row["ttft"] is not None else -1,
                round(row["decodeDt"], 3) if row["decodeDt"] is not None else -1,
                round(row["decodeRate"], 2) if row["decodeRate"] is not None else -1,
                round(row["overheadDt"], 3) if row["overheadDt"] is not None else -1,
                round(row["toolSpanDt"], 3) if row["toolSpanDt"] is not None else -1,
                round(row["toolGapDt"], 3) if row["toolGapDt"] is not None else -1,
                [
                    [
                        tool_index[call["name"]],
                        round(call["runtime"], 3) if call.get("runtime") is not None else -1,
                        1 if call.get("error") else 0,
                        round(call.get("allocated") or 0.0, 3),
                    ]
                    for call in row["toolDetails"]
                ],
                round(row["ratio"], 4) if row.get("ratio") is not None else -1,
                timing_index[row["timing"]],
            ]
        )
    steps_out.sort(key=lambda item: item[0])

    session_rows = collections.defaultdict(list)
    for row in rows:
        session_rows[row["sessionId"]].append(row)
    ledger_by_session = collections.defaultdict(list)
    for entry in ledger:
        ledger_by_session[entry["sessionId"]].append(entry)

    direct = {}
    for sid in session_ids:
        rs = session_rows[sid]
        ledger_rows = ledger_by_session.get(sid, [])
        error_rows = [
            entry for entry in ledger_rows if entry["reason"] in ("error", "both")
        ]
        empty_rows = [
            entry for entry in ledger_rows if entry["reason"] in ("empty", "both")
        ]
        direct[sid] = {
            "steps": len(rs),
            "generated": sum(row["gen"] for row in rs),
            "cost": math.fsum(row["cost"] for row in rs),
            "skippedCost": math.fsum(entry["cost"] for entry in ledger_rows),
            "errorSteps": len(error_rows),
            "emptySteps": len(empty_rows),
            "ledgerSteps": len(ledger_rows),
            "errorCost": math.fsum(entry["cost"] for entry in error_rows),
            "errorSeconds": math.fsum(entry["totalDt"] or 0 for entry in error_rows),
            "inputTotal": sum(
                row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs
            ),
            "cacheRead": sum(row["cacheRead"] for row in rs),
        }
    for sid, ledger_rows in ledger_by_session.items():
        if sid in direct:
            continue
        error_rows = [
            entry for entry in ledger_rows if entry["reason"] in ("error", "both")
        ]
        direct[sid] = {
            "steps": 0,
            "generated": 0,
            "cost": 0.0,
            "skippedCost": math.fsum(entry["cost"] for entry in ledger_rows),
            "errorSteps": len(error_rows),
            "emptySteps": len(ledger_rows) - len(error_rows),
            "ledgerSteps": len(ledger_rows),
            "errorCost": math.fsum(entry["cost"] for entry in error_rows),
            "errorSeconds": math.fsum(entry["totalDt"] or 0 for entry in error_rows),
            "inputTotal": 0,
            "cacheRead": 0,
        }

    children = collections.defaultdict(list)
    for child_id, meta in loaded["sessionsMeta"].items():
        parent_id = meta.get("parent")
        if parent_id:
            children[parent_id].append(child_id)

    descendant_cache = {}

    def descendants(sid):
        if sid in descendant_cache:
            return descendant_cache[sid]
        found = []
        seen = set()
        stack = list(children.get(sid, []))
        while stack:
            child = stack.pop()
            if child in seen:
                continue
            seen.add(child)
            found.append(child)
            stack.extend(children.get(child, []))
        descendant_cache[sid] = found
        return found

    session_list = []
    for sid in session_ids:
        rs = session_rows[sid]
        stamps = [int(row["ts"].timestamp()) for row in rs]
        first_ts = min(stamps) if stamps else None
        last_ts = max(stamps) if stamps else None
        wall_minutes = ((last_ts - first_ts) / 60) if first_ts is not None and last_ts is not None else 0
        active_seconds = sum(row["totalDt"] for row in rs if row["totalDt"] is not None)
        active_minutes = active_seconds / 60
        idle_minutes = max(0.0, wall_minutes - active_minutes)
        timed_rs = [row for row in rs if is_timed(row)]
        gen_tokens = sum(row["gen"] for row in rs)
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs)
        project_path = rs[0]["project"]
        project_label = project_labels.get(project_path, project_path)
        ledger_rows = ledger_by_session.get(sid, [])
        error_rows = [
            entry for entry in ledger_rows if entry["reason"] in ("error", "both")
        ]
        empty_rows = [
            entry for entry in ledger_rows if entry["reason"] in ("empty", "both")
        ]
        skipped_cost = math.fsum(entry["cost"] for entry in ledger_rows)
        error_cost = math.fsum(entry["cost"] for entry in error_rows)
        error_seconds = math.fsum(entry["totalDt"] or 0 for entry in error_rows)
        children_ids = descendants(sid)
        rollup_ids = [sid] + children_ids
        rollup_steps = sum(direct.get(item, {}).get("steps", 0) for item in rollup_ids)
        rollup_generated = sum(
            direct.get(item, {}).get("generated", 0) for item in rollup_ids
        )
        rollup_cost = sum(direct.get(item, {}).get("cost", 0) for item in rollup_ids)
        rollup_skipped = sum(
            direct.get(item, {}).get("skippedCost", 0) for item in rollup_ids
        )
        subagent_cost = sum(
            direct.get(item, {}).get("cost", 0)
            + direct.get(item, {}).get("skippedCost", 0)
            for item in children_ids
        )
        compactions = compaction_by_session.get(sid, 0) + sum(
            compaction_by_session.get(item, 0) for item in children_ids
        )
        if direct.get(sid, {}).get("errorSteps", 0) or any(
            direct.get(item, {}).get("errorSteps", 0) for item in children_ids
        ):
            outcome = "error"
        elif compactions:
            outcome = "compacted"
        elif rs and rs[-1]["finish"] == "stop":
            outcome = "completed"
        else:
            outcome = "incomplete"
        session_list.append(
            {
                "id": sid[:16],
                "sessionId": sid,
                "title": rs[0].get("sessionTitle") or sid[:16],
                "project": project_label,
                "projectPath": project_path,
                "entrypoint": rs[0]["entry"],
                "agent": rs[0]["agent"],
                "models": sorted({row["short"] for row in rs}),
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": sum(row["out"] for row in rs),
                "visibleOutputTokens": sum(row["out"] for row in rs),
                "reasoningTokens": sum(row["reas"] for row in rs),
                "generatedTokens": gen_tokens,
                "inputTokens": total_in,
                "inputRawTokens": sum(row["inp"] for row in rs),
                "freshInputTokens": sum(row["inp"] for row in rs),
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheWriteTokens": sum(row["cacheWrite"] for row in rs),
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "skippedCostUsd": round(skipped_cost, 6),
                "totalSpendUsd": round(
                    math.fsum(row["cost"] for row in rs) + skipped_cost, 6
                ),
                "errorSteps": len(error_rows),
                "emptySteps": len(empty_rows),
                "ledgerSteps": len(ledger_rows),
                "duplicateSteps": loaded["duplicateSessions"].get(sid, 0),
                "errorCostUsd": round(error_cost, 6),
                "errorSeconds": round(error_seconds, 3),
                "timedN": len(timed_rs),
                "lowSample": len(timed_rs) < 30,
                "weighted": round(
                    sum(row["gen"] for row in timed_rs) / sum(row["dt"] for row in timed_rs), 1
                ) if timed_rs else None,
                "firstTs": first_ts,
                "lastTs": last_ts,
                "spanMinutes": round(wall_minutes, 1),
                "activeMinutes": round(active_minutes, 1),
                "idleMinutes": round(idle_minutes, 1),
                "outputPerMinute": round(gen_tokens / active_minutes, 1) if active_minutes >= 0.5 else None,
                "wallOutputPerMinute": round(gen_tokens / wall_minutes, 1) if wall_minutes >= 0.5 else None,
                "compactions": compactions,
                "outcome": outcome,
                "subagentSessions": len(children_ids),
                "subagentCostUsd": round(subagent_cost, 6),
                "rollupSteps": rollup_steps,
                "rollupGeneratedTokens": rollup_generated,
                "rollupCostUsd": round(rollup_cost, 6),
                "rollupTotalSpendUsd": round(rollup_cost + rollup_skipped, 6),
            }
        )

    fits_out = [
        {
            "all": next((item["all"] for item in regressions if item["model"] == model), None),
            "text": next((item["text"] for item in regressions if item["model"] == model), None),
            "tool": next((item["tool"] for item in regressions if item["model"] == model), None),
        }
        for model in models
    ]
    prices_out = [
        {
            "fresh": next((item["freshInputRate"] for item in cost_by_model if item["model"] == model), None),
            "cache": next((item["cacheRate"] for item in cost_by_model if item["model"] == model), None),
            "cacheWrite": next((item["cacheWriteRate"] for item in cost_by_model if item["model"] == model), None),
            "output": next((item["outputRate"] for item in cost_by_model if item["model"] == model), None),
            "inputIncludesCache": False,
        }
        for model in models
    ]

    # ---- distribution
    edges = [0, 10, 20, 30, 40, 50, 66, 80, 100, 120, 150, 200, 250, 300, 400]
    labels = [
        "0-10", "10-20", "20-30", "30-40", "40-50", "50-66", "66-80",
        "80-100", "100-120", "120-150", "150-200", "200-250", "250-300", "300+",
    ]

    # ---- length buckets
    size_buckets = bucketize(
        timed,
        [
            ("20-100", 20, 100),
            ("100-300", 100, 300),
            ("300-600", 300, 600),
            ("600-1k", 600, 1000),
            ("1k-2k", 1000, 2000),
            ("2k-5k", 2000, 5000),
            ("5k+", 5000, 10**9),
        ],
    )

    # ---- daily (all steps, with timed rate/index)
    ledger_by_day = collections.defaultdict(list)
    ledger_by_hour = collections.defaultdict(list)
    ledger_by_model_day = collections.defaultdict(list)
    for entry in ledger:
        if entry["ts"] is None:
            continue
        local = entry["ts"].astimezone()
        ledger_by_day[local.date().isoformat()].append(entry)
        ledger_by_hour[local.hour].append(entry)
        ledger_by_model_day[(entry["model"], local.date().isoformat())].append(entry)

    by_day_all = time_group(rows, lambda row: row["ts"].astimezone().date().isoformat())
    daily = []
    for date in sorted(by_day_all):
        rs = by_day_all[date]
        rs_timed = [row for row in rs if is_timed(row)]
        text_rs = [row for row in rs_timed if not row["tool"]]
        tokens = sum(row["gen"] for row in rs)
        timed_tokens = sum(row["gen"] for row in rs_timed)
        timed_seconds = sum(row["dt"] for row in rs_timed)
        visible = sum(row["out"] for row in rs)
        reasoning = sum(row["reas"] for row in rs)
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs)
        day_ledger = ledger_by_day.get(date, [])
        error_rows = [
            entry for entry in day_ledger if entry["reason"] in ("error", "both")
        ]
        empty_rows = [
            entry for entry in day_ledger if entry["reason"] in ("empty", "both")
        ]
        skipped_cost = math.fsum(entry["cost"] for entry in day_ledger)
        error_cost = math.fsum(entry["cost"] for entry in error_rows)
        ratio_values = [row["ratio"] for row in rs_timed if row.get("ratio") is not None]
        ratio = median_or_none(ratio_values)
        daily.append(
            {
                "date": date,
                "nAll": len(rs),
                "nTimed": len(rs_timed),
                "n": len(rs_timed),
                "tokens": timed_tokens,
                "generatedTokens": tokens,
                "visibleOutputTokens": visible,
                "reasoningTokens": reasoning,
                "timedTokens": timed_tokens,
                "timedSeconds": timed_seconds,
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "errorCostUsd": round(error_cost, 6),
                "skippedCostUsd": round(skipped_cost, 6),
                "totalSpendUsd": round(
                    math.fsum(row["cost"] for row in rs) + skipped_cost, 6
                ),
                "errorSteps": len(error_rows),
                "emptySteps": len(empty_rows),
                "ledgerSteps": len(day_ledger),
                "errorRate": round(len(error_rows) / (len(rs) + len(error_rows)), 4)
                if (len(rs) + len(error_rows)) else None,
                "sessions": len({row["sessionId"] for row in rs}),
                "weighted": round(timed_tokens / timed_seconds, 1)
                if timed_seconds else None,
                "median": r1(statistics.median([row["gen"] / row["dt"] for row in rs_timed]))
                if rs_timed else None,
                "p90": r1(percentile(sorted(row["gen"] / row["dt"] for row in rs_timed), 90))
                if rs_timed else None,
                "ratio": round(ratio, 6) if ratio is not None else None,
                "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                "ratioN": len(ratio_values),
                "index": pace_index([row["ratio"] for row in rs_timed], pace_base),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["gen"] / row["dt"] for row in text_rs]))
                if text_rs else None,
                "textIndex": pace_index([row["ratio"] for row in text_rs], pace_base),
            }
        )

    def window_7d(entries, index, days=7):
        start = dt.date.fromisoformat(entries[index]["date"]) - dt.timedelta(days=days - 1)
        return [
            entry for entry in entries[: index + 1]
            if dt.date.fromisoformat(entry["date"]) >= start
        ]

    for index, entry in enumerate(daily):
        window = window_7d(daily, index)
        tokens = sum(item["timedTokens"] for item in window)
        seconds = sum(item["timedSeconds"] for item in window)
        entry["weighted7d"] = round(tokens / seconds, 1) if seconds else None
        entry["cost7d"] = round(
            sum(item["totalSpendUsd"] for item in window) / len(window), 6
        ) if window else None
        entry["spend7dTotal"] = round(sum(item["totalSpendUsd"] for item in window), 6)
        indexes = [item["index"] for item in window if item["index"] is not None]
        entry["index7d"] = r1(statistics.median(indexes)) if indexes else None

    anomalies = []
    for metric in ("totalSpendUsd", "weighted", "index", "cacheHitRate", "errorRate"):
        values = [entry.get(metric) for entry in daily]
        for entry in daily:
            z = robust_z(values, entry.get(metric))
            if z is not None and abs(z) >= 3.5:
                anomalies.append(
                    {
                        "date": entry["date"],
                        "metric": metric,
                        "value": entry.get(metric),
                        "z": round(z, 2),
                        "direction": "high" if z > 0 else "low",
                    }
                )

    daily_by_model = []
    for model in models:
        rs = [row for row in rows if row["model"] == model]
        by_date = time_group(rs, lambda row: row["ts"].astimezone().date().isoformat())
        series = []
        for date in sorted(by_date):
            day_rows = by_date[date]
            day_timed = [row for row in day_rows if is_timed(row)]
            model_ledger = ledger_by_model_day.get((model, date), [])
            cache_read = sum(row["cacheRead"] for row in day_rows)
            total_in = sum(
                row["inp"] + row["cacheRead"] + row["cacheWrite"]
                for row in day_rows
            )
            ratio_values = [
                row["ratio"] for row in day_timed if row.get("ratio") is not None
            ]
            ratio = median_or_none(ratio_values)
            series.append(
                {
                    "date": date,
                    "nAll": len(day_rows),
                    "nTimed": len(day_timed),
                    "generatedTokens": sum(row["gen"] for row in day_rows),
                    "visibleOutputTokens": sum(row["out"] for row in day_rows),
                    "reasoningTokens": sum(row["reas"] for row in day_rows),
                    "costUsd": round(math.fsum(row["cost"] for row in day_rows), 6),
                    "errorCostUsd": round(
                        math.fsum(
                            entry["cost"] for entry in model_ledger
                            if entry["reason"] in ("error", "both")
                        ),
                        6,
                    ),
                    "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                    "weighted": round(
                        sum(row["gen"] for row in day_timed)
                        / sum(row["dt"] for row in day_timed),
                        1,
                    ) if day_timed else None,
                    "median": r1(statistics.median([row["gen"] / row["dt"] for row in day_timed]))
                    if day_timed else None,
                    "ratio": round(ratio, 6) if ratio is not None else None,
                    "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                    "ratioN": len(ratio_values),
                    "index": pace_index([row["ratio"] for row in day_timed], pace_base),
                }
            )
        daily_by_model.append({"model": model, "series": series})

    compaction = compaction_impact(
        rows,
        loaded["events"]["compactionEvents"],
        lambda row: row["inp"] + row["cacheRead"] + row["cacheWrite"],
        lambda row: row["gen"] / row["dt"] if row["dt"] else None,
    )

    # ---- hourly (all steps, with timed rate/index)
    by_hour_all = time_group(rows, lambda row: row["ts"].astimezone().hour)
    hourly = []
    for hour in range(24):
        rs = by_hour_all.get(hour, [])
        rs_timed = [row for row in rs if is_timed(row)]
        text_rs = [row for row in rs_timed if not row["tool"]]
        tokens = sum(row["gen"] for row in rs)
        timed_tokens = sum(row["gen"] for row in rs_timed)
        timed_seconds = sum(row["dt"] for row in rs_timed)
        visible = sum(row["out"] for row in rs)
        reasoning = sum(row["reas"] for row in rs)
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs)
        hour_ledger = ledger_by_hour.get(hour, [])
        error_rows = [
            entry for entry in hour_ledger if entry["reason"] in ("error", "both")
        ]
        skipped_cost = math.fsum(entry["cost"] for entry in hour_ledger)
        error_cost = math.fsum(entry["cost"] for entry in error_rows)
        ratio_values = [row["ratio"] for row in rs_timed if row.get("ratio") is not None]
        ratio = median_or_none(ratio_values)
        hourly.append(
            {
                "hour": hour,
                "nAll": len(rs),
                "nTimed": len(rs_timed),
                "n": len(rs_timed),
                "tokens": timed_tokens,
                "generatedTokens": tokens,
                "visibleOutputTokens": visible,
                "reasoningTokens": reasoning,
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "errorCostUsd": round(error_cost, 6),
                "skippedCostUsd": round(skipped_cost, 6),
                "totalSpendUsd": round(
                    math.fsum(row["cost"] for row in rs) + skipped_cost, 6
                ),
                "errorSteps": len(error_rows),
                "sessions": len({row["sessionId"] for row in rs}),
                "weighted": round(timed_tokens / timed_seconds, 1)
                if timed_seconds else None,
                "median": r1(statistics.median([row["gen"] / row["dt"] for row in rs_timed]))
                if rs_timed else None,
                "p90": r1(percentile(sorted(row["gen"] / row["dt"] for row in rs_timed), 90))
                if rs_timed else None,
                "ratio": round(ratio, 6) if ratio is not None else None,
                "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                "ratioN": len(ratio_values),
                "index": pace_index([row["ratio"] for row in rs_timed], pace_base),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["gen"] / row["dt"] for row in text_rs]))
                if text_rs else None,
                "textRatio": round(median_or_none([row["ratio"] for row in text_rs]), 6)
                if median_or_none([row["ratio"] for row in text_rs]) is not None else None,
                "textIndex": pace_index([row["ratio"] for row in text_rs], pace_base),
            }
        )

    # ---- context size (total input tokens)
    def context_buckets(source: list[dict]) -> list[dict]:
        out = []
        for label, lo, hi in [
            ("<50k", 0, 50_000),
            ("50-150k", 50_000, 150_000),
            ("150-400k", 150_000, 400_000),
            ("400k+", 400_000, 10**12),
        ]:
            rs = [
                row for row in source
                if lo <= (row["inp"] + row["cacheRead"] + row["cacheWrite"]) < hi
            ]
            if not rs:
                continue
            ratio_values = [row["ratio"] for row in rs if row.get("ratio") is not None]
            ratio = median_or_none(ratio_values)
            out.append(
                {
                    "label": label,
                    "n": len(rs),
                    "median": r1(statistics.median([row["gen"] / row["dt"] for row in rs])),
                    "weighted": round(sum(row["gen"] for row in rs) / sum(row["dt"] for row in rs), 1),
                    "ratio": round(ratio, 6) if ratio is not None else None,
                    "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                    "ratioN": len(ratio_values),
                    "index": pace_index([row["ratio"] for row in rs], pace_base),
                }
            )
        return out

    context = context_buckets(timed)
    context_text = context_buckets(text)

    # ---- peaks / slowest / lowest rate: all rows, with the timing reason
    def peak_row(row):
        return {
            "tps": r1(row["gen"] / row["dt"]) if row.get("dt") else None,
            "out": row["gen"],
            "visible": row["out"],
            "reasoning": row["reas"],
            "dt": r1(row["dt"]) if row["dt"] is not None else None,
            "model": row["short"],
            "at": row["ts"].astimezone().strftime("%Y-%m-%d %H:%M"),
            "tool": row["tool"],
            "inputTotal": row["inp"] + row["cacheRead"] + row["cacheWrite"],
            "reason": row["timing"],
            "sessionId": row["sessionId"],
            "project": row["projectLabel"],
        }

    valid_rate = [
        row for row in rows
        if row["dt"] and row["dt"] >= 0.5 and row["gen"] > 0
        and row["timing"] in ("plausible", "fastRate", "slowRate")
    ]
    fast_rows = [
        row for row in valid_rate if row["timing"] in ("plausible", "fastRate")
    ]
    slow_rows = [
        row for row in valid_rate if row["timing"] in ("plausible", "slowRate")
    ]
    duration_rows = [row for row in rows if row["dt"] and row["dt"] > 0]
    invalid_rate = [
        row for row in rows
        if row["dt"] and row["gen"] > 0
        and row["timing"] in ("tooShort", "tooShortDuration", "noTiming")
    ]
    peaks = [
        peak_row(row)
        for row in sorted(fast_rows, key=lambda item: item["gen"] / item["dt"], reverse=True)[:10]
    ]
    slowest = [
        peak_row(row)
        for row in sorted(duration_rows, key=lambda item: item["dt"], reverse=True)[:8]
    ]
    lowest_rate = [
        peak_row(row)
        for row in sorted(slow_rows, key=lambda item: item["gen"] / item["dt"])[:8]
    ]
    rejected_extremes = [
        peak_row(row)
        for row in sorted(invalid_rate, key=lambda item: item["gen"] / item["dt"], reverse=True)[:8]
    ]

    timed_all = summarize(timed)
    observed_rates = sorted(row["gen"] / row["dt"] for row in valid_rate)
    rejected_rates = sorted(row["gen"] / row["dt"] for row in invalid_rate)
    valid_durations = sorted(row["dt"] for row in valid_rate)
    all_durations = sorted(row["dt"] for row in duration_rows)
    timed_all["maxIncluded"] = timed_all.get("max")
    timed_all["maxObserved"] = r1(observed_rates[-1]) if observed_rates else None
    timed_all["maxRejected"] = r1(rejected_rates[-1]) if rejected_rates else None
    timed_all["maxRejectedReason"] = (
        max(invalid_rate, key=lambda item: item["gen"] / item["dt"])["timing"]
        if invalid_rate else None
    )
    timed_all["minIncluded"] = (
        r1(min((row["gen"] / row["dt"] for row in timed), default=None))
        if timed else None
    )
    timed_all["maxDurationIncluded"] = (
        r1(max((row["dt"] for row in timed), default=None)) if timed else None
    )
    timed_all["maxDurationObserved"] = r1(valid_durations[-1]) if valid_durations else None
    timed_all["maxDurationAny"] = r1(all_durations[-1]) if all_durations else None
    timed_all["upperCensored"] = timing_info["fastRate"]
    timed_all["lowerCensored"] = timing_info["slowRate"]
    timed_all["durationCensoredHigh"] = timing_info["tooLong"]
    timed_all["durationCensoredLow"] = timing_info["tooShortDuration"]

    # ---- totals
    date_from = min((row["ts"] for row in rows), default=None)
    date_to = max((row["ts"] for row in rows), default=None)
    all_reasoning = reasoning_summary(rows)
    error_ledger = [
        {
            "ts": int(entry["ts"].timestamp()) if entry["ts"] else None,
            "sessionId": entry["sessionId"],
            "project": project_labels.get(entry["project"], entry["projectLabel"]),
            "projectPath": entry["project"],
            "entry": entry["entry"],
            "model": entry["model"],
            "provider": entry["provider"],
            "short": entry["short"],
            "agent": entry["agent"],
            "variant": entry["variant"],
            "finish": entry["finish"],
            "reason": entry["reason"],
            "outputTokens": entry["out"],
            "reasoningTokens": entry["reas"],
            "generatedTokens": entry["gen"],
            "freshInputTokens": entry["inp"],
            "cacheReadTokens": entry["cacheRead"],
            "cacheWriteTokens": entry["cacheWrite"],
            "costUsd": round(entry["cost"], 6),
            "llmSeconds": round(entry["dt"], 3) if entry["dt"] is not None else None,
            "totalSeconds": round(entry["totalDt"], 3) if entry["totalDt"] is not None else None,
            "toolSeconds": round(entry["toolDt"], 3) if entry["toolDt"] is not None else None,
            "toolCalls": entry["nTools"],
            "empty": entry["empty"],
            "error": entry["error"],
        }
        for entry in ledger
    ]
    error_cost = math.fsum(entry["cost"] for entry in ledger)
    error_seconds = math.fsum(entry["totalDt"] or 0 for entry in ledger)
    runtime["negativeOverhead"] = loaded["events"]["negativeOverhead"]

    data = {
        "schemaVersion": SCHEMA_VERSION,
        "meta": {
            "generatedAt": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
            "source": "OpenCode SQLite",
            "dbLabel": db_path.replace(os.path.expanduser("~"), "~"),
            "modelFilters": model_filters,
            "models": len(models),
            "sessions": len(loaded["sessions"]),
            "sessionsTotal": loaded["sessionsTotal"],
            "sessionsRoot": loaded["sessionsRoot"],
            "sessionsSubagent": loaded["sessionsSubagent"],
            "stepsScanned": loaded["scanned"],
            "emptyStepsSkipped": loaded["emptySteps"],
            "errorSteps": loaded["errorSteps"],
            "errorCostUsd": round(error_cost, 6),
            "errorSeconds": round(error_seconds, 3),
            "duplicatesSkipped": loaded["duplicates"],
            "fingerprintCollisions": loaded["collisions"],
            "timing": timing_info,
            "sourceWatermark": {
                "maxCreatedMs": loaded["maxCreatedMs"],
                "scanned": loaded["scanned"],
                "rows": len(rows),
                "duplicates": loaded["duplicates"],
            },
            "includeTitles": include_titles,
            "fullPaths": full_paths,
            "since": since,
            "until": until,
            "timezone": local_tz_label(),
            "paceBase": round(pace_base, 6) if pace_base else None,
            "paceBasePct": round(pace_base * 100, 1) if pace_base else None,
            "paceBaseline": (
                {
                    "from": baseline_from,
                    "to": baseline_to,
                    "paceBase": round(pace_base, 6) if pace_base else None,
                    "paceBasePct": round(pace_base * 100, 1) if pace_base else None,
                    "n": len(ratio_values),
                }
                if baseline_from and baseline_to
                else None
            ),
            "dateFrom": date_from.astimezone().date().isoformat() if date_from else None,
            "dateTo": date_to.astimezone().date().isoformat() if date_to else None,
        },
        "totals": {
            "steps": len(rows),
            "toolSteps": sum(1 for row in rows if row["tool"]),
            "textSteps": sum(1 for row in rows if not row["tool"]),
            "toolCalls": total_calls,
            "outputTokens": total_output,
            "visibleOutputTokens": total_output,
            "reasoningTokens": total_reasoning,
            "generatedTokens": total_generated,
            "reasoningShare": round(total_reasoning / total_generated, 4) if total_generated else None,
            "inputTokens": total_input,
            "inputRawTokens": total_raw_input,
            "freshInputTokens": total_fresh_input,
            "inputTotal": total_input,
            "cacheReadTokens": total_cache_read,
            "cacheWriteTokens": total_cache_write,
            "costUsd": round(total_cost, 6),
            "errorCostUsd": round(error_cost, 6),
            "totalSpendUsd": round(total_spend, 6),
            "models": len(models),
        },
        "timed": {
            "all": timed_all,
            "text": summarize(text),
            "tool": summarize([row for row in timed if row["tool"]]),
        },
        "byModel": by_model,
        "cache": cache,
        "cost": cost,
        "reasoning": all_reasoning,
        "runtime": runtime,
        "sessionHealth": session_health,
        "coverage": coverage,
        "tools": tools,
        "thinking": thinking,
        "agents": agents,
        "variants": variants,
        "entrypoints": entrypoints,
        "projects": projects,
        "projectsTotal": len(projects),
        "errorLedger": error_ledger,
        "lookups": {
            "models": [
                {
                    "id": model,
                    "short": model_meta[model]["short"],
                    "provider": model_meta[model]["provider"],
                }
                for model in models
            ],
            "tools": tool_names,
            "agents": agent_names,
            "variants": variant_names,
            "projects": project_names,
            "projectLabels": project_labels_out,
            "entrypoints": entrypoint_names,
            "finishes": finish_names,
            "timingReasons": TIMING_REASONS,
        },
        "fits": fits_out,
        "fitsGlobal": global_fits,
        "prices": [
            {
                "fresh": next((item["freshInputRate"] for item in cost_by_model if item["model"] == model), None),
                "cache": next((item["cacheRate"] for item in cost_by_model if item["model"] == model), None),
                "cacheWrite": next((item["cacheWriteRate"] for item in cost_by_model if item["model"] == model), None),
                "output": next((item["outputRate"] for item in cost_by_model if item["model"] == model), None),
                "inputIncludesCache": False,
                "inputSemanticsSource": "normalized",
            }
            for model in models
        ],
        "steps": steps_out,
        "sessions": session_list,
        "regression": regressions,
        "histogram": {
            "labels": labels,
            "all": histogram(timed, edges),
            "text": histogram(text, edges),
        },
        "sizeBuckets": size_buckets,
        "daily": daily,
        "dailyByModel": daily_by_model,
        "hourly": hourly,
        "context": context,
        "contextText": context_text,
        "peaks": peaks,
        "slowest": slowest,
        "lowestRate": lowest_rate,
        "rejectedExtremes": rejected_extremes,
        "anomalies": anomalies,
        "fork": {
            "duplicateSteps": loaded["duplicates"],
            "duplicateTokens": loaded["duplicateTokens"],
            "duplicateCostUsd": round(loaded["duplicateCost"], 6),
            "sessionsWithDuplicates": len(loaded["duplicateSessions"]),
            "fingerprintCollisions": loaded["collisions"],
        },
        "compactionImpact": compaction,
        "compactionEvents": [
            {
                "ts": int(event["ts"].timestamp()) if event["ts"] else None,
                "sessionId": event["sessionId"],
                "tokensBefore": event["tokensBefore"],
            }
            for event in loaded["events"]["compactionEvents"]
        ],
        "modelChangeEvents": [
            {
                "ts": int(event["ts"].timestamp()) if event["ts"] else None,
                "sessionId": event["sessionId"],
                "model": event["model"],
            }
            for event in loaded["events"]["modelSwitchEvents"]
        ],
    }
    errors = validate_common(data)
    if errors:
        raise SystemExit(
            "telemetry invariant failure:\n- " + "\n- ".join(errors)
        )
    return data


# ---------------------------------------------------------------- output


def write_outputs(data: dict, out_dir: str) -> tuple[Path, Path, Path]:
    target = Path(out_dir)
    target.mkdir(parents=True, exist_ok=True)
    json_path = target / "telemetry-data.json"
    js_path = target / "telemetry-data.js"
    json_path.write_text(json.dumps(data, indent=1, ensure_ascii=False), encoding="utf-8")
    js_path.write_text(
        "window.OPENCODE_TELEMETRY = "
        + json.dumps(data, ensure_ascii=False, separators=(",", ":"))
        + ";\n",
        encoding="utf-8",
    )
    tool_dir = Path(__file__).resolve().parent
    html_source = tool_dir / "index.html"
    if target.resolve() == (tool_dir / "data").resolve():
        html_path = html_source
    else:
        html_path = target / "index.html"
        html_text = html_source.read_text(encoding="utf-8").replace(
            'src="data/telemetry-data.js"', 'src="telemetry-data.js"'
        )
        html_path.write_text(html_text, encoding="utf-8")
    return json_path, js_path, html_path


def print_summary(data: dict, json_path: Path, js_path: Path) -> None:
    totals = data["totals"]
    timed = data["timed"]
    meta = data["meta"]
    cache = data["cache"]
    cost = data["cost"]
    reasoning = data["reasoning"]
    timing = meta["timing"]
    print(
        f"Scanned {meta['stepsScanned']:,} assistant steps · {meta['sessions']:,} sessions · "
        f"{meta['models']} models · {totals['steps']:,} steps with tokens "
        f"({meta['duplicatesSkipped']} fork copies, {meta['fingerprintCollisions']} fingerprint collisions, {timing['empty']} empty skipped)"
    )
    print(
        f"Generated {totals['generatedTokens']:,} tokens "
        f"({totals['outputTokens']:,} visible + {totals['reasoningTokens']:,} reasoning, "
        f"{totals['reasoningShare'] * 100:.1f}% reasoning) · "
        f"{timing['plausible']:,} timed steps"
    )
    if timed["all"]["n"]:
        print(
            f"Speed: weighted {timed['all']['weighted']} tok/s · median {timed['all']['median']} · "
            f"p90 {timed['all']['p90']} · visible median {timed['all']['visibleMedian']}"
        )
    if timed["text"]["n"]:
        print(
            f"Final answers ({timed['text']['n']:,}): weighted {timed['text']['weighted']} tok/s · "
            f"median {timed['text']['median']} · p90 {timed['text']['p90']}"
        )
    if reasoning["tokenShare"] is not None:
        print(
            f"Reasoning: {totals['reasoningShare'] * 100:.0f}% of generated tokens · "
            f"{reasoning['steps']:,} timed steps · median {reasoning['medianReasonTokens']} reasoning tokens"
        )
    runtime = data.get("runtime") or {}
    if runtime.get("steps"):
        model_share = runtime.get("modelTimeShare")
        tool_share = runtime.get("weightedShare")
        print(
            f"Step clock: model {model_share * 100 if model_share is not None else 0:.0f}% · "
            f"tools {tool_share * 100 if tool_share is not None else 0:.0f}% "
            f"(median tool {runtime['medianToolSeconds']}s, p90 {runtime['p90ToolSeconds']}s)"
        )
    top = sorted(data["byModel"], key=lambda item: -item["steps"])[:6]
    print("\nModel speed (by steps):")
    print(f"  {'model':<44} {'steps':>7} {'gen wtd':>8} {'gen med':>8} {'visible':>8} {'marginal':>8} {'fixed':>7} {'reas%':>6}")
    for item in top:
        print(
            f"  {item['model'][:44]:<44} {item['steps']:>7} "
            f"{item['weighted'] if item['weighted'] is not None else '-':>8} "
            f"{item['median'] if item['median'] is not None else '-':>8} "
            f"{item['visibleMedian'] if item['visibleMedian'] is not None else '-':>8} "
            f"{item['pureRate'] if item['pureRate'] is not None else '-':>8} "
            f"{item['fixedSeconds'] if item['fixedSeconds'] is not None else '-':>7} "
            f"{(item['reasoningShare'] or 0) * 100:>5.0f}%"
        )
    bits = []
    if cache.get("hitRate") is not None:
        bits.append(f"{cache['hitRate'] * 100:.1f}% cache read")
    if cache.get("savings") is not None:
        bits.append(f"~${cache['savings']:,.2f} read savings")
    bits.append(f"recorded ${cost['recorded']:,.2f}")
    if cost.get("perStep") is not None:
        bits.append(f"${cost['perStep']:.4f}/step")
    print("\nCost: " + " · ".join(bits))
    print(f"Wrote {json_path}")
    print(f"Wrote {js_path}")


def serve(root: Path, port: int, open_browser: bool) -> None:
    import functools
    import http.server
    import webbrowser

    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(root))
    with http.server.ThreadingHTTPServer(("127.0.0.1", port), handler) as httpd:
        url = f"http://127.0.0.1:{port}/index.html"
        print(f"Serving {root} at {url}")
        if open_browser:
            webbrowser.open(url)
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print("\nStopped.")


def main() -> None:
    here = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(
        description="Build a speed, token and cost report from the OpenCode SQLite store."
    )
    parser.add_argument("--db", default=DEFAULT_DB, help=f"OpenCode database (default: {DEFAULT_DB})")
    parser.add_argument("--models", default="", help="comma-separated model filters (default: all models)")
    parser.add_argument("--since", default=None, help="earliest local date, YYYY-MM-DD")
    parser.add_argument("--until", default=None, help="latest local date, YYYY-MM-DD")
    parser.add_argument("--out", default=str(here / "data"), help="output directory (default: ./data)")
    parser.add_argument("--no-titles", action="store_true", help="omit session titles")
    parser.add_argument("--full-paths", action="store_true", help="keep full project paths (default: basenames)")
    parser.add_argument("--baseline", default=None, help="pace-index baseline window, YYYY-MM-DD..YYYY-MM-DD")
    parser.add_argument("--min-seconds", type=float, default=0.5, help="minimum LLM seconds (default 0.5)")
    parser.add_argument("--max-seconds", type=float, default=300.0, help="maximum LLM seconds (default 300)")
    parser.add_argument("--min-tokens", type=int, default=20, help="minimum generated tokens (default 20)")
    parser.add_argument("--min-rate", type=float, default=3.0, help="minimum generated tok/s (default 3)")
    parser.add_argument("--max-rate", type=float, default=400.0, help="maximum generated tok/s (default 400)")
    parser.add_argument("--serve", action="store_true", help="serve the report and open it")
    parser.add_argument("--port", type=int, default=8000, help="port for --serve (default: 8000)")
    parser.add_argument("--no-open", action="store_true", help="do not open a browser with --serve")
    args = parser.parse_args()

    model_filters = [term.strip() for term in args.models.split(",") if term.strip()]
    data = build(
        args.db,
        model_filters,
        args.since,
        args.until,
        include_titles=not args.no_titles,
        full_paths=args.full_paths,
        thresholds={
            "minSeconds": args.min_seconds,
            "maxSeconds": args.max_seconds,
            "minTokens": args.min_tokens,
            "minRate": args.min_rate,
            "maxRate": args.max_rate,
        },
        baseline=args.baseline,
    )
    json_path, js_path, html_path = write_outputs(data, args.out)
    print_summary(data, json_path, js_path)
    if args.serve:
        serve(html_path.parent, args.port, open_browser=not args.no_open)
    else:
        print(f"Open {html_path} in a browser, or re-run with --serve.")


if __name__ == "__main__":
    main()
