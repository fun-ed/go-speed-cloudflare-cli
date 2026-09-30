# Completion gates

- Verify actual changed behavior with local HTTP/CLI fixtures and the local TURN integration, then run Go tests, race checks, vet, formatting inspection and build from `src/`. Source tests do not call Cloudflare; dependency downloads can need network access.
- Race-check loaded-probe lifecycle and WebRTC callbacks. Cover setup/sending/receiving cancellation, bounded retries, failure cleanup, duplicate IDs and final100%loss snapshots. Do not interpret transport/setup failure as a measured loss ratio.
- Validate offline help/version and real output availability/exit status. Empty/nonfinite samples must not look healthy. Derived NQS can legitimately remain N/A when both loaded directions are unavailable.
- The stale synthetic P90 expectations were corrected to Type7; preserve the formula rather than rounding before aggregation. Record actual verification, not historical coverage or a permanent failing baseline.
- When relevant, validate GoReleaser from src and each container runtime separately. Local builds do not prove published artifacts or remote CI execution. Never publish as part of local QA.
- Run live scenarios only when needed, choose explicit direction/lite options and protect IP/location/credential output. Report an unreachable credential endpoint or blocked UDP as an external runtime limitation, not a successful loss measurement.
- Finish with repository-root `git diff --check` and `git status --short`, preserving unrelated local state. Report unavailable tools and actual failures without inventing success.
