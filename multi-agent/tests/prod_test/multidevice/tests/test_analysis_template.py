"""analysis_template.md tests. Covers spec §6 + §7(i)."""
from __future__ import annotations


_SCOPE_LINES = [
    "本文件为 paper_outputs/evaluation_v3.md §6.6 外部效度提供依据。",
    "不产 §6.3 主实验数字。",
    "不产 §6.5 消融数字。",
    "核心 metric 仅抽 TaskSuccessRate / TimeToCompletion / WrongContextFailureRate / RoutingAccuracy 四个上表；divergent-unexplained 行数 = 0 是通过外部效度的硬门槛。",
]

_METRIC_SECTIONS = [
    "## TaskSuccessRate abs_diff > 5pp: <workload>",
    "## TimeToCompletion rel_diff > 100%: <workload>",
    "## WrongContextFailureRate abs_diff > 5pp: <workload>",
    "## RoutingAccuracy abs_diff > 5pp: <workload>",
]

_ROOT_CAUSES = [
    "OAuth round-trip",
    "真 tunnel",
    "跨机 RTT",
    "云 sandbox 冷启动",
]


def test_analysis_template_scope_declaration_lines(analysis_template_path):
    text = analysis_template_path.read_text(encoding="utf-8")
    for line in _SCOPE_LINES:
        assert line in text, f"missing scope line: {line!r}"


def test_analysis_template_metric_sections(analysis_template_path):
    text = analysis_template_path.read_text(encoding="utf-8")
    for section in _METRIC_SECTIONS:
        assert section in text, f"missing metric section: {section!r}"


def test_analysis_template_required_root_causes(analysis_template_path):
    text = analysis_template_path.read_text(encoding="utf-8")
    for cause in _ROOT_CAUSES:
        assert cause in text, f"missing root-cause candidate: {cause!r}"


def test_analysis_template_disclaimer_names_followup(analysis_template_path):
    text = analysis_template_path.read_text(encoding="utf-8")
    assert "p3-prod-multidevice-run" in text, \
        "disclaimer must name the follow-up worktree paper/v3/p3-prod-multidevice-run"


def test_analysis_template_2device_impact_section(analysis_template_path):
    """Spec §2.2 / plan Step 6: analysis_template.md ships the
    '## 降级 2 设备 影响段' section unconditionally."""
    text = analysis_template_path.read_text(encoding="utf-8")
    assert "## 降级 2 设备 影响段" in text, \
        "missing '## 降级 2 设备 影响段' fallback section"
    # Both dropped-axis impact sentences appear.
    assert "无 cross-OS coverage" in text, \
        "missing 'no cross-OS coverage' impact sentence"
    assert ("无 cross-internet tunnel coverage" in text
            or "cross-internet" in text), \
        "missing 'no cross-internet tunnel coverage' impact sentence"
