# Server-side DSP Control Guard (v0.2.46)

This release adds defense-in-depth: the engine blocks RC control writes when DSP health is DISCONNECTED.

Endpoints:
- GET /api/dsp/health  (read-only)
- POST /api/dsp/test   (manual, single-shot connectivity test)

Guard:
- /api/rc/... checks Engine.DSPControlAllowed() and returns HTTP 409 if not allowed.

Notes:
- Health changes only when /api/dsp/test is called (no polling).
- Connectivity test is a TCP connect only (protocol-agnostic).
