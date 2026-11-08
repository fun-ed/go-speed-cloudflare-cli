# Changelog

## [0.2.1] - 2026-11-08

### Added

- Add Makefile commands for help, tests, race checks, coverage, vet, CI-pinned lint, formatting, and a combined `check` target.
- Add `install-local`, `verify-install`, and `version` targets for managing the installed CLI without requiring a system-wide installation.
- Document the management commands and runtime-manager overrides. Plain `make` continues to build the CLI without running tests or installing it.

Measurement behavior is unchanged from 0.2.0.

## [0.2.0] - 2026-11-08

### Added

- Show the selected Full or Lite mode for each direction, planned HTTP payload, and loaded-latency and packet-loss settings at startup.
- Include mode descriptions, traffic estimates, and command examples in `-help`.
- Show the latest completed sample's speed or latency in terminal progress, and announce each phase before its first request.
- Adapt progress to terminal width, with a compact format for narrow windows.
- Print `Test complete.` only when all required measurements succeed.
- Support local installation through `make install`, including custom prefixes.

### Changed

- Update the shared HTTP User-Agent to Chrome 157 on macOS.
- Update Pion WebRTC, ICE, TURN, terminal support, and their runtime dependencies to the latest stable versions available on the release date, within existing major versions.
- Add regression coverage for mode summaries, planned payload totals, terminal widths, disabled features, and completion status.

The phase plan, timing formulas, bandwidth P90, and NQS scoring remain unchanged. Progress counts completed, failed, and skipped planned work; it does not estimate time remaining. Lite limits individual request sizes, not total traffic.

[0.2.0]: https://github.com/fun-ed/go-speed-cloudflare-cli/compare/v0.1.0...v0.2.0
[0.2.1]: https://github.com/fun-ed/go-speed-cloudflare-cli/compare/v0.2.0...v0.2.1
