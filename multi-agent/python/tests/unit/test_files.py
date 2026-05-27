"""Unit tests for inputs/outputs placeholder substitution."""
import pytest

from loom.files import substitute_io_placeholders


def test_substitute_input_placeholder():
    prompt = "Read {input:data} and process it."
    inputs = {"data": "/loom/scratch/abc/data.csv"}
    out = substitute_io_placeholders(prompt, inputs=inputs, outputs={})
    assert out == "Read /loom/scratch/abc/data.csv and process it."


def test_substitute_output_placeholder():
    prompt = "Write summary to {output:report}."
    outputs = {"report": "/loom/scratch/abc/report.md"}
    out = substitute_io_placeholders(prompt, inputs={}, outputs=outputs)
    assert out == "Write summary to /loom/scratch/abc/report.md."


def test_substitute_both():
    prompt = "Read {input:a} write {output:b}."
    out = substitute_io_placeholders(
        prompt,
        inputs={"a": "/in/a.txt"},
        outputs={"b": "/out/b.txt"},
    )
    assert out == "Read /in/a.txt write /out/b.txt."


def test_substitute_missing_input_raises():
    prompt = "Read {input:missing}."
    with pytest.raises(KeyError, match="missing"):
        substitute_io_placeholders(prompt, inputs={}, outputs={})


def test_substitute_no_placeholders_passes_through():
    prompt = "no placeholders here"
    assert substitute_io_placeholders(prompt, inputs={}, outputs={}) == prompt
