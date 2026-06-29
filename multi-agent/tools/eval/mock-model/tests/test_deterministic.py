"""RED tests for mock_model.deterministic — hash-stable replies for reproducibility."""

from __future__ import annotations

from mock_model.deterministic import canonical_json, deterministic_content


def test_same_input_returns_same_content() -> None:
    """Same model + same messages must produce byte-identical content twice."""
    model = "mock-glm-5.2"
    messages = [{"role": "user", "content": "hello world"}]

    a = deterministic_content(model, messages)
    b = deterministic_content(model, messages)

    assert a == b
    assert a.startswith("MOCK[")
    assert a.endswith("]")
    # 16-hex hash slice between brackets
    inner = a[len("MOCK[") : -1]
    assert len(inner) == 16
    assert all(c in "0123456789abcdef" for c in inner)


def test_different_model_changes_content() -> None:
    """Differing model id must change the hash."""
    messages = [{"role": "user", "content": "hi"}]
    a = deterministic_content("mock-glm-5.2", messages)
    b = deterministic_content("mock-gpt-5.5", messages)
    assert a != b


def test_different_messages_change_content() -> None:
    """Differing message content must change the hash."""
    model = "mock-glm-5.2"
    a = deterministic_content(model, [{"role": "user", "content": "hi"}])
    b = deterministic_content(model, [{"role": "user", "content": "bye"}])
    assert a != b


def test_canonical_json_orders_keys() -> None:
    """canonical_json must sort keys so dict insertion order can't shift the hash."""
    a = canonical_json({"b": 1, "a": 2})
    b = canonical_json({"a": 2, "b": 1})
    assert a == b
    # Sanity: actually sorted
    assert a == '{"a":2,"b":1}'


def test_canonical_json_nested_orders_keys() -> None:
    """Sorting must recurse into nested dicts (Pydantic message dicts are nested)."""
    a = canonical_json({"outer": {"z": 1, "a": 2}, "alpha": 0})
    b = canonical_json({"alpha": 0, "outer": {"a": 2, "z": 1}})
    assert a == b


def test_canonical_json_no_whitespace() -> None:
    """Compact separators — whitespace must not creep into the hash input."""
    s = canonical_json({"a": [1, 2, {"b": "c"}]})
    assert " " not in s
    assert "\n" not in s
