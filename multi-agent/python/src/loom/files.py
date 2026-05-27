"""inputs/outputs placeholder substitution + Blob abstraction.

User writes prompts with {input:name} / {output:name} placeholders. The library
allocates unique slave-side scratch paths per task and substitutes them in
before sending the prompt. Blob wraps file content returned by read_file.
"""
from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

_PH_INPUT = re.compile(r"\{input:([^}]+)\}")
_PH_OUTPUT = re.compile(r"\{output:([^}]+)\}")


def substitute_io_placeholders(
    prompt: str,
    *,
    inputs: dict[str, str],
    outputs: dict[str, str],
) -> str:
    """Replace {input:NAME} / {output:NAME} placeholders with slave-side paths.

    Raises KeyError if a placeholder references an undeclared name.
    """
    def sub_input(m: re.Match) -> str:
        name = m.group(1)
        if name not in inputs:
            raise KeyError(f"{{input:{name}}} referenced but not in inputs={list(inputs)}")
        return inputs[name]

    def sub_output(m: re.Match) -> str:
        name = m.group(1)
        if name not in outputs:
            raise KeyError(f"{{output:{name}}} referenced but not in outputs={list(outputs)}")
        return outputs[name]

    s = _PH_INPUT.sub(sub_input, prompt)
    s = _PH_OUTPUT.sub(sub_output, s)
    return s


@dataclass
class Blob:
    """File content returned by Workflow.read_file."""

    data: bytes
    slave_path: str = ""

    def bytes(self) -> bytes:
        return self.data

    def text(self, encoding: str = "utf-8") -> str:
        return self.data.decode(encoding)

    def save_to(self, local_path: str | Path) -> str:
        p = Path(local_path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(self.data)
        return str(p)
