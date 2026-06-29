"""CLI entrypoint: ``python -m commit_meta.collect [--loom ...] [--format ...]``.

Default search order for each repo path (first hit wins):

  loom         : --loom flag → git root of cwd → /root/multi-agent
  agentserver  : --agentserver flag → $AGENTSERVER_ROOT → /root/agentserver
  modelserver  : --modelserver flag → $MODELSERVER_ROOT → /root/modelserver
  app          : --app flag → $APP_ROOT → /root/app

Anything not found is recorded as ``"N/A: not present at <path>"`` — the
collector never panics on a missing repo.
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import sys
import time
from typing import Optional

import yaml

from commit_meta.git_rev import find_git_root, get_commit
from commit_meta.os_info import collect_os_info
from commit_meta.schema import CommitMetaSchema, OSInfo

# Hard-coded fallbacks for the default loom layout.
_DEFAULTS = {
    "loom": "/root/multi-agent",
    "agentserver": "/root/agentserver",
    "modelserver": "/root/modelserver",
    "app": "/root/app",
}


def _resolve_loom(cli_value: Optional[str]) -> Optional[str]:
    if cli_value:
        return cli_value
    # Try git root of cwd before falling back to /root/multi-agent.
    root = find_git_root(os.getcwd())
    if root:
        return root
    return _DEFAULTS["loom"] if os.path.isdir(_DEFAULTS["loom"]) else None


def _resolve(cli_value: Optional[str], env_var: str, default: str) -> Optional[str]:
    if cli_value:
        return cli_value
    env = os.environ.get(env_var)
    if env:
        return env
    return default if os.path.isdir(default) else None


def _build(args: argparse.Namespace) -> CommitMetaSchema:
    loom_path = _resolve_loom(args.loom)
    agent_path = _resolve(args.agentserver, "AGENTSERVER_ROOT", _DEFAULTS["agentserver"])
    model_path = _resolve(args.modelserver, "MODELSERVER_ROOT", _DEFAULTS["modelserver"])
    app_path = _resolve(args.app, "APP_ROOT", _DEFAULTS["app"])

    os_info = collect_os_info()

    return CommitMetaSchema(
        loom_commit=get_commit(loom_path),
        agentserver_commit=get_commit(agent_path),
        modelserver_commit=get_commit(model_path),
        app_commit=get_commit(app_path),
        os=OSInfo(**os_info),
        collected_at_unix=int(time.time()),
        machine_hostname=socket.gethostname(),
    )


def _format(schema: CommitMetaSchema, fmt: str) -> str:
    if fmt == "yaml":
        # yaml.safe_dump is fine — every value is a primitive or dict thereof.
        return yaml.safe_dump(schema.model_dump(), sort_keys=False).rstrip("\n")
    if fmt == "json":
        return json.dumps(schema.model_dump(), indent=2)
    raise ValueError(f"unknown format: {fmt!r}")


def _parse_args(argv: Optional[list[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        prog="commit_meta.collect",
        description="Collect git commit + OS metadata for an eval run.",
    )
    parser.add_argument("--loom", help="path to the loom repo (default: cwd git root)")
    parser.add_argument(
        "--agentserver", help="path to the agentserver repo (default: $AGENTSERVER_ROOT)"
    )
    parser.add_argument(
        "--modelserver", help="path to the modelserver repo (default: $MODELSERVER_ROOT)"
    )
    parser.add_argument("--app", help="path to the app repo (default: $APP_ROOT)")
    parser.add_argument(
        "--format",
        choices=["json", "yaml"],
        default="json",
        help="output format (default: json)",
    )
    return parser.parse_args(argv)


def main(argv: Optional[list[str]] = None) -> int:
    args = _parse_args(argv)
    schema = _build(args)
    sys.stdout.write(_format(schema, args.format) + "\n")
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
