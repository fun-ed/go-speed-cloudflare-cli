# Repository guidelines

## Project and source of truth

Go CLI for Cloudflare HTTP speed tests, UDP-relayed WebRTC message loss, and Network Quality Score experience categories. Read `specs/speedtest.md` before changing measurements or scoring; it pins the upstream methodology and explains native-client differences. Read `README.md` for supported flags and traffic estimates.

The single `package main` and Go module live in `src/`. Run Go tooling there, not at the repository root. There is no service, frontend, persistent storage, or configuration-file layer. Preserve unrelated local `.serena/` tooling state.

## Architecture and measurement contracts

- `main.go` owns flags, metadata, interleaved phase orchestration, result availability, exit codes, and terminal UI. `runCLIContext` provides narrow local-test configuration. Version is a variable so releases can inject it with `-X main.version`.
- `measurement.go` owns the shared HTTP transport, trace timing, bounded retries, phase sampling, and metric reducers. Timing starts at `WroteHeaders`, first-response-byte timing ends at `GotFirstResponseByte`. Upload uses TTFB, while download adds corrected ping and response-body duration. Preserve this difference.
- Positive-size downloads require exactly the requested payload. HTTP errors and read/close errors are not samples. Browser transfer-size accounting is unavailable, so throughput uses validated payload bytes multiplied by 1.005. Do not describe native results as byte-identical browser measurements.
- Final bandwidth is pooled cross-size P90 with a 10 ms minimum duration. Per-size medians are supplemental. Finish decisions occur after a complete phase and retain successful slow samples. Loaded probes run concurrently, are canceled and joined at phase end, and use eligible size groups with a 250 ms minimum transfer duration.
- `packetloss.go` creates two Pion WebRTC peers with UDP-only TURN relay routing and an unordered data channel with zero message retransmissions. It measures application-message loss, not independent directional IP-packet loss. Credentials and connection failure are unavailable results, not measured zero or 100% loss.
- `scores.go` implements the pinned streaming/gaming/RTC point tables. Missing packet loss contributes neutral zero points; measured zero loss earns positive points. Native scoring requires both loaded directions, rejects nonfinite inputs, and does not reproduce upstream NaN/null bonuses or normalize to 100.
- `stats.go` provides finite-safe pure statistics. Type-7 quartiles interpolate at `(n-1)*percentile` without mutating input. Preserve temporal sample order for jitter. Empty helper return values are not proof a metric exists; production reducers expose availability separately.
- `color.go` contains pure ANSI wrappers. The UI controls terminal detection, `NO_COLOR`, and progress clearing. Results use stdout; warnings and progress use stderr. Redirected streams must not gain escape codes.

## Development and verification

From `src/`:

```sh
go mod download
go build -o /tmp/go-speed-cloudflare-cli .
go test ./...
go test -race ./...
go test -cover ./...
go vet ./...
gofmt -l *.go
golangci-lint run --timeout=5m
/tmp/go-speed-cloudflare-cli -version
/tmp/go-speed-cloudflare-cli -help
```

Use `gofmt` on edited files. Tests use the standard `testing` package, table-driven cases, floating-point tolerances, local HTTP fixtures, fake sessions, and a real local TURN server. Source tests do not contact Cloudflare. Race checks are required for loaded-probe or WebRTC callback changes. Preserve cleanup and timeout tests on error paths. The old stale P90 expectations have been corrected to match the interpolation formula.

## Packaging

Read `.github/README.md` before changing CI or releases, then verify against workflow YAML. CI reads Go from `src/go.mod`. Docker builders use Go 1.26, with `final-alpine` and default `final-slim` runtime targets. Images use `CMD`, so include the executable when overriding arguments:

```sh
docker build --target final-alpine -t go-speed-cloudflare-cli:local .
docker run --rm go-speed-cloudflare-cli:local ./main -version
```

Run GoReleaser from `src/` with `--config ../.goreleaser.yml`; its `main: .` refers to the nested module. Release CI tests before packaging, injects the tag version through the linker, and does not rewrite or push source. Local QA must not publish images or release artifacts.

## Runtime boundaries

Live runs disclose public IP/location and consume real bandwidth. Full scheduled payload is about 1.266 GB before retries; lite is a per-request-size limit, not a total traffic budget. Lone `-lite-download` or `-lite-upload` selects its direction. Loaded latency and packet loss default on; disable explicitly for isolated live HTTP checks. TURN needs outbound UDP and temporary credentials. Never record credential responses, tokens, cookies, or public IPs in documentation or memory.

Metadata failure alone is a warning. Actual measurement failure produces exit 1 while later independent phases continue. Invalid arguments exit 2; cancellation exits 130. N/A from eligibility filtering or missing derived-score inputs is not a fabricated zero. Dependency manifests are authoritative; native HTTP uses the standard library and WebRTC uses Pion. Keep dependency changes deliberate and preserve the CGO-disabled release path.
