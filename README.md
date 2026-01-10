# argo-sync

A TUI tool for **bulk operations** on Argo CD Applications across multiple Argo CD contexts.

## TUI preview

![TUI preview](docs/tui.gif)

## Usage

Run:

```bash
./argo-sync
```

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
