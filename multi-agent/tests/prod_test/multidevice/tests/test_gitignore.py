"""gitignore invariant tests. Covers spec §7(a)."""
from __future__ import annotations

import pathlib


# 6 invariant paths: 2 tracked + 4 ignored.
_INVARIANTS = [
    ("tests/prod_test/multidevice/laptop.yaml.template", 1),
    ("tests/prod_test/multidevice/wrappers/laptop_up.sh", 1),
    ("tests/prod_test/multidevice/tokens/x.yaml", 0),
    ("tests/prod_test/multidevice/x.token", 0),
    ("tests/prod_test/multidevice/subdir/x.pem", 0),
    ("tests/prod_test/multidevice/tokens.yaml", 0),
]

# Extra invariants: pycache / .pyc / .pytest_cache are ignored (they
# are generated at test-time and must never be tracked, else the
# secret scanner would trip on their bytecode). Regression guard for
# the round-3 code review finding.
_PYCACHE_IGNORED = [
    "tests/prod_test/multidevice/__pycache__/secretscrub_python.cpython-314.pyc",
    "tests/prod_test/multidevice/tests/__pycache__/test_gitignore.cpython-314-pytest-9.0.2.pyc",
    "tests/prod_test/multidevice/.pytest_cache/CACHEDIR.TAG",
]


def test_gitignore_tokens(multi_agent_root, git_runner):
    for rel, expected_rc in _INVARIANTS:
        proc = git_runner(["check-ignore", rel], cwd=multi_agent_root)
        assert proc.returncode == expected_rc, (
            f"git check-ignore {rel!r}: rc={proc.returncode}, "
            f"expected {expected_rc}; stdout={proc.stdout!r}"
        )


def test_pycache_ignored(multi_agent_root, git_runner):
    """Regression: __pycache__ / *.pyc / .pytest_cache under multidevice/
    MUST be gitignored. Otherwise the secret scanner trips on the
    bytecode of secretscrub_python.pyc (which contains the pattern
    strings) when the module has been imported."""
    for rel in _PYCACHE_IGNORED:
        proc = git_runner(["check-ignore", rel], cwd=multi_agent_root)
        assert proc.returncode == 0, (
            f"git check-ignore {rel!r}: expected ignored (rc=0), "
            f"got rc={proc.returncode}"
        )


def test_multidevice_dir_no_secrets(multidevice_dir):
    """ERE grep from spec §7(a) — no real tokens in multidevice/.

    The placeholder `<OAUTH_TOKEN_HERE_DO_NOT_COMMIT>` does NOT contain
    any of the listed token prefixes, so it is fine. secretscrub_python.py
    itself is a scanner-source file; matches inside it would be from the
    regex list documentation, not from real secrets — exclude it.
    """
    import subprocess

    # Excludes: files that legitimately quote the token prefixes as
    # part of the scanner infrastructure itself, not as leaked secrets:
    #   - secretscrub_python.py: the regex list
    #   - lint.sh: the CI static-check driver that runs the same grep
    #   - tests/ subdir: the pytest module whose test names + assertion
    #     strings mention the prefixes to prove the scanner catches
    #     them (all matches are inside quoted regex strings or
    #     assertion messages, not on-disk secrets that could be
    #     committed as OAuth material)
    proc = subprocess.run(
        [
            "grep", "-REn",
            "--exclude=secretscrub_python.py",
            "--exclude=lint.sh",
            "--exclude-dir=tests",
            "sk-|ghp_|AKIA|xoxb|Bearer[[:space:]]+[A-Za-z0-9]|refresh_token",
            str(multidevice_dir),
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode == 0 and proc.stdout.strip():
        raise AssertionError(f"secret-scan hit in multidevice/:\n{proc.stdout}")
    # rc=1 = no match found (grep). rc=0 = match found (we assert no
    # stdout). Any other rc is a real error.
    assert proc.returncode in (0, 1), f"grep failed rc={proc.returncode}: {proc.stderr!r}"


def test_multidevice_dir_no_secrets_negative_case(tmp_path, multidevice_dir):
    """Positive control: inject a fake `sk-` into a scratch file inside
    a temp dir shaped like multidevice/, prove the same grep finds it.

    We do NOT drop the fake token into the real multidevice/ tree
    because that would trip test_multidevice_dir_no_secrets in the same
    session.
    """
    import subprocess

    fake_dir = tmp_path / "multidevice_fake"
    fake_dir.mkdir()
    (fake_dir / "leaked.yaml").write_text(
        "oauth_placeholder: sk-abc12345notrealbutlookslike\n"
    )
    proc = subprocess.run(
        [
            "grep", "-REn",
            "sk-|ghp_|AKIA|xoxb|Bearer[[:space:]]+[A-Za-z0-9]|refresh_token",
            str(fake_dir),
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, "scanner failed to find injected sk- token"
    assert "leaked.yaml" in proc.stdout, f"scanner output missing file: {proc.stdout!r}"
