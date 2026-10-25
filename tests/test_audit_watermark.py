import json
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CMD = ROOT / "cmd-telemetry"


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
    return sys.executable


PYTHON = python_with_numpy()


def test_cmd_audit_ignores_rows_newer_than_build(tmp_path):
    root = tmp_path / "root"
    out = tmp_path / "out"
    root.mkdir()
    (root / "session.meta.json").write_text(
        json.dumps({"entrypoint": "test", "title": "fixture"})
    )
    original = [
        {"type": "session", "id": "sess1", "cwd": "/tmp/fixture-project"},
        {
            "type": "message",
            "id": "u1",
            "message": {"role": "user", "content": []},
            "timestamp": "2026-10-25T10:00:00Z",
        },
        {
            "type": "message",
            "id": "a1",
            "parentId": "u1",
            "message": {"role": "assistant", "content": [{"type": "text", "text": "ok"}]},
            "usage": {
                "inputTokens": 100, "outputTokens": 50,
                "cacheReadTokens": 0, "cacheWriteTokens": 0, "costUsd": 0.01,
            },
            "model": "test-model",
            "timestamp": "2026-10-25T10:00:01Z",
        },
    ]
    source = root / "session.jsonl"
    source.write_text("".join(json.dumps(row) + "\n" for row in original))

    build = subprocess.run(
        [PYTHON, str(CMD / "telemetry.py"), "--root", str(root), "--out", str(out)],
        cwd=CMD,
        capture_output=True,
        text=True,
    )
    assert build.returncode == 0, build.stdout[-2000:] + build.stderr[-2000:]

    newer = {
        "type": "message",
        "id": "a2",
        "parentId": "a1",
        "message": {"role": "assistant", "content": [{"type": "text", "text": "later"}]},
        "usage": {
            "inputTokens": 200, "outputTokens": 80,
            "cacheReadTokens": 0, "cacheWriteTokens": 0, "costUsd": 0.02,
        },
        "model": "test-model",
        "timestamp": "2026-10-25T11:00:00Z",
    }
    with open(source, "a") as handle:
        handle.write(json.dumps(newer) + "\n")

    audit = subprocess.run(
        [
            PYTHON, str(CMD / "audit.py"),
            "--root", str(root),
            "--data", str(out / "telemetry-data.json"),
        ],
        cwd=CMD,
        capture_output=True,
        text=True,
    )
    assert audit.returncode == 0, audit.stdout[-4000:] + audit.stderr[-4000:]
