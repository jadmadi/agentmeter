#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Independent audit of command-code-telemetry data against the JSONL source.

Deliberately does not import telemetry.py. Re-derives raw counts, dedupe,
canonical input semantics, totals, timing, per-model aggregates, fits, prices,
tools, projects, sessions, daily/hourly, extremes and contract invariants.
Exit code 0 only when every check passes.
"""

import argparse
import collections
import datetime as dt
import glob
import json
import math
import os
import statistics
import sys
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT.parent.parent))
from telemetry_common import (  # noqa: E402
    SCHEMA_VERSION,
    TIMING_REASONS,
    linear_fit,
    validate_common,
)

parser = argparse.ArgumentParser(
    description="Independently verify cmd telemetry-data.json against the JSONL source."
)
parser.add_argument("--root", default=str(Path.home() / ".commandcode/projects"))
parser.add_argument("--data", default=str(ROOT.parent / "data/telemetry-data.json"))
parser.add_argument("--verbose", "-v", action="store_true")
ARGS = parser.parse_args()
VERBOSE = ARGS.verbose
DATA = Path(ARGS.data).expanduser()
JS = DATA.parent / "telemetry-data.js"
DATA_JSON = json.loads(DATA.read_text(encoding="utf-8"))
WATERMARK = DATA_JSON.get("meta", {}).get("sourceWatermark") or {}
MAX_TS = WATERMARK.get("maxTs")

PASS = []
FAIL = []
CURRENT = None
SECTION_PASS = 0
SECTION_FAIL = 0
SECTIONS = []

THRESHOLDS = {
    "minSeconds": 0.5, "maxSeconds": 180.0, "minTokens": 20,
    "minRate": 3.0, "maxRate": 400.0,
}


def section(title):
    global CURRENT, SECTION_PASS, SECTION_FAIL
    end_section()
    print("\n" + "=" * 78)
    print(title)
    print("=" * 78)
    CURRENT = title
    SECTION_PASS = 0
    SECTION_FAIL = 0


def end_section():
    global CURRENT, SECTION_PASS, SECTION_FAIL
    if CURRENT:
        print(f"  -> {SECTION_PASS:,} checks passed, {SECTION_FAIL:,} failed")
        SECTIONS.append((CURRENT, SECTION_PASS, SECTION_FAIL))
    CURRENT = None
    SECTION_PASS = 0
    SECTION_FAIL = 0


def detail(label, value):
    print(f"  {label:<40} {value}")


def check(name, actual, expected, tolerance=None):
    global SECTION_PASS, SECTION_FAIL
    if tolerance is None:
        ok = actual == expected
    else:
        try:
            ok = abs(float(actual) - float(expected)) <= tolerance
        except (TypeError, ValueError):
            ok = actual == expected
    if ok:
        PASS.append(name)
        SECTION_PASS += 1
        if VERBOSE:
            print(f"  PASS {name}: {actual!r}")
    else:
        FAIL.append((name, actual, expected))
        SECTION_FAIL += 1
        def brief(v):
            text = repr(v)
            return text if len(text) <= 240 else text[:237] + "..."
        print(f"  FAIL {name}: actual={brief(actual)} expected={brief(expected)}")


def r1(v):
    return None if v is None else round(v, 1)


def r2(v):
    return None if v is None else round(v, 2)


def pct(values, p):
    values = sorted(values)
    if not values:
        return None
    if len(values) == 1:
        return values[0]
    pos = (len(values) - 1) * p / 100.0
    lo = int(pos)
    hi = min(lo + 1, len(values) - 1)
    return values[lo] + (values[hi] - values[lo]) * (pos - lo)


def parse_ts(value):
    if not value:
        return None
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (ValueError, TypeError):
        return None


def timing_bucket(row):
    d = row.get("dt")
    if d is None or d <= 0:
        return "noTiming"
    if row["out"] < THRESHOLDS["minTokens"]:
        return "tooShort"
    if d < THRESHOLDS["minSeconds"]:
        return "tooShortDuration"
    if d > THRESHOLDS["maxSeconds"]:
        return "tooLong"
    rate = row["out"] / d
    if rate < THRESHOLDS["minRate"]:
        return "slowRate"
    if rate > THRESHOLDS["maxRate"]:
        return "fastRate"
    return "plausible"


def is_timed(row):
    return row["timing"] == "plausible"


def summarize(rows):
    if not rows:
        return {
            "n": 0, "tokens": 0, "seconds": 0, "weighted": None, "median": None,
            "p10": None, "p25": None, "p75": None, "p90": None, "p99": None,
            "max": None,
        }
    tps = sorted(row["tps"] for row in rows)
    tokens = sum(row["out"] for row in rows)
    seconds = sum(row["dt"] for row in rows)
    return {
        "n": len(rows), "tokens": tokens, "seconds": round(seconds, 1),
        "weighted": round(tokens / seconds, 1) if seconds else None,
        "median": r1(statistics.median(tps)),
        "p10": r1(pct(tps, 10)), "p25": r1(pct(tps, 25)),
        "p75": r1(pct(tps, 75)), "p90": r1(pct(tps, 90)),
        "p99": r1(pct(tps, 99)), "max": r1(tps[-1]),
    }


def price_fit(rows):
    rs = [r for r in rows if not r["web"]]
    if len(rs) < 30:
        return None

    def attempt(subtract):
        def features(row):
            non_cached = (
                max(0, row["inp"] - row["cr"] - row["cw"])
                if subtract else row["inp"]
            )
            return [
                non_cached / 1e6, row["cr"] / 1e6,
                row["cw"] / 1e6, row["out"] / 1e6,
            ]

        sample = np.array([features(r) for r in rs], dtype=float)
        y = np.array([r["cost"] for r in rs], dtype=float)
        active = [i for i in range(4) if (sample[:, i] > 0).any()]
        if not active:
            return None
        coef, *_ = np.linalg.lstsq(sample[:, active], y, rcond=None)
        rates = [0.0] * 4
        for i, value in zip(active, coef):
            rates[i] = float(value)
        if any(value < -0.01 for value in rates):
            return None
        rates = [max(0.0, value) for value in rates]
        input_rate, cache_rate, write_rate = rates[0], rates[1], rates[2]
        if input_rate <= 0:
            plausible = all(abs(value) < 1e-9 for value in rates)
        else:
            plausible = (
                cache_rate <= input_rate * 1.001
                and write_rate <= max(input_rate * 3, 0.01)
            )
        if not plausible:
            return None
        pred = sample @ np.array(rates)
        ss_res = ((y - pred) ** 2).sum()
        ss_tot = ((y - y.mean()) ** 2).sum()
        r2 = 1 - ss_res / ss_tot if ss_tot else None
        return {"rates": rates, "r2": r2, "subtract": subtract}

    candidates = [
        candidate for candidate in (attempt(True), attempt(False))
        if candidate is not None
    ]
    if not candidates:
        return None
    candidates.sort(
        key=lambda c: (
            round(c["r2"] or 0, 4),
            -(c["rates"][1] / c["rates"][0] if c["rates"][0] > 0 else 0),
            1 if c["subtract"] else 0,
        ),
        reverse=True,
    )
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


def fit_rate(rows):
    fit = linear_fit([r["out"] for r in rows], [r["dt"] for r in rows], min_n=30)
    if fit:
        fit["usable"] = fit["n"] >= 30 and (fit["r2"] or 0) >= 0.50
    return fit


# ---------------------------------------------------------------- load source
META = DATA_JSON.get("meta") or {}
MODEL_FILTERS = [
    str(term).strip().lower()
    for term in (META.get("modelFilters") or [])
    if str(term).strip()
]
SINCE = META.get("since")
UNTIL = META.get("until")
INCLUDE_TITLES = bool(META.get("includeTitles", True))

root = os.path.expanduser(ARGS.root)
files = sorted(glob.glob(os.path.join(root, "**", "*.jsonl"), recursive=True))
seen = set()
rows = []
ledger = []
sessions = set()
counts = collections.Counter()
timing_counts = collections.Counter()
duplicate_sessions = collections.Counter()
duplicate_cost = 0.0
duplicate_tokens = 0
compaction_events = []

for path in files:
    entries = {}
    session_id = None
    cwd = None
    branch = None
    entrypoint = None
    title = None
    try:
        with open(path[:-6] + ".meta.json") as handle:
            sidecar = json.load(handle)
            entrypoint = sidecar.get("entrypoint")
            if INCLUDE_TITLES:
                title = sidecar.get("title")
    except (OSError, ValueError, TypeError):
        pass
    try:
        handle = open(path, errors="ignore")
    except OSError:
        continue
    with handle:
        for line in handle:
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
                compaction_events.append(
                    {
                        "ts": parse_ts(rec.get("timestamp")),
                        "sessionId": rec.get("sessionId") or session_id,
                        "tokensBefore": int(rec.get("tokensBefore") or 0),
                    }
                )
            if rec.get("gitBranch"):
                branch = rec.get("gitBranch")

    for rid, rec in entries.items():
        message = rec.get("message") or {}
        usage = rec.get("usage") or {}
        if message.get("role") != "assistant":
            continue
        ts = parse_ts(rec.get("timestamp"))
        if MAX_TS is not None and ts is not None and ts.timestamp() > MAX_TS:
            continue
        counts["scanned"] += 1
        model = rec.get("model") or ""
        if MODEL_FILTERS and not any(term in model.lower() for term in MODEL_FILTERS):
            counts["filteredModel"] += 1
            continue
        counts["considered"] += 1
        session = session_id or Path(path).stem
        project_path = cwd or "(unknown)"
        out = int(usage.get("outputTokens") or 0)
        inp = int(usage.get("inputTokens") or 0)
        cr = int(usage.get("cacheReadTokens") or 0)
        cw = int(usage.get("cacheWriteTokens") or 0)
        cost = float(usage.get("costUsd") or 0.0)
        if ts is not None:
            day = ts.astimezone().date().isoformat()
            if (SINCE and day < SINCE) or (UNTIL and day > UNTIL):
                if out > 0:
                    seen.add(rid)
                counts["filteredDate"] += 1
                continue
        if out <= 0:
            counts["noOutput"] += 1
            ledger.append({
                "reason": "noOutput", "session": session, "project": project_path,
                "model": model, "ts": ts, "out": 0, "inp": inp, "cr": cr, "cw": cw,
                "cost": cost,
            })
            continue
        if rid in seen:
            counts["duplicates"] += 1
            duplicate_sessions[session] += 1
            duplicate_tokens += out
            duplicate_cost += cost
            ledger.append({
                "reason": "duplicate", "session": session, "project": project_path,
                "model": model, "ts": ts, "out": out, "inp": inp, "cr": cr, "cw": cw,
                "cost": cost,
            })
            continue
        seen.add(rid)
        if ts is None:
            counts["noTimestamp"] += 1
            ledger.append({
                "reason": "noTimestamp", "session": session, "project": project_path,
                "model": model, "ts": None, "out": out, "inp": inp, "cr": cr, "cw": cw,
                "cost": cost,
            })
            continue
        content = message.get("content") or []
        tools = [
            block.get("name")
            for block in content
            if isinstance(block, dict) and block.get("type") == "tool_use"
        ]
        n_thinking = sum(
            1 for block in content
            if isinstance(block, dict) and block.get("type") == "thinking"
        )
        n_text = sum(
            1 for block in content
            if isinstance(block, dict) and block.get("type") == "text"
        )
        parent = entries.get(rec.get("parentId")) or {}
        parent_ts = parse_ts(parent.get("timestamp"))
        duration = (ts - parent_ts).total_seconds() if ts and parent_ts else None
        if duration is not None and duration <= 0:
            duration = None
        row = {
            "id": rid, "model": model, "effort": rec.get("effort"),
            "ts": ts, "out": out, "inp": inp,
            "cr": cr, "cw": cw, "cost": cost, "tool": bool(tools),
            "tools": tools, "primary": tools[0] if tools else "(final answer)",
            "ntools": len(tools), "nthinking": n_thinking, "ntext": n_text,
            "thinking": n_thinking > 0, "dt": duration,
            "tps": out / duration if duration else None,
            "session": session, "project_path": project_path,
            "entrypoint": entrypoint, "title": title, "branch": branch,
            "web": any(name in ("web_search", "web_fetch") for name in tools),
        }
        row["timing"] = timing_bucket(row)
        timing_counts[row["timing"]] += 1
        rows.append(row)
        counts["rows"] += 1
        sessions.add(session)

timed = [row for row in rows if is_timed(row)]
text = [row for row in timed if not row["tool"]]
models = sorted({row["model"] for row in rows})
project_paths = sorted({row["project_path"] for row in rows})
base_counts = collections.Counter(
    os.path.basename(path.rstrip("/")) or path for path in project_paths
)
full_paths = DATA_JSON.get("meta", {}).get("fullPaths", False)
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

data = DATA_JSON

# ---------------------------------------------------------------- 1 source
section("1. SOURCE COUNTS AND DEDUPE")
check("schema version", data["schemaVersion"], SCHEMA_VERSION)
check("files scanned at build", data["meta"]["filesScanned"], WATERMARK.get("files", len(files)))
check("source files now >= build", len(files) >= data["meta"]["filesScanned"], True)
check("watermark scanned", WATERMARK.get("scanned"), data["meta"]["timing"]["scanned"])
check("watermark rows", WATERMARK.get("rows"), len(rows))
check("assistant rows scanned", data["meta"]["timing"]["scanned"], counts["scanned"])
check("considered rows", data["meta"]["timing"]["considered"], counts["considered"])
check("filteredModel", data["meta"]["timing"]["filteredModel"], counts["filteredModel"])
check("filteredDate", data["meta"]["timing"]["filteredDate"], counts["filteredDate"])
check("rows", len(rows), len(data["steps"]))
check("duplicates", data["meta"]["timing"]["duplicates"], counts["duplicates"])
check("duplicate sessions", data["fork"]["sessionsWithDuplicates"], len(duplicate_sessions))
check("duplicate tokens", data["fork"]["duplicateTokens"], duplicate_tokens)
check("duplicate cost", data["fork"]["duplicateCostUsd"], round(duplicate_cost, 6))
check("plausible", data["meta"]["timing"]["plausible"], timing_counts["plausible"])
for key in TIMING_REASONS:
    check(f"timing {key}", data["meta"]["timing"].get(key, timing_counts.get(key, 0)), timing_counts.get(key, 0))
check("compactions", data["sessionHealth"]["compactions"]["count"], len(compaction_events))

# ---------------------------------------------------------------- 2 prices
section("2. PRICE FITS AND INPUT SEMANTICS")
by_model = collections.defaultdict(list)
for row in rows:
    by_model[row["model"]].append(row)
prices = {m["id"]: p for m, p in zip(data["lookups"]["models"], data["prices"])}
price_by_model = {}
for model in models:
    expected = price_fit(by_model[model])
    actual = prices[model]
    if expected is None:
        check(f"{model}.price", actual["fresh"], None)
        price_by_model[model] = None
    else:
        check(f"{model}.price.fresh", actual["fresh"], expected["inputRate"])
        check(f"{model}.price.cache", actual["cache"], expected["cacheReadRate"])
        check(f"{model}.price.cacheWrite", actual["cacheWrite"], expected["cacheWriteRate"])
        check(f"{model}.price.output", actual["output"], expected["outputRate"])
        check(f"{model}.price.inputIncludesCache", actual["inputIncludesCache"], expected["inputIncludesCache"])
        price_by_model[model] = expected


def semantics_for(model):
    fit = price_by_model.get(model)
    if fit is not None and (fit["inputRate"] > 0 or fit["outputRate"] > 0):
        return bool(fit["inputIncludesCache"]), "fit"
    if any(row["cr"] > row["inp"] for row in by_model[model]):
        return False, "heuristic"
    return True, "assumed"


semantics = {model: semantics_for(model) for model in models}
for model in models:
    actual = prices[model]
    check(f"{model}.semantics", actual["inputIncludesCache"], semantics[model][0])


def fresh_input(row):
    includes, _ = semantics[row["model"]]
    if includes:
        return max(0, row["inp"] - row["cr"] - row["cw"])
    return row["inp"]


def input_total(row):
    return fresh_input(row) + row["cr"] + row["cw"]


# ---------------------------------------------------------------- 3 totals
section("3. TOTALS")
total_in = sum(input_total(row) for row in rows)
total_fresh = sum(fresh_input(row) for row in rows)
total_raw = sum(row["inp"] for row in rows)
total_generated = sum(row["out"] for row in rows)
total_cost = math.fsum(row["cost"] for row in rows)
error_cost = math.fsum(e["cost"] for e in ledger if e["reason"] != "duplicate")
check("totals.steps", data["totals"]["steps"], len(rows))
check("totals.generatedTokens", data["totals"]["generatedTokens"], total_generated)
check("totals.outputTokens", data["totals"]["outputTokens"], total_generated)
check("totals.freshInputTokens", data["totals"]["freshInputTokens"], total_fresh)
check("totals.inputRawTokens", data["totals"]["inputRawTokens"], total_raw)
check("totals.inputTotal", data["totals"]["inputTotal"], total_in)
check("totals.inputTokens", data["totals"]["inputTokens"], total_in)
check("totals.cacheReadTokens", data["totals"]["cacheReadTokens"], sum(row["cr"] for row in rows))
check("totals.cacheWriteTokens", data["totals"]["cacheWriteTokens"], sum(row["cw"] for row in rows))
check("totals.costUsd", data["totals"]["costUsd"], round(total_cost, 6))
check("totals.errorCostUsd", data["totals"]["errorCostUsd"], round(error_cost, 6))
check("totals.totalSpendUsd", data["totals"]["totalSpendUsd"], round(total_cost + error_cost, 6))
check("cache.hitRate", data["cache"]["hitRate"], round(sum(row["cr"] for row in rows) / total_in, 4) if total_in else None)
check("cache.inputTokens", data["cache"]["inputTokens"], total_in)

# ---------------------------------------------------------------- 4 timing
section("4. TIMING AND PER-MODEL AGGREGATES")
expected_timed_all = summarize(timed)
for key, value in expected_timed_all.items():
    check(f"timed.all.{key}", data["timed"]["all"][key], value)
check("timed.text", data["timed"]["text"], summarize(text))
check("timed.tool", data["timed"]["tool"], summarize([row for row in timed if row["tool"]]))
check("timed.all.n", data["timed"]["all"]["n"], len(timed))
check("timed.all.n == sum(byModel.timedN)", data["timed"]["all"]["n"], sum(m["timedN"] for m in data["byModel"]))
for model in models:
    rs = by_model[model]
    ts = [row for row in rs if is_timed(row)]
    actual = next(m for m in data["byModel"] if m["model"] == model)
    summary = summarize(ts)
    check(f"{model}.steps", actual["steps"], len(rs))
    check(f"{model}.generatedTokens", actual["generatedTokens"], sum(row["out"] for row in rs))
    check(f"{model}.freshInputTokens", actual["freshInputTokens"], sum(fresh_input(row) for row in rs))
    check(f"{model}.inputTotal", actual["inputTotal"], sum(input_total(row) for row in rs))
    check(f"{model}.cacheReadTokens", actual["cacheReadTokens"], sum(row["cr"] for row in rs))
    check(f"{model}.costUsd", actual["costUsd"], round(math.fsum(row["cost"] for row in rs), 6))
    check(f"{model}.timedN", actual["timedN"], summary["n"])
    check(f"{model}.weighted", actual["weighted"], summary["weighted"])
    check(f"{model}.median", actual["median"], summary["median"])
    check(f"{model}.p90", actual["p90"], summary["p90"])
    check(f"{model}.cacheHitRate", actual["cacheHitRate"], round(sum(row["cr"] for row in rs) / sum(input_total(row) for row in rs), 4) if sum(input_total(row) for row in rs) else None)
    fits = {
        "all": fit_rate(ts),
        "text": fit_rate([row for row in ts if not row["tool"]]),
        "tool": fit_rate([row for row in ts if row["tool"]]),
    }
    regression = next(item for item in data["regression"] if item["model"] == model)
    for kind in ("all", "text", "tool"):
        check(f"{model}.fit.{kind}", regression[kind], fits[kind])

# ---------------------------------------------------------------- 5 cost
section("5. COST DECOMPOSITION")
cost_models = {c["model"]: c for c in data["cost"]["byModel"]}
for model in models:
    actual = cost_models[model]
    rs = by_model[model]
    fit = price_by_model.get(model)
    includes, source = semantics[model]
    fresh = sum(fresh_input(row) for row in rs)
    total_in_model = sum(input_total(row) for row in rs)
    cr = sum(row["cr"] for row in rs)
    cw = sum(row["cw"] for row in rs)
    out = sum(row["out"] for row in rs)
    recorded = round(math.fsum(row["cost"] for row in rs), 6)
    check(f"cost.{model}.recordedCost", actual["recordedCost"], recorded)
    check(f"cost.{model}.inputTotal", actual["inputTotal"], total_in_model)
    check(f"cost.{model}.freshInputTokens", actual["freshInputTokens"], fresh)
    check(f"cost.{model}.inputIncludesCache", actual["inputIncludesCache"], includes)
    check(f"cost.{model}.inputSemanticsSource", actual["inputSemanticsSource"], source)
    if fit is not None and (fit["inputRate"] > 0 or fit["outputRate"] > 0):
        fresh_cost = fresh / 1e6 * fit["inputRate"]
        cache_cost = cr / 1e6 * fit["cacheReadRate"]
        write_cost = cw / 1e6 * fit["cacheWriteRate"]
        output_cost = out / 1e6 * fit["outputRate"]
        token_cost = fresh_cost + cache_cost + write_cost + output_cost
        savings = max(0.0, cr * (fit["inputRate"] - fit["cacheReadRate"]) / 1e6)
        check(f"cost.{model}.tokenCost", actual["tokenCost"], round(token_cost, 6))
        check(f"cost.{model}.freshCost", actual["freshCost"], round(fresh_cost, 6))
        check(f"cost.{model}.cacheReadCost", actual["cacheReadCost"], round(cache_cost, 6))
        check(f"cost.{model}.cacheWriteCost", actual["cacheWriteCost"], round(write_cost, 6))
        check(f"cost.{model}.outputCost", actual["outputCost"], round(output_cost, 6))
        check(f"cost.{model}.cacheSavings", actual["cacheSavings"], round(savings, 6))
check("cost.recorded", data["cost"]["recorded"], round(total_cost, 6))
check("cost.errorCostUsd", data["cost"]["errorCostUsd"], round(error_cost, 6))
check("cost.totalSpendUsd", data["cost"]["totalSpendUsd"], round(total_cost + error_cost, 6))

# ---------------------------------------------------------------- 6 tools
section("6. TOOL ATTRIBUTION")
tool_stats = collections.defaultdict(
    lambda: {
        "calls": 0, "steps": set(), "generated": 0.0, "cost": 0.0,
        "tokens": [], "rates": [], "timedCalls": 0, "timedGenerated": 0.0,
        "timedSeconds": 0.0,
    }
)
total_calls = 0
for row in rows:
    calls = row["tools"] or ["(final answer)"]
    n = len(calls)
    total_calls += n
    for name in calls:
        stat = tool_stats[name]
        stat["calls"] += 1
        stat["steps"].add(row["id"])
        stat["generated"] += row["out"] / n
        stat["cost"] += row["cost"] / n
        stat["tokens"].append(row["out"] / n)
        if is_timed(row):
            stat["timedCalls"] += 1
            stat["timedGenerated"] += row["out"] / n
            stat["timedSeconds"] += row["dt"] / n
            stat["rates"].append(row["tps"])
check("totals.toolCalls", data["totals"]["toolCalls"], total_calls)
tools_json = {item["tool"]: item for item in data["tools"]}
check("tool keys", sorted(tools_json), sorted(tool_stats))
check("tool shareCalls sum", sum(item["shareCalls"] for item in data["tools"]), 1.0, 1e-6)
for name, stat in tool_stats.items():
    actual = tools_json[name]
    check(f"tools.{name}.calls", actual["calls"], stat["calls"])
    check(f"tools.{name}.stepsWithTool", actual["stepsWithTool"], len(stat["steps"]))
    check(f"tools.{name}.shareCalls", actual["shareCalls"], stat["calls"] / total_calls)
    check(f"tools.{name}.generatedTokens", actual["generatedTokens"], round(stat["generated"], 1))
    check(f"tools.{name}.costUsd", actual["costUsd"], round(stat["cost"], 6))
    check(f"tools.{name}.weighted", actual["weighted"], round(stat["timedGenerated"] / stat["timedSeconds"], 1) if stat["timedSeconds"] else None)

# ---------------------------------------------------------------- 7 projects
section("7. PROJECTS AND SESSIONS")
project_groups = collections.defaultdict(list)
for row in rows:
    project_groups[row["project_path"]].append(row)
projects_json = {p["path"]: p for p in data["projects"]}
check("projects length", len(projects_json), len(project_groups))
check("projects sum steps", sum(p["steps"] for p in data["projects"]), len(rows))
check("project labels unique", len({p["name"] for p in data["projects"]}), len(project_groups))
for path, rs in project_groups.items():
    actual = projects_json[path]
    total_in_project = sum(input_total(row) for row in rs)
    cache_read = sum(row["cr"] for row in rs)
    check(f"projects.{path}.name", actual["name"], project_labels[path])
    check(f"projects.{path}.steps", actual["steps"], len(rs))
    check(f"projects.{path}.generatedTokens", actual["generatedTokens"], sum(row["out"] for row in rs))
    check(f"projects.{path}.inputTotal", actual["inputTotal"], total_in_project)
    check(f"projects.{path}.cacheHitRate", actual["cacheHitRate"], round(cache_read / total_in_project, 4) if total_in_project else None)
    check(f"projects.{path}.costUsd", actual["costUsd"], round(math.fsum(row["cost"] for row in rs), 6))

ledger_by_session = collections.defaultdict(list)
for entry in ledger:
    ledger_by_session[entry["session"]].append(entry)
session_groups = collections.defaultdict(list)
for row in rows:
    session_groups[row["session"]].append(row)
check("sessions length", len(data["sessions"]), len(session_groups))
check("session sum steps", sum(s["steps"] for s in data["sessions"]), len(rows))
check("session sum cost", sum(s["costUsd"] for s in data["sessions"]), round(total_cost, 6), 1e-3)
sessions_json = {s["sessionId"]: s for s in data["sessions"]}
for session, rs in session_groups.items():
    actual = sessions_json[session]
    skipped = ledger_by_session.get(session, [])
    skipped_cost = math.fsum(e["cost"] for e in skipped if e["reason"] != "duplicate")
    total_in_session = sum(input_total(row) for row in rs)
    cache_read = sum(row["cr"] for row in rs)
    expected_title = rs[0].get("title") or session[:8]
    expected_project = project_labels.get(rs[0]["project_path"], rs[0]["project_path"])
    check(f"session.{session}.title", actual["title"], expected_title)
    check(f"session.{session}.project", actual["project"], expected_project)
    check(f"session.{session}.projectPath", actual["projectPath"], rs[0]["project_path"])
    check(f"session.{session}.entrypoint", actual["entrypoint"], rs[0]["entrypoint"] or "interactive")
    check(f"session.{session}.branch", actual["branch"], rs[0].get("branch"))
    check(f"session.{session}.models", actual["models"], sorted({row["model"].split("/")[-1] for row in rs}))
    check(f"session.{session}.steps", actual["steps"], len(rs))
    check(f"session.{session}.generatedTokens", actual["generatedTokens"], sum(row["out"] for row in rs))
    check(f"session.{session}.freshInputTokens", actual["freshInputTokens"], sum(fresh_input(row) for row in rs))
    check(f"session.{session}.inputTotal", actual["inputTotal"], total_in_session)
    check(f"session.{session}.cacheHitRate", actual["cacheHitRate"], round(cache_read / total_in_session, 4) if total_in_session else None)
    check(f"session.{session}.costUsd", actual["costUsd"], round(math.fsum(row["cost"] for row in rs), 6))
    check(f"session.{session}.skippedSteps", actual["skippedSteps"], len(skipped))
    check(f"session.{session}.skippedCostUsd", actual["skippedCostUsd"], round(skipped_cost, 6))
    check(f"session.{session}.duplicateSteps", actual["duplicateSteps"], duplicate_sessions.get(session, 0))
    check(f"session.{session}.outcome", actual["outcome"], "unknown")
    check(f"session.{session}.totalSpendUsd", actual["totalSpendUsd"], round(math.fsum(row["cost"] for row in rs) + skipped_cost, 6))

check("steps out sum", sum(s[3] for s in data["steps"]), total_generated)
check("steps cost sum", round(math.fsum(s[8] for s in data["steps"]), 6), round(total_cost, 6), 1e-3)

# ---------------------------------------------------------------- 8 pace/time
section("8. PACE, DAILY, HOURLY, EXTREMES")
regression = {item["model"]: item for item in data["regression"]}
global_fits = {
    "text": fit_rate(text),
    "tool": fit_rate([row for row in timed if row["tool"]]),
}


def ratio_for(row):
    fit = regression[row["model"]]
    kind = "tool" if row["tool"] else "text"
    candidates = (fit.get(kind), fit.get("all"), global_fits.get(kind))
    chosen = next((item for item in candidates if item and item.get("usable")), None)
    if not chosen or not row.get("dt"):
        return None
    expected = chosen["fixedSeconds"] + row["out"] / chosen["pureRate"]
    return expected / row["dt"] if expected > 0 else None


ratio_values = [ratio_for(row) for row in timed if ratio_for(row) is not None]
pace_base = statistics.median(ratio_values) if ratio_values else None
check("meta.paceBase", data["meta"]["paceBase"], round(pace_base, 6) if pace_base else None)
check("meta.paceBasePct", data["meta"]["paceBasePct"], round(pace_base * 100, 1) if pace_base else None)


def pace(values):
    vals = [v for v in values if v is not None]
    if not vals or not pace_base:
        return None
    return round(statistics.median(vals) / pace_base * 100, 1)


ledger_by_day = collections.defaultdict(list)
ledger_by_hour = collections.defaultdict(list)
ledger_by_model_day = collections.defaultdict(list)
for entry in ledger:
    if entry["ts"] is None or entry["reason"] == "duplicate":
        continue
    local = entry["ts"].astimezone()
    day = local.date().isoformat()
    ledger_by_day[day].append(entry)
    ledger_by_hour[local.hour].append(entry)
    ledger_by_model_day[(entry["model"], day)].append(entry)

by_day_all = collections.defaultdict(list)
for row in rows:
    by_day_all[row["ts"].astimezone().date().isoformat()].append(row)
all_days = sorted(set(by_day_all) | set(ledger_by_day))
daily_json = {d["date"]: d for d in data["daily"]}
check("daily length", len(daily_json), len(all_days))
for date in all_days:
    rs_all = by_day_all.get(date, [])
    actual = daily_json[date]
    rs = [row for row in rs_all if is_timed(row)]
    text_rs = [row for row in rs if not row["tool"]]
    ratio_vals = [ratio_for(row) for row in rs if ratio_for(row) is not None]
    ratio = statistics.median(ratio_vals) if ratio_vals else None
    day_ledger = ledger_by_day.get(date, [])
    skipped = math.fsum(e["cost"] for e in day_ledger)
    check(f"daily.{date}.nAll", actual["nAll"], len(rs_all))
    check(f"daily.{date}.nTimed", actual["nTimed"], len(rs))
    check(f"daily.{date}.generatedTokens", actual["generatedTokens"], sum(row["out"] for row in rs_all))
    check(f"daily.{date}.costUsd", actual["costUsd"], round(math.fsum(row["cost"] for row in rs_all), 6))
    check(f"daily.{date}.errorCostUsd", actual["errorCostUsd"], round(skipped, 6))
    check(f"daily.{date}.totalSpendUsd", actual["totalSpendUsd"], round(math.fsum(row["cost"] for row in rs_all) + skipped, 6))
    check(f"daily.{date}.ratio", actual["ratio"], round(ratio, 6) if ratio is not None else None)
    check(f"daily.{date}.ratioN", actual["ratioN"], len(ratio_vals))
    check(f"daily.{date}.index", actual["index"], pace([ratio_for(row) for row in rs]))
    check(f"daily.{date}.weighted", actual["weighted"], round(math.fsum(row["out"] for row in rs) / math.fsum(row["dt"] for row in rs), 1) if rs else None)

daily_by_model_json = {entry["model"]: entry["series"] for entry in data["dailyByModel"]}
check("dailyByModel models", sorted(daily_by_model_json), models)
for model in models:
    m_rows = by_model[model]
    m_by_day = collections.defaultdict(list)
    for row in m_rows:
        m_by_day[row["ts"].astimezone().date().isoformat()].append(row)
    m_dates = sorted(set(m_by_day) | {d for (m, d), es in ledger_by_model_day.items() if m == model and es})
    series_json = {item["date"]: item for item in daily_by_model_json[model]}
    check(f"dailyByModel.{model}.len", len(series_json), len(m_dates))
    for date in m_dates:
        rs_all = m_by_day.get(date, [])
        rs = [row for row in rs_all if is_timed(row)]
        actual = series_json[date]
        m_ledger = ledger_by_model_day.get((model, date), [])
        m_err = math.fsum(e["cost"] for e in m_ledger if e["reason"] != "duplicate")
        check(f"dailyByModel.{model}.{date}.nAll", actual["nAll"], len(rs_all))
        check(f"dailyByModel.{model}.{date}.nTimed", actual["nTimed"], len(rs))
        check(f"dailyByModel.{model}.{date}.generatedTokens", actual["generatedTokens"], sum(row["out"] for row in rs_all))
        check(f"dailyByModel.{model}.{date}.costUsd", actual["costUsd"], round(math.fsum(row["cost"] for row in rs_all), 6))
        check(f"dailyByModel.{model}.{date}.errorCostUsd", actual["errorCostUsd"], round(m_err, 6))

by_hour_all = collections.defaultdict(list)
for row in rows:
    by_hour_all[row["ts"].astimezone().hour].append(row)
hourly_json = {h["hour"]: h for h in data["hourly"]}
check("hourly length", len(hourly_json), 24)
for hour in range(24):
    rs_all = by_hour_all.get(hour, [])
    actual = hourly_json[hour]
    rs = [row for row in rs_all if is_timed(row)]
    hour_ledger = ledger_by_hour.get(hour, [])
    skipped = sum(e["cost"] for e in hour_ledger)
    check(f"hourly.{hour}.nAll", actual["nAll"], len(rs_all))
    check(f"hourly.{hour}.nTimed", actual["nTimed"], len(rs))
    check(f"hourly.{hour}.generatedTokens", actual["generatedTokens"], sum(row["out"] for row in rs_all))
    check(f"hourly.{hour}.costUsd", actual["costUsd"], round(math.fsum(row["cost"] for row in rs_all), 6))
    check(f"hourly.{hour}.totalSpendUsd", actual["totalSpendUsd"], round(math.fsum(row["cost"] for row in rs_all) + skipped, 6))
    check(f"hourly.{hour}.index", actual["index"], pace([ratio_for(row) for row in rs]))


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
        "sessionId": row["session"],
        "project": project_labels[row["project_path"]],
    }


valid_rate = [
    row for row in rows
    if row["dt"] and row["dt"] >= 0.5 and row["out"] > 0
    and row["timing"] in ("plausible", "fastRate", "slowRate")
]
fast_rows = [row for row in valid_rate if row["timing"] in ("plausible", "fastRate")]
slow_rows = [row for row in valid_rate if row["timing"] in ("plausible", "slowRate")]
duration_rows = [row for row in rows if row["dt"] and row["dt"] > 0]
invalid_rate = [
    row for row in rows
    if row["dt"] and row["out"] > 0
    and row["timing"] in ("tooShort", "tooShortDuration", "noTiming")
]
check("peaks", data["peaks"], [peak_row(row) for row in sorted(fast_rows, key=lambda r: r["tps"], reverse=True)[:10]])
check("slowest", data["slowest"], [peak_row(row) for row in sorted(duration_rows, key=lambda r: r["dt"], reverse=True)[:8]])
check("lowestRate", data["lowestRate"], [peak_row(row) for row in sorted(slow_rows, key=lambda r: r["tps"])[:8]])
check("rejectedExtremes", data["rejectedExtremes"], [peak_row(row) for row in sorted(invalid_rate, key=lambda r: r["tps"], reverse=True)[:8]])

# ---------------------------------------------------------------- 9 health
section("9. HEALTH, META, INVARIANTS")
health = data["sessionHealth"]
check("health.toolCalls", health["toolCalls"], total_calls)
check("health.skippedSteps", health["skippedSteps"], len(ledger))
skipped_cost = sum(e["cost"] for e in ledger if e["reason"] != "duplicate")
check("health.skippedCostUsd", health["skippedCostUsd"], round(skipped_cost, 6))
check("health.duplicateSteps", health["duplicateSteps"], counts["duplicates"])
check("health.duplicateCostUsd", health["duplicateCostUsd"], round(duplicate_cost, 6))
check("health.duplicateTokens", health["duplicateTokens"], duplicate_tokens)
check("health.emptySteps", health["emptySteps"], counts["noOutput"])
check("health.noTimestampSteps", health["noTimestampSteps"], counts["noTimestamp"])
check("coverage.timingBreakdown", data["coverage"]["timingBreakdown"], True)
check("coverage.skipLedger", data["coverage"]["skipLedger"], bool(ledger))
check("coverage.titles", data["coverage"]["titles"], any(row.get("title") for row in rows))
check("meta.dateFrom", data["meta"]["dateFrom"], min((row["ts"].astimezone().date().isoformat() for row in rows), default=None))
check("meta.dateTo", data["meta"]["dateTo"], max((row["ts"].astimezone().date().isoformat() for row in rows), default=None))
check("lookup timingReasons", data["lookups"]["timingReasons"], TIMING_REASONS)

contract_errors = validate_common(data)
check("validate_common", contract_errors, [])
check("validate_common report", data["schemaVersion"], SCHEMA_VERSION)

js_text = JS.read_text()
prefix = "window.CMD_TELEMETRY = "
js_data = json.loads(js_text[len(prefix):].rstrip().rstrip(";"))
check("js prefix", js_text.startswith(prefix), True)
check("js bundle deep equal", js_data == data, True)

# ---------------------------------------------------------------- summary
end_section()
print("\n" + "=" * 78)
print("AUDIT SUMMARY")
print("=" * 78)
total_pass = sum(p for _, p, _ in SECTIONS)
total_fail = sum(f for _, _, f in SECTIONS)
for label, passed, failed in SECTIONS:
    print(f"  {label:<52}{passed:>9,} passed {failed:>7,} failed")
print(f"  {'TOTAL':<52}{total_pass:>9,} passed {total_fail:>7,} failed")
if total_fail:
    print("\n  FAILURES:")
    for name, actual, expected in FAIL:
        print(f"    {name}: actual={actual!r} expected={expected!r}")
    sys.exit(1)
print("  ALL CHECKS PASSED")
