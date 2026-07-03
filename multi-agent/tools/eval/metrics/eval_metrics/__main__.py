"""Entry point for `python -m eval_metrics ...`.

Delegates to eval_metrics.cli.main so both `python -m eval_metrics`
and the `eval-metrics` console script from pyproject.toml go through
the same argparse pipeline.
"""

from eval_metrics.cli import main

if __name__ == "__main__":  # pragma: no cover — trivial passthrough
    raise SystemExit(main())
