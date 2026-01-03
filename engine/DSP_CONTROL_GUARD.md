# DSP Control Guard (v0.2.45)

This release adds an operator-visible guard to prevent sending DSP control commands
when DSP health is DISCONNECTED.

UI behavior:
- Blocks control attempts locally and warns operator
- Offers 'Test DSP Now' to confirm link

Future hardening (optional):
- Mirror the guard on the engine API side for defense-in-depth.

No automatic reconnect logic.
