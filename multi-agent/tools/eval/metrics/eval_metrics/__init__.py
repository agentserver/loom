"""eval_metrics — WT-2-metric-extract paper-metric extractor.

See docs/specs/wt2-metric-extract.spec.md for the full contract.
This package exports nothing at the top level; callers use the CLI
via `python -m eval_metrics` or the `eval-metrics` entry point, or
import `eval_metrics.cli`, `eval_metrics.db`, `eval_metrics.filter`,
etc. directly (documented as internal APIs — the CLI is the only
supported user surface).
"""

__version__ = "0.1.0"
