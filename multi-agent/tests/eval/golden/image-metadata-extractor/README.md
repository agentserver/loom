# image-metadata-extractor — E4 task family

> Tool produced by user-promoted固化: `image_metadata`

Extract `{width, height, format, color_mode, bytes, sha256, exif}` from an
image file. Stage A handles a single file with a one-shot script (probably
`identify` + `exiftool`); Stage B固化 wraps both into one MCP that returns
a stable schema.

The fixture images are tiny synthesized PNGs (≤ 100 bytes each) with no
real EXIF, so `expected.exif` is `null` everywhere — that keeps the repo
slim while still exercising the schema.

## Layout

| Path | Role |
|---|---|
| `first-task/`              | 1×1 LA PNG — minimal happy case |
| `reuse-1/`, `reuse-2/`, `reuse-3/` | 2×1 LA, 3×2 RGBA, 4×4 RGBA PNGs |
| `acceptance/cases.jsonl`   | Stage B固化 gate (includes a corrupt-file negative) |

## Running once the MCP exists

```bash
skills/mcp-acceptance --tool image_metadata \
  --cases tests/eval/golden/image-metadata-extractor/acceptance/cases.jsonl

tools/eval/runner --spec tests/eval/golden/image-metadata-extractor/first-task/spec.yaml
```

Oracle compares against `expected/metadata.json` ignoring `exif: null`
keys when the source has no EXIF.
