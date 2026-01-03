# Manual DSP Test (v0.2.44)

This document describes the manual DSP connectivity test.

Behavior:
- Triggered only by operator action
- Single request / response cycle
- Enforced timeout (short, conservative)
- On success: update last_ok_timestamp and health state
- On failure: increment failure count and record error
- Append state change to DSP health timeline if applicable

No loops, no retries, no automation.
