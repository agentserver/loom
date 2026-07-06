"""build_prod_vs_stub.py tests. Covers spec §5 + §7(f)."""
from __future__ import annotations

import csv
import hashlib
import subprocess
import sys


def _sha256_bytes(path):
    h = hashlib.sha256()
    h.update(path.read_bytes())
    return h.hexdigest()


def test_build_prod_vs_stub_sample_columns(multidevice_dir, tmp_path):
    """--sample-mode → correct column header, at least 8 rows
    (2 workloads × 4 metrics)."""
    out = tmp_path / "prod_vs_stub_sample.csv"
    proc = subprocess.run(
        [
            sys.executable,
            str(multidevice_dir / "build_prod_vs_stub.py"),
            "--prod-dir", str(multidevice_dir / "fixtures" / "fake_prod"),
            "--stub-table", "unused-in-sample-mode",
            "--out", str(out),
            "--sample-mode",
            "--generated-at", "1970-01-01T00:00:00Z",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, f"stderr={proc.stderr!r}"
    assert out.is_file()

    # Column header check.
    text = out.read_text()
    lines = text.splitlines()
    non_meta = [l for l in lines if not l.startswith("#")]
    assert non_meta, f"no data lines: {text!r}"
    header = non_meta[0]
    assert header == "workload,metric,stub_value,prod_value,abs_diff,rel_diff_pct,verdict", \
        f"unexpected header: {header!r}"

    # At least 8 data rows.
    data_rows = non_meta[1:]
    assert len(data_rows) >= 8, f"expected ≥ 8 rows, got {len(data_rows)}: {data_rows!r}"

    # Verdict values are in the enum.
    reader = csv.DictReader([header, *data_rows])
    for row in reader:
        assert row["verdict"] in {
            "consistent", "divergent-explained", "divergent-unexplained",
        }, f"bad verdict: {row['verdict']!r}"


def test_stub_source_pinned(multidevice_dir, tmp_path):
    """Output CSV meta header stub_source_sha256 matches independent
    sha256sum of the fixture stub CSV file."""
    out = tmp_path / "sample.csv"
    subprocess.run(
        [
            sys.executable,
            str(multidevice_dir / "build_prod_vs_stub.py"),
            "--prod-dir", str(multidevice_dir / "fixtures" / "fake_prod"),
            "--stub-table", "unused-in-sample-mode",
            "--out", str(out),
            "--sample-mode",
            "--generated-at", "1970-01-01T00:00:00Z",
        ],
        check=True,
        capture_output=True,
    )
    text = out.read_text()
    sha_lines = [l for l in text.splitlines() if l.startswith("# stub_source_sha256=")]
    assert len(sha_lines) == 1, f"expected exactly one stub_source_sha256 meta line: {sha_lines!r}"
    reported_sha = sha_lines[0].split("=", 1)[1].strip()

    # In --sample-mode the fixture bundle is used.
    fixture_stub = multidevice_dir / "fixtures" / "fake_stub_table.csv"
    actual_sha = _sha256_bytes(fixture_stub)
    assert reported_sha == actual_sha, \
        f"stub sha meta {reported_sha} != actual {actual_sha}"


def test_stub_source_allowlist_reject(multidevice_dir, tmp_path):
    """Passes a non-allowlisted --stub-table path; expect exit 2 +
    stderr saying 'not in allow-list'."""
    fake_stub = tmp_path / "fake.csv"
    fake_stub.write_text("workload_id,configuration,TaskSuccessRate,run_count\nfoo,full,1.0,1\n")
    out = tmp_path / "should_not_be_written.csv"

    proc = subprocess.run(
        [
            sys.executable,
            str(multidevice_dir / "build_prod_vs_stub.py"),
            "--prod-dir", str(multidevice_dir / "fixtures" / "fake_prod"),
            "--stub-table", str(fake_stub),
            "--out", str(out),
            "--generated-at", "1970-01-01T00:00:00Z",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 2, f"expected exit 2; got {proc.returncode}"
    assert "not in allow-list" in proc.stderr.lower() or "allow-list" in proc.stderr.lower(), \
        f"expected allow-list rejection in stderr: {proc.stderr!r}"
    assert not out.exists(), "output file should not be written on reject"
