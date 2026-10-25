#!/usr/bin/env python3
"""
Command Code session telemetry.

Reconstructs per-step throughput, token/cache usage, cost, tools, sessions and
time patterns from Command Code session transcripts. Works with every model in
the logs; no prompts, assistant text, tool arguments or file contents are read
or emitted.

Each assistant message carries `usage` and (for timed replies) a parent entry
whose timestamp marks when the request was triggered:

    parent entry committed  ->  this reply committed

That includes TTFT / prefill / queueing / generation, and excludes tool
execution (tool results share the assistant's commit timestamp, so tool runtime
is not recoverable from the transcript).

Outputs:
    data/telemetry-data.json   machine-readable aggregate
    data/telemetry-data.js     same data as `window.CMD_TELEMETRY` for index.html

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
import glob
import json
import os
import re
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

# ---------------------------------------------------------------- utilities


def parse_ts(value: str | None) -> dt.datetime | None:
    if not value:
        return None
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (ValueError, TypeError):
        return None


def local_tz_label() -> str:
    now = dt.datetime.now().astimezone()
    offset = now.utcoffset() or dt.timedelta(0)
    minutes = int(offset.total_seconds() // 60)
    sign = "+" if minutes >= 0 else "-"
    return f"UTC{sign}{abs(minutes) // 60:02d}:{abs(minutes) % 60:02d}"


# ---------------------------------------------------------------- loading


def load_rows(
    root: str,
    model_filters: list[str],
    since: str | None,
    until: str | None,
    include_titles: bool,
    full_paths: bool,
):
    pattern = os.path.join(os.path.expanduser(root), "**", "*.jsonl")
    files = sorted(glob.glob(pattern, recursive=True))
    seen: set[str] = set()
    rows: list[dict] = []
    ledger: list[dict] = []
    sessions: set[str] = set()
    duplicate_sessions = collections.Counter()
    duplicate_tokens = 0
    duplicate_cost = 0.0
    counts = {
        "files": len(files),
        "scanned": 0,
        "assistant": 0,
        "considered": 0,
        "rows": 0,
        "duplicates": 0,
        "noOutput": 0,
        "noTimestamp": 0,
        "filteredModel": 0,
        "filteredDate": 0,
    }
    timing = {key: 0 for key in TIMING_REASONS}
    events = {
        "compactions": 0,
        "compactionTokens": 0,
        "compactionEvents": [],
        "modelChanges": 0,
        "modelChangeEvents": [],
        "fileSnapshots": 0,
        "hooks": [],
        "toolWindows": [],
        "branches": set(),
    }

    def ledger_entry(reason, rid, session, project, model, ts, out, inp, cr, cw, cost):
        entry = {
            "id": rid,
            "reason": reason,
            "sessionId": session,
            "projectPath": project,
            "model": model,
            "ts": int(ts.timestamp()) if ts else None,
            "outputTokens": out,
            "inputTokens": inp,
            "cacheReadTokens": cr,
            "cacheWriteTokens": cw,
            "costUsd": round(cost, 6),
        }
        ledger.append(entry)
        return entry

    for path in files:
        entries: dict[str, dict] = {}
        session_id = None
        cwd = None
        branch = None
        contributed = False
        entrypoint = None
        session_title = None
        pre_hooks: dict[str, float] = {}
        try:
            with open(path[:-6] + ".meta.json") as mfh:
                sidecar = json.load(mfh)
                entrypoint = sidecar.get("entrypoint")
                if include_titles:
                    session_title = sidecar.get("title")
        except (OSError, ValueError, TypeError):
            pass
        try:
            fh = open(path, errors="ignore")
        except OSError:
            continue
        with fh:
            for line in fh:
                try:
                    rec = json.loads(line)
                except (json.JSONDecodeError, TypeError):
                    continue
                kind = rec.get("type")
                if kind == "session":
                    session_id = rec.get("id") or session_id
                    cwd = rec.get("cwd") or cwd
                elif kind == "message" and rec.get("id"):
                    entries[rec["id"]] = rec
                elif kind == "compaction":
                    events["compactions"] += 1
                    events["compactionTokens"] += int(rec.get("tokensBefore") or 0)
                    events["compactionEvents"].append(
                        {
                            "ts": parse_ts(rec.get("timestamp")),
                            "sessionId": rec.get("sessionId") or session_id,
                            "tokensBefore": int(rec.get("tokensBefore") or 0),
                        }
                    )
                elif kind == "model_change":
                    events["modelChanges"] += 1
                    events["modelChangeEvents"].append(
                        {
                            "ts": parse_ts(rec.get("timestamp")),
                            "sessionId": rec.get("sessionId") or session_id,
                            "model": rec.get("model") or rec.get("to") or "",
                        }
                    )
                elif kind == "file-history-snapshot":
                    events["fileSnapshots"] += 1
                elif kind is None and rec.get("eventName"):
                    ts = parse_ts(rec.get("timestamp"))
                    events["hooks"].append(
                        {
                            "eventName": rec.get("eventName"),
                            "hookName": rec.get("hookName"),
                            "hookType": rec.get("hookType"),
                            "toolName": rec.get("toolName"),
                            "duration": rec.get("duration"),
                            "exitCode": rec.get("exitCode"),
                            "blocked": bool(rec.get("blocked")),
                            "ts": ts,
                        }
                    )
                    tool_use_id = rec.get("toolUseId")
                    if rec.get("eventName") == "PreToolUse" and tool_use_id and ts:
                        pre_hooks[tool_use_id] = ts.timestamp()
                    elif rec.get("eventName") == "PostToolUse" and tool_use_id and ts and tool_use_id in pre_hooks:
                        window = ts.timestamp() - pre_hooks[tool_use_id]
                        if 0 < window < 3600:
                            events["toolWindows"].append(
                                {"tool": rec.get("toolName"), "seconds": window}
                            )
                if rec.get("gitBranch"):
                    branch = rec.get("gitBranch")

        for rid, rec in entries.items():
            msg = rec.get("message") or {}
            usage = rec.get("usage") or {}
            if msg.get("role") != "assistant":
                continue
            counts["scanned"] += 1
            counts["assistant"] += 1
            model = rec.get("model") or ""
            if model_filters and not any(term.lower() in model.lower() for term in model_filters):
                counts["filteredModel"] += 1
                continue
            counts["considered"] += 1
            ts = parse_ts(rec.get("timestamp"))
            session = session_id or Path(path).stem
            project = cwd or "(unknown)"
            out_tokens = int(usage.get("outputTokens") or 0)
            inp = int(usage.get("inputTokens") or 0)
            cr = int(usage.get("cacheReadTokens") or 0)
            cw = int(usage.get("cacheWriteTokens") or 0)
            cost = float(usage.get("costUsd") or 0.0)
            if out_tokens <= 0:
                counts["noOutput"] += 1
                ledger_entry("noOutput", rid, session, project, model, ts, 0, inp, cr, cw, cost)
                continue
            if rid in seen:
                counts["duplicates"] += 1
                duplicate_sessions[session] += 1
                duplicate_tokens += out_tokens
                duplicate_cost += cost
                ledger_entry("duplicate", rid, session, project, model, ts, out_tokens, inp, cr, cw, cost)
                continue
            seen.add(rid)
            if ts is None:
                counts["noTimestamp"] += 1
                ledger_entry("noTimestamp", rid, session, project, model, None, out_tokens, inp, cr, cw, cost)
                continue
            day = ts.astimezone().date().isoformat()
            if (since and day < since) or (until and day > until):
                counts["filteredDate"] += 1
                continue

            content = msg.get("content") or []
            tool_names = [
                block.get("name")
                for block in content
                if isinstance(block, dict) and block.get("type") == "tool_use"
            ]
            n_thinking = sum(
                1 for block in content if isinstance(block, dict) and block.get("type") == "thinking"
            )
            n_text = sum(
                1 for block in content if isinstance(block, dict) and block.get("type") == "text"
            )
            has_tool = bool(tool_names)
            parent = entries.get(rec.get("parentId")) or {}
            pts = parse_ts(parent.get("timestamp"))
            duration = (ts - pts).total_seconds() if ts and pts else None
            if duration is not None and duration <= 0:
                duration = None
            row = {
                "id": rid,
                "model": model,
                "effort": rec.get("effort"),
                "ts": ts,
                "out": out_tokens,
                "inp": inp,
                "cacheRead": cr,
                "cacheWrite": cw,
                "cost": cost,
                "tool": has_tool,
                "tools": tool_names,
                "toolDetails": [{"name": name, "error": False} for name in tool_names],
                "primaryTool": tool_names[0] if tool_names else None,
                "nTools": len(tool_names),
                "nThinking": n_thinking,
                "nText": n_text,
                "web": any(name in ("web_search", "web_fetch") for name in tool_names),
                "thinking": n_thinking > 0,
                "dt": duration,
                "tps": out_tokens / duration if duration else None,
                "sessionId": session,
                "sessionTitle": session_title,
                "projectPath": project,
                "projectLabel": None,
                "branch": branch,
                "entrypoint": entrypoint,
            }
            bucket = timing_bucket(row)
            row["timing"] = bucket
            timing[bucket] += 1
            rows.append(row)
            counts["rows"] += 1
            contributed = True

        if contributed and branch:
            events["branches"].add(branch)
        if contributed:
            sessions.add(session_id or Path(path).stem)

    return {
        "files": files,
        "sessions": sessions,
        "rows": rows,
        "ledger": ledger,
        "counts": counts,
        "timing": timing,
        "events": events,
        "duplicateSessions": duplicate_sessions,
        "duplicateTokens": duplicate_tokens,
        "duplicateCost": duplicate_cost,
    }


# ---------------------------------------------------------------- analysis


def timing_bucket(row: dict) -> str:
    d = row.get("dt")
    if d is None or d <= 0:
        return "noTiming"
    if row["out"] < 20:
        return "tooShort"
    if d < 0.5:
        return "tooShortDuration"
    if d > 180:
        return "tooLong"
    rate = row["out"] / d
    if rate < 3:
        return "slowRate"
    if rate > 400:
        return "fastRate"
    return "plausible"


def is_timed(row: dict) -> bool:
    bucket = row.get("timing")
    if bucket is None:
        bucket = timing_bucket(row)
    return bucket == "plausible"


def summarize(rows: list[dict]) -> dict:
    if not rows:
        return {"n": 0, "tokens": 0, "seconds": 0, "weighted": None,
                "median": None, "p10": None, "p25": None, "p75": None,
                "p90": None, "p99": None, "max": None}
    tps = sorted(row["tps"] for row in rows)
    tokens = sum(row["out"] for row in rows)
    seconds = sum(row["dt"] for row in rows)
    return {
        "n": len(rows),
        "tokens": tokens,
        "seconds": round(seconds, 1),
        "weighted": round(tokens / seconds, 1) if seconds else None,
        "median": r1(statistics.median(tps)),
        "p10": r1(percentile(tps, 10)),
        "p25": r1(percentile(tps, 25)),
        "p75": r1(percentile(tps, 75)),
        "p90": r1(percentile(tps, 90)),
        "p99": r1(percentile(tps, 99)),
        "max": r1(tps[-1]),
    }


def fit_rate(rows: list[dict]) -> dict | None:
    """Regress duration on output tokens with a non-negative intercept."""
    fit = linear_fit(
        [row["out"] for row in rows],
        [row["dt"] for row in rows],
        min_n=30,
    )
    if fit:
        fit["usable"] = fit["n"] >= 30 and (fit["r2"] or 0) >= 0.50
    return fit


def fit_prices(rows: list[dict]) -> dict | None:
    """Fit recorded cost against token categories ($ per 1M tokens).

    Providers normalize cache tokens differently. Try both decompositions:
    `nonCached = input - cacheRead - cacheWrite` (Command Code's in-process
    usage) and `nonCached = input` (API-native usage). Pick the fit with the
    best R² and non-negative rates.
    """
    rs = [row for row in rows if not row["web"]]
    if len(rs) < 30:
        return None

    def attempt(subtract: bool):
        def features(row):
            non_cached = (
                max(0, row["inp"] - row["cacheRead"] - row["cacheWrite"])
                if subtract
                else row["inp"]
            )
            return [non_cached / 1e6, row["cacheRead"] / 1e6, row["cacheWrite"] / 1e6, row["out"] / 1e6]

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
        full = [0.0] * 4
        for idx, value in zip(active, coeffs):
            full[idx] = value
        if any(value < -0.01 for value in full):
            return None
        full = [max(0.0, value) for value in full]
        mean = sum(row["cost"] for row in rs) / len(rs)
        ss_res = 0.0
        ss_tot = 0.0
        for feats, row in zip(sample, rs):
            pred = sum(feats[i] * full[i] for i in range(4))
            ss_res += (row["cost"] - pred) ** 2
            ss_tot += (row["cost"] - mean) ** 2
        r_squared = 1 - ss_res / ss_tot if ss_tot else None
        return {"rates": full, "r2": r_squared, "subtract": subtract}

    def plausible(candidate: dict) -> bool:
        rates = candidate["rates"]
        input_rate, cache_rate, write_rate = rates[0], rates[1], rates[2]
        if input_rate <= 0:
            return all(abs(value) < 1e-9 for value in rates)
        # A cache read should not cost more than fresh input, and cache writes
        # typically cost at most ~1.25x fresh input.
        if cache_rate > input_rate * 1.001:
            return False
        if write_rate > max(input_rate * 3, 0.01):
            return False
        return True

    def score(candidate: dict):
        rates = candidate["rates"]
        input_rate = rates[0]
        cache_ratio = rates[1] / input_rate if input_rate > 0 else 0.0
        return (
            round(candidate["r2"] or 0, 4),
            -cache_ratio,
            1 if candidate["subtract"] else 0,
        )

    candidates = [c for c in (attempt(True), attempt(False)) if c and plausible(c)]
    if not candidates:
        return None
    candidates.sort(key=score, reverse=True)
    best = candidates[0]
    if best["r2"] is not None and best["r2"] < 0.85:
        return None
    rates = best["rates"]
    return {
        "inputRate": round(rates[0], 4),
        "cacheReadRate": round(rates[1], 4),
        "cacheWriteRate": round(rates[2], 4),
        "outputRate": round(rates[3], 4),
        "r2": round(best["r2"], 4) if best["r2"] is not None else None,
        "n": len(rs),
        "inputIncludesCache": best["subtract"],
    }


def cost_summary(
    model: str,
    rows: list[dict],
    price_fit: dict | None,
    includes_cache: bool,
    semantics_source: str,
) -> dict:
    raw_input = sum(row["inp"] for row in rows)
    cache_read = sum(row["cacheRead"] for row in rows)
    cache_write = sum(row["cacheWrite"] for row in rows)
    fresh = sum(
        max(0, row["inp"] - row["cacheRead"] - row["cacheWrite"])
        if includes_cache
        else row["inp"]
        for row in rows
    )
    out = sum(row["out"] for row in rows)
    recorded = sum(row["cost"] for row in rows)
    total_input = fresh + cache_read + cache_write
    if price_fit:
        fresh_cost = fresh / 1e6 * price_fit["inputRate"]
        cache_read_cost = cache_read / 1e6 * price_fit["cacheReadRate"]
        cache_write_cost = cache_write / 1e6 * price_fit["cacheWriteRate"]
        output_cost = out / 1e6 * price_fit["outputRate"]
        token_cost = fresh_cost + cache_read_cost + cache_write_cost + output_cost
        cache_savings = max(
            0.0,
            cache_read * (price_fit["inputRate"] - price_fit["cacheReadRate"]) / 1e6,
        )
        input_cost = fresh_cost + cache_read_cost + cache_write_cost
        effective_input = input_cost / (total_input / 1e6) if total_input else None
    else:
        fresh_cost = cache_read_cost = cache_write_cost = output_cost = None
        token_cost = recorded
        cache_savings = None
        effective_input = None
    return {
        "model": model,
        "short": model.split("/")[-1],
        "steps": len(rows),
        "outputTokens": out,
        "generatedTokens": out,
        "visibleOutputTokens": None,
        "inputTokens": total_input,
        "inputRawTokens": raw_input,
        "freshInputTokens": fresh,
        "inputTotal": total_input,
        "cacheReadTokens": cache_read,
        "cacheWriteTokens": cache_write,
        "cacheHitRate": round(cache_read / total_input, 4) if total_input else None,
        "cacheWriteShare": round(cache_write / total_input, 4) if total_input else None,
        "inputIncludesCache": includes_cache,
        "inputSemanticsSource": semantics_source,
        "recordedCost": round(recorded, 6),
        "tokenCost": round(token_cost, 6),
        "freshCost": round(fresh_cost, 6) if fresh_cost is not None else None,
        "cacheReadCost": round(cache_read_cost, 6) if cache_read_cost is not None else None,
        "cacheWriteCost": round(cache_write_cost, 6) if cache_write_cost is not None else None,
        "outputCost": round(output_cost, 6) if output_cost is not None else None,
        "cacheSavings": round(cache_savings, 6) if cache_savings is not None else None,
        "effectiveInputPrice": round(effective_input, 4) if effective_input is not None else None,
        "freshInputRate": price_fit["inputRate"] if price_fit else None,
        "cacheRate": price_fit["cacheReadRate"] if price_fit else None,
        "cacheWriteRate": price_fit["cacheWriteRate"] if price_fit else None,
        "outputRate": price_fit["outputRate"] if price_fit else None,
        "priceR2": price_fit["r2"] if price_fit else None,
        "costPerStep": round(recorded / len(rows), 4) if rows else None,
        "costPerMillionGenerated": round(recorded / out * 1e6, 2) if out else None,
        "costPerMillionOutput": round(recorded / out * 1e6, 2) if out else None,
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
    expected = fit["fixedSeconds"] + row["out"] / fit["pureRate"]
    if expected <= 0:
        return None
    return expected / row["dt"]  # >1 means faster than expected


def bucketize(rows: list[dict], buckets: list[tuple[str, int, int]]) -> list[dict]:
    out = []
    for label, lo, hi in buckets:
        rs = [row for row in rows if lo <= row["out"] < hi]
        if not rs:
            continue
        tps = sorted(row["tps"] for row in rs)
        tokens = sum(row["out"] for row in rs)
        seconds = sum(row["dt"] for row in rs)
        out.append(
            {
                "label": label,
                "n": len(rs),
                "tokens": tokens,
                "weighted": round(tokens / seconds, 1),
                "median": r1(statistics.median(tps)),
                "p90": r1(percentile(tps, 90)),
            }
        )
    return out


def histogram(rows: list[dict], edges: list[int]) -> list[int]:
    counts = [0] * (len(edges) - 1)
    for row in rows:
        idx = bisect.bisect_right(edges, row["tps"]) - 1
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


# ---------------------------------------------------------------- build


def build(
    root: str,
    model_filters: list[str],
    since: str | None,
    until: str | None,
    include_titles: bool,
    full_paths: bool,
) -> dict:
    loaded = load_rows(root, model_filters, since, until, include_titles, full_paths)
    files = loaded["files"]
    sessions = loaded["sessions"]
    rows = loaded["rows"]
    ledger = loaded["ledger"]
    counts = loaded["counts"]
    timing_counts = loaded["timing"]
    events = loaded["events"]
    duplicates = counts["duplicates"]

    project_paths = sorted({row["projectPath"] for row in rows})
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
        row["projectLabel"] = project_labels[row["projectPath"]]

    timed = [row for row in rows if is_timed(row)]
    text = [row for row in timed if not row["tool"]]

    models = sorted({row["model"] for row in rows})
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
                "short": model.split("/")[-1],
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

    ratio_values = [row["ratio"] for row in timed if row["ratio"] is not None]
    pace_base = statistics.median(ratio_values) if ratio_values else None

    def pace_index(values):
        vals = [v for v in values if v is not None]
        if not vals or not pace_base:
            return None
        return round((statistics.median(vals) / pace_base) * 100, 1)

    # ---- price fits and canonical input semantics
    price_fits = {}
    for model in models:
        price_fit = fit_prices([row for row in rows if row["model"] == model])
        if price_fit:
            price_fits[model] = price_fit

    def infer_semantics(rs: list[dict], price_fit: dict | None):
        if price_fit is not None and (
            price_fit["inputRate"] > 0 or price_fit["outputRate"] > 0
        ):
            return bool(price_fit["inputIncludesCache"]), "fit"
        if any(row["cacheRead"] > row["inp"] for row in rs):
            return False, "heuristic"
        return True, "assumed"

    semantics = {}
    for model in models:
        rs = [row for row in rows if row["model"] == model]
        semantics[model] = infer_semantics(rs, price_fits.get(model))

    def fresh_input(row: dict) -> int:
        includes_cache, _ = semantics[row["model"]]
        if includes_cache:
            return max(0, row["inp"] - row["cacheRead"] - row["cacheWrite"])
        return row["inp"]

    def input_total(row: dict) -> int:
        return fresh_input(row) + row["cacheRead"] + row["cacheWrite"]

    def stable_seed(text: str) -> int:
        return sum((index + 1) * ord(ch) for index, ch in enumerate(text)) % 1_000_000

    # ---- per model
    by_model = []
    for model in models:
        rs = [row for row in rows if row["model"] == model]
        ts = [row for row in timed if row["model"] == model]
        summary = summarize(ts)
        fit = fits.get((model, "text")) or fits.get((model, "all")) or {}
        includes_cache, semantics_source = semantics[model]
        cache_read = sum(row["cacheRead"] for row in rs)
        cache_write = sum(row["cacheWrite"] for row in rs)
        fresh = sum(fresh_input(row) for row in rs)
        total_in = fresh + cache_read + cache_write
        rates = [row["tps"] for row in ts if row["tps"] is not None]
        model_timing = collections.Counter(row["timing"] for row in rs)
        by_model.append(
            {
                "model": model,
                "short": model.split("/")[-1],
                "provider": model.split("/")[0] if "/" in model else "unknown",
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": sum(row["out"] for row in rs),
                "generatedTokens": sum(row["out"] for row in rs),
                "visibleOutputTokens": None,
                "inputTokens": total_in,
                "inputRawTokens": sum(row["inp"] for row in rs),
                "freshInputTokens": fresh,
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheWriteTokens": cache_write,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "inputIncludesCache": includes_cache,
                "inputSemanticsSource": semantics_source,
                "costUsd": round(sum(row["cost"] for row in rs), 6),
                "timedN": summary["n"],
                "rated": len(ts) >= 30,
                "lowSample": len(ts) < 30,
                "medianCI": bootstrap_median_ci(rates, seed=stable_seed(model)),
                "weighted": summary["weighted"],
                "median": summary["median"],
                "p90": summary["p90"],
                "pureRate": fit.get("pureRate"),
                "fixedSeconds": fit.get("fixedSeconds"),
                "r2": fit.get("r2"),
                "fitUsable": bool(fit.get("usable")),
                "timing": {key: model_timing.get(key, 0) for key in TIMING_REASONS},
                "rejected": len(rs) - summary["n"],
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
        bucket = size_bucket(row["out"])
        if bucket:
            global_strata[("tool" if row["tool"] else "text", bucket)] += 1
    for item in by_model:
        model = item["model"]
        strata = collections.defaultdict(list)
        for row in timed:
            if row["model"] != model:
                continue
            bucket = size_bucket(row["out"])
            if bucket:
                strata[("tool" if row["tool"] else "text", bucket)].append(row["tps"])
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

    cost_by_model = [
        cost_summary(
            model,
            [row for row in rows if row["model"] == model],
            price_fits.get(model),
            *semantics[model],
        )
        for model in models
    ]
    total_input = sum(item["inputTotal"] for item in cost_by_model)
    total_raw_input = sum(item["inputRawTokens"] for item in cost_by_model)
    total_fresh_input = sum(item["freshInputTokens"] for item in cost_by_model)
    total_cache_read = sum(row["cacheRead"] for row in rows)
    total_cache_write = sum(row["cacheWrite"] for row in rows)
    total_output = sum(row["out"] for row in rows)
    totals_cost = sum(row["cost"] for row in rows)
    error_cost = sum(
        entry["costUsd"] for entry in ledger if entry["reason"] != "duplicate"
    )
    undated_error_cost = sum(
        entry["costUsd"]
        for entry in ledger
        if entry["reason"] != "duplicate" and entry["ts"] is None
    )
    total_spend = totals_cost + error_cost
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
        "generatedTokens": total_output,
        "hitRate": round(total_cache_read / total_input, 4) if total_input else None,
        "writeShare": round(total_cache_write / total_input, 4) if total_input else None,
        "savings": round(sum(item["cacheSavings"] or 0 for item in cost_by_model), 6),
        "writeCost": round(sum(item["cacheWriteCost"] or 0 for item in cost_by_model), 6),
        "fittedModels": fitted_models,
        "unfittedModels": len(models) - fitted_models,
        "savingsCoverage": round(fitted_cache_read / total_cache_read, 4) if total_cache_read else None,
    }
    cost = {
        "recorded": round(totals_cost, 6),
        "errorCostUsd": round(error_cost, 6),
        "undatedErrorCostUsd": round(undated_error_cost, 6),
        "totalSpendUsd": round(total_spend, 6),
        "tokenCost": round(sum(item["tokenCost"] for item in cost_by_model), 6),
        "freshCost": round(sum(item["freshCost"] or 0 for item in cost_by_model), 6),
        "cacheReadCost": round(sum(item["cacheReadCost"] or 0 for item in cost_by_model), 6),
        "cacheWriteCost": round(sum(item["cacheWriteCost"] or 0 for item in cost_by_model), 6),
        "outputCost": round(sum(item["outputCost"] or 0 for item in cost_by_model), 6),
        "perStep": round(totals_cost / len(rows), 4) if rows else None,
        "perMillionGenerated": round(totals_cost / total_output * 1e6, 2) if total_output else None,
        "perMillionOutput": round(totals_cost / total_output * 1e6, 2) if total_output else None,
        "byModel": cost_by_model,
    }

    # ---- tool mix: attribute every call, not just the step's first tool
    tool_stats = collections.defaultdict(
        lambda: {
            "calls": 0,
            "steps": set(),
            "generated": 0.0,
            "cost": 0.0,
            "tokensPerCall": [],
            "rates": [],
            "timedCalls": 0,
            "timedGenerated": 0.0,
            "timedSeconds": 0.0,
        }
    )
    total_calls = 0
    for row in rows:
        calls = row["toolDetails"] or [{"name": "(final answer)", "error": False}]
        n = len(calls)
        total_calls += n
        row_timed = is_timed(row)
        for call in calls:
            stat = tool_stats[call["name"]]
            stat["calls"] += 1
            stat["steps"].add(row["id"])
            stat["generated"] += row["out"] / n
            stat["cost"] += row["cost"] / n
            stat["tokensPerCall"].append(row["out"] / n)
            if row_timed:
                stat["timedCalls"] += 1
                stat["timedGenerated"] += row["out"] / n
                stat["timedSeconds"] += row["dt"] / n
                stat["rates"].append(row["tps"])
    tools = []
    for name, stat in tool_stats.items():
        tools.append(
            {
                "tool": name,
                "calls": stat["calls"],
                "stepsWithTool": len(stat["steps"]),
                "shareCalls": stat["calls"] / total_calls if total_calls else None,
                "generatedTokens": round(stat["generated"], 1),
                "visibleOutputTokens": None,
                "medianTokensPerCall": r1(statistics.median(stat["tokensPerCall"]))
                if stat["tokensPerCall"] else None,
                "timedCalls": stat["timedCalls"],
                "weighted": round(stat["timedGenerated"] / stat["timedSeconds"], 1)
                if stat["timedSeconds"] else None,
                "medianTps": r1(statistics.median(stat["rates"])) if stat["rates"] else None,
                "errors": 0,
                "errRate": None,
                "costUsd": round(stat["cost"], 6),
                "costPerCall": round(stat["cost"] / stat["calls"], 6) if stat["calls"] else None,
                "timeShare": None,
                "attribution": "equal-share",
                "runtime": "unavailable",
            }
        )
    tools.sort(key=lambda item: -item["calls"])

    # ---- behavior slices
    def behavior_slice(rs: list[dict]) -> dict:
        tr = [row for row in rs if is_timed(row)]
        return {
            "steps": len(rs),
            "share": round(len(rs) / len(rows), 4) if rows else None,
            "outputTokens": sum(row["out"] for row in rs),
            "medianOut": r1(statistics.median([row["out"] for row in rs])) if rs else None,
            "timedN": len(tr),
            "weighted": round(sum(row["out"] for row in tr) / sum(row["dt"] for row in tr), 1) if tr else None,
            "medianTps": r1(statistics.median([row["tps"] for row in tr])) if tr else None,
        }

    thinking = {
        "with": behavior_slice([row for row in rows if row["thinking"]]),
        "without": behavior_slice([row for row in rows if not row["thinking"]]),
    }

    effort_groups = collections.defaultdict(list)
    for row in rows:
        effort_groups[str(row["effort"] or "unset")].append(row)
    efforts = []
    for name, rs in effort_groups.items():
        item = behavior_slice(rs)
        item["effort"] = name
        efforts.append(item)
    efforts.sort(key=lambda item: -item["steps"])

    entrypoint_groups = collections.defaultdict(list)
    for row in rows:
        entrypoint_groups[row["entrypoint"] or "interactive"].append(row)
    entrypoints = []
    for name, rs in entrypoint_groups.items():
        item = behavior_slice(rs)
        item["entrypoint"] = name
        item["sessions"] = len({row["sessionId"] for row in rs})
        entrypoints.append(item)
    entrypoints.sort(key=lambda item: -item["steps"])

    project_groups = collections.defaultdict(list)
    for row in rows:
        project_groups[row["projectPath"]].append(row)
    projects = []
    for path, rs in project_groups.items():
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(input_total(row) for row in rs)
        projects.append(
            {
                "name": project_labels.get(path, path),
                "path": path,
                "steps": len(rs),
                "sessions": len({row["sessionId"] for row in rs}),
                "generatedTokens": sum(row["out"] for row in rs),
                "outputTokens": sum(row["out"] for row in rs),
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(sum(row["cost"] for row in rs), 6),
            }
        )
    projects.sort(key=lambda item: -item["generatedTokens"])

    # ---- hooks / health / coverage
    hook_groups = collections.defaultdict(list)
    for ev in events["hooks"]:
        hook_groups[(ev.get("hookName") or "(unnamed)", ev.get("eventName") or "(event)")].append(ev)
    hooks = []
    for (name, event_name), evs in hook_groups.items():
        durations = sorted(
            ev["duration"] for ev in evs if isinstance(ev.get("duration"), (int, float))
        )
        hooks.append(
            {
                "hookName": name,
                "eventName": event_name,
                "count": len(evs),
                "failures": sum(1 for ev in evs if ev.get("exitCode") not in (0, None)),
                "unknownExit": sum(1 for ev in evs if ev.get("exitCode") is None),
                "blocked": sum(1 for ev in evs if ev.get("blocked")),
                "medianMs": r1(statistics.median(durations)) if durations else None,
                "p90Ms": r1(percentile(durations, 90)) if durations else None,
            }
        )
    hooks.sort(key=lambda item: -item["count"])

    tool_windows = sorted(w["seconds"] for w in events["toolWindows"] if w.get("seconds"))
    ledger_cost = sum(
        entry["costUsd"] for entry in ledger if entry["reason"] != "duplicate"
    )
    session_health = {
        "hooks": hooks,
        "hookEvents": len(events["hooks"]),
        "toolWindows": {
            "count": len(tool_windows),
            "medianSeconds": r2(statistics.median(tool_windows)) if tool_windows else None,
            "p90Seconds": r2(percentile(tool_windows, 90)) if tool_windows else None,
        },
        "compactions": {
            "count": events["compactions"],
            "tokensBefore": events["compactionTokens"],
        },
        "modelChanges": {"count": events["modelChanges"]},
        "fileSnapshots": {"count": events["fileSnapshots"]},
        "branches": sorted(events["branches"]),
        "timing": {key: timing_counts.get(key, 0) for key in TIMING_REASONS},
        "toolCalls": total_calls,
        "skippedSteps": len(ledger),
        "skippedCostUsd": round(ledger_cost, 6),
        "emptySteps": counts["noOutput"],
        "noTimestampSteps": counts["noTimestamp"],
        "duplicateSteps": counts["duplicates"],
        "duplicateCostUsd": round(loaded["duplicateCost"], 6),
        "duplicateTokens": loaded["duplicateTokens"],
    }
    coverage = {
        "cacheRead": any(row["cacheRead"] for row in rows),
        "cacheWrite": any(row["cacheWrite"] for row in rows),
        "thinking": any(row["thinking"] for row in rows),
        "hooks": bool(events["hooks"]),
        "toolWindows": bool(tool_windows),
        "compactions": events["compactions"] > 0,
        "modelChanges": events["modelChanges"] > 0,
        "fileSnapshots": events["fileSnapshots"] > 0,
        "branches": bool(events["branches"]),
        "titles": any(row.get("sessionTitle") for row in rows),
        "entrypoints": any(row.get("entrypoint") for row in rows),
        "timing": any(row.get("dt") is not None for row in rows),
        "timingBreakdown": True,
        "toolCalls": True,
        "skipLedger": bool(ledger),
        "projectPaths": True,
        "inputSemantics": True,
    }

    # ---- compact per-step rows and session index for the interactive page
    tool_name_set = {"(final answer)"}
    for row in rows:
        if row["toolDetails"]:
            tool_name_set.update(call["name"] for call in row["toolDetails"])
    tool_names = sorted(tool_name_set)
    effort_names = sorted({str(row["effort"] or "unset") for row in rows})
    project_names = sorted({row["projectPath"] for row in rows})
    project_labels_out = [project_labels.get(path, path) for path in project_names]
    entrypoint_names = sorted({row["entrypoint"] or "interactive" for row in rows})
    session_ids = sorted({row["sessionId"] for row in rows})
    session_index = {sid: idx for idx, sid in enumerate(session_ids)}
    tool_index = {name: idx for idx, name in enumerate(tool_names)}
    effort_index = {name: idx for idx, name in enumerate(effort_names)}
    project_index = {name: idx for idx, name in enumerate(project_names)}
    entrypoint_index = {name: idx for idx, name in enumerate(entrypoint_names)}
    model_index = {name: idx for idx, name in enumerate(models)}
    timing_index = {name: idx for idx, name in enumerate(TIMING_REASONS)}

    ledger_by_session = collections.defaultdict(list)
    for entry in ledger:
        ledger_by_session[entry["sessionId"]].append(entry)

    steps_out = []
    for row in rows:
        steps_out.append(
            [
                int(row["ts"].timestamp()) if row["ts"] else 0,
                model_index[row["model"]],
                tool_index[row["primaryTool"] or "(final answer)"],
                row["out"],
                round(row["dt"], 2) if row["dt"] is not None else -1,
                row["inp"],
                row["cacheRead"],
                row["cacheWrite"],
                round(row["cost"], 6),
                effort_index[str(row["effort"] or "unset")],
                1 if row["thinking"] else 0,
                project_index[row["projectPath"]],
                entrypoint_index[row["entrypoint"] or "interactive"],
                session_index[row["sessionId"]],
                row["nTools"],
                row["nThinking"],
                row["nText"],
                round(row["ratio"], 4) if row.get("ratio") is not None else -1,
                timing_index[row["timing"]],
                [tool_index[name] for name in row["tools"]],
            ]
        )
    steps_out.sort(key=lambda item: item[0])

    session_rows = collections.defaultdict(list)
    for row in rows:
        session_rows[row["sessionId"]].append(row)

    session_list = []
    for sid in session_ids:
        rs = session_rows[sid]
        stamps = [row["ts"] for row in rs if row["ts"]]
        first_ts = min(stamps) if stamps else None
        last_ts = max(stamps) if stamps else None
        span_minutes = ((last_ts - first_ts).total_seconds() / 60) if first_ts and last_ts else 0
        timed_rs = [row for row in rs if is_timed(row)]
        out_tokens = sum(row["out"] for row in rs)
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(input_total(row) for row in rs)
        skipped = ledger_by_session.get(sid, [])
        skipped_cost = sum(
            entry["costUsd"] for entry in skipped if entry["reason"] != "duplicate"
        )
        session_list.append(
            {
                "id": sid[:8],
                "sessionId": sid,
                "title": rs[0].get("sessionTitle") or sid[:8],
                "project": rs[0]["projectLabel"] or rs[0]["projectPath"],
                "projectPath": rs[0]["projectPath"],
                "entrypoint": rs[0]["entrypoint"] or "interactive",
                "branch": rs[0].get("branch"),
                "models": sorted({row["model"].split("/")[-1] for row in rs}),
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": out_tokens,
                "generatedTokens": out_tokens,
                "visibleOutputTokens": None,
                "inputTokens": total_in,
                "inputRawTokens": sum(row["inp"] for row in rs),
                "freshInputTokens": sum(fresh_input(row) for row in rs),
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheWriteTokens": sum(row["cacheWrite"] for row in rs),
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(sum(row["cost"] for row in rs), 6),
                "skippedSteps": len(skipped),
                "skippedCostUsd": round(skipped_cost, 6),
                "totalSpendUsd": round(sum(row["cost"] for row in rs) + skipped_cost, 6),
                "duplicateSteps": loaded["duplicateSessions"].get(sid, 0),
                "timedN": len(timed_rs),
                "weighted": round(
                    sum(row["out"] for row in timed_rs) / sum(row["dt"] for row in timed_rs), 1
                ) if timed_rs else None,
                "firstTs": int(first_ts.timestamp()) if first_ts else None,
                "lastTs": int(last_ts.timestamp()) if last_ts else None,
                "spanMinutes": round(span_minutes, 1),
                "activeMinutes": None,
                "idleMinutes": None,
                "outputPerMinute": round(out_tokens / span_minutes, 1) if span_minutes >= 0.5 else None,
                "wallOutputPerMinute": round(out_tokens / span_minutes, 1) if span_minutes >= 0.5 else None,
                "outcome": "unknown",
            }
        )
    # Keep session_list aligned with session_index (used by the compact steps),
    # not sorted by output; the page sorts filtered sessions itself.

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
            "inputIncludesCache": next((item["inputIncludesCache"] for item in cost_by_model if item["model"] == model), None),
        }
        for model in models
    ]

    # ---- distribution
    edges = [0, 10, 20, 30, 40, 50, 66, 80, 100, 120, 150, 200, 401]
    labels = [
        "0-10", "10-20", "20-30", "30-40", "40-50", "50-66",
        "66-80", "80-100", "100-120", "120-150", "150-200", "200+",
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

    # ---- ledger indexes for all-step spend/error series
    ledger_by_day = collections.defaultdict(list)
    ledger_by_hour = collections.defaultdict(list)
    ledger_by_model_day = collections.defaultdict(list)
    for entry in ledger:
        if entry["ts"] is None or entry["reason"] == "duplicate":
            continue
        local = dt.datetime.fromtimestamp(entry["ts"], tz=dt.timezone.utc).astimezone()
        day = local.date().isoformat()
        ledger_by_day[day].append(entry)
        ledger_by_hour[local.hour].append(entry)
        ledger_by_model_day[(entry["model"], day)].append(entry)

    # ---- daily (all steps, with timed rate/index)
    by_day_all = time_group(rows, lambda row: row["ts"].astimezone().date().isoformat())
    daily = []
    for date in sorted(by_day_all):
        rs = by_day_all[date]
        rs_timed = [row for row in rs if is_timed(row)]
        text_rs = [row for row in rs_timed if not row["tool"]]
        tokens = sum(row["out"] for row in rs)
        timed_tokens = sum(row["out"] for row in rs_timed)
        timed_seconds = sum(row["dt"] for row in rs_timed)
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(input_total(row) for row in rs)
        day_ledger = ledger_by_day.get(date, [])
        error_steps = len(day_ledger)
        error_cost = sum(entry["costUsd"] for entry in day_ledger)
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
                "timedTokens": timed_tokens,
                "timedSeconds": timed_seconds,
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(sum(row["cost"] for row in rs), 6),
                "errorCostUsd": round(error_cost, 6),
                "totalSpendUsd": round(sum(row["cost"] for row in rs) + error_cost, 6),
                "errorSteps": error_steps,
                "errorRate": round(error_steps / (len(rs) + error_steps), 4)
                if (len(rs) + error_steps) else None,
                "sessions": len({row["sessionId"] for row in rs}),
                "weighted": round(timed_tokens / timed_seconds, 1) if timed_seconds else None,
                "median": r1(statistics.median([row["tps"] for row in rs_timed]))
                if rs_timed else None,
                "p90": r1(percentile(sorted(row["tps"] for row in rs_timed), 90))
                if rs_timed else None,
                "ratio": round(ratio, 6) if ratio is not None else None,
                "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                "ratioN": len(ratio_values),
                "index": pace_index([row["ratio"] for row in rs_timed]),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["tps"] for row in text_rs]))
                if text_rs else None,
                "textIndex": pace_index([row["ratio"] for row in text_rs]),
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
            total_in = sum(input_total(row) for row in day_rows)
            ratio_values = [
                row["ratio"] for row in day_timed if row.get("ratio") is not None
            ]
            ratio = median_or_none(ratio_values)
            series.append(
                {
                    "date": date,
                    "nAll": len(day_rows),
                    "nTimed": len(day_timed),
                    "generatedTokens": sum(row["out"] for row in day_rows),
                    "costUsd": round(sum(row["cost"] for row in day_rows), 6),
                    "errorCostUsd": round(
                        sum(entry["costUsd"] for entry in model_ledger
                            if entry["reason"] != "duplicate"),
                        6,
                    ),
                    "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                    "weighted": round(
                        sum(row["out"] for row in day_timed)
                        / sum(row["dt"] for row in day_timed),
                        1,
                    ) if day_timed else None,
                    "median": r1(statistics.median([row["tps"] for row in day_timed]))
                    if day_timed else None,
                    "ratio": round(ratio, 6) if ratio is not None else None,
                    "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                    "ratioN": len(ratio_values),
                    "index": pace_index([row["ratio"] for row in day_timed]),
                }
            )
        daily_by_model.append({"model": model, "series": series})

    compaction = compaction_impact(
        rows,
        events["compactionEvents"],
        input_total,
        lambda row: row["out"] / row["dt"] if row["dt"] else None,
    )

    # ---- hourly (all steps, with timed rate/index)
    by_hour_all = time_group(rows, lambda row: row["ts"].astimezone().hour)
    hourly = []
    for hour in range(24):
        rs = by_hour_all.get(hour, [])
        rs_timed = [row for row in rs if is_timed(row)]
        text_rs = [row for row in rs_timed if not row["tool"]]
        tokens = sum(row["out"] for row in rs)
        timed_tokens = sum(row["out"] for row in rs_timed)
        timed_seconds = sum(row["dt"] for row in rs_timed)
        cache_read = sum(row["cacheRead"] for row in rs)
        total_in = sum(input_total(row) for row in rs)
        hour_ledger = ledger_by_hour.get(hour, [])
        error_steps = len(hour_ledger)
        error_cost = sum(entry["costUsd"] for entry in hour_ledger)
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
                "inputTotal": total_in,
                "cacheReadTokens": cache_read,
                "cacheHitRate": round(cache_read / total_in, 4) if total_in else None,
                "costUsd": round(sum(row["cost"] for row in rs), 6),
                "errorCostUsd": round(error_cost, 6),
                "totalSpendUsd": round(sum(row["cost"] for row in rs) + error_cost, 6),
                "errorSteps": error_steps,
                "sessions": len({row["sessionId"] for row in rs}),
                "weighted": round(timed_tokens / timed_seconds, 1) if timed_seconds else None,
                "median": r1(statistics.median([row["tps"] for row in rs_timed]))
                if rs_timed else None,
                "p90": r1(percentile(sorted(row["tps"] for row in rs_timed), 90))
                if rs_timed else None,
                "ratio": round(ratio, 6) if ratio is not None else None,
                "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                "ratioN": len(ratio_values),
                "index": pace_index([row["ratio"] for row in rs_timed]),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["tps"] for row in text_rs]))
                if text_rs else None,
                "textRatio": round(median_or_none([row["ratio"] for row in text_rs]), 6)
                if median_or_none([row["ratio"] for row in text_rs]) is not None else None,
                "textIndex": pace_index([row["ratio"] for row in text_rs]),
            }
        )

    # ---- context size (canonical total input)
    def context_buckets(source: list[dict]) -> list[dict]:
        out = []
        for label, lo, hi in [
            ("<50k", 0, 50_000),
            ("50-150k", 50_000, 150_000),
            ("150-400k", 150_000, 400_000),
            ("400k+", 400_000, 10**12),
        ]:
            rs = [row for row in source if lo <= input_total(row) < hi]
            if not rs:
                continue
            ratio_values = [row["ratio"] for row in rs if row.get("ratio") is not None]
            ratio = median_or_none(ratio_values)
            out.append(
                {
                    "label": label,
                    "n": len(rs),
                    "median": r1(statistics.median([row["tps"] for row in rs])),
                    "weighted": round(sum(row["out"] for row in rs) / sum(row["dt"] for row in rs), 1),
                    "ratio": round(ratio, 6) if ratio is not None else None,
                    "ratioPct": round(ratio * 100, 2) if ratio is not None else None,
                    "ratioN": len(ratio_values),
                    "index": pace_index([row["ratio"] for row in rs]),
                }
            )
        return out

    context = context_buckets(timed)
    context_text = context_buckets(text)

    # ---- peaks / slowest / lowest rate: all rows, with the timing reason
    def peak_row(row):
        return {
            "tps": r1(row["tps"]) if row.get("tps") is not None else None,
            "out": row["out"],
            "dt": r1(row["dt"]) if row["dt"] is not None else None,
            "model": row["model"].split("/")[-1],
            "at": row["ts"].astimezone().strftime("%Y-%m-%d %H:%M"),
            "tool": row["tool"],
            "inputTotal": input_total(row),
            "reason": row["timing"],
            "sessionId": row["sessionId"],
            "project": row["projectLabel"],
        }

    valid_rate = [
        row for row in rows
        if row["dt"] and row["dt"] >= 0.5 and row["out"] > 0
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
        if row["dt"] and row["out"] > 0
        and row["timing"] in ("tooShort", "tooShortDuration", "noTiming")
    ]
    peaks = [
        peak_row(row)
        for row in sorted(fast_rows, key=lambda item: item["out"] / item["dt"], reverse=True)[:10]
    ]
    slowest = [
        peak_row(row)
        for row in sorted(duration_rows, key=lambda item: item["dt"], reverse=True)[:8]
    ]
    lowest_rate = [
        peak_row(row)
        for row in sorted(slow_rows, key=lambda item: item["out"] / item["dt"])[:8]
    ]
    rejected_extremes = [
        peak_row(row)
        for row in sorted(invalid_rate, key=lambda item: item["out"] / item["dt"], reverse=True)[:8]
    ]

    timed_all = summarize(timed)
    observed_rates = sorted(row["out"] / row["dt"] for row in valid_rate)
    rejected_rates = sorted(row["out"] / row["dt"] for row in invalid_rate)
    valid_durations = sorted(row["dt"] for row in valid_rate)
    all_durations = sorted(row["dt"] for row in duration_rows)
    timed_all["maxIncluded"] = timed_all.get("max")
    timed_all["maxObserved"] = r1(observed_rates[-1]) if observed_rates else None
    timed_all["maxRejected"] = r1(rejected_rates[-1]) if rejected_rates else None
    timed_all["maxRejectedReason"] = (
        max(invalid_rate, key=lambda item: item["out"] / item["dt"])["timing"]
        if invalid_rate else None
    )
    timed_all["minIncluded"] = (
        r1(min((row["out"] / row["dt"] for row in timed), default=None))
        if timed else None
    )
    timed_all["maxDurationIncluded"] = (
        r1(max((row["dt"] for row in timed), default=None)) if timed else None
    )
    timed_all["maxDurationObserved"] = r1(valid_durations[-1]) if valid_durations else None
    timed_all["maxDurationAny"] = r1(all_durations[-1]) if all_durations else None
    timed_all["upperCensored"] = timing_counts.get("fastRate", 0)
    timed_all["lowerCensored"] = timing_counts.get("slowRate", 0)
    timed_all["durationCensoredHigh"] = timing_counts.get("tooLong", 0)
    timed_all["durationCensoredLow"] = timing_counts.get("tooShortDuration", 0)

    # ---- totals
    date_from = min((row["ts"] for row in rows if row["ts"]), default=None)
    date_to = max((row["ts"] for row in rows if row["ts"]), default=None)
    meta_timing = {
        "files": counts["files"],
        "scanned": counts["scanned"],
        "considered": counts["considered"],
        "rows": counts["rows"],
        "plausible": timing_counts.get("plausible", 0),
        "noTiming": timing_counts.get("noTiming", 0),
        "tooShort": timing_counts.get("tooShort", 0),
        "tooShortDuration": timing_counts.get("tooShortDuration", 0),
        "tooLong": timing_counts.get("tooLong", 0),
        "slowRate": timing_counts.get("slowRate", 0),
        "fastRate": timing_counts.get("fastRate", 0),
        "duplicates": counts["duplicates"],
        "noOutput": counts["noOutput"],
        "noTimestamp": counts["noTimestamp"],
        "filteredModel": counts["filteredModel"],
        "filteredDate": counts["filteredDate"],
        "thresholds": {
            "minSeconds": 0.5,
            "maxSeconds": 180.0,
            "minTokens": 20,
            "minRate": 3.0,
            "maxRate": 400.0,
        },
    }

    data = {
        "schemaVersion": SCHEMA_VERSION,
        "meta": {
            "generatedAt": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
            "rootLabel": root.replace(os.path.expanduser("~"), "~"),
            "modelFilters": model_filters,
            "models": len(models),
            "filesScanned": len(files),
            "sessions": len(sessions),
            "duplicatesSkipped": duplicates,
            "skippedSteps": len(ledger),
            "emptySteps": counts["noOutput"],
            "includeTitles": include_titles,
            "fullPaths": full_paths,
            "since": since,
            "until": until,
            "timezone": local_tz_label(),
            "paceBase": round(pace_base, 6) if pace_base else None,
            "paceBasePct": round(pace_base * 100, 1) if pace_base else None,
            "timing": meta_timing,
            "counts": counts,
            "dateFrom": date_from.astimezone().date().isoformat() if date_from else None,
            "dateTo": date_to.astimezone().date().isoformat() if date_to else None,
        },
        "totals": {
            "steps": len(rows),
            "toolSteps": sum(1 for row in rows if row["tool"]),
            "textSteps": sum(1 for row in rows if not row["tool"]),
            "toolCalls": total_calls,
            "outputTokens": total_output,
            "generatedTokens": total_output,
            "visibleOutputTokens": None,
            "inputTokens": total_input,
            "inputRawTokens": total_raw_input,
            "freshInputTokens": total_fresh_input,
            "inputTotal": total_input,
            "cacheReadTokens": total_cache_read,
            "cacheWriteTokens": total_cache_write,
            "costUsd": round(totals_cost, 6),
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
        "sessionHealth": session_health,
        "coverage": coverage,
        "tools": tools,
        "thinking": thinking,
        "efforts": efforts,
        "entrypoints": entrypoints,
        "projects": projects,
        "projectsTotal": len(projects),
        "lookups": {
            "models": [
                {
                    "id": model,
                    "short": model.split("/")[-1],
                    "provider": model.split("/")[0] if "/" in model else "unknown",
                }
                for model in models
            ],
            "tools": tool_names,
            "efforts": effort_names,
            "projects": project_names,
            "projectLabels": project_labels_out,
            "entrypoints": entrypoint_names,
            "timingReasons": TIMING_REASONS,
        },
        "fits": fits_out,
        "prices": [
            {
                "fresh": next((item["freshInputRate"] for item in cost_by_model if item["model"] == model), None),
                "cache": next((item["cacheRate"] for item in cost_by_model if item["model"] == model), None),
                "cacheWrite": next((item["cacheWriteRate"] for item in cost_by_model if item["model"] == model), None),
                "output": next((item["outputRate"] for item in cost_by_model if item["model"] == model), None),
                "inputIncludesCache": semantics[model][0],
                "inputSemanticsSource": semantics[model][1],
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
        "skipLedger": ledger,
        "fork": {
            "duplicateSteps": counts["duplicates"],
            "duplicateTokens": loaded["duplicateTokens"],
            "duplicateCostUsd": round(loaded["duplicateCost"], 6),
            "sessionsWithDuplicates": len(loaded["duplicateSessions"]),
        },
        "compactionImpact": compaction,
        "compactionEvents": [
            {
                "ts": int(event["ts"].timestamp()) if event["ts"] else None,
                "sessionId": event["sessionId"],
                "tokensBefore": event["tokensBefore"],
            }
            for event in events["compactionEvents"]
        ],
        "modelChangeEvents": [
            {
                "ts": int(event["ts"].timestamp()) if event["ts"] else None,
                "sessionId": event["sessionId"],
                "model": event["model"],
            }
            for event in events["modelChangeEvents"]
        ],
    }
    errors = validate_common(data)
    if errors:
        raise SystemExit(
            "telemetry invariant failure:\n- " + "\n- ".join(errors)
        )
    return data


# ---------------------------------------------------------------- output


def write_outputs(data: dict, out_dir: str) -> tuple[Path, Path]:
    target = Path(out_dir)
    target.mkdir(parents=True, exist_ok=True)
    json_path = target / "telemetry-data.json"
    js_path = target / "telemetry-data.js"
    json_path.write_text(json.dumps(data, indent=1, ensure_ascii=False), encoding="utf-8")
    js_path.write_text(
        "window.CMD_TELEMETRY = "
        + json.dumps(data, ensure_ascii=False, separators=(",", ":"))
        + ";\n",
        encoding="utf-8",
    )
    return json_path, js_path


def print_summary(data: dict, json_path: Path, js_path: Path) -> None:
    totals = data["totals"]
    all_timed = data["timed"]["all"]
    text_timed = data["timed"]["text"]
    meta = data["meta"]
    health = data["sessionHealth"]
    top = max(data["byModel"], key=lambda item: item["steps"]) if data["byModel"] else None
    print(
        f"Scanned {meta['filesScanned']} files · {meta['sessions']} sessions · "
        f"{meta['models']} models · {totals['steps']:,} steps ({meta['duplicatesSkipped']} fork duplicates skipped)"
    )
    print(
        f"Recorded {totals['outputTokens']:,} output tokens · "
        f"{totals.get('inputTotal', totals['inputTokens']):,} input (incl. cache) · ${totals['costUsd']:,.2f}"
    )
    if all_timed["n"]:
        print(
            f"Timed {all_timed['n']:,} steps: weighted {all_timed['weighted']} tok/s · "
            f"median {all_timed['median']} · p90 {all_timed['p90']}"
        )
    if text_timed["n"]:
        print(
            f"Text replies ({text_timed['n']:,}): weighted {text_timed['weighted']} tok/s · "
            f"median {text_timed['median']} · p90 {text_timed['p90']}"
        )
    if top and top.get("pureRate"):
        print(
            f"Marginal decode ({top['short']}): {top['pureRate']} tok/s · fixed {top['fixedSeconds']}s"
        )
    cache = data["cache"]
    cost = data["cost"]
    bits = []
    if cache.get("hitRate") is not None:
        bits.append(f"{cache['hitRate'] * 100:.1f}% read hit")
    if cache.get("writeShare") is not None:
        bits.append(f"{cache['writeShare'] * 100:.2f}% writes")
    if cache.get("savings") is not None:
        bits.append(f"~${cache['savings']:,.2f} read savings")
    bits.append(f"recorded ${cost['recorded']:,.2f}")
    if cost.get("perStep") is not None:
        bits.append(f"${cost['perStep']:.4f}/step")
    print("Cache: " + " · ".join(bits))
    print(
        f"Hooks (full history): {health['hookEvents']} events · "
        f"compactions: {health['compactions']['count']} · model changes: {health['modelChanges']['count']}"
    )
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
        description="Build a model-agnostic telemetry report from Command Code session transcripts."
    )
    parser.add_argument(
        "--root",
        default="~/.commandcode/projects",
        help="session root (default: ~/.commandcode/projects)",
    )
    parser.add_argument(
        "--models",
        default="",
        help="comma-separated model id filters (default: all models)",
    )
    parser.add_argument("--since", default=None, help="earliest local date, YYYY-MM-DD")
    parser.add_argument("--until", default=None, help="latest local date, YYYY-MM-DD")
    parser.add_argument("--out", default=str(here / "data"), help="output directory (default: ./data)")
    parser.add_argument("--no-titles", action="store_true", help="omit session titles")
    parser.add_argument("--full-paths", action="store_true", help="keep full project paths (default: basenames)")
    parser.add_argument("--serve", action="store_true", help="serve the report and open it")
    parser.add_argument("--port", type=int, default=8000, help="port for --serve (default: 8000)")
    parser.add_argument("--no-open", action="store_true", help="do not open a browser with --serve")
    args = parser.parse_args()

    model_filters = [term.strip() for term in args.models.split(",") if term.strip()]
    data = build(
        args.root,
        model_filters,
        args.since,
        args.until,
        include_titles=not args.no_titles,
        full_paths=args.full_paths,
    )
    json_path, js_path = write_outputs(data, args.out)
    print_summary(data, json_path, js_path)
    if args.serve:
        serve(here, args.port, open_browser=not args.no_open)
    else:
        print(f"Open {here / 'index.html'} in a browser, or re-run with --serve.")


if __name__ == "__main__":
    main()
