# Toolchain and packaging

- Go tools run in `src/`; use its committed module instead of creating a root module. `src/go.mod` is the required Go version and dependency authority; CI reads it and Docker uses Go1.26 builders.
- Measurement HTTP uses the standard library, WebRTC uses Pion. TURN is also imported by the real local integration fixture. Keep manifests/checksums deliberate; preserve CGO-disabled release builds.
- Docker copies the nested module into `/app`. Runtime targets are `final-alpine` and default `final-slim`, both with CA roots. They use CMD, not ENTRYPOINT; overriding arguments requires `./main` explicitly.
- GoReleaser runs from `src/` with `--config ../.goreleaser.yml`; main `.` is relative to that module. Version is injected into the variable `main.version` through `-X`, not by source rewrites.
- QA workflow covers branch pushes and PRs. Tag releases test before packaging and do not commit/push source. Current action/tool versions remain configuration facts, not memory guarantees.
- Local container/snapshot QA must not publish. Source validation does not prove a GitHub-hosted workflow or release ran.
- The publication repository is `fun-ed/go-speed-cloudflare-cli` on `master`, using the `fun-ed` remote. `origin` points to the older upstream and is not the release target. Verify live remote configuration before publishing.
- Read the linter version from `.github/workflows/test.yml` when checking a release. A newer local linter can add diagnostics; a clean diff-only lint does not prove the CI-pinned full lint passes.
- Source installs use the default version in `main.go`, while release assets inject the tag version. Keep source version, changelog, README upgrade instructions and release tag aligned. Verify CI, published assets and the installed executable separately.
