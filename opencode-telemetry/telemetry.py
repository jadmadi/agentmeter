#!/usr/bin/env python3
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
from pathlib import Path

DEFAULT_DB = "~/.local/share/opencode/opencode.db"
DEFAULT_THRESHOLDS = {
    "minSeconds": 0.5,
    "maxSeconds": 300.0,
    "minTokens": 20,
    "minRate": 3.0,
    "maxRate": 400.0,
}
THRESHOLDS = dict(DEFAULT_THRESHOLDS)

# ---------------------------------------------------------------- utilities


def percentile(sorted_values: list[float], p: float) -> float | None:
    if not sorted_values:
        return None
    if len(sorted_values) == 1:
        return sorted_values[0]
    pos = (len(sorted_values) - 1) * p / 100.0
    lo = int(pos)
    hi = min(lo + 1, len(sorted_values) - 1)
    return sorted_values[lo] + (sorted_values[hi] - sorted_values[lo]) * (pos - lo)


def r1(value):
    return None if value is None else round(value, 1)


def r2(value):
    return None if value is None else round(value, 2)


def median_or_none(values):
    values = [v for v in values if v is not None]
    return statistics.median(values) if values else None


def local_tz_label() -> str:
    now = dt.datetime.now().astimezone()
    offset = now.utcoffset() or dt.timedelta(0)
    minutes = int(offset.total_seconds() // 60)
    sign = "+" if minutes >= 0 else "-"
    return f"UTC{sign}{abs(minutes) // 60:02d}:{abs(minutes) % 60:02d}"


def connect_ro(path: str) -> sqlite3.Connection:
    uri = Path(path).expanduser().resolve().as_uri() + "?mode=ro"
    conn = sqlite3.connect(uri, uri=True, timeout=15)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA query_only = ON")
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
    collisions = 0
    empty_steps = 0
    error_steps = 0
    scanned = 0
    considered = 0
    no_created = 0
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
        "modelSwitches": 0,
        "agentSwitches": 0,
        "idle": 0,
        "shell": 0,
        "errors": 0,
        "noStream": 0,
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
            overhead_seconds = total_seconds - llm_seconds - tool_seconds

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
        "duplicates": duplicates,
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
    """Regress duration on generated tokens: dt = fixed + tokens / rate."""
    if len(rows) < 30:
        return None
    xs = [row["gen"] for row in rows]
    ys = [row["dt"] for row in rows]
    n = len(xs)
    mx = sum(xs) / n
    my = sum(ys) / n
    sxx = sum((x - mx) ** 2 for x in xs)
    if sxx == 0:
        return None
    sxy = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    slope = sxy / sxx
    intercept = my - slope * mx
    if slope <= 0:
        return None
    syy = sum((y - my) ** 2 for y in ys)
    r_squared = (sxy * sxy) / (sxx * syy) if syy else None
    return {
        "fixedSeconds": round(intercept, 2),
        "pureRate": round(1 / slope, 1),
        "r2": round(r_squared, 2) if r_squared is not None else None,
        "n": n,
    }


def solve_linear(matrix, rhs):
    n = len(rhs)
    m = [list(matrix[i]) + [rhs[i]] for i in range(n)]
    for col in range(n):
        pivot = max(range(col, n), key=lambda r: abs(m[r][col]))
        m[col], m[pivot] = m[pivot], m[col]
        if abs(m[col][col]) < 1e-12:
            return None
        for r in range(col + 1, n):
            factor = m[r][col] / m[col][col]
            for k in range(col, n + 1):
                m[r][k] -= factor * m[col][k]
    x = [0.0] * n
    for r in range(n - 1, -1, -1):
        x[r] = (m[r][n] - sum(m[r][c] * x[c] for c in range(r + 1, n))) / m[r][r]
    return x


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
        "reasoningTokens": reasoning,
        "generatedTokens": generated,
        "reasoningShare": round(reasoning / generated, 4) if generated else None,
        "inputTokens": inp,
        "inputTotal": total_input,
        "cacheReadTokens": cache_read,
        "cacheWriteTokens": cache_write,
        "cacheHitRate": round(cache_read / total_input, 4) if total_input else None,
        "cacheWriteShare": round(cache_write / total_input, 4) if total_input else None,
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
        "inputIncludesCache": False,
        "costPerStep": round(recorded / len(rows), 4) if rows else None,
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

    call_rows = []
    for row in with_tools:
        for call in row.get("toolDetails") or []:
            if call.get("runtime") is not None:
                call_rows.append((call["name"], call["runtime"], row["totalDt"]))
    by_tool = []
    for name in sorted({name for name, _, _ in call_rows}):
        rs = [item for item in call_rows if item[0] == name]
        values = sorted(item[1] for item in rs)
        totals = [item[2] for item in rs]
        by_tool.append(
            {
                "tool": name,
                "calls": len(rs),
                "medianToolSeconds": r2(statistics.median(values)),
                "p90ToolSeconds": r2(percentile(values, 90)),
                "medianStepSeconds": r2(statistics.median(totals)),
                "weightedShare": round(sum(values) / sum(totals), 4) if sum(totals) else None,
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
        "byTool": by_tool,
    }


def expected_ratio(row: dict, fits: dict, global_fits: dict) -> float | None:
    kind = "tool" if row["tool"] else "text"
    fit = fits.get((row["model"], kind)) or fits.get((row["model"], "all")) or global_fits.get(kind)
    if not fit or not row.get("dt"):
        return None
    expected = fit["fixedSeconds"] + row["gen"] / fit["pureRate"]
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
        model_timing = collections.Counter(row["timingBucket"] for row in rs)
        by_model.append(
            {
                "model": model,
                "short": model_meta[model]["short"],
                "provider": model_meta[model]["provider"],
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": sum(row["out"] for row in rs),
                "reasoningTokens": model_reas,
                "generatedTokens": model_gen,
                "reasoningShare": round(model_reas / model_gen, 4) if model_gen else None,
                "reasoningSteps": reasoning["steps"],
                "inputTokens": sum(row["inp"] for row in rs),
                "inputTotal": sum(row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs),
                "cacheReadTokens": sum(row["cacheRead"] for row in rs),
                "cacheWriteTokens": sum(row["cacheWrite"] for row in rs),
                "cacheHitRate": round(
                    sum(row["cacheRead"] for row in rs) / (sum(row["inp"] for row in rs) + sum(row["cacheRead"] for row in rs) + sum(row["cacheWrite"] for row in rs)),
                    4,
                ) if (sum(row["inp"] for row in rs) + sum(row["cacheRead"] for row in rs) + sum(row["cacheWrite"] for row in rs)) else None,
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "timedN": summary["n"],
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
                "timing": {
                    key: model_timing.get(key, 0)
                    for key in ("plausible", "noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate", "fastRate")
                },
                "rejected": len(rs) - model_timing.get("plausible", 0),
            }
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
    total_cache_read = sum(row["cacheRead"] for row in rows)
    total_cache_write = sum(row["cacheWrite"] for row in rows)
    total_output = sum(row["out"] for row in rows)
    total_reasoning = sum(row["reas"] for row in rows)
    total_generated = sum(row["gen"] for row in rows)
    total_cost = math.fsum(row["cost"] for row in rows)
    unattributed_cost = math.fsum(
        item["recordedCost"] for item in cost_by_model if item["freshCost"] is None
    )
    cache = {
        "inputTokens": total_input,
        "cacheReadTokens": total_cache_read,
        "cacheWriteTokens": total_cache_write,
        "outputTokens": total_output,
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
    }
    cost = {
        "recorded": round(total_cost, 6),
        "tokenCost": round(math.fsum(item["tokenCost"] for item in cost_by_model), 6),
        "freshCost": round(math.fsum(item["freshCost"] or 0 for item in cost_by_model), 6),
        "cacheReadCost": round(math.fsum(item["cacheReadCost"] or 0 for item in cost_by_model), 6),
        "cacheWriteCost": round(math.fsum(item["cacheWriteCost"] or 0 for item in cost_by_model), 6),
        "outputCost": round(math.fsum(item["outputCost"] or 0 for item in cost_by_model), 6),
        "unattributedCost": round(unattributed_cost, 6),
        "perStep": round(total_cost / len(rows), 4) if rows else None,
        "perMillionOutput": round(total_cost / total_generated * 1e6, 2) if total_generated else None,
        "byModel": cost_by_model,
    }

    # ---- tool mix: attribute every call, not just the step's first tool
    tool_stats = collections.defaultdict(
        lambda: {
            "calls": 0, "steps": set(), "generated": 0.0, "visible": 0.0,
            "reasoning": 0.0, "cost": 0.0, "tokensPerCall": [], "runtimes": [],
            "errors": 0, "timedCalls": 0, "timedGenerated": 0.0, "timedSeconds": 0.0,
            "rates": [],
        }
    )
    total_calls = 0
    for row in rows:
        calls = row["toolDetails"] or [{"name": "(final answer)", "runtime": None, "error": False}]
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
                "shareCalls": round(stat["calls"] / total_calls, 4) if total_calls else None,
                "generatedTokens": round(stat["generated"], 1),
                "visibleTokens": round(stat["visible"], 1),
                "reasoningTokens": round(stat["reasoning"], 1),
                "medianTokensPerCall": r1(statistics.median(stat["tokensPerCall"])),
                "timedCalls": stat["timedCalls"],
                "weighted": round(stat["timedGenerated"] / stat["timedSeconds"], 1) if stat["timedSeconds"] else None,
                "medianTps": r1(statistics.median(stat["rates"])) if stat["rates"] else None,
                "medianRuntime": r2(statistics.median(runtimes)) if runtimes else None,
                "p90Runtime": r2(percentile(runtimes, 90)) if runtimes else None,
                "errors": stat["errors"],
                "errRate": round(stat["errors"] / stat["calls"], 4) if stat["calls"] else None,
                "costUsd": round(stat["cost"], 6),
                "costPerCall": round(stat["cost"] / stat["calls"], 4) if stat["calls"] else None,
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

    project_paths = sorted({row["project"] for row in rows})
    project_base_counts = collections.Counter(
        os.path.basename(path.rstrip("/")) or path for path in project_paths
    )
    project_labels = {}
    for path in project_paths:
        base = os.path.basename(path.rstrip("/")) or path
        if full_paths:
            label = path
        elif project_base_counts[base] == 1:
            label = base
        else:
            parent = os.path.basename(os.path.dirname(path.rstrip("/"))) or ""
            label = f"{base} · {parent}" if parent else base
        project_labels[path] = label
    project_groups = collections.defaultdict(list)
    for row in rows:
        project_groups[row["project"]].append(row)
    projects = []
    for path, rs in project_groups.items():
        projects.append(
            {
                "name": project_labels.get(path, os.path.basename(path.rstrip("/")) or path),
                "path": path,
                "steps": len(rs),
                "sessions": len({row["sessionId"] for row in rs}),
                "generatedTokens": sum(row["gen"] for row in rs),
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
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
        "errorSteps": len(ledger),
        "errorCostUsd": round(error_cost, 6),
        "errorSeconds": round(error_seconds, 3),
        "versions": [
            {"version": version, "sessions": count}
            for version, count in event_versions.most_common()
        ],
    }
    timing_counts = collections.Counter(row["timingBucket"] for row in rows)
    coverage = {
        "timing": timing_counts.get("plausible", 0) > 0,
        "streamed": any(row["dt"] is not None for row in rows),
        "completed": any(row["totalDt"] is not None for row in rows),
        "toolRuntime": any(row["toolDt"] is not None for row in rows),
        "reasoning": total_reasoning > 0,
        "cacheRead": total_cache_read > 0,
        "cacheWrite": total_cache_write > 0,
        "titles": any(row.get("sessionTitle") for row in rows),
        "parentSessions": loaded["sessionsSubagent"] > 0,
        "cost": total_cost > 0,
        "variants": any(row["variant"] != "default" for row in rows),
        "errors": loaded["events"]["errors"] > 0,
        "errorLedger": bool(ledger),
        "ttft": any(row["ttft"] is not None for row in rows),
        "overhead": any(row["overheadDt"] is not None for row in rows),
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
                    ]
                    for call in row["toolDetails"]
                ],
            ]
        )
    steps_out.sort(key=lambda item: item[0])

    session_rows = collections.defaultdict(list)
    for row in rows:
        session_rows[row["sessionId"]].append(row)

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
        project_path = rs[0]["project"]
        project_label = project_labels.get(project_path, rs[0].get("projectLabel") or project_path)
        ledger_rows = [entry for entry in ledger if entry["sessionId"] == sid]
        error_cost = math.fsum(entry["cost"] for entry in ledger_rows)
        error_seconds = math.fsum(entry["totalDt"] or 0 for entry in ledger_rows)
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
                "reasoningTokens": sum(row["reas"] for row in rs),
                "generatedTokens": gen_tokens,
                "inputTokens": sum(row["inp"] for row in rs),
                "inputTotal": sum(row["inp"] + row["cacheRead"] + row["cacheWrite"] for row in rs),
                "cacheReadTokens": sum(row["cacheRead"] for row in rs),
                "cacheWriteTokens": sum(row["cacheWrite"] for row in rs),
                "costUsd": round(math.fsum(row["cost"] for row in rs), 6),
                "errorSteps": len(ledger_rows),
                "errorCostUsd": round(error_cost, 6),
                "errorSeconds": round(error_seconds, 3),
                "timedN": len(timed_rs),
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

    # ---- daily
    by_day = time_group(timed, lambda row: row["ts"].astimezone().date().isoformat())
    daily = []
    for date in sorted(by_day):
        rs = by_day[date]
        tokens = sum(row["gen"] for row in rs)
        seconds = sum(row["dt"] for row in rs)
        text_rs = [row for row in rs if not row["tool"]]
        daily.append(
            {
                "date": date,
                "n": len(rs),
                "tokens": tokens,
                "weighted": round(tokens / seconds, 1),
                "median": r1(statistics.median([row["gen"] / row["dt"] for row in rs])),
                "ratio": r1(median_or_none([row["ratio"] for row in rs])),
                "index": pace_index([row["ratio"] for row in rs], pace_base),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["gen"] / row["dt"] for row in text_rs])) if text_rs else None,
                "textIndex": pace_index([row["ratio"] for row in text_rs], pace_base),
            }
        )

    # ---- hourly
    by_hour = time_group(timed, lambda row: row["ts"].astimezone().hour)
    hourly = []
    for hour in range(24):
        rs = by_hour.get(hour, [])
        text_rs = [row for row in rs if not row["tool"]]
        tokens = sum(row["gen"] for row in rs)
        seconds = sum(row["dt"] for row in rs)
        hourly.append(
            {
                "hour": hour,
                "n": len(rs),
                "tokens": tokens,
                "weighted": round(tokens / seconds, 1) if seconds else None,
                "median": r1(statistics.median([row["gen"] / row["dt"] for row in rs])) if rs else None,
                "ratio": r1(median_or_none([row["ratio"] for row in rs])),
                "index": pace_index([row["ratio"] for row in rs], pace_base),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["gen"] / row["dt"] for row in text_rs])) if text_rs else None,
                "textRatio": r1(median_or_none([row["ratio"] for row in text_rs])),
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
            out.append(
                {
                    "label": label,
                    "n": len(rs),
                    "median": r1(statistics.median([row["gen"] / row["dt"] for row in rs])),
                    "weighted": round(sum(row["gen"] for row in rs) / sum(row["dt"] for row in rs), 1),
                    "ratio": r1(median_or_none([row["ratio"] for row in rs])),
                    "index": pace_index([row["ratio"] for row in rs], pace_base),
                }
            )
        return out

    context = context_buckets(timed)
    context_text = context_buckets(text)

    # ---- peaks / slowest
    def peak_row(row):
        return {
            "tps": r1(row["gen"] / row["dt"]),
            "out": row["gen"],
            "visible": row["out"],
            "reasoning": row["reas"],
            "dt": r1(row["dt"]),
            "model": row["short"],
            "at": row["ts"].astimezone().strftime("%Y-%m-%d %H:%M"),
            "tool": row["tool"],
            "input": row["inp"] + row["cacheRead"] + row["cacheWrite"],
        }

    peaks = [peak_row(row) for row in sorted(timed, key=lambda row: row["gen"] / row["dt"], reverse=True)[:10]]
    slowest = [peak_row(row) for row in sorted(timed, key=lambda row: row["gen"] / row["dt"])[:8]]

    # ---- totals
    date_from = min((row["ts"] for row in rows), default=None)
    date_to = max((row["ts"] for row in rows), default=None)
    all_reasoning = reasoning_summary(rows)
    error_ledger = [
        {
            "ts": int(entry["ts"].timestamp()) if entry["ts"] else None,
            "sessionId": entry["sessionId"],
            "project": entry["projectLabel"],
            "projectPath": entry["project"],
            "entry": entry["entry"],
            "model": entry["model"],
            "provider": entry["provider"],
            "short": entry["short"],
            "agent": entry["agent"],
            "variant": entry["variant"],
            "finish": entry["finish"],
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

    data = {
        "schemaVersion": 2,
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
            "includeTitles": include_titles,
            "fullPaths": full_paths,
            "since": since,
            "until": until,
            "timezone": local_tz_label(),
            "paceBase": round(pace_base * 100, 1) if pace_base else None,
            "paceBaseline": (
                {
                    "from": baseline_from,
                    "to": baseline_to,
                    "paceBase": round(pace_base * 100, 1) if pace_base else None,
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
            "outputTokens": total_output,
            "reasoningTokens": total_reasoning,
            "generatedTokens": total_generated,
            "reasoningShare": round(total_reasoning / total_generated, 4) if total_generated else None,
            "inputTokens": sum(row["inp"] for row in rows),
            "inputTotal": total_input,
            "cacheReadTokens": total_cache_read,
            "cacheWriteTokens": total_cache_write,
            "costUsd": round(total_cost, 6),
            "models": len(models),
        },
        "timed": {
            "all": summarize(timed),
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
        "projects": projects[:12],
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
        },
        "fits": fits_out,
        "fitsGlobal": global_fits,
        "prices": prices_out,
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
        "hourly": hourly,
        "context": context,
        "contextText": context_text,
        "peaks": peaks,
        "slowest": slowest,
    }
    return data


# ---------------------------------------------------------------- output


def write_outputs(data: dict, out_dir: str) -> tuple[Path, Path]:
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
    return json_path, js_path


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
    json_path, js_path = write_outputs(data, args.out)
    print_summary(data, json_path, js_path)
    if args.serve:
        serve(here, args.port, open_browser=not args.no_open)
    else:
        print(f"Open {here / 'index.html'} in a browser, or re-run with --serve.")


if __name__ == "__main__":
    main()
