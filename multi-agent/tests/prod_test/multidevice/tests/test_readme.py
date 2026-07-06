"""README + workspace_id lifecycle tests. Covers spec §7(e) + handoff."""
from __future__ import annotations


def test_workspace_id_lifecycle_documented(multidevice_dir):
    readme = (multidevice_dir / "README.md").read_text(encoding="utf-8")
    assert "## workspace_id lifecycle" in readme, \
        "README missing '## workspace_id lifecycle' section"
    # 4-item shutdown checklist references.
    for keyword in [
        "OAuth token", "workspace_id", "cloud droplet", "Windows executor",
    ]:
        assert keyword in readme, f"README missing shutdown checklist item: {keyword!r}"


def test_readme_names_followup_worktree(multidevice_dir):
    readme = (multidevice_dir / "README.md").read_text(encoding="utf-8")
    assert "paper/v3/p3-prod-multidevice-run" in readme, \
        "README must name the follow-up worktree paper/v3/p3-prod-multidevice-run"
