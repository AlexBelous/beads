"""Manual CLI/persistence probe. Use only a supplied binary and fresh fixture path.

This tests sequential claims, not concurrent dependency/registry admission.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("binary", type=pathlib.Path)
parser.add_argument("fixture", type=pathlib.Path)
args = parser.parse_args()
binary = args.binary.resolve(strict=True)
root = args.fixture.resolve()
root.mkdir()  # Refuse reuse of an existing store or HOME.
repo = root / "repo"
repo.mkdir()
(root / "home").mkdir()
env = {"HOME": str(root / "home"), "PATH": "/usr/bin:/bin", "CI": "true",
       "BEADS_ACTOR": "off128-fixture-worker", "BD_SYNC_DISABLE": "1", "GOMAXPROCS": "2"}
receipt = {"binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "commands": []}


def run(argv):
    result = subprocess.run(argv, cwd=repo, env=env, capture_output=True, text=True, timeout=60)
    receipt["commands"].append({"argv": [str(x) for x in argv], "exit": result.returncode,
                                "stdout": result.stdout, "stderr": result.stderr})
    (root / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return result


def bd(*argv, ok=True):
    result = run([str(binary), "--sandbox", *argv])
    if ok:
        assert result.returncode == 0, receipt["commands"][-1]
    return result


def show(identity):
    rows = json.loads(bd("show", identity, "--json").stdout)
    return rows[0] if isinstance(rows, list) else rows


def create(title):
    row = json.loads(bd("create", "--title", title, "--status", "open", "--json").stdout)
    return row["id"]


for argv in (["git", "init"], ["git", "config", "user.name", "isolated fixture"],
             ["git", "config", "user.email", "fixture@invalid"],
             ["git", "config", "core.hooksPath", ".git/hooks"]):
    assert run(argv).returncode == 0
bd("version")
bd("init", "--prefix", "cug", "--non-interactive", "--skip-hooks", "--skip-agents")
work = create("enrolled work")
legacy = create("legacy control")
first = create("first prerequisite")
second = create("second prerequisite")
bd("update", work, "--set-metadata", "claim.require_unblocked=true", "--json")
assert show(work)["metadata"]["claim.require_unblocked"] is True
for identity, blocker in ((work, first), (work, second), (legacy, second)):
    bd("dep", "add", identity, blocker, "--json")
assert bd("update", legacy, "--claim", "--json").returncode == 0
bd("close", first, "--reason", "first prerequisite complete", "--json")
before = show(work)
refusal = bd("update", work, "--claim", "--json", ok=False)
after = show(work)
assert refusal.returncode != 0 and after["status"] == "open" and not after.get("assignee")
assert after["revision"] == before["revision"]
bd("close", second, "--reason", "last prerequisite complete", "--json")
bd("update", work, "--claim", "--json")
assert show(work)["status"] == "in_progress"
late = create("prerequisite added after successful claim")
bd("dep", "add", work, late, "--json")
before = show(work)
bd("update", work, "--claim", "--json")
after = show(work)
assert after["revision"] == before["revision"] and after["assignee"] == before["assignee"]
print(json.dumps({"sequential_cli_probe": "PASS", "binary_sha256": receipt["binary_sha256"],
                  "fixture": str(root), "commands": len(receipt["commands"]),
                  "concurrent_admission_proven": False}))
