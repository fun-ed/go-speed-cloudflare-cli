# Cloudflare speed test CLI

`go-speed-cloudflare-cli` measures latency, jitter, download speed, upload speed, and packet loss using Cloudflare's `speed.cloudflare.com` service. It also calculates Cloudflare Network Quality Score (NQS) experience categories when the required measurements are available. The current development version is 0.3.0-dev. It uses native Go HTTP and WebRTC clients, so its results are not byte-for-byte or timing-equivalent to a browser run.

See [the measurement specification](specs/speedtest.md) for the phase plan, formulas, traffic estimates, native-client limits, and pinned Cloudflare source references.

Version 0.2.0 added mode and traffic summaries, terminal progress that adapts to window width and shows the latest sample, and mode examples in `-help`. It updated dependencies and the shared User-Agent to Chrome 157. Version 0.2.1 adds Makefile commands for development checks and local installation. See the [changelog](CHANGELOG.md) for release details.

Development version 0.3.0-dev adds the interactive dashboard and Prometheus textfile output described below. The published 0.2.1 release does not include these features.

## Build and run

The Go module lives in `src/`. Use Go 1.26 or newer from a checkout:

```sh
cd src
go build -o ../go-speed-cloudflare-cli .
../go-speed-cloudflare-cli -help
../go-speed-cloudflare-cli
cd ..
```

The module and dependencies are already declared. Do not run `go mod init` or create a second module.

From the repository root, use the Makefile to install in `/usr/local/bin`:

```sh
make build
sudo make install
go-speed-cloudflare-cli -version
```

Build as your normal user before using `sudo` for the install step. If `/usr/local/bin` is writable without administrator privileges, `make install` builds and installs directly.

To install without administrator privileges, use a user-local prefix:

```sh
make install PREFIX="$HOME/.local"
```

Make sure `$HOME/.local/bin` is on `PATH`. Run `make clean` to remove the local build executable; it does not uninstall the CLI.

Use `make help` to list the management commands:

| Command | Action |
| --- | --- |
| `make check` | Check formatting, tests, race detection, vet, CI-pinned lint, and build |
| `make test`, `make race`, `make coverage` | Run tests, race checks, or coverage separately |
| `make vet`, `make lint` | Run static checks separately |
| `make fmt`, `make fmt-check` | Format Go files, or fail if formatting is needed |
| `make build`, `make version` | Build, or print the built CLI's version |
| `make install-local` | Install to `$HOME/.local/bin` without sudo |
| `make verify-install` | Show the executable resolved by PATH and its version |
| `make clean` | Remove the local build executable |

Tests use local fixtures and do not run a public speed test. Lint uses `go run` with the version pinned by this Makefile to match CI; its first run may download the tool. `GO` and `GOFMT` can select a runtime manager, for example `make GO='mise exec -- go' GOFMT='mise exec -- gofmt' check`. `make clean` does not remove downloaded tools or the installed CLI.

To install the published stable version 0.2.1 from an existing checkout and replace the installed CLI:

```sh
git fetch https://github.com/fun-ed/go-speed-cloudflare-cli.git tag v0.2.1
git switch --detach v0.2.1
make build
sudo make install
go-speed-cloudflare-cli -version
```

Keep local changes committed or saved before switching versions. The version command should print `go-speed-cloudflare-cli version 0.2.1`. Use `make install-local` instead of `sudo make install` for a user-local installation. Prebuilt archives and checksums are available on the [v0.2.1 release page](https://github.com/fun-ed/go-speed-cloudflare-cli/releases/tag/v0.2.1).

To build and run the container image from the repository root:

```sh
docker build --target final-alpine -t go-speed-cloudflare-cli:local .
docker run --rm go-speed-cloudflare-cli:local ./main -lite
```

## Interactive dashboard

```sh
go-speed-cloudflare-cli -tui -lite
```

`-tui` shows download and upload sample curves, provisional pooled P90 speeds, latency, jitter, packet loss, and Network Quality Score categories. Wide terminals use columns; narrow terminals stack the panels. Curves follow sample order, use independent vertical scales, and include only bandwidth samples eligible for the final P90. Loaded metrics update after completed phases establish eligibility. Missing values remain `N/A`.

| Key | Action |
| --- | --- |
| `Tab`, `Shift+Tab`, or `1` / `2` / `3` | Switch Overview, Measurements, and Warnings |
| Arrow up/down, Page Up/Down, Home/End | Scroll the selected panel |
| `c` | Cancel the current run and keep its partial results open |
| `r` | Start a fresh run after completion or cancellation, using the same flags |
| `q` or `Ctrl+C` | Cancel active work, wait for cleanup, and leave the dashboard |

Each rerun uses real bandwidth again. The default full/lite traffic estimates still apply. The dashboard stays open after completion; quitting restores the terminal and prints the last run's text report to stdout and warnings to stderr. Its exit code reflects that run, including 130 for cancellation.

The dashboard requires interactive stdin, stdout, and stderr. Redirected streams, `TERM=dumb`, or `-no-progress` select the existing text output instead. `-no-color` and `NO_COLOR` disable dashboard styling while retaining terminal navigation. `-help` and `-version` stay offline even with `-tui`.

## Prometheus metrics file

```sh
go-speed-cloudflare-cli -lite -metrics-file ./cloudflare-speedtest.prom
go-speed-cloudflare-cli -tui -lite -metrics-file ./cloudflare-speedtest.prom
```

The CLI writes the last run's metrics after success, measurement failure, or cancellation. Each TUI rerun replaces the same snapshot. On Unix, a temporary file in the same directory and a rename keep readers from seeing partial output. The metrics file contains the final or partial results from the same reducers as the CLI, not separate measurements.

The parent directory must already exist and be writable. The destination must end in `.prom`; existing symlinks and directories are rejected before a run starts. On Unix, new files use mode `0600`, and replacement preserves an existing file's permission bits. The replacement file belongs to the CLI process. If node_exporter runs under another account, configure file and directory read access before collecting it. Temporary files use a `.tmp` extension, so the collector ignores them.

Point the existing [node_exporter textfile collector](https://github.com/prometheus/node_exporter#textfile-collector) at the directory containing this file with `--collector.textfile.directory=/path/to/collector-directory`. Prometheus then scrapes node_exporter as usual. Schedule CLI runs separately if regular measurements are needed; every run consumes bandwidth.

All metric names start with `cloudflare_speedtest_`:

| Metric suffix | Labels | Meaning |
| --- | --- | --- |
| `bandwidth_bits_per_second` | `direction="download"` or `"upload"` | Pooled bandwidth P90 in bits/s |
| `latency_seconds` | `load="unloaded"`, `"download"`, or `"upload"` | Median latency in seconds |
| `jitter_seconds` | `load` | Consecutive-probe jitter in seconds |
| `packet_loss_ratio` | None | WebRTC message loss as a fraction from 0 to 1 |
| `network_quality_points` | `category="streaming"`, `"gaming"`, or `"rtc"` | Existing native NQS points, including its zero floor; not normalized to 100 |
| `bandwidth_available`, `latency_available`, `jitter_available`, `packet_loss_available`, `network_quality_available` | Corresponding metric labels | 1 for an available result, 0 for unavailable |
| `run_success` | None | 1 when the measurement run succeeded, otherwise 0 |
| `run_exit_code` | None | Measurement outcome: 0, 1, or 130 |
| `run_completed_timestamp_seconds` | None | Unix completion time, including failed or canceled runs |
| `build_info` | `version` | Build version with value 1 |

Unavailable numeric series are omitted rather than exported as zero, NaN, or infinity. Measured zero latency, jitter, packet loss, or NQS points remain valid zero values. Disabled measurements are omitted. Partial runs can still have available measurements, so check `run_success` before treating them as a successful full result. Labels do not include IPs, locations, URLs, credentials, or error strings. The completion time is a gauge value, not a sample timestamp, as required by the textfile collector.

Invalid output paths exit 2 before measurement. A write failure exits 1 unless the run was canceled, which keeps exit 130. Failed replacement preserves the previous snapshot; use `run_completed_timestamp_seconds` to detect stale data. Standard output and error keep their existing report and warning roles.

## Select measurements

With no direction flags, the CLI runs the full download and upload plan. The plan interleaves bandwidth and idle-latency phases. The initial 100 kB download phase always completes; later bandwidth phases for a direction stop after a complete phase when its shortest successful sample takes more than one second.

| Command | Selection |
| --- | --- |
| `go-speed-cloudflare-cli` | Full download and upload plan |
| `go-speed-cloudflare-cli -download` | Download only |
| `go-speed-cloudflare-cli -upload` | Upload only |
| `go-speed-cloudflare-cli -lite` | Both directions, request sizes no larger than 10 MB |
| `go-speed-cloudflare-cli -lite-download` | Lite download only |
| `go-speed-cloudflare-cli -lite-upload` | Lite upload only |
| `go-speed-cloudflare-cli -download -upload -lite` | Lite download and upload |
| `go-speed-cloudflare-cli -download -upload -lite-download` | Lite download and full upload |

Lite mode limits each selected direction's bandwidth requests to 10 MB or less. It does not limit total transfer volume to 10 MB. Direction flags can be combined. Without explicit direction flags, `-lite-download` and `-lite-upload` select only their named direction. With explicit directions, they limit only the named direction, which must be selected; other selected directions remain full unless limited by `-lite` or their own lite flag.

### Other flags

| Flag | Default | Description |
| --- | --- | --- |
| `-timeout duration` | `30s` | Deadline per logical HTTP measurement including 429 retry waits; also caps the whole packet-loss session |
| `-loaded-latency` | `true` | Probe latency during bandwidth phases and report loaded latency and jitter |
| `-packet-loss` | `true` | Run the WebRTC-over-UDP TURN packet-loss measurement |
| `-turn-creds-url URL` | Cloudflare `/turn-creds` | Use another endpoint that returns the flat TURN credentials JSON described below |
| `-turn-server host:port` | Cloudflare TURN server | Fallback TURN authority when the response provides neither `server` nor a `urls` field |
| `-no-progress` | `false` | Disable terminal progress output |
| `-no-color` | `false` | Disable terminal colors |
| `-tui` | `false` | Use an interactive dashboard when all streams are terminals and progress is enabled |
| `-metrics-file path` | Disabled | Replace a `.prom` snapshot after each run for a Prometheus textfile collector |
| `-version` | | Print the version without network access |
| `-help` | | Print usage without network access |

The credentials endpoint must return JSON with non-empty `username` and `credential` strings. An explicit `server` host and port takes precedence. Otherwise the CLI selects a UDP `turn:` URL from `urls`, which may be a string or an array. This supports the current public Cloudflare response. If neither field exists, it uses `-turn-server` or the default relay. TCP, TLS, and STUN alternatives cannot replace the UDP relay. Unknown fields are ignored; nested Realtime API `iceServers` wrappers are not supported. Temporary credentials and raw responses must not be logged.

Packet loss needs outbound UDP to a TURN relay. A proxy or firewall that permits HTTPS but blocks UDP can make this phase unavailable. Credential, relay, or peer setup failure does not stop the HTTP speed test, but it marks the run incomplete. The CLI never reports setup failure as measured zero packet loss.

The packet-loss measurement uses `-timeout`; shutdown may use an additional bounded two-second cleanup grace.

## Results and output

The CLI prints the detected Cloudflare location and client IP when metadata is available. Metadata failure prints a warning but does not by itself fail the run. A failed location lookup shows `Unknown city` while preserving available trace data; a failed connection trace shows `N/A` for the location and IP. The results section reports:

| Result | Meaning |
| --- | --- |
| Unloaded latency | Median of valid idle latency probes |
| Unloaded jitter | Mean absolute difference between consecutive valid idle latency probes |
| Download and upload speed (P90) | 90th percentile of eligible samples pooled across request sizes for that direction |
| Loaded latency and jitter | Idle-style probes sent during each bandwidth phase, summarized from eligible phases |
| Packet loss | Percentage of unique application messages not received during the UDP-relayed WebRTC data-channel test |
| Streaming, gaming, RTC NQS | Experience classifications calculated from available measurements; these are categories, not a 0-to-100 score |

Bandwidth samples with non-positive or non-finite speed, or a measured duration below 10 ms, do not enter the P90. Loaded-latency samples are available only for bandwidth-size groups whose shortest successful transfer lasts at least 250 ms. At most the latest 20 eligible loaded-latency probes are summarized. A missing metric or too few valid samples prints `N/A`; the CLI does not substitute zero. Disabling a feature omits its measurements.

Speed uses validated payload bytes multiplied by 1.005 as an approximation for transfer overhead. It is not the browser's exact `transferSize`. The HTTP timer starts when Go reports that it wrote request headers, not at browser navigation or DNS/TCP/TLS setup. This native timing and byte accounting can differ from Cloudflare's browser UI. Packet loss also uses Pion's native WebRTC implementation rather than a browser engine.

Progress appears on a terminal's standard error. Warnings also go to standard error; results go to standard output. Color requires standard output to be a terminal and is disabled by `-no-color` or by setting `NO_COLOR` to any value. `-no-progress` disables progress even on a terminal. In all output modes, `TERM=dumb` disables both progress control sequences and colors.

At startup, a mode summary shows Full or Lite for each selected direction, planned HTTP payload, and whether loaded latency and packet loss are enabled. The payload estimate follows the selected phase plan; early finish, retries, and protocol overhead can change actual traffic. `-help` includes mode examples.

The percentage tracks planned work units: metadata requests, HTTP sample attempts, and one packet-loss session. Completed, failed, and skipped work all advance it. It is not an estimate of remaining time or transferred bytes.

The progress line adapts to terminal width, announces each phase before its first request, and shows the latest completed sample's speed or latency. `last` is a single sample, not the final pooled P90. During packet loss it shows relay setup followed by the number of messages sent. A request or relay setup may wait until its deadline without changing the percentage. `Test complete.` appears only when required measurements succeed.

## Planned HTTP payload

The following decimal byte totals cover one scheduled request for every bandwidth sample before early finish behavior. They count bytes downloaded and uploaded, not retransmissions or transport overhead.

| Mode | Downloaded payload | Uploaded payload | Combined payload |
| --- | ---: | ---: | ---: |
| Full | 969 MB | 296.8 MB | 1,265.8 MB |
| Lite, both directions | 69 MB | 46.8 MB | 115.8 MB |

The values are planned totals, not a hard cap. Early finish can reduce them. A 429 response can cause up to three retries after the original attempt; an upload body may have crossed the network before a 429 response. Metadata, idle-latency probes, TURN/WebRTC traffic, and HTTP/TCP/TLS/IP overhead are not included.

## Exit status

| Code | Meaning |
| ---: | --- |
| `0` | The run completed with its required measurements |
| `1` | A measurement failed or a required result was unavailable |
| `2` | Invalid flags or arguments |
| `130` | The run was interrupted or its context was canceled |

The CLI retries an HTTP 429 up to three times, honoring `Retry-After` as seconds or an HTTP date. A missing or unparsable value uses a five-second delay; a negative delay or expired date causes no wait. HTTP requests and TURN packet-loss sessions have deadlines, and interruption cancels pending work. A timeout or exhausted retries make the affected measurement unavailable rather than producing a fabricated speed.

## References

HTTP test behavior and NQS formulas are documented against Cloudflare speedtest 1.14.1 at commit [`323da2ea5697ab4953f2c90c931125ac35d019d8`](https://github.com/cloudflare/speedtest/tree/323da2ea5697ab4953f2c90c931125ac35d019d8). The current website's internal JavaScript scoring overrides were not available for inspection, so this CLI does not claim to reproduce any undocumented website overrides.