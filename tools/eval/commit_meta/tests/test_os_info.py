"""OS info collection tests.

We assert structural properties, not the exact strings of the host kernel —
tests must stay green across Arch / Ubuntu / macOS CI runners.
"""

from __future__ import annotations

from commit_meta.os_info import collect_os_info


def test_returns_kernel_distro_arch() -> None:
    info = collect_os_info()
    assert set(info.keys()) == {"kernel", "distro", "arch"}
    # All three are strings; none should be empty even when /etc/os-release
    # is missing (fallback path must still populate distro).
    for key, value in info.items():
        assert isinstance(value, str), f"{key} must be str, got {type(value)}"
        assert value, f"{key} must be non-empty"


def test_kernel_includes_uname_release(tmp_path, monkeypatch) -> None:
    # Whatever the host runs, the kernel string must include the release.
    import platform

    info = collect_os_info()
    assert platform.release() in info["kernel"]


def test_distro_fallback_when_os_release_missing(tmp_path, monkeypatch) -> None:
    # Point _OS_RELEASE_PATH at a non-existent file to force fallback.
    monkeypatch.setattr(
        "commit_meta.os_info._OS_RELEASE_PATH", str(tmp_path / "nope.txt")
    )
    info = collect_os_info()
    # Fallback should still produce *something* (platform.system() at minimum).
    assert info["distro"]
