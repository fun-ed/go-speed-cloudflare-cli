# Toolchain and packaging

- Go tools run in `src/`; use its committed module instead of creating a root module. `src/go.mod` is the required Go version and dependency authority; CI reads it and Docker uses Go1.26 builders.
- Measurement HTTP uses the standard library, WebRTC uses Pion. TURN is also imported by the real local integration fixture. Keep manifests/checksums deliberate; preserve CGO-disabled release builds.
- Docker copies the nested module into `/app`. Runtime targets are `final-alpine` and default `final-slim`, both with CA roots. They use CMD, not ENTRYPOINT; overriding arguments requires `./main` explicitly.
- GoReleaser runs from `src/` with `--config ../.goreleaser.yml`; main `.` is relative to that module. Version is injected into the variable `main.version` through `-X`, not by source rewrites.
- QA workflow covers branch pushes and PRs. Tag releases test before packaging and do not commit/push source. Current action/tool versions remain configuration facts, not memory guarantees.
- Local container/snapshot QA must not publish. Source validation does not prove a GitHub-hosted workflow or release ran.
