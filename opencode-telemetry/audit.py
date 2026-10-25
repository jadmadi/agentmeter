#!/usr/bin/env python3
"""Independent audit of opencode-telemetry data against the SQLite source.

Deliberately does not import telemetry.py. Re-derives counts, tokens, timing,
deduplication, per-model stats, fitted rates, sessions, buckets and step rows
with SQL, a separate Python implementation and numpy least squares.

Exit code 0 only when every check passes.
"""
import argparse
import collections
import datetime as dt
import hashlib
import json
import math
import os
import sqlite3
import statistics
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description="Independently verify telemetry-data.json against the SQLite store.")
parser.add_argument("--db", default=str(Path.home() / ".local/share/opencode/opencode.db"))
parser.add_argument("--data", default=str(ROOT / "data/telemetry-data.json"))
parser.add_argument("--verbose", "-v", action="store_true", help="print every individual assertion")
ARGS = parser.parse_args()
VERBOSE = ARGS.verbose
DB = Path(ARGS.db).expanduser()
DATA = Path(ARGS.data).expanduser()
JS = DATA.parent / "telemetry-data.js"

PASS = []
FAIL = []
CURRENT_SECTION = None
SECTION_PASS = 0
SECTION_FAIL = 0
SECTIONS = []


def section(title):
    global CURRENT_SECTION, SECTION_PASS, SECTION_FAIL
    end_section()
    print("\n" + "=" * 78)
    print(title)
    print("=" * 78)
    CURRENT_SECTION = title
    SECTION_PASS = 0
    SECTION_FAIL = 0


def end_section():
    global CURRENT_SECTION, SECTION_PASS, SECTION_FAIL
    if CURRENT_SECTION:
        print(f"  -> {SECTION_PASS:,} checks passed, {SECTION_FAIL:,} failed")
        SECTIONS.append((CURRENT_SECTION, SECTION_PASS, SECTION_FAIL))
    CURRENT_SECTION = None
    SECTION_PASS = 0
    SECTION_FAIL = 0


def detail(label, value):
    print(f"  {label:<38} {value}")


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
            return text if len(text) <= 300 else text[:297] + "..."
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


def summarize(rows):
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
    decodes = sorted(row["decode_rate"] for row in rows if row.get("decode_rate") is not None)
    tokens = sum(row["gen"] for row in rows)
    visible = sum(row["out"] for row in rows)
    reasoning = sum(row["reas"] for row in rows)
    seconds = sum(row["dt"] for row in rows)
    return {
        "n": len(rows), "tokens": tokens, "visible": visible, "reasoning": reasoning,
        "seconds": round(seconds, 3),
        "ttftMedian": r1(statistics.median(ttfts)) if ttfts else None,
        "ttftN": len(ttfts),
        "decodeMedian": r1(statistics.median(decodes)) if decodes else None,
        "decodeN": len(decodes),
        "weighted": round(tokens / seconds, 1) if seconds else None,
        "visibleWeighted": round(visible / seconds, 1) if seconds else None,
        "median": r1(statistics.median(rates)),
        "visibleMedian": r1(statistics.median(visible_rates)),
        "p10": r1(pct(rates, 10)), "p25": r1(pct(rates, 25)),
        "p75": r1(pct(rates, 75)), "p90": r1(pct(rates, 90)),
        "p99": r1(pct(rates, 99)), "max": r1(rates[-1]),
    }


AUDIT_THRESHOLDS = {
    "minSeconds": 0.5, "maxSeconds": 300.0, "minTokens": 20,
    "minRate": 3.0, "maxRate": 400.0,
}


def is_timed(row):
    d = row.get("dt")
    if d is None or d <= 0:
        return False
    rate = row["gen"] / d
    t = AUDIT_THRESHOLDS
    return (
        t["minSeconds"] <= d <= t["maxSeconds"]
        and row["gen"] >= t["minTokens"]
        and t["minRate"] <= rate <= t["maxRate"]
    )


def bucket_timing(row):
    d = row.get("dt")
    if d is None or d <= 0:
        return "noTiming"
    t = AUDIT_THRESHOLDS
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


data = json.loads(DATA.read_text())
AUDIT_THRESHOLDS = data["meta"]["timing"].get("thresholds", AUDIT_THRESHOLDS)
include_titles = data["meta"]["includeTitles"]
full_paths = data["meta"]["fullPaths"]
model_filters = [t.lower() for t in (data["meta"].get("modelFilters") or [])]
since = data["meta"].get("since")
until = data["meta"].get("until")
conn = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
conn.row_factory = sqlite3.Row

sessions_meta = {}
for r in conn.execute("select id,title,slug,directory,parent_id,fork_session_id,version,time_created from session_v2"):
    sessions_meta[r["id"]] = dict(r)

# ---------------------------------------------------------------- load source
source_rows = conn.execute(
    "SELECT m.id, m.session_id, m.time_created, m.data "
    "FROM session_message m LEFT JOIN session_v2 s ON s.id = m.session_id "
    "WHERE m.type='assistant' "
    "ORDER BY (s.fork_session_id IS NOT NULL) ASC, m.time_created, m.id"
).fetchall()
scanned = len(source_rows)

DEDUP_DROP = {
    "id", "parentID", "sessionID", "sessionId", "messageID", "callID",
    "snapshot", "path", "time_created", "time_updated",
}


def normalize_payload(value):
    if isinstance(value, dict):
        return {k: normalize_payload(v) for k, v in value.items() if k not in DEDUP_DROP}
    if isinstance(value, list):
        return [normalize_payload(v) for v in value]
    return value


def merge_intervals(intervals):
    if not intervals:
        return 0.0, 0.0
    ordered = sorted(intervals)
    total = 0
    cs, ce = ordered[0]
    for start, end in ordered[1:]:
        if start <= ce:
            ce = max(ce, end)
        else:
            total += ce - cs
            cs, ce = start, end
    total += ce - cs
    span = max(e for _, e in ordered) - min(s for s, _ in ordered)
    return total / 1000.0, span / 1000.0


kept = []
ledger = []
seen = {}
duplicates = 0
collisions = 0
empty = 0
error_steps = 0
considered = 0
no_created = 0
sessions = set()
finish_counts = collections.Counter()
tool_calls = collections.Counter()
tool_errors = collections.Counter()
health_errors = 0
health_no_stream = 0

for rec in source_rows:
    x = json.loads(rec["data"])
    model = x.get("model") or {}
    provider = model.get("providerID") or ""
    model_id = model.get("id") or ""
    model_key = f"{provider}/{model_id}" if provider else (model_id or "(unknown)")
    if model_filters and not any(t in model_key.lower() or t in model_id.lower() for t in model_filters):
        continue
    tok = x.get("tokens") or {}
    out = int(tok.get("output") or 0)
    reas = int(tok.get("reasoning") or 0)
    gen = out + reas
    cache = tok.get("cache") or {}
    cr = int(cache.get("read") or 0)
    cw = int(cache.get("write") or 0)
    inp = int(tok.get("input") or 0)
    t = x.get("time") or {}
    created = int(t.get("created") or 0)
    streamed = int(t.get("streamed") or 0)
    completed = int(t.get("completed") or 0)
    if not created:
        no_created += 1
        continue
    ts = dt.datetime.fromtimestamp(created / 1000, tz=dt.timezone.utc).astimezone()
    day = ts.date().isoformat()
    if since and day < since:
        continue
    if until and day > until:
        continue
    considered += 1

    blocks = x.get("content") or []
    tool_names = []
    tool_details = []
    tool_windows = []
    first_block_created = None
    nreason = 0
    ntext = 0
    for b in blocks:
        if not isinstance(b, dict):
            continue
        kind = b.get("type")
        if kind == "tool":
            name = b.get("name") or "(unnamed)"
            tool_names.append(name)
            bt = b.get("time") or {}
            ran = int(bt.get("ran") or 0)
            done = int(bt.get("completed") or 0)
            bc = int(bt.get("created") or 0)
            runtime = (done - ran) / 1000.0 if ran and done and done > ran else None
            state = b.get("state") or {}
            tool_details.append({"name": name, "ran": ran, "completed": done, "runtime": runtime, "error": state.get("status") == "error"})
            if ran and done and done > ran:
                tool_windows.append((ran, done))
            if bc:
                first_block_created = bc if first_block_created is None else min(first_block_created, bc)
        elif kind == "reasoning":
            nreason += 1
            bt = b.get("time") or {}
            bc = int(bt.get("created") or 0)
            if bc:
                first_block_created = bc if first_block_created is None else min(first_block_created, bc)
        elif kind == "text":
            ntext += 1

    if streamed and streamed > created:
        llm_end = streamed
    elif completed and completed > created and not tool_names:
        llm_end = completed
    else:
        llm_end = 0
    dt_sec = (llm_end - created) / 1000 if llm_end else None
    total_dt = (completed - created) / 1000 if completed > created else None
    tool_dt, tool_span = merge_intervals(tool_windows)
    tool_gap = max(0.0, tool_span - tool_dt) if tool_windows else 0.0
    overhead = total_dt - dt_sec - tool_dt if total_dt is not None and dt_sec is not None else None
    ttft = None
    decode_dt = None
    decode_rate = None
    if first_block_created and created and dt_sec:
        candidate = (first_block_created - created) / 1000.0
        if 0.05 <= candidate < dt_sec:
            ttft = candidate
            decode_dt = dt_sec - candidate
            if decode_dt >= 0.1:
                decode_rate = gen / decode_dt

    fp = (created, model_key, inp, out, reas, cr, cw)
    sig = hashlib.sha256(
        json.dumps(normalize_payload(x), sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()
    sigs = seen.setdefault(fp, set())
    if sig in sigs:
        duplicates += 1
        continue
    if sigs:
        collisions += 1
    sigs.add(sig)

    meta = sessions_meta.get(rec["session_id"]) or {}
    directory = meta.get("directory") or "(unknown)"
    project_path = directory
    project_label = directory if full_paths else (os.path.basename(directory.rstrip("/")) or directory)
    entry = "subagent" if meta.get("parent_id") else "root"
    title = (meta.get("title") or meta.get("slug")) if include_titles else None
    finish = x.get("finish") or "unknown"
    base = {
        "id": rec["id"], "session": rec["session_id"], "model": model_key,
        "short": model_id.split("/")[-1] if model_id else provider,
        "provider": provider or "unknown", "variant": model.get("variant") or "default",
        "agent": x.get("agent") or "unknown", "finish": finish,
        "ts": ts, "epoch": int(ts.timestamp()),
        "out": out, "reas": reas, "gen": gen, "inp": inp, "cr": cr, "cw": cw,
        "inpt": inp + cr + cw, "cost": float(x.get("cost") or 0.0),
        "tool": bool(tool_names), "tools": tool_names, "tool_details": tool_details,
        "primary": tool_names[0] if tool_names else "(final answer)",
        "ntools": len(tool_names), "nreason": nreason, "ntext": ntext,
        "thinking": nreason > 0, "dt": dt_sec, "totaldt": total_dt,
        "tooldt": tool_dt if tool_windows else None,
        "toolspan": tool_span if tool_windows else None,
        "toolgap": tool_gap if tool_windows else None,
        "overhead": overhead, "ttft": ttft, "decode_dt": decode_dt,
        "decode_rate": decode_rate, "project_path": project_path,
        "project_label": project_label, "entry": entry, "title": title,
        "version": meta.get("version"), "streamed": bool(streamed),
    }
    finish_counts[finish] += 1
    if finish == "error":
        health_errors += 1
    if not streamed:
        health_no_stream += 1
    for tool in tool_details:
        tool_calls[tool["name"]] += 1
        if tool["error"]:
            tool_errors[tool["name"]] += 1

    if gen <= 0 or finish == "error":
        ledger.append({**base, "empty": gen <= 0, "error": finish == "error"})
        if gen <= 0:
            empty += 1
        if finish == "error":
            error_steps += 1
        continue
    kept.append(base)
    sessions.add(rec["session_id"])

timed = [r for r in kept if is_timed(r)]
timed_text = [r for r in timed if not r["tool"]]
timed_tool = [r for r in timed if r["tool"]]

# ---------------------------------------------------------------- 1 raw counts
section("1. SOURCE COUNTS AND DEDUPE")
check("assistant rows scanned", data["meta"]["stepsScanned"], scanned)
check("considered", data["meta"]["timing"]["considered"], considered)
check("no created", data["meta"]["timing"]["noCreated"], no_created)
check("empty steps", data["meta"]["emptyStepsSkipped"], empty)
check("duplicates", data["meta"]["duplicatesSkipped"], duplicates)
check("fingerprint collisions", data["meta"]["fingerprintCollisions"], collisions)
check("steps", data["totals"]["steps"], len(kept))
check("error steps", data["meta"]["timing"]["errorSteps"], error_steps)
check("error ledger length", data["meta"]["timing"]["ledger"], len(ledger))
check("error ledger data length", len(data["errorLedger"]), len(ledger))
check("rows in timing", data["meta"]["timing"]["rows"], len(kept))
check("considered composition", duplicates + len(ledger) + len(kept), considered)
check("meta error cost", data["meta"]["errorCostUsd"], round(math.fsum(e["cost"] for e in ledger), 6))
check("meta error seconds", data["meta"]["errorSeconds"], round(math.fsum(e["totaldt"] or 0 for e in ledger), 3))
for idx, (actual, expected) in enumerate(zip(data["errorLedger"], ledger)):
    pairs = [
        ("ts", actual["ts"], expected["epoch"]),
        ("sessionId", actual["sessionId"], expected["session"]),
        ("project", actual["project"], expected["project_label"]),
        ("projectPath", actual["projectPath"], expected["project_path"]),
        ("entry", actual["entry"], expected["entry"]),
        ("model", actual["model"], expected["model"]),
        ("provider", actual["provider"], expected["provider"]),
        ("short", actual["short"], expected["short"]),
        ("agent", actual["agent"], expected["agent"]),
        ("variant", actual["variant"], expected["variant"]),
        ("finish", actual["finish"], expected["finish"]),
        ("outputTokens", actual["outputTokens"], expected["out"]),
        ("reasoningTokens", actual["reasoningTokens"], expected["reas"]),
        ("generatedTokens", actual["generatedTokens"], expected["gen"]),
        ("freshInputTokens", actual["freshInputTokens"], expected["inp"]),
        ("cacheReadTokens", actual["cacheReadTokens"], expected["cr"]),
        ("cacheWriteTokens", actual["cacheWriteTokens"], expected["cw"]),
        ("costUsd", actual["costUsd"], round(expected["cost"], 6)),
        ("toolCalls", actual["toolCalls"], expected["ntools"]),
        ("empty", actual["empty"], expected["empty"]),
        ("error", actual["error"], expected["error"]),
    ]
    for key, a, e in pairs:
        check(f"ledger[{idx}].{key}", a, e)
    check(f"ledger[{idx}].llmSeconds", actual["llmSeconds"], round(expected["dt"], 3) if expected["dt"] is not None else None)
    check(f"ledger[{idx}].totalSeconds", actual["totalSeconds"], round(expected["totaldt"], 3) if expected["totaldt"] is not None else None)
    check(f"ledger[{idx}].toolSeconds", actual["toolSeconds"], round(expected["tooldt"], 3) if expected["tooldt"] is not None else None)

# ---------------------------------------------------------------- 2 totals
detail("assistant rows scanned", f"{scanned:,}")
detail("rows considered after filters", f"{considered:,}")
detail("main step rows", f"{len(kept):,}")
detail("timed steps", f"{len(timed):,}")
detail("empty steps", f"{empty:,}")
detail("error steps", f"{error_steps:,}")
detail("error/empty ledger", f"{len(ledger):,}")
detail("considered", f"{considered:,} = {duplicates:,} dup + {len(ledger):,} ledger + {len(kept):,} rows")
detail("fork copies skipped", f"{duplicates:,}")
detail("fingerprint collisions", f"{collisions:,}")
detail("thresholds", data["meta"]["timing"]["thresholds"])
section("2. TOTALS")
tot = collections.Counter()
for r in kept:
    tot["visible"] += r["out"]
    tot["reasoning"] += r["reas"]
    tot["generated"] += r["gen"]
    tot["inp"] += r["inp"]
    tot["cr"] += r["cr"]
    tot["cw"] += r["cw"]
    tot["cost"] += r["cost"]
for name, key in [("visible", "outputTokens"), ("reasoning", "reasoningTokens"), ("generated", "generatedTokens"), ("inp", "inputTokens"), ("cr", "cacheReadTokens"), ("cw", "cacheWriteTokens")]:
    check(f"total {name}", data["totals"][key], tot[name])
check("total cost", data["totals"]["costUsd"], round(math.fsum(r["cost"] for r in kept), 6))
check("total inputTotal", data["totals"]["inputTotal"], tot["inp"] + tot["cr"] + tot["cw"])
check("reasoning share", data["totals"]["reasoningShare"], round(tot["reasoning"] / tot["generated"], 4))
check("cache hit rate", data["cache"]["hitRate"], round(tot["cr"] / (tot["inp"] + tot["cr"] + tot["cw"]), 4))
check("tool steps", data["totals"]["toolSteps"], sum(1 for r in kept if r["tool"]))
check("text steps", data["totals"]["textSteps"], sum(1 for r in kept if not r["tool"]))
check("sessions contributed", data["meta"]["sessions"], len({r["session"] for r in kept}))
check("models", data["meta"]["models"], len({r["model"] for r in kept}))

# ---------------------------------------------------------------- 3 timing
detail("visible tokens", f"{tot['visible']:,}")
detail("reasoning tokens", f"{tot['reasoning']:,}")
detail("generated tokens", f"{tot['generated']:,}")
detail("fresh input tokens", f"{tot['inp']:,}")
detail("cache read tokens", f"{tot['cr']:,}")
detail("recorded cost", f"${data['totals']['costUsd']:.6f}")
detail("error ledger cost", f"${math.fsum(e['cost'] for e in ledger):.6f}")
detail("error ledger seconds", f"{math.fsum(e['totaldt'] or 0 for e in ledger):,.1f}s")
detail("main + ledger cost", f"${data['totals']['costUsd'] + math.fsum(e['cost'] for e in ledger):.6f}")
detail("tool / text steps", f"{data['totals']['toolSteps']:,} / {data['totals']['textSteps']:,}")
section("3. TIMING BUCKETS AND SUMMARIES")
buckets = collections.Counter(bucket_timing(r) for r in kept)
for key in ("plausible", "noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate", "fastRate"):
    check(f"timing {key}", data["meta"]["timing"][key], buckets.get(key, 0))
check("timing stalled", data["meta"]["timing"]["stalled"], buckets.get("tooLong", 0))
check("timing outOfRange", data["meta"]["timing"]["outOfRange"], buckets.get("tooShortDuration", 0) + buckets.get("tooLong", 0))
check("timing implausibleRate", data["meta"]["timing"]["implausibleRate"], buckets.get("slowRate", 0) + buckets.get("fastRate", 0))
check("timing buckets sum", sum(buckets.values()), len(kept))
for label, rows in [("all", timed), ("text", timed_text), ("tool", timed_tool)]:
    summary = summarize(rows)
    for key, value in summary.items():
        check(f"timed.{label}.{key}", data["timed"][label][key], value)

# ---------------------------------------------------------------- 4 per-model
detail("plausible / noTiming", f"{buckets.get('plausible', 0):,} / {buckets.get('noTiming', 0):,}")
detail("tooShort tokens / short dur / long dur", f"{buckets.get('tooShort', 0):,} / {buckets.get('tooShortDuration', 0):,} / {buckets.get('tooLong', 0):,}")
detail("slowRate / fastRate", f"{buckets.get('slowRate', 0):,} / {buckets.get('fastRate', 0):,}")
detail("timed n / tokens", f"{data['timed']['all']['n']:,} / {data['timed']['all']['tokens']:,}")
detail("weighted / median / p90", f"{data['timed']['all']['weighted']} / {data['timed']['all']['median']} / {data['timed']['all']['p90']} tok/s")
detail("visible median", f"{data['timed']['all']['visibleMedian']} tok/s")
detail("TTFT median / n", f"{data['timed']['all']['ttftMedian']}s / {data['timed']['all']['ttftN']:,}")
detail("decode median / n", f"{data['timed']['all']['decodeMedian']} tok/s / {data['timed']['all']['decodeN']:,}")
section("4. PER-MODEL AGGREGATES AND FITS")
by_model = collections.defaultdict(list)
for r in kept:
    by_model[r["model"]].append(r)
json_models = {m["model"]: m for m in data["byModel"]}
check("model key set", set(json_models), set(by_model))
for model in sorted(by_model):
    rows = by_model[model]
    tr = [r for r in rows if is_timed(r)]
    jm = json_models[model]
    msum = summarize(tr)
    check(f"{model}.steps", jm["steps"], len(rows))
    check(f"{model}.toolSteps", jm["toolSteps"], sum(1 for r in rows if r["tool"]))
    check(f"{model}.textSteps", jm["textSteps"], sum(1 for r in rows if not r["tool"]))
    check(f"{model}.outputTokens", jm["outputTokens"], sum(r["out"] for r in rows))
    check(f"{model}.reasoningTokens", jm["reasoningTokens"], sum(r["reas"] for r in rows))
    check(f"{model}.generatedTokens", jm["generatedTokens"], sum(r["gen"] for r in rows))
    check(f"{model}.reasoningShare", jm["reasoningShare"], round(sum(r["reas"] for r in rows) / sum(r["gen"] for r in rows), 4))
    check(f"{model}.inputTokens", jm["inputTokens"], sum(r["inp"] for r in rows))
    check(f"{model}.cacheReadTokens", jm["cacheReadTokens"], sum(r["cr"] for r in rows))
    check(f"{model}.cacheWriteTokens", jm["cacheWriteTokens"], sum(r["cw"] for r in rows))
    total_in = sum(r["inp"] + r["cr"] + r["cw"] for r in rows)
    check(f"{model}.cacheHitRate", jm["cacheHitRate"], round(sum(r["cr"] for r in rows) / total_in, 4) if total_in else None)
    check(f"{model}.costUsd", jm["costUsd"], round(math.fsum(r["cost"] for r in rows), 6))
    check(f"{model}.timedN", jm["timedN"], msum["n"])
    check(f"{model}.weighted", jm["weighted"], msum["weighted"])
    check(f"{model}.median", jm["median"], msum["median"])
    check(f"{model}.visibleMedian", jm["visibleMedian"], msum["visibleMedian"])
    check(f"{model}.p90", jm["p90"], msum["p90"])
    check(f"{model}.ttftMedian", jm["ttftMedian"], msum["ttftMedian"])
    check(f"{model}.ttftN", jm["ttftN"], msum["ttftN"])
    check(f"{model}.decodeMedian", jm["decodeMedian"], msum["decodeMedian"])
    check(f"{model}.decodeN", jm["decodeN"], msum["decodeN"])
    mt = collections.Counter(bucket_timing(r) for r in rows)
    for key in ("plausible", "noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate", "fastRate"):
        check(f"{model}.timing.{key}", jm["timing"][key], mt.get(key, 0))
    check(f"{model}.rejected", jm["rejected"], len(rows) - mt.get("plausible", 0))

# Independent least-squares fits (all/text/tool), same model as telemetry.py
fits_json = {m["model"]: f for m, f in zip(data["byModel"], data["fits"])}
def fit_rows(rows):
    if len(rows) < 30:
        return None
    x = np.array([r["gen"] for r in rows], dtype=float)
    y = np.array([r["dt"] for r in rows], dtype=float)
    sxx = ((x - x.mean()) ** 2).sum()
    if sxx == 0:
        return None
    sxy = ((x - x.mean()) * (y - y.mean())).sum()
    slope = sxy / sxx
    intercept = y.mean() - slope * x.mean()
    if slope <= 0:
        return None
    syy = ((y - y.mean()) ** 2).sum()
    r2 = (sxy * sxy) / (sxx * syy) if syy else None
    return {"fixedSeconds": round(intercept, 2), "pureRate": round(1 / slope, 1), "r2": round(r2, 2) if r2 is not None else None, "n": len(rows)}

for model in sorted(by_model):
    rows = [r for r in by_model[model] if is_timed(r)]
    expected = {
        "all": fit_rows(rows),
        "text": fit_rows([r for r in rows if not r["tool"]]),
        "tool": fit_rows([r for r in rows if r["tool"]]),
    }
    for kind in ("all", "text", "tool"):
        check(f"{model}.fit.{kind}", fits_json[model][kind], expected[kind])

# ---------------------------------------------------------------- 5 prices
detail("models checked", len(by_model))
for m in sorted(data["byModel"], key=lambda x: -x["steps"])[:8]:
    f = fits_json[m["model"]]["all"]
    fit = f"{f['pureRate']} / {f['fixedSeconds']}s / r2 {f['r2']}" if f else "no fit"
    ttft = f"{m['ttftMedian']:.2f}s" if m['ttftMedian'] is not None else "—"
    print(f"    {m['provider'] + '/' + m['short']:<42} steps {m['steps']:>7,}  rej {m['rejected']:>5,}  gen {m['generatedTokens']:>10,}  ${m['costUsd']:>8.2f}  ttft {ttft:>6}  {fit}")
section("5. PRICE FITS")
def price_fit(rows):
    rs = [r for r in rows if r["cost"] > 0]
    if len(rs) < 30:
        return None
    feats = np.array([[r["inp"] / 1e6, r["cr"] / 1e6, r["cw"] / 1e6, r["gen"] / 1e6] for r in rs], dtype=float)
    y = np.array([r["cost"] for r in rs], dtype=float)
    active = [i for i in range(4) if (feats[:, i] > 0).any()]
    if not active:
        return None
    coef, *_ = np.linalg.lstsq(feats[:, active], y, rcond=None)
    rates = [0.0] * 4
    for i, v in zip(active, coef):
        rates[i] = float(v)
    if any(v < -0.01 for v in rates):
        return None
    rates = [max(0.0, v) for v in rates]
    if rates[1] > rates[0] * 1.001:
        return None
    if rates[2] > max(rates[0] * 3, 0.01):
        return None
    pred = feats @ np.array(rates)
    ss_res = ((y - pred) ** 2).sum()
    ss_tot = ((y - y.mean()) ** 2).sum()
    r2 = 1 - ss_res / ss_tot if ss_tot else None
    if r2 is not None and r2 < 0.85:
        return None
    return {
        "fresh": round(rates[0], 4), "cache": round(rates[1], 4),
        "cacheWrite": round(rates[2], 4), "output": round(rates[3], 4),
        "r2": round(r2, 4) if r2 is not None else None, "n": len(rs),
    }

prices_json = {m["model"]: p for m, p in zip(data["byModel"], data["prices"])}
for model in sorted(by_model):
    expected = price_fit(by_model[model])
    actual = prices_json[model]
    if expected is None:
        check(f"{model}.price", actual["fresh"], None)
    else:
        check(f"{model}.price.fresh", actual["fresh"], expected["fresh"])
        check(f"{model}.price.cache", actual["cache"], expected["cache"])
        check(f"{model}.price.cacheWrite", actual["cacheWrite"], expected["cacheWrite"])
        check(f"{model}.price.output", actual["output"], expected["output"])
        # predicted token cost from the JSON rates
        rows = by_model[model]
        predicted = (
            sum(r["inp"] for r in rows) / 1e6 * (actual["fresh"] or 0)
            + sum(r["cr"] for r in rows) / 1e6 * (actual["cache"] or 0)
            + sum(r["cw"] for r in rows) / 1e6 * (actual["cacheWrite"] or 0)
            + sum(r["gen"] for r in rows) / 1e6 * (actual["output"] or 0)
        )
        jc = next(c for c in data["cost"]["byModel"] if c["model"] == model)
        check(f"{model}.price.tokenCost", jc["tokenCost"], round(predicted, 6))

# ---------------------------------------------------------------- 6 step rows
priced = [c for c in data["cost"]["byModel"] if c["priceR2"] is not None]
detail("models with a price fit", len(priced))
for c in sorted(priced, key=lambda x: -x["recordedCost"])[:6]:
    print(f"    {c['model']:<46} fresh ${c['freshInputRate']:.4f}  cache ${c['cacheRate']:.4f}  out ${c['outputRate']:.4f}  r2 {c['priceR2']}  n {c['steps']:,}")
section("6. STEP ROWS EXACTLY EQUAL SOURCE")
models = sorted({r["model"] for r in kept})
tool_name_set = set()
for r in kept:
    if r["tool_details"]:
        tool_name_set.update(d["name"] for d in r["tool_details"])
    else:
        tool_name_set.add("(final answer)")
tools = sorted(tool_name_set)
agents = sorted({r["agent"] for r in kept})
variants = sorted({r["variant"] for r in kept})
project_paths = sorted({r["project_path"] for r in kept})
project_base_counts = collections.Counter(os.path.basename(path.rstrip("/")) or path for path in project_paths)
project_labels = {}
for path in project_paths:
    base = os.path.basename(path.rstrip("/")) or path
    if full_paths:
        project_labels[path] = path
    elif project_base_counts[base] == 1:
        project_labels[path] = base
    else:
        parent = os.path.basename(os.path.dirname(path.rstrip("/"))) or ""
        project_labels[path] = f"{base} · {parent}" if parent else base
projects = project_paths
entries = sorted({r["entry"] for r in kept})
finishes = sorted({r["finish"] for r in kept})
sids = sorted({r["session"] for r in kept})
mi = {x: i for i, x in enumerate(models)}
ti = {x: i for i, x in enumerate(tools)}
ai = {x: i for i, x in enumerate(agents)}
vi = {x: i for i, x in enumerate(variants)}
pi = {x: i for i, x in enumerate(projects)}
ei = {x: i for i, x in enumerate(entries)}
fi = {x: i for i, x in enumerate(finishes)}
si = {x: i for i, x in enumerate(sids)}

def source_step(r):
    dt_val = round(r["dt"], 3) if r["dt"] is not None else -1
    total_val = round(r["totaldt"], 3) if r["totaldt"] is not None else -1
    tool_val = round(r["tooldt"], 3) if r["tooldt"] is not None else -1
    calls = [
        [ti[d["name"]], round(d["runtime"], 3) if d.get("runtime") is not None else -1, 1 if d.get("error") else 0]
        for d in r["tool_details"]
    ]
    return (
        r["epoch"], mi[r["model"]], ti[r["primary"]], r["out"], dt_val, r["inp"], r["cr"], r["cw"],
        r["cost"], ai[r["agent"]], 1 if r["thinking"] else 0, pi[r["project_path"]], ei[r["entry"]],
        si[r["session"]], r["ntools"], r["nreason"], r["ntext"], r["reas"], total_val, tool_val,
        r["inpt"], vi[r["variant"]], fi[r["finish"]],
        round(r["ttft"], 3) if r["ttft"] is not None else -1,
        round(r["decode_dt"], 3) if r["decode_dt"] is not None else -1,
        round(r["decode_rate"], 2) if r["decode_rate"] is not None else -1,
        round(r["overhead"], 3) if r["overhead"] is not None else -1,
        round(r["toolspan"], 3) if r["toolspan"] is not None else -1,
        round(r["toolgap"], 3) if r["toolgap"] is not None else -1,
        calls,
    )

src = sorted(source_step(r) for r in kept)
out = sorted(tuple(row) for row in data["steps"])
check("step row count", len(out), len(src))
check("step rows exactly equal source", out, src)

# duplicate timestamps are legitimate only across different sessions
ts_sessions = collections.Counter((r[0], r[13]) for r in data["steps"])
check("unique (timestamp, session)", sum(1 for v in ts_sessions.values() if v > 1), 0)
ts_groups = collections.Counter(r[0] for r in data["steps"])
print(f"  note: {sum(1 for v in ts_groups.values() if v > 1)} timestamps repeat across different sessions (parallel steps)")

# ---------------------------------------------------------------- 7 sessions
detail("compact step rows", f"{len(out):,}")
detail("source step rows", f"{len(src):,}")
detail("rows exactly equal source", f"{len(out) == len(src)}")
detail("timestamps repeated across sessions", f"{sum(1 for v in ts_groups.values() if v > 1)} groups")
detail("(timestamp, session) duplicates", f"{sum(1 for v in ts_sessions.values() if v > 1)}")
section("7. SESSIONS AND LOOKUPS")
json_sessions = data["sessions"]
check("session array length", len(json_sessions), len(sids))
check("session full ids unique", len({s["sessionId"] for s in json_sessions}), len(json_sessions))
check("session display ids unique", len({s["id"] for s in json_sessions}), len(json_sessions))
check("display id is prefix", all(s["sessionId"].startswith(s["id"]) for s in json_sessions), True)
used = collections.Counter(row[13] for row in data["steps"])
check("all session indices used", sum(1 for i in range(len(sids)) if used[i] == 0), 0)

for i, s in enumerate(json_sessions):
    rows = [r for r in kept if r["session"] == sids[i]]
    tr = [r for r in rows if is_timed(r)]
    stamps = [int(r["ts"].timestamp()) for r in rows]
    first, last = min(stamps), max(stamps)
    span = (last - first) / 60
    gen = sum(r["gen"] for r in rows)
    check(f"session[{i}].sessionId", s["sessionId"], sids[i])
    check(f"session[{i}].id", s["id"], sids[i][:16])
    expected_title = (sessions_meta[sids[i]].get("title") or sessions_meta[sids[i]].get("slug")) if include_titles else None
    check(f"session[{i}].title", s["title"], expected_title)
    check(f"session[{i}].project", s["project"], project_labels[rows[0]["project_path"]])
    check(f"session[{i}].projectPath", s["projectPath"], rows[0]["project_path"])
    check(f"session[{i}].entrypoint", s["entrypoint"], rows[0]["entry"])
    check(f"session[{i}].agent", s["agent"], rows[0]["agent"])
    check(f"session[{i}].models", s["models"], sorted({r["short"] for r in rows}))
    check(f"session[{i}].steps", s["steps"], len(rows))
    check(f"session[{i}].toolSteps", s["toolSteps"], sum(1 for r in rows if r["tool"]))
    check(f"session[{i}].textSteps", s["textSteps"], sum(1 for r in rows if not r["tool"]))
    check(f"session[{i}].outputTokens", s["outputTokens"], sum(r["out"] for r in rows))
    check(f"session[{i}].generatedTokens", s["generatedTokens"], gen)
    check(f"session[{i}].reasoningTokens", s["reasoningTokens"], sum(r["reas"] for r in rows))
    check(f"session[{i}].inputTokens", s["inputTokens"], sum(r["inp"] for r in rows))
    check(f"session[{i}].inputTotal", s["inputTotal"], sum(r["inp"] + r["cr"] + r["cw"] for r in rows))
    check(f"session[{i}].cacheReadTokens", s["cacheReadTokens"], sum(r["cr"] for r in rows))
    check(f"session[{i}].cacheWriteTokens", s["cacheWriteTokens"], sum(r["cw"] for r in rows))
    check(f"session[{i}].costUsd", s["costUsd"], round(math.fsum(r["cost"] for r in rows), 6))
    ledger_rows = [e for e in ledger if e["session"] == sids[i]]
    check(f"session[{i}].errorSteps", s["errorSteps"], len(ledger_rows))
    check(f"session[{i}].errorCostUsd", s["errorCostUsd"], round(math.fsum(e["cost"] for e in ledger_rows), 6))
    check(f"session[{i}].errorSeconds", s["errorSeconds"], round(math.fsum(e["totaldt"] or 0 for e in ledger_rows), 3))
    check(f"session[{i}].timedN", s["timedN"], len(tr))
    check(f"session[{i}].weighted", s["weighted"], round(sum(r["gen"] for r in tr) / sum(r["dt"] for r in tr), 1) if tr else None)
    check(f"session[{i}].firstTs", s["firstTs"], first)
    check(f"session[{i}].lastTs", s["lastTs"], last)
    active = sum(r["totaldt"] for r in rows if r["totaldt"] is not None) / 60
    idle = max(0.0, span - active)
    check(f"session[{i}].spanMinutes", s["spanMinutes"], round(span, 1))
    check(f"session[{i}].activeMinutes", s["activeMinutes"], round(active, 1))
    check(f"session[{i}].idleMinutes", s["idleMinutes"], round(idle, 1))
    check(f"session[{i}].outputPerMinute", s["outputPerMinute"], round(gen / active, 1) if active >= 0.5 else None)
    check(f"session[{i}].wallOutputPerMinute", s["wallOutputPerMinute"], round(gen / span, 1) if span >= 0.5 else None)

check("lookup models", data["lookups"]["models"], [
    {"id": m, "short": m.split("/")[-1], "provider": m.split("/")[0]} for m in models
])
check("lookup tools", data["lookups"]["tools"], tools)
check("lookup agents", data["lookups"]["agents"], agents)
check("lookup variants", data["lookups"]["variants"], variants)
check("lookup projects", data["lookups"]["projects"], projects)
check("lookup project labels", data["lookups"]["projectLabels"], [project_labels[p] for p in projects])
check("lookup entries", data["lookups"]["entrypoints"], entries)
check("lookup finishes", data["lookups"]["finishes"], finishes)

# ---------------------------------------------------------------- 7b objects
detail("sessions in build", f"{len(json_sessions):,}")
detail("unique full session ids", f"{len({x['sessionId'] for x in json_sessions}):,}")
detail("unique display session ids", f"{len({x['id'] for x in json_sessions}):,}")
detail("session indices unused", f"{sum(1 for i in range(len(sids)) if used[i] == 0)}")
section("7b. RUNTIME, REASONING, TOOLS, PROJECTS, COST, PACE")

# global fits
check("fitsGlobal.text", data["fitsGlobal"]["text"], fit_rows([r for r in kept if is_timed(r) and not r["tool"]]))
check("fitsGlobal.tool", data["fitsGlobal"]["tool"], fit_rows([r for r in kept if is_timed(r) and r["tool"]]))

# runtime
with_tools = [
    r for r in kept
    if r["tooldt"] is not None and r["totaldt"] is not None and 0 <= r["tooldt"] <= r["totaldt"] <= 3600
]
complete = [r for r in with_tools if r["dt"] is not None]
tool_vals = sorted(r["tooldt"] for r in with_tools)
step_vals = [r["totaldt"] for r in with_tools]
share_vals = [r["tooldt"] / r["totaldt"] for r in with_tools]
overheads = [r["overhead"] for r in complete if r["overhead"] is not None]
gaps = sorted(r["toolgap"] for r in with_tools if r["toolgap"] is not None)
expected_runtime = {
    "steps": len(with_tools),
    "medianToolSeconds": r2(statistics.median(tool_vals)) if tool_vals else None,
    "p90ToolSeconds": r2(pct(tool_vals, 90)) if tool_vals else None,
    "medianStepSeconds": r2(statistics.median(step_vals)) if step_vals else None,
    "medianShare": round(statistics.median(share_vals), 4) if share_vals else None,
    "weightedShare": round(sum(r["tooldt"] for r in complete) / sum(r["totaldt"] for r in complete), 4) if complete else None,
    "modelTimeShare": round(sum(r["dt"] for r in complete) / sum(r["totaldt"] for r in complete), 4) if complete else None,
    "medianOverheadSeconds": r2(statistics.median(overheads)) if overheads else None,
    "p90OverheadSeconds": r2(pct(overheads, 90)) if overheads else None,
    "overheadShare": round(sum(overheads) / sum(r["totaldt"] for r in complete), 4) if complete and overheads else None,
    "medianGapSeconds": r2(statistics.median(gaps)) if gaps else None,
    "p90GapSeconds": r2(pct(gaps, 90)) if gaps else None,
}
for key, value in expected_runtime.items():
    check(f"runtime.{key}", data["runtime"][key], value)
call_rows = []
for r in with_tools:
    for d in r["tool_details"]:
        if d.get("runtime") is not None:
            call_rows.append((d["name"], d["runtime"], r["totaldt"]))
runtime_by_tool = {item["tool"]: item for item in data["runtime"]["byTool"]}
for tool in sorted({name for name, _, _ in call_rows}):
    rs = [x for x in call_rows if x[0] == tool]
    vals = sorted(x[1] for x in rs)
    totals = [x[2] for x in rs]
    item = runtime_by_tool.get(tool)
    if not item:
        check(f"runtime.byTool.{tool}", None, "present")
        continue
    check(f"runtime.byTool.{tool}.calls", item["calls"], len(rs))
    check(f"runtime.byTool.{tool}.medianToolSeconds", item["medianToolSeconds"], r2(statistics.median(vals)))
    check(f"runtime.byTool.{tool}.p90ToolSeconds", item["p90ToolSeconds"], r2(pct(vals, 90)))
    check(f"runtime.byTool.{tool}.medianStepSeconds", item["medianStepSeconds"], r2(statistics.median(totals)))
    check(f"runtime.byTool.{tool}.weightedShare", item["weightedShare"], round(sum(vals) / sum(totals), 4))

# reasoning
with_reas = [r for r in timed if r["reas"] > 0]
timed_gen = sum(r["gen"] for r in timed)
timed_reas = sum(r["reas"] for r in timed)
expected_reasoning = {
    "steps": len(with_reas),
    "shareSteps": round(len(with_reas) / len(timed), 4) if timed else None,
    "tokenShare": round(timed_reas / timed_gen, 4) if timed_gen else None,
    "medianReasonTokens": r1(statistics.median([r["reas"] for r in with_reas])) if with_reas else None,
    "medianOutputTokens": r1(statistics.median([r["out"] for r in with_reas])) if with_reas else None,
}
for key, value in expected_reasoning.items():
    check(f"reasoning.{key}", data["reasoning"][key], value)

# tools: per-call allocation
tool_stats = collections.defaultdict(lambda: {
    "calls": 0, "steps": set(), "generated": 0.0, "visible": 0.0, "reasoning": 0.0,
    "cost": 0.0, "tokens": [], "runtimes": [], "errors": 0, "timedCalls": 0,
    "timedGenerated": 0.0, "timedSeconds": 0.0, "rates": [],
})
total_calls = 0
for r in kept:
    calls = r["tool_details"] or [{"name": "(final answer)", "runtime": None, "error": False}]
    n = len(calls)
    total_calls += n
    for d in calls:
        stat = tool_stats[d["name"]]
        stat["calls"] += 1
        stat["steps"].add(r["id"])
        stat["generated"] += r["gen"] / n
        stat["visible"] += r["out"] / n
        stat["reasoning"] += r["reas"] / n
        stat["cost"] += r["cost"] / n
        stat["tokens"].append(r["gen"] / n)
        if d.get("runtime") is not None:
            stat["runtimes"].append(d["runtime"])
        if d.get("error"):
            stat["errors"] += 1
        if is_timed(r):
            stat["timedCalls"] += 1
            stat["timedGenerated"] += r["gen"] / n
            stat["timedSeconds"] += r["dt"] / n
            stat["rates"].append(r["gen"] / r["dt"])
expected_tools = []
for tool, stat in tool_stats.items():
    runtimes = sorted(stat["runtimes"])
    expected_tools.append({
        "tool": tool,
        "calls": stat["calls"],
        "stepsWithTool": len(stat["steps"]),
        "shareCalls": round(stat["calls"] / total_calls, 4),
        "generatedTokens": round(stat["generated"], 1),
        "visibleTokens": round(stat["visible"], 1),
        "reasoningTokens": round(stat["reasoning"], 1),
        "medianTokensPerCall": r1(statistics.median(stat["tokens"])),
        "timedCalls": stat["timedCalls"],
        "weighted": round(stat["timedGenerated"] / stat["timedSeconds"], 1) if stat["timedSeconds"] else None,
        "medianTps": r1(statistics.median(stat["rates"])) if stat["rates"] else None,
        "medianRuntime": r2(statistics.median(runtimes)) if runtimes else None,
        "p90Runtime": r2(pct(runtimes, 90)) if runtimes else None,
        "errors": stat["errors"],
        "errRate": round(stat["errors"] / stat["calls"], 4),
        "costUsd": round(stat["cost"], 6),
        "costPerCall": round(stat["cost"] / stat["calls"], 4),
    })
tools_json = {item["tool"]: item for item in data["tools"]}
check("tools keys", sorted(tools_json), sorted({t["tool"] for t in expected_tools}))
for item in expected_tools:
    actual = tools_json.get(item["tool"])
    if not actual:
        continue
    for key, value in item.items():
        if key == "tool":
            continue
        check(f"tools.{item['tool']}.{key}", actual[key], value, 1e-4 if key == "costPerCall" else None)

# projects
project_groups = collections.defaultdict(list)
for r in kept:
    project_groups[r["project_path"]].append(r)
expected_projects = []
for path, rs in project_groups.items():
    expected_projects.append({
        "name": project_labels[path],
        "path": path,
        "steps": len(rs),
        "sessions": len({r["session"] for r in rs}),
        "generatedTokens": sum(r["gen"] for r in rs),
        "costUsd": round(math.fsum(r["cost"] for r in rs), 6),
    })
expected_projects.sort(key=lambda item: -item["generatedTokens"])
projects_json = {p["path"]: p for p in data["projects"]}
check("projects length", len(projects_json), min(12, len(expected_projects)))
for item in expected_projects[:12]:
    actual = projects_json.get(item["path"])
    if not actual:
        check(f"projects.{item['path']}", None, "present")
        continue
    for key in ("name", "steps", "sessions", "generatedTokens", "costUsd"):
        check(f"projects.{item['path']}.{key}", actual[key], item[key])

# cost by model and overall cost/cache
prices_by_model = {m["model"]: p for m, p in zip(data["byModel"], data["prices"])}
cost_models_json = {c["model"]: c for c in data["cost"]["byModel"]}
for model in sorted(by_model):
    rows = by_model[model]
    inp = sum(r["inp"] for r in rows)
    cr = sum(r["cr"] for r in rows)
    cw = sum(r["cw"] for r in rows)
    gen = sum(r["gen"] for r in rows)
    total_in = inp + cr + cw
    recorded = round(math.fsum(r["cost"] for r in rows), 6)
    actual = cost_models_json[model]
    check(f"cost.byModel.{model}.recordedCost", actual["recordedCost"], recorded)
    check(f"cost.byModel.{model}.inputTokens", actual["inputTokens"], inp)
    check(f"cost.byModel.{model}.inputTotal", actual["inputTotal"], total_in)
    check(f"cost.byModel.{model}.cacheReadTokens", actual["cacheReadTokens"], cr)
    check(f"cost.byModel.{model}.cacheWriteTokens", actual["cacheWriteTokens"], cw)
    check(f"cost.byModel.{model}.costPerStep", actual["costPerStep"], round(recorded / len(rows), 4))
    check(f"cost.byModel.{model}.costPerMillionOutput", actual["costPerMillionOutput"], round(recorded / gen * 1e6, 2) if gen else None)
    check(f"cost.byModel.{model}.cacheHitRate", actual["cacheHitRate"], round(cr / total_in, 4) if total_in else None)
    check(f"cost.byModel.{model}.cacheWriteShare", actual["cacheWriteShare"], round(cw / total_in, 4) if total_in else None)
    price = prices_by_model[model]
    if price["fresh"] is None:
        check(f"cost.byModel.{model}.freshCost", actual["freshCost"], None)
        check(f"cost.byModel.{model}.tokenCost", actual["tokenCost"], recorded)
        check(f"cost.byModel.{model}.unattributedCost", actual["unattributedCost"], recorded)
        check(f"cost.byModel.{model}.cacheSavings", actual["cacheSavings"], None)
        check(f"cost.byModel.{model}.effectiveInputPrice", actual["effectiveInputPrice"], None)
    else:
        fresh_cost = inp / 1e6 * price["fresh"]
        cache_cost = cr / 1e6 * price["cache"]
        write_cost = cw / 1e6 * price["cacheWrite"]
        output_cost = gen / 1e6 * price["output"]
        token_cost = fresh_cost + cache_cost + write_cost + output_cost
        read_savings = cr * (price["fresh"] - price["cache"]) / 1e6
        write_penalty = cw * (price["cacheWrite"] - price["fresh"]) / 1e6
        savings = read_savings - write_penalty
        effective = (fresh_cost + cache_cost + write_cost) / (total_in / 1e6) if total_in else None
        check(f"cost.byModel.{model}.freshCost", actual["freshCost"], round(fresh_cost, 6))
        check(f"cost.byModel.{model}.cacheReadCost", actual["cacheReadCost"], round(cache_cost, 6))
        check(f"cost.byModel.{model}.cacheWriteCost", actual["cacheWriteCost"], round(write_cost, 6))
        check(f"cost.byModel.{model}.outputCost", actual["outputCost"], round(output_cost, 6))
        check(f"cost.byModel.{model}.tokenCost", actual["tokenCost"], round(token_cost, 6))
        check(f"cost.byModel.{model}.unattributedCost", actual["unattributedCost"], 0.0)
        check(f"cost.byModel.{model}.cacheSavings", actual["cacheSavings"], round(savings, 6))
        check(f"cost.byModel.{model}.cacheReadSavings", actual["cacheReadSavings"], round(read_savings, 6))
        check(f"cost.byModel.{model}.cacheWritePenalty", actual["cacheWritePenalty"], round(write_penalty, 6))
        check(f"cost.byModel.{model}.effectiveInputPrice", actual["effectiveInputPrice"], round(effective, 4) if effective is not None else None)

check("cost.recorded", data["cost"]["recorded"], round(math.fsum(r["cost"] for r in kept), 6))
check("cost.tokenCost", data["cost"]["tokenCost"], round(math.fsum(c["tokenCost"] for c in data["cost"]["byModel"]), 6))
check("cost.freshCost", data["cost"]["freshCost"], round(math.fsum((c["freshCost"] or 0) for c in data["cost"]["byModel"]), 6))
check("cost.cacheReadCost", data["cost"]["cacheReadCost"], round(math.fsum((c["cacheReadCost"] or 0) for c in data["cost"]["byModel"]), 6))
check("cost.cacheWriteCost", data["cost"]["cacheWriteCost"], round(math.fsum((c["cacheWriteCost"] or 0) for c in data["cost"]["byModel"]), 6))
check("cost.outputCost", data["cost"]["outputCost"], round(math.fsum((c["outputCost"] or 0) for c in data["cost"]["byModel"]), 6))
check("cost.unattributedCost", data["cost"]["unattributedCost"], round(math.fsum(c["unattributedCost"] for c in data["cost"]["byModel"]), 6))
check("cache.readCost", data["cache"]["readCost"], data["cost"]["cacheReadCost"])
check("cache.writeCost", data["cache"]["writeCost"], data["cost"]["cacheWriteCost"])
check("cache.tokenCost", data["cache"]["tokenCost"], data["cost"]["tokenCost"])
check("cache.savings", data["cache"]["savings"], round(math.fsum((c["cacheSavings"] or 0) for c in data["cost"]["byModel"]), 6))
check("cache.readSavings", data["cache"]["readSavings"], round(math.fsum((c["cacheReadSavings"] or 0) for c in data["cost"]["byModel"]), 6))
check("cache.writePenalty", data["cache"]["writePenalty"], round(math.fsum((c["cacheWritePenalty"] or 0) for c in data["cost"]["byModel"]), 6))
check("cache.hitRate", data["cache"]["hitRate"], round(sum(r["cr"] for r in kept) / sum(r["inpt"] for r in kept), 4))
check("cache.writeShare", data["cache"]["writeShare"], round(sum(r["cw"] for r in kept) / sum(r["inpt"] for r in kept), 4))

# pace index
fits_by_model = {m["model"]: f for m, f in zip(data["byModel"], data["fits"])}
def expected_ratio(r):
    fit = fits_by_model[r["model"]]
    kind = "tool" if r["tool"] else "text"
    chosen = fit.get(kind) or fit.get("all") or data["fitsGlobal"].get(kind)
    if not chosen or not r["dt"]:
        return None
    return (chosen["fixedSeconds"] + r["gen"] / chosen["pureRate"]) / r["dt"]

ratios = {id(r): expected_ratio(r) for r in timed}
baseline = data["meta"].get("paceBaseline")
base_rows = timed
if baseline:
    base_rows = [
        r for r in timed
        if baseline["from"] <= r["ts"].date().isoformat() <= baseline["to"]
    ]
ratio_values = [ratios[id(r)] for r in base_rows if ratios[id(r)] is not None]
pace_base = statistics.median(ratio_values) if ratio_values else None
if baseline:
    check("paceBaseline.from", baseline["from"], data["meta"].get("paceBaseline", {}).get("from"))
    check("paceBaseline.n", baseline["n"], len(ratio_values))
    check("paceBaseline.paceBase", baseline["paceBase"], round(pace_base * 100, 1) if pace_base else None)
check("meta.paceBase", data["meta"]["paceBase"], round(pace_base * 100, 1) if pace_base else None)

def expected_pace(values):
    vals = [v for v in values if v is not None]
    if not vals or not pace_base:
        return None
    return round(statistics.median(vals) / pace_base * 100, 1)

# daily
for day in data["daily"]:
    rs = [r for r in timed if r["ts"].date().isoformat() == day["date"]]
    trs = [r for r in rs if not r["tool"]]
    vals = [ratios[id(r)] for r in rs]
    vals_t = [ratios[id(r)] for r in trs]
    check(f"daily[{day['date']}].ratio", day["ratio"], r1(statistics.median([v for v in vals if v is not None])) if any(v is not None for v in vals) else None)
    check(f"daily[{day['date']}].index", day["index"], expected_pace(vals))
    check(f"daily[{day['date']}].textIndex", day["textIndex"], expected_pace(vals_t))

# hourly
for h in data["hourly"]:
    rs = [r for r in timed if r["ts"].hour == h["hour"]]
    trs = [r for r in rs if not r["tool"]]
    vals = [ratios[id(r)] for r in rs]
    vals_t = [ratios[id(r)] for r in trs]
    check(f"hourly[{h['hour']}].ratio", h["ratio"], r1(statistics.median([v for v in vals if v is not None])) if any(v is not None for v in vals) else None)
    check(f"hourly[{h['hour']}].index", h["index"], expected_pace(vals))
    check(f"hourly[{h['hour']}].textRatio", h["textRatio"], r1(statistics.median([v for v in vals_t if v is not None])) if any(v is not None for v in vals_t) else None)
    check(f"hourly[{h['hour']}].textIndex", h["textIndex"], expected_pace(vals_t))

# context
for section_name, source in (("context", timed), ("contextText", timed_text)):
    for bucket in data[section_name]:
        label = bucket["label"]
        if label == "<50k":
            lo, hi = 0, 50000
        elif label == "50-150k":
            lo, hi = 50000, 150000
        elif label == "150-400k":
            lo, hi = 150000, 400000
        else:
            lo, hi = 400000, 10**12
        rs = [r for r in source if lo <= r["inpt"] < hi]
        vals = [ratios[id(r)] for r in rs]
        check(f"{section_name}[{label}].ratio", bucket["ratio"], r1(statistics.median([v for v in vals if v is not None])) if any(v is not None for v in vals) else None)
        check(f"{section_name}[{label}].index", bucket["index"], expected_pace(vals))

# ---------------------------------------------------------------- 8 derived
rt = data["runtime"]
def pct_text(v):
    return "—" if v is None else f"{v * 100:.1f}%"
overhead_pct = "—" if rt['overheadShare'] is None else f"{rt['overheadShare'] * 100:.2f}%"
detail("model / tool / overhead", f"{pct_text(rt['modelTimeShare'])} / {pct_text(rt['weightedShare'])} / {overhead_pct}")
detail("tool runtime median / p90", f"{rt['medianToolSeconds']}s / {rt['p90ToolSeconds']}s")
detail("overhead median / p90", f"{rt['medianOverheadSeconds']}s / {rt['p90OverheadSeconds']}s")
detail("reasoning share (timed)", pct_text(data["reasoning"]["tokenShare"]))
detail("TTFT median / decode", f"{data['timed']['all']['ttftMedian']}s / {data['timed']['all']['decodeMedian']} tok/s")
detail("error ledger", f"{len(data['errorLedger'])} entries · ${data['meta']['errorCostUsd']:.6f} · {data['meta']['errorSeconds']:,.1f}s")
detail("tools / projects", f"{len(data['tools'])} / {len(data['projects'])}")
detail("recorded / fitted token cost", f"${data['cost']['recorded']:.6f} / ${data['cost']['tokenCost']:.6f}")
detail("unattributed cost", f"${data['cost']['unattributedCost']:.6f}")
detail("pace base", data["meta"]["paceBase"])
section("8. DERIVED BUCKETS, TIME GROUPS, PEAKS")
# histogram
edges = [0, 10, 20, 30, 40, 50, 66, 80, 100, 120, 150, 200, 250, 300, 400]
def hist(rows):
    out = [0] * (len(edges) - 1)
    for r in rows:
        v = r["gen"] / r["dt"]
        i = 0
        while i < len(edges) - 2 and v >= edges[i + 1]:
            i += 1
        out[i] += 1
    return out
check("histogram labels", data["histogram"]["labels"], ["0-10", "10-20", "20-30", "30-40", "40-50", "50-66", "66-80", "80-100", "100-120", "120-150", "150-200", "200-250", "250-300", "300+"])
check("histogram all", data["histogram"]["all"], hist(timed))
check("histogram text", data["histogram"]["text"], hist(timed_text))

# size buckets
def bucketize(rows, defs):
    out = []
    for label, lo, hi in defs:
        rs = [r for r in rows if lo <= r["gen"] < hi]
        if not rs:
            continue
        rates = sorted(r["gen"] / r["dt"] for r in rs)
        tokens = sum(r["gen"] for r in rs)
        seconds = sum(r["dt"] for r in rs)
        out.append({"label": label, "n": len(rs), "tokens": tokens, "weighted": round(tokens / seconds, 1), "median": r1(statistics.median(rates)), "p90": r1(pct(rates, 90))})
    return out
size_defs = [("20-100", 20, 100), ("100-300", 100, 300), ("300-600", 300, 600), ("600-1k", 600, 1000), ("1k-2k", 1000, 2000), ("2k-5k", 2000, 5000), ("5k+", 5000, 10**9)]
check("sizeBuckets", data["sizeBuckets"], bucketize(timed, size_defs))

# context buckets
def context_buckets(rows, defs):
    out = []
    for label, lo, hi in defs:
        rs = [r for r in rows if lo <= r["inpt"] < hi]
        if not rs:
            continue
        out.append({"label": label, "n": len(rs), "median": r1(statistics.median([r["gen"] / r["dt"] for r in rs])), "weighted": round(sum(r["gen"] for r in rs) / sum(r["dt"] for r in rs), 1)})
    return out
ctx_defs = [("<50k", 0, 50_000), ("50-150k", 50_000, 150_000), ("150-400k", 150_000, 400_000), ("400k+", 400_000, 10**12)]
ctx = context_buckets(timed, ctx_defs)
ctx_text = context_buckets(timed_text, ctx_defs)
for section_name, expected in [("context", ctx), ("contextText", ctx_text)]:
    actual = data[section_name]
    check(f"{section_name}.n", [x["n"] for x in actual], [x["n"] for x in expected])
    check(f"{section_name}.median", [x["median"] for x in actual], [x["median"] for x in expected])
    check(f"{section_name}.weighted", [x["weighted"] for x in actual], [x["weighted"] for x in expected])

# daily / hourly
def local_date(r):
    return r["ts"].date().isoformat()
def local_hour(r):
    return r["ts"].hour
daily_rows = collections.defaultdict(list)
for r in timed:
    daily_rows[local_date(r)].append(r)
for day in data["daily"]:
    rs = daily_rows[day["date"]]
    check(f"daily[{day['date']}].n", day["n"], len(rs))
    check(f"daily[{day['date']}].tokens", day["tokens"], sum(r["gen"] for r in rs))
    check(f"daily[{day['date']}].weighted", day["weighted"], round(sum(r["gen"] for r in rs) / sum(r["dt"] for r in rs), 1))
    check(f"daily[{day['date']}].median", day["median"], r1(statistics.median([r["gen"] / r["dt"] for r in rs])))
    trs = [r for r in rs if not r["tool"]]
    check(f"daily[{day['date']}].textN", day["textN"], len(trs))
    check(f"daily[{day['date']}].textMedian", day["textMedian"], r1(statistics.median([r["gen"] / r["dt"] for r in trs])) if trs else None)
hourly_rows = collections.defaultdict(list)
for r in timed:
    hourly_rows[local_hour(r)].append(r)
for h in data["hourly"]:
    rs = hourly_rows.get(h["hour"], [])
    check(f"hourly[{h['hour']}].n", h["n"], len(rs))
    check(f"hourly[{h['hour']}].tokens", h["tokens"], sum(r["gen"] for r in rs))
    check(f"hourly[{h['hour']}].median", h["median"], r1(statistics.median([r["gen"] / r["dt"] for r in rs])) if rs else None)
    trs = [r for r in rs if not r["tool"]]
    check(f"hourly[{h['hour']}].textN", h["textN"], len(trs))
    check(f"hourly[{h['hour']}].textMedian", h["textMedian"], r1(statistics.median([r["gen"] / r["dt"] for r in trs])) if trs else None)

# peaks / slowest
def peak_row(r):
    return {"tps": r1(r["gen"] / r["dt"]), "out": r["gen"], "visible": r["out"], "reasoning": r["reas"], "dt": r1(r["dt"]), "model": r["short"], "at": r["ts"].strftime("%Y-%m-%d %H:%M"), "tool": r["tool"], "input": r["inpt"]}
peaks = [peak_row(r) for r in sorted(timed, key=lambda r: r["gen"] / r["dt"], reverse=True)[:10]]
slowest = [peak_row(r) for r in sorted(timed, key=lambda r: r["gen"] / r["dt"])[:8]]
check("peaks", data["peaks"], peaks)
check("slowest", data["slowest"], slowest)

# ---------------------------------------------------------------- 9 health/meta
detail("histogram steps", f"{sum(data['histogram']['all']):,}")
detail("size / context buckets", f"{len(data['sizeBuckets'])} / {len(data['context'])}")
detail("daily / hourly groups", f"{len(data['daily'])} / {len(data['hourly'])}")
detail("fastest / slowest rows", f"{len(data['peaks'])} / {len(data['slowest'])}")
detail("peaks top rate", f"{data['peaks'][0]['tps'] if data['peaks'] else '—'} tok/s")
section("9. HEALTH, META, JS BUNDLE")
h = data["sessionHealth"]
check("health sessions total", h["sessionsTotal"], conn.execute("select count(*) from session_v2").fetchone()[0])
check("health sessions root", h["sessionsRoot"], conn.execute("select count(*) from session_v2 where parent_id is null").fetchone()[0])
check("health sessions subagent", h["sessionsSubagent"], conn.execute("select count(*) from session_v2 where parent_id is not null").fetchone()[0])
check("health errors", h["errors"], health_errors)
check("health noStream", h["noStream"], health_no_stream)
check("health toolCalls", h["toolCalls"], sum(tool_calls.values()))
check("health toolErrors total", sum(x["count"] for x in h["toolErrors"]), sum(tool_errors.values()))
check("health finishes sum", sum(x["count"] for x in h["finishes"]), len(kept) + len(ledger))
check("health sessionsInWindow", h["sessionsInWindow"], len(sessions))
check("health errorSteps", h["errorSteps"], len(ledger))
check("health errorCostUsd", h["errorCostUsd"], round(math.fsum(e["cost"] for e in ledger), 6))
check("health errorSeconds", h["errorSeconds"], round(math.fsum(e["totaldt"] or 0 for e in ledger), 3))
finish_map = {x["finish"]: x["count"] for x in h["finishes"]}
for kind, count in finish_counts.items():
    check(f"health finish {kind}", finish_map.get(kind, 0), count)
tool_error_map = {x["tool"]: x["count"] for x in h["toolErrors"]}
for name, count in tool_errors.items():
    check(f"health tool error {name}", tool_error_map.get(name, 0), count)
check("health versions sum", sum(x["sessions"] for x in h["versions"]), len(sessions_meta))
for kind, count in conn.execute("select type,count(*) from session_message where type not in ('assistant','user') group by type"):
    actual = next((x["count"] for x in h["events"] if x["type"] == kind), 0)
    check(f"health event {kind}", actual, count)

date_from = min(r["ts"] for r in kept).date().isoformat()
date_to = max(r["ts"] for r in kept).date().isoformat()
check("meta dateFrom", data["meta"]["dateFrom"], date_from)
check("meta dateTo", data["meta"]["dateTo"], date_to)

js_text = JS.read_text()
prefix = "window.OPENCODE_TELEMETRY = "
js_data = json.loads(js_text[len(prefix):].rstrip().rstrip(";"))
check("js prefix", js_text.startswith(prefix), True)
js_equal = js_data == data
check("js bundle deep equal", js_equal, True)

# ---------------------------------------------------------------- summary
h9 = data["sessionHealth"]
detail("sessions window / total / root / subagent", f"{h9['sessionsInWindow']:,} / {h9['sessionsTotal']:,} / {h9['sessionsRoot']:,} / {h9['sessionsSubagent']:,}")
detail("schema version", data.get("schemaVersion"))
detail("error ledger / cost / sec", f"{len(data['errorLedger']):,} / ${h9['errorCostUsd']:.6f} / {h9['errorSeconds']:,.1f}s")
detail("compactions / model switches", f"{h9['compactions']} / {h9['modelSwitches']}")
detail("errors / no stream", f"{h9['errors']} / {h9['noStream']}")
detail("tool calls / tool errors", f"{h9['toolCalls']:,} / {sum(x['count'] for x in h9['toolErrors']):,}")
detail("js bundle deep equal", js_equal)
end_section()
print("\n" + "=" * 78)
print("AUDIT SUMMARY")
print("=" * 78)
for label, p, f in SECTIONS:
    print(f"  {label:<52} {p:>6,} passed   {f:>3} failed")
print(f"  {'TOTAL':<52} {len(PASS):>6,} passed   {len(FAIL):>3} failed")
if FAIL:
    print("\n  FAILURES:")
    for name, actual, expected in FAIL:
        print(f"    {name}: actual={actual!r} expected={expected!r}")
    raise SystemExit(1)
print("  ALL CHECKS PASSED")
