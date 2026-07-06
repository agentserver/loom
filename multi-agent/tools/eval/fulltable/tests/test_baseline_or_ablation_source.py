"""Spec §7 (g) — baseline_or_ablation comes from matrix, never CLI."""
from __future__ import annotations

import os
import subprocess
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT


def _run(args, env=None):
    full_env = os.environ.copy()
    if env:
        full_env.update(env)
    return subprocess.run(
        ["bash", str(FULLTABLE_DIR / "run.sh"), *args],
        cwd=str(MODULE_ROOT), capture_output=True, text=True, env=full_env,
    )


def test_run_sh_has_no_configuration_override_flag():
    """--help / --usage list must not advertise a --configuration or
    --baseline-name override."""
    proc = _run(["--help"])
    assert "--configuration" not in proc.stderr + proc.stdout
    # run.sh does support --baseline-name? No — matrix owns the value.
    assert "--baseline-name" not in proc.stderr + proc.stdout


def test_unknown_flag_configuration_exits_nonzero():
    proc = _run(["--configuration", "NoAcceptanceGate"])
    assert proc.returncode != 0
    assert "unknown flag" in proc.stderr or "configuration" in proc.stderr


def test_dry_run_ablation_value_comes_from_matrix():
    """Each --ablation NAME in the dry-run must appear in matrix.yaml
    as a `baseline_or_ablation` value for that same row's workload_id."""
    import yaml
    matrix = yaml.safe_load((FULLTABLE_DIR / "matrix.yaml").read_text())
    matrix_pairs = {(r["workload_id"], r["baseline_or_ablation"]) for r in matrix}

    proc = _run(["--dry-run"])
    for line in proc.stdout.splitlines():
        if "--ablation" not in line:
            continue
        tokens = line.split()
        ablation_idx = tokens.index("--ablation")
        ablation_name = tokens[ablation_idx + 1]
        workload_idx = tokens.index("--workload")
        workload_id = tokens[workload_idx + 1]
        assert (workload_id, ablation_name) in matrix_pairs, (
            f"planned --ablation {ablation_name} for workload "
            f"{workload_id} not in matrix.yaml"
        )


def test_typo_in_matrix_still_rejected_by_schema():
    """Second defence: schema test lives in test_matrix_schema; this
    test just cross-links the two defences under §7 (g)."""
    import json
    import jsonschema
    import yaml
    schema = json.loads((FULLTABLE_DIR / "matrix.schema.json").read_text())
    matrix = yaml.safe_load((FULLTABLE_DIR / "matrix.yaml").read_text())
    # OK when unmodified.
    jsonschema.validate(matrix, schema)
    matrix[6]["baseline_or_ablation"] = "NoAccetpanceGate"
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(matrix, schema)
