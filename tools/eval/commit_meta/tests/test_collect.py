"""End-to-end CLI tests.

Drives `python -m commit_meta.collect` as a subprocess to exercise the real
argparse + main() wiring rather than calling internals.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest
import yaml

PKG_ROOT = Path(__file__).resolve().parent.parent  # …/commit_meta


def _run_cli(
    *args: str,
    cwd: Path | None = None,
    extra_env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    # Make commit_meta importable from the package root regardless of cwd.
    env["PYTHONPATH"] = str(PKG_ROOT) + os.pathsep + env.get("PYTHONPATH", "")
    if extra_env:
        env.update(extra_env)
    return subprocess.run(
        [sys.executable, "-m", "commit_meta.collect", *args],
        cwd=str(cwd) if cwd else None,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )


@pytest.fixture
def tiny_repo(tmp_path: Path) -> Path:
    repo = tmp_path / "tinyrepo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q", "-b", "main"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=repo, check=True)
    (repo / "f.txt").write_text("hi\n")
    subprocess.run(["git", "add", "f.txt"], cwd=repo, check=True)
    subprocess.run(
        ["git", "commit", "-q", "-m", "init"], cwd=repo, check=True
    )
    return repo


def test_json_output_has_all_fields(tiny_repo: Path, tmp_path: Path) -> None:
    # All 4 repos absent except loom => N/A strings for the rest.
    missing = tmp_path / "missing"
    proc = _run_cli(
        "--loom",
        str(tiny_repo),
        "--agentserver",
        str(missing / "agent"),
        "--modelserver",
        str(missing / "model"),
        "--app",
        str(missing / "app"),
    )
    assert proc.returncode == 0, proc.stderr
    payload = json.loads(proc.stdout)

    expected_keys = {
        "loom_commit",
        "agentserver_commit",
        "modelserver_commit",
        "app_commit",
        "os",
        "collected_at_unix",
        "machine_hostname",
    }
    assert set(payload.keys()) == expected_keys

    assert set(payload["os"].keys()) == {"kernel", "distro", "arch"}
    assert isinstance(payload["collected_at_unix"], int)
    assert payload["collected_at_unix"] > 0
    assert isinstance(payload["machine_hostname"], str)
    assert payload["machine_hostname"]

    assert "(" in payload["loom_commit"]  # branch+state suffix
    assert payload["agentserver_commit"].startswith("N/A:")
    assert payload["modelserver_commit"].startswith("N/A:")
    assert payload["app_commit"].startswith("N/A:")


def test_env_vars_override_defaults(tiny_repo: Path, tmp_path: Path) -> None:
    other = tmp_path / "other"
    other.mkdir()
    subprocess.run(["git", "init", "-q", "-b", "main"], cwd=other, check=True)
    subprocess.run(
        ["git", "config", "user.email", "t@t"], cwd=other, check=True
    )
    subprocess.run(["git", "config", "user.name", "t"], cwd=other, check=True)
    (other / "x").write_text("x\n")
    subprocess.run(["git", "add", "x"], cwd=other, check=True)
    subprocess.run(
        ["git", "commit", "-q", "-m", "init"], cwd=other, check=True
    )

    proc = _run_cli(
        "--loom",
        str(tiny_repo),
        extra_env={
            "AGENTSERVER_ROOT": str(other),
            "MODELSERVER_ROOT": "/definitely/not/there",
            "APP_ROOT": "/also/not/there",
        },
    )
    assert proc.returncode == 0, proc.stderr
    payload = json.loads(proc.stdout)
    # AGENTSERVER_ROOT pointed at a real repo => real SHA, not N/A.
    assert not payload["agentserver_commit"].startswith("N/A:")
    assert payload["modelserver_commit"].startswith("N/A:")
    assert payload["app_commit"].startswith("N/A:")


def test_yaml_output_format(tiny_repo: Path, tmp_path: Path) -> None:
    missing = tmp_path / "missing"
    proc = _run_cli(
        "--loom",
        str(tiny_repo),
        "--agentserver",
        str(missing),
        "--modelserver",
        str(missing),
        "--app",
        str(missing),
        "--format=yaml",
    )
    assert proc.returncode == 0, proc.stderr
    payload = yaml.safe_load(proc.stdout)
    assert "loom_commit" in payload
    assert payload["os"]["kernel"]


def test_default_format_is_json(tiny_repo: Path, tmp_path: Path) -> None:
    missing = tmp_path / "missing"
    proc = _run_cli(
        "--loom",
        str(tiny_repo),
        "--agentserver",
        str(missing),
        "--modelserver",
        str(missing),
        "--app",
        str(missing),
    )
    assert proc.returncode == 0, proc.stderr
    json.loads(proc.stdout)  # raises if not valid JSON


def test_missing_default_path_preserved_in_na_string(
    tiny_repo: Path, tmp_path: Path
) -> None:
    """When all CLI flags and env vars are unset and the /root/<repo>
    defaults don't exist, the N/A string must name the path we actually
    tried (the default), not ``<unset>`` — otherwise the user has no clue
    where the collector looked. Documented in README's default-search table.
    """
    # Clear inherited env vars so we exercise the pure default-fallback path.
    proc = subprocess.run(
        [
            sys.executable,
            "-m",
            "commit_meta.collect",
            "--loom",
            str(tiny_repo),
        ],
        cwd=str(tmp_path),
        env={
            "PYTHONPATH": str(PKG_ROOT)
            + os.pathsep
            + os.environ.get("PYTHONPATH", ""),
            "PATH": os.environ.get("PATH", ""),
        },
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr
    payload = json.loads(proc.stdout)
    # On a host where /root/{agentserver,modelserver,app} are missing,
    # every N/A string should embed the attempted default path.
    for field, default in [
        ("agentserver_commit", "/root/agentserver"),
        ("modelserver_commit", "/root/modelserver"),
        ("app_commit", "/root/app"),
    ]:
        if payload[field].startswith("N/A:"):
            assert default in payload[field], (
                f"{field} N/A string lost path info: {payload[field]!r}; "
                f"expected '{default}' to appear"
            )


def test_loom_defaults_to_cwd_git_root(tiny_repo: Path, tmp_path: Path) -> None:
    # No --loom => collector finds git root from cwd.
    missing = tmp_path / "missing"
    proc = _run_cli(
        "--agentserver",
        str(missing),
        "--modelserver",
        str(missing),
        "--app",
        str(missing),
        cwd=tiny_repo,
    )
    assert proc.returncode == 0, proc.stderr
    payload = json.loads(proc.stdout)
    assert not payload["loom_commit"].startswith("N/A:")
