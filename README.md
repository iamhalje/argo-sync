# argo-sync

A TUI tool for **bulk operations** on Argo CD Applications across multiple Argo CD contexts.

## TUI preview

![TUI preview](docs/tui.gif)

## Usage

Run:

```bash
./argo-sync
```

Keys (TUI):

- **Step 1 (apps)**: `d` opens diff view for the highlighted app (select cluster → resources → patch/details).

Useful flags:

- **`--config`**: path to Argo CD config (default: `~/.config/argocd/config`)
- **`--contexts`**: comma-separated context allowlist (if empty — all contexts)
- **`--parallel`**: parallelism limit for targets
- **`--no-wait`**: do not wait for sync to finish
- **`--wait-timeout`**, **`--poll-interval`**: wait/poll settings for sync

## Troubleshooting / Debug

If something doesn’t work (API/auth/timeouts), run with debug logs:

```bash
./argo-sync --debug --log-file ./argo-sync.json
```

## Roadmap

- **Argo CD v3**: the tool uses the Argo CD gRPC API and **auto-detects server version** via `VersionService` (best-effort). The core operations used by `argo-sync` (list/get/refresh/sync) are compatible with both Argo CD v2 and v3. Future improvements can add v3-only features (e.g. server-side diff) when the server is v3+.
