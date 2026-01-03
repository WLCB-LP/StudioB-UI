# DSP Health Timeline Format (v0.2.43)

This file documents the append-only DSP health timeline.

Storage: `dsp/HEALTH_TIMELINE.jsonl`

Each line is a JSON object (JSONL) with fields:
- `time` (ISO8601)
- `state` ("OK" | "DEGRADED" | "DISCONNECTED")
- `failures` (integer)
- `last_error` (string, may be empty)

The engine appends a new line only when the state changes.
The file is bounded (e.g., last 200 lines) to avoid unbounded growth.
Visibility-only: this timeline is for operators and debugging.
