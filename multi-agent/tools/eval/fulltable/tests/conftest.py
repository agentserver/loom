"""Common test-suite paths (spec §2 layout).

Kept minimal on purpose — plan §1 forbids anything under
`multi-agent/tools/eval/runner/` being touched, so tests must consume
the fulltable package via relative-parent path only.
"""
from __future__ import annotations

import sys
from pathlib import Path

FULLTABLE_DIR = Path(__file__).resolve().parent.parent
MODULE_ROOT = FULLTABLE_DIR.parent.parent.parent  # multi-agent/
REPO_ROOT = MODULE_ROOT.parent

# Expose the fulltable package on sys.path so `import lib.*` works.
for p in (str(FULLTABLE_DIR),):
    if p not in sys.path:
        sys.path.insert(0, p)

# Expose eval_metrics so `from eval_metrics.metrics import ALL_METRICS` resolves.
_metrics_pkg_root = MODULE_ROOT / "tools" / "eval" / "metrics"
if str(_metrics_pkg_root) not in sys.path:
    sys.path.insert(0, str(_metrics_pkg_root))
