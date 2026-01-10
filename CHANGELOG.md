# Changelog
Short and simple notes about what changed between releases.

## [v0.0.2] - 2026-01-10

### New
- Auto-detect **Argo CD server version** (v2 vs v3) using gRPC, with a fallback to HTTP `GET /api/version` (also works when Argo CD is served under a subpath).
- Show the detected **version next to each context** in the TUI (cluster selection + per-app cluster details).

### Improved
- If you start a sync while another sync is already running, and **wait** is enabled, the tool will **wait for the ongoing operation** instead of failing immediately.
- After a run finishes, the app list shows **fresher statuses** when you go back to Step 1 (no mandatory manual refresh).

### Fixes
- Bulk runs no longer mark targets as **success** when the underlying API call actually failed.

## [v0.0.1] - 2025-12-28

- First tagged release.

