# DSP Health Timeline (v0.2.43)

This release adds the concept of a DSP health timeline.

Implementation expectations:
- Append a JSONL record when DSP health state changes.
- Keep the file bounded to a fixed max lines (e.g., 200).
- Expose a read-only API endpoint that returns the recent timeline.
- UI renders it under Engineering → DSP section.

Visibility-only: no automation changes.
