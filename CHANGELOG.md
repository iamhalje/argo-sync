# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.0.2] - 2026-01-10

### Added
- **Argo CD server version auto-detection** (major v2/v3) via gRPC `VersionService/Version`, with an HTTP fallback to `GET /api/version` (also supports subpath installs via `grpc-web-root-path`).
- **Argo CD version display in the TUI**: shown next to each context in the cluster selection step and in per-app cluster details.

### Changed
- **Sync behavior when an operation is already in progress**: if `wait` is enabled and the server reports “another operation is already in progress”, the tool switches to waiting for completion instead of failing immediately.
- **Status UX after a run**: cached inventory is updated from observed operation status so returning to Step 1 shows fresher app states without requiring a full refresh.

### Fixed
- **Incorrect success reporting in bulk runs**: targets are no longer marked `success` when the underlying API call failed.

## [v0.0.1] - 2025-12-28

- Initial tagged release.

