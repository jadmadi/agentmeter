# SPDX-License-Identifier: AGPL-3.0-only
import json
import shutil
import sqlite3
import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
OC = ROOT / "opencode-telemetry" / "py"


def python_with_numpy():
    for candidate in (sys.executable, shutil.which("python3")):
        if not candidate:
            continue
        try:
            result = subprocess.run(
                [candidate, "-c", "import numpy"], capture_output=True
            )
        except OSError:
            continue
        if result.returncode == 0:
            return candidate
    return None


PYTHON = python_with_numpy()
if PYTHON is None:
    pytest.skip("audit tests require numpy", allow_module_level=True)


def message_data(created_ms, output=50, reasoning=10, cost=0.01):
    return {
        "model": {"providerID": "test", "id": "model-a"},
        "tokens": {
            "input": 100,
            "output": output,
            "reasoning": reasoning,
            "cache": {"read": 0, "write": 0},
        },
        "time": {
            "created": created_ms,
            "streamed": created_ms + 1500,
            "completed": created_ms + 2000,
        },
        "content": [{"type": "text"}],
        "finish": "stop",
        "agent": "build",
        "cost": cost,
    }


def test_opencode_build_and_audit_survive_newer_row(tmp_path):
    db = tmp_path / "opencode.db"
    out = tmp_path / "out"
    conn = sqlite3.connect(db)
    conn.executescript(
        """
        CREATE TABLE session_v2 (
            id TEXT PRIMARY KEY, title TEXT, slug TEXT, directory TEXT,
            parent_id TEXT, fork_session_id TEXT, version TEXT,
            time_created INTEGER
        );
        CREATE TABLE session_message (
            id TEXT PRIMARY KEY, session_id TEXT, type TEXT,
            time_created INTEGER, data TEXT
        );
        """
    )
    conn.execute(
        "INSERT INTO session_v2 VALUES (?,?,?,?,?,?,?,?)",
        ("ses1", "fixture", "fixture", "/tmp/project", None, None, "1.0", 1000),
    )
    conn.execute(
        "INSERT INTO session_message VALUES (?,?,?,?,?)",
        ("msg1", "ses1", "assistant", 2000, json.dumps(message_data(2_000_000))),
    )
    conn.commit()
    conn.close()

    build = subprocess.run(
        [
            PYTHON, str(OC / "telemetry.py"),
            "--db", str(db),
            "--out", str(out),
        ],
        cwd=OC,
        capture_output=True,
        text=True,
    )
    assert build.returncode == 0, build.stdout[-2000:] + build.stderr[-2000:]

    conn = sqlite3.connect(db)
    conn.execute(
        "INSERT INTO session_message VALUES (?,?,?,?,?)",
        (
            "msg2", "ses1", "assistant", 3000,
            json.dumps(message_data(3_000_000, output=80, cost=0.02)),
        ),
    )
    conn.commit()
    conn.close()

    audit = subprocess.run(
        [
            PYTHON, str(OC / "audit.py"),
            "--db", str(db),
            "--data", str(out / "telemetry-data.json"),
        ],
        cwd=OC,
        capture_output=True,
        text=True,
    )
    assert audit.returncode == 0, audit.stdout[-4000:] + audit.stderr[-4000:]
