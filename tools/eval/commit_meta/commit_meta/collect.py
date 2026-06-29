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
    # Hand back the default path even if it does not exist; get_commit will
    # render a "N/A: not present at /root/multi-agent" string instead of the
    # uninformative "<unset>" we used to emit.
    return _DEFAULTS["loom"]


def _resolve(cli_value: Optional[str], env_var: str, default: str) -> Optional[str]:
    # Always return *some* path string so the resulting N/A message names
    # where we actually looked. Returning None here would erase the default
    # path and yield "N/A: not present at <unset>", which is unhelpful when
    # debugging missing-repo eval runs.
    if cli_value:
        return cli_value
    env = os.environ.get(env_var)
    if env:
        return env
    return default


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
        # Defer to schema.to_json so CLI output and the canonical schema
        # serialization can never drift; Phase 1 consumers reading the
        # artifact see exactly what from_json(schema.to_json()) produced.
        return schema.to_json()
    # argparse choices= guards against this in practice; keep the explicit
    # raise as a defense for direct callers.
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
