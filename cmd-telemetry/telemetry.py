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
from pathlib import Path

# ---------------------------------------------------------------- utilities


def parse_ts(value: str | None) -> dt.datetime | None:
    if not value:
        return None
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (ValueError, TypeError):
        return None


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
    sessions: set[str] = set()
    duplicates = 0
    events = {
        "compactions": 0,
        "compactionTokens": 0,
        "modelChanges": 0,
        "fileSnapshots": 0,
        "hooks": [],
        "toolWindows": [],
        "branches": set(),
    }

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
                elif kind == "model_change":
                    events["modelChanges"] += 1
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

        if cwd and not full_paths:
            cwd = os.path.basename(cwd.rstrip("/")) or cwd

        for rid, rec in entries.items():
            msg = rec.get("message") or {}
            usage = rec.get("usage") or {}
            if msg.get("role") != "assistant":
                continue
            model = rec.get("model") or ""
            if model_filters and not any(term.lower() in model.lower() for term in model_filters):
                continue
            if not usage.get("outputTokens"):
                continue
            if rid in seen:
                duplicates += 1
                continue
            seen.add(rid)

            ts = parse_ts(rec.get("timestamp"))
            if since or until:
                if not ts:
                    continue
                day = ts.astimezone().date().isoformat()
                if since and day < since:
                    continue
                if until and day > until:
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
            out_tokens = int(usage["outputTokens"])
            tps = out_tokens / duration if duration and duration > 0 else None

            rows.append(
                {
                    "id": rid,
                    "model": model,
                    "effort": rec.get("effort"),
                    "ts": ts,
                    "out": out_tokens,
                    "inp": int(usage.get("inputTokens") or 0),
                    "cacheRead": int(usage.get("cacheReadTokens") or 0),
                    "cacheWrite": int(usage.get("cacheWriteTokens") or 0),
                    "cost": float(usage.get("costUsd") or 0.0),
                    "tool": has_tool,
                    "tools": tool_names,
                    "primaryTool": tool_names[0] if tool_names else None,
                    "nTools": len(tool_names),
                    "nThinking": n_thinking,
                    "nText": n_text,
                    "web": any(name in ("web_search", "web_fetch") for name in tool_names),
                    "thinking": n_thinking > 0,
                    "dt": duration,
                    "tps": tps,
                    "sessionId": session_id or Path(path).stem,
                    "sessionTitle": session_title,
                    "cwd": cwd,
                    "branch": branch,
                    "entrypoint": entrypoint,
                }
            )
            contributed = True

        if contributed and branch:
            events["branches"].add(branch)
        if contributed:
            sessions.add(session_id or Path(path).stem)

    return files, sessions, rows, duplicates, events


# ---------------------------------------------------------------- analysis


def is_timed(row: dict) -> bool:
    d = row.get("dt")
    t = row.get("tps")
    if d is None or t is None:
        return False
    return 0.5 <= d <= 180 and row["out"] >= 20 and 3 <= t <= 400


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
    """Regress duration on output tokens: dt = fixed + tokens / rate."""
    if len(rows) < 30:
        return None
    xs = [row["out"] for row in rows]
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


def cost_summary(model: str, rows: list[dict], price_fit: dict | None) -> dict:
    inp = sum(row["inp"] for row in rows)
    cache_read = sum(row["cacheRead"] for row in rows)
    cache_write = sum(row["cacheWrite"] for row in rows)
    out = sum(row["out"] for row in rows)
    recorded = sum(row["cost"] for row in rows)
    subtract = price_fit.get("inputIncludesCache", True) if price_fit else True
    non_cached = max(0, inp - cache_read - cache_write) if subtract else inp
    total_input = inp if subtract else inp + cache_read + cache_write
    if price_fit:
        fresh_cost = non_cached / 1e6 * price_fit["inputRate"]
        cache_read_cost = cache_read / 1e6 * price_fit["cacheReadRate"]
        cache_write_cost = cache_write / 1e6 * price_fit["cacheWriteRate"]
        output_cost = out / 1e6 * price_fit["outputRate"]
        token_cost = fresh_cost + cache_read_cost + cache_write_cost + output_cost
        cache_savings = max(0.0, cache_read * (price_fit["inputRate"] - price_fit["cacheReadRate"]) / 1e6)
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
        "inputTokens": inp,
        "inputTotal": total_input,
        "cacheReadTokens": cache_read,
        "cacheWriteTokens": cache_write,
        "cacheHitRate": round(cache_read / total_input, 4) if total_input else None,
        "cacheWriteShare": round(cache_write / total_input, 4) if total_input else None,
        "recordedCost": round(recorded, 2),
        "tokenCost": round(token_cost, 2),
        "freshCost": round(fresh_cost, 2) if fresh_cost is not None else None,
        "cacheReadCost": round(cache_read_cost, 2) if cache_read_cost is not None else None,
        "cacheWriteCost": round(cache_write_cost, 2) if cache_write_cost is not None else None,
        "outputCost": round(output_cost, 2) if output_cost is not None else None,
        "cacheSavings": round(cache_savings, 2) if cache_savings is not None else None,
        "effectiveInputPrice": round(effective_input, 4) if effective_input is not None else None,
        "freshInputRate": price_fit["inputRate"] if price_fit else None,
        "cacheRate": price_fit["cacheReadRate"] if price_fit else None,
        "cacheWriteRate": price_fit["cacheWriteRate"] if price_fit else None,
        "outputRate": price_fit["outputRate"] if price_fit else None,
        "priceR2": price_fit["r2"] if price_fit else None,
        "inputIncludesCache": price_fit["inputIncludesCache"] if price_fit else None,
        "costPerStep": round(recorded / len(rows), 4) if rows else None,
        "costPerMillionOutput": round(recorded / out * 1e6, 2) if out else None,
    }


def expected_ratio(row: dict, fits: dict, global_fits: dict) -> float | None:
    kind = "tool" if row["tool"] else "text"
    fit = fits.get((row["model"], kind)) or fits.get((row["model"], "all")) or global_fits.get(kind)
    if not fit or not row.get("dt"):
        return None
    expected = fit["fixedSeconds"] + row["out"] / fit["pureRate"]
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


def median_or_none(values):
    values = [v for v in values if v is not None]
    return r1(statistics.median(values) * 100) if values else None


# ---------------------------------------------------------------- build


def build(
    root: str,
    model_filters: list[str],
    since: str | None,
    until: str | None,
    include_titles: bool,
    full_paths: bool,
) -> dict:
    files, sessions, rows, duplicates, events = load_rows(
        root, model_filters, since, until, include_titles, full_paths
    )
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

    # ---- per model
    by_model = []
    for model in models:
        rs = [row for row in rows if row["model"] == model]
        ts = [row for row in timed if row["model"] == model]
        summary = summarize(ts)
        fit = fits.get((model, "text")) or fits.get((model, "all")) or {}
        by_model.append(
            {
                "model": model,
                "short": model.split("/")[-1],
                "provider": model.split("/")[0] if "/" in model else "unknown",
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": sum(row["out"] for row in rs),
                "inputTokens": sum(row["inp"] for row in rs),
                "cacheReadTokens": sum(row["cacheRead"] for row in rs),
                "cacheWriteTokens": sum(row["cacheWrite"] for row in rs),
                "cacheHitRate": round(sum(row["cacheRead"] for row in rs) / sum(row["inp"] for row in rs), 4) if sum(row["inp"] for row in rs) else None,
                "costUsd": round(sum(row["cost"] for row in rs), 2),
                "timedN": summary["n"],
                "weighted": summary["weighted"],
                "median": summary["median"],
                "p90": summary["p90"],
                "pureRate": fit.get("pureRate"),
                "fixedSeconds": fit.get("fixedSeconds"),
                "r2": fit.get("r2"),
            }
        )

    # ---- cache & price
    price_fits = {}
    for model in models:
        price_fit = fit_prices([row for row in rows if row["model"] == model])
        if price_fit:
            price_fits[model] = price_fit

    cost_by_model = [
        cost_summary(model, [row for row in rows if row["model"] == model], price_fits.get(model))
        for model in models
    ]
    total_input = sum(item["inputTotal"] for item in cost_by_model)
    total_cache_read = sum(row["cacheRead"] for row in rows)
    total_output = sum(row["out"] for row in rows)
    total_cost = sum(row["cost"] for row in rows)
    total_cache_write = sum(row["cacheWrite"] for row in rows)
    cache = {
        "inputTokens": total_input,
        "cacheReadTokens": total_cache_read,
        "cacheWriteTokens": total_cache_write,
        "outputTokens": total_output,
        "hitRate": round(total_cache_read / total_input, 4) if total_input else None,
        "writeShare": round(total_cache_write / total_input, 4) if total_input else None,
        "savings": round(sum(item["cacheSavings"] or 0 for item in cost_by_model), 2),
        "writeCost": round(sum(item["cacheWriteCost"] or 0 for item in cost_by_model), 2),
    }
    cost = {
        "recorded": round(total_cost, 2),
        "tokenCost": round(sum(item["tokenCost"] for item in cost_by_model), 2),
        "freshCost": round(sum(item["freshCost"] or 0 for item in cost_by_model), 2),
        "cacheReadCost": round(sum(item["cacheReadCost"] or 0 for item in cost_by_model), 2),
        "cacheWriteCost": round(sum(item["cacheWriteCost"] or 0 for item in cost_by_model), 2),
        "outputCost": round(sum(item["outputCost"] or 0 for item in cost_by_model), 2),
        "perStep": round(total_cost / len(rows), 4) if rows else None,
        "perMillionOutput": round(total_cost / total_output * 1e6, 2) if total_output else None,
        "byModel": cost_by_model,
    }

    # ---- tool mix
    tool_groups = collections.defaultdict(list)
    for row in rows:
        tool_groups[row["primaryTool"] or "(final answer)"].append(row)
    tools = []
    for name, rs in tool_groups.items():
        tr = [row for row in rs if is_timed(row)]
        tools.append(
            {
                "tool": name,
                "steps": len(rs),
                "share": round(len(rs) / len(rows), 4) if rows else None,
                "outputTokens": sum(row["out"] for row in rs),
                "medianOut": r1(statistics.median([row["out"] for row in rs])) if rs else None,
                "timedN": len(tr),
                "weighted": round(sum(row["out"] for row in tr) / sum(row["dt"] for row in tr), 1) if tr else None,
                "medianTps": r1(statistics.median([row["tps"] for row in tr])) if tr else None,
                "costUsd": round(sum(row["cost"] for row in rs), 2),
                "costPerStep": round(sum(row["cost"] for row in rs) / len(rs), 4) if rs else None,
            }
        )
    tools.sort(key=lambda item: -item["steps"])

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
        project_groups[row["cwd"] or "(unknown)"].append(row)
    projects = []
    for path, rs in project_groups.items():
        projects.append(
            {
                "name": os.path.basename(path.rstrip("/")) or path,
                "path": path,
                "steps": len(rs),
                "sessions": len({row["sessionId"] for row in rs}),
                "outputTokens": sum(row["out"] for row in rs),
                "costUsd": round(sum(row["cost"] for row in rs), 2),
            }
        )
    projects.sort(key=lambda item: -item["outputTokens"])

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
    }

    # ---- compact per-step rows and session index for the interactive page
    tool_names = sorted({row["primaryTool"] or "(final answer)" for row in rows})
    effort_names = sorted({str(row["effort"] or "unset") for row in rows})
    project_names = sorted({row["cwd"] or "(unknown)" for row in rows})
    entrypoint_names = sorted({row["entrypoint"] or "interactive" for row in rows})
    session_ids = sorted({row["sessionId"] for row in rows})
    session_index = {sid: idx for idx, sid in enumerate(session_ids)}
    tool_index = {name: idx for idx, name in enumerate(tool_names)}
    effort_index = {name: idx for idx, name in enumerate(effort_names)}
    project_index = {name: idx for idx, name in enumerate(project_names)}
    entrypoint_index = {name: idx for idx, name in enumerate(entrypoint_names)}
    model_index = {name: idx for idx, name in enumerate(models)}

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
                project_index[row["cwd"] or "(unknown)"],
                entrypoint_index[row["entrypoint"] or "interactive"],
                session_index[row["sessionId"]],
                row["nTools"],
                row["nThinking"],
                row["nText"],
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
        session_list.append(
            {
                "id": sid[:8],
                "title": rs[0].get("sessionTitle") or sid[:8],
                "project": rs[0]["cwd"] or "(unknown)",
                "entrypoint": rs[0]["entrypoint"] or "interactive",
                "branch": rs[0].get("branch"),
                "models": sorted({row["model"].split("/")[-1] for row in rs}),
                "steps": len(rs),
                "toolSteps": sum(1 for row in rs if row["tool"]),
                "textSteps": sum(1 for row in rs if not row["tool"]),
                "outputTokens": out_tokens,
                "inputTokens": sum(row["inp"] for row in rs),
                "cacheReadTokens": sum(row["cacheRead"] for row in rs),
                "cacheWriteTokens": sum(row["cacheWrite"] for row in rs),
                "costUsd": round(sum(row["cost"] for row in rs), 2),
                "timedN": len(timed_rs),
                "weighted": round(
                    sum(row["out"] for row in timed_rs) / sum(row["dt"] for row in timed_rs), 1
                ) if timed_rs else None,
                "firstTs": int(first_ts.timestamp()) if first_ts else None,
                "lastTs": int(last_ts.timestamp()) if last_ts else None,
                "spanMinutes": round(span_minutes, 1),
                "outputPerMinute": round(out_tokens / span_minutes, 1) if span_minutes >= 0.5 else None,
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

    # ---- daily (all timed steps)
    by_day = time_group(timed, lambda row: row["ts"].astimezone().date().isoformat())
    daily = []
    for date in sorted(by_day):
        rs = by_day[date]
        tokens = sum(row["out"] for row in rs)
        seconds = sum(row["dt"] for row in rs)
        text_rs = [row for row in rs if not row["tool"]]
        daily.append(
            {
                "date": date,
                "n": len(rs),
                "tokens": tokens,
                "weighted": round(tokens / seconds, 1),
                "median": r1(statistics.median([row["tps"] for row in rs])),
                "ratio": median_or_none([row["ratio"] for row in rs]),
                "index": pace_index([row["ratio"] for row in rs]),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["tps"] for row in text_rs])) if text_rs else None,
                "textIndex": pace_index([row["ratio"] for row in text_rs]),
            }
        )

    # ---- hourly (all timed + text-only)
    by_hour = time_group(timed, lambda row: row["ts"].astimezone().hour)
    hourly = []
    for hour in range(24):
        rs = by_hour.get(hour, [])
        text_rs = [row for row in rs if not row["tool"]]
        tokens = sum(row["out"] for row in rs)
        seconds = sum(row["dt"] for row in rs)
        hourly.append(
            {
                "hour": hour,
                "n": len(rs),
                "tokens": tokens,
                "weighted": round(tokens / seconds, 1) if seconds else None,
                "median": r1(statistics.median([row["tps"] for row in rs])) if rs else None,
                "ratio": median_or_none([row["ratio"] for row in rs]),
                "index": pace_index([row["ratio"] for row in rs]),
                "textN": len(text_rs),
                "textMedian": r1(statistics.median([row["tps"] for row in text_rs])) if text_rs else None,
                "textRatio": median_or_none([row["ratio"] for row in text_rs]),
                "textIndex": pace_index([row["ratio"] for row in text_rs]),
            }
        )

    # ---- context size (input tokens)
    def context_buckets(source: list[dict]) -> list[dict]:
        out = []
        for label, lo, hi in [
            ("<50k", 0, 50_000),
            ("50-150k", 50_000, 150_000),
            ("150-400k", 150_000, 400_000),
            ("400k+", 400_000, 10**12),
        ]:
            rs = [row for row in source if lo <= row["inp"] < hi]
            if not rs:
                continue
            out.append(
                {
                    "label": label,
                    "n": len(rs),
                    "median": r1(statistics.median([row["tps"] for row in rs])),
                    "weighted": round(sum(row["out"] for row in rs) / sum(row["dt"] for row in rs), 1),
                    "ratio": median_or_none([row["ratio"] for row in rs]),
                    "index": pace_index([row["ratio"] for row in rs]),
                }
            )
        return out

    context = context_buckets(timed)
    context_text = context_buckets(text)

    # ---- peaks / slowest
    def peak_row(row):
        return {
            "tps": r1(row["tps"]),
            "out": row["out"],
            "dt": r1(row["dt"]),
            "model": row["model"].split("/")[-1],
            "at": row["ts"].astimezone().strftime("%Y-%m-%d %H:%M"),
            "tool": row["tool"],
            "input": row["inp"],
        }

    peaks = [peak_row(row) for row in sorted(timed, key=lambda row: row["tps"], reverse=True)[:10]]
    slowest = [peak_row(row) for row in sorted(timed, key=lambda row: row["tps"])[:8]]

    # ---- totals
    date_from = min((row["ts"] for row in rows if row["ts"]), default=None)
    date_to = max((row["ts"] for row in rows if row["ts"]), default=None)

    data = {
        "meta": {
            "generatedAt": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
            "rootLabel": root.replace(os.path.expanduser("~"), "~"),
            "modelFilters": model_filters,
            "models": len(models),
            "filesScanned": len(files),
            "sessions": len(sessions),
            "duplicatesSkipped": duplicates,
            "includeTitles": include_titles,
            "fullPaths": full_paths,
            "since": since,
            "until": until,
            "timezone": local_tz_label(),
            "paceBase": round(pace_base * 100, 1) if pace_base else None,
            "dateFrom": date_from.astimezone().date().isoformat() if date_from else None,
            "dateTo": date_to.astimezone().date().isoformat() if date_to else None,
        },
        "totals": {
            "steps": len(rows),
            "toolSteps": sum(1 for row in rows if row["tool"]),
            "textSteps": sum(1 for row in rows if not row["tool"]),
            "outputTokens": sum(row["out"] for row in rows),
            "inputTokens": sum(row["inp"] for row in rows),
            "inputTotal": sum(item["inputTotal"] for item in cost_by_model),
            "cacheReadTokens": sum(row["cacheRead"] for row in rows),
            "cacheWriteTokens": sum(row["cacheWrite"] for row in rows),
            "costUsd": round(sum(row["cost"] for row in rows), 2),
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
        "sessionHealth": session_health,
        "coverage": coverage,
        "tools": tools,
        "thinking": thinking,
        "efforts": efforts,
        "entrypoints": entrypoints,
        "projects": projects[:12],
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
            "entrypoints": entrypoint_names,
        },
        "fits": fits_out,
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
