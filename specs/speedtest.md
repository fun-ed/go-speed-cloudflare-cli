# Speed-test measurement specification

This document describes `go-speed-cloudflare-cli` version 0.1.0. The Cloudflare reference is `@cloudflare/speedtest` 1.14.1 at commit [`323da2ea5697ab4953f2c90c931125ac35d019d8`](https://github.com/cloudflare/speedtest/tree/323da2ea5697ab4953f2c90c931125ac35d019d8). The pinned source provides the reference plan and formulas. It does not establish that the current website deploys that exact revision or applies no private overrides.

The CLI is a native Go HTTP and WebRTC client. Its output is an approximation of the reference methodology, not a browser-equivalent timing or transfer-size record. Source links below point to the pinned Cloudflare commit unless another repository is named.

## Reference source map

| Behavior | Pinned source |
| --- | --- |
| Default phase plan and packet-loss options | [`src/config/defaultConfig.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/config/defaultConfig.ts) |
| HTTP timing, request shape, and loaded probes | [`BandwidthEngine.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/engines/BandwidthEngine/BandwidthEngine.ts), [`ParallelLatency.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/engines/BandwidthEngine/ParallelLatency.ts), [`MeasurementCalculations.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/Results/MeasurementCalculations.ts) |
| Packet-loss credentials, phase, and results | [`src/engines/PacketLossEngine/PacketLossEngine.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/engines/PacketLossEngine/PacketLossEngine.ts) |
| WebRTC peers and data channel | [`src/engines/PacketLossEngine/SelfWebRtcDataConnection.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/engines/PacketLossEngine/SelfWebRtcDataConnection.ts) |
| NQS metric and category formulas | [`src/Results/ScoresCalculations.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/Results/ScoresCalculations.ts), [`src/utils/scaleThreshold.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/utils/scaleThreshold.ts) |
| NQS threshold tables | [`src/config/internalConfig.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/src/config/internalConfig.ts) |
| Reference score fixture | [`tests/unit/Results/ScoresCalculations.test.ts`](https://github.com/cloudflare/speedtest/blob/323da2ea5697ab4953f2c90c931125ac35d019d8/tests/unit/Results/ScoresCalculations.test.ts) |

The public [Cloudflare AIM documentation](https://developers.cloudflare.com/speed/aim/) describes streaming, gaming, and real-time communication categories. The website's static JavaScript scoring bundle was not available for inspection, so exact live-site overrides are unverified.

## HTTP request plan

The CLI uses Cloudflare's `https://speed.cloudflare.com` service. It fetches `/locations` and `/cdn-cgi/trace` for optional metadata, then measures with these requests:

- Latency: `GET /__down?bytes=0&during=idle|download|upload`.
- Download: `GET /__down?bytes=N` and require exactly `N` response bytes for a positive-size sample.
- Upload: `POST /__up?bytes=N` with an `N`-byte ASCII `0` body.

All HTTP requests use the shared client and request context. Positive-size samples require a successful status and checked body reads/drains. Upload retries get a fresh body. A latency response may contain a control body; its size is not treated as a download sample.

The default ordered plan is:

| Step | Direction | Bytes per request | Attempts |
| ---: | --- | ---: | ---: |
| 1 | Idle latency | 0 | 2 |
| 2 | Download | 100,000 | 1, early finish bypassed |
| 3 | Idle latency | 0 | 20 |
| 4 | Download | 100,000 | 9 |
| 5 | Idle latency | 0 | 2 |
| 6 | Download | 1,000,000 | 8 |
| 7 | Idle latency | 0 | 2 |
| 8 | Upload | 100,000 | 8 |
| 9 | Idle latency | 0 | 2 |
| 10 | Packet loss | 1,000 data-channel messages | 1 phase |
| 11 | Upload | 1,000,000 | 6 |
| 12 | Idle latency | 0 | 2 |
| 13 | Download | 10,000,000 | 6 |
| 14 | Idle latency | 0 | 2 |
| 15 | Upload | 10,000,000 | 4 |
| 16 | Idle latency | 0 | 2 |
| 17 | Download | 25,000,000 | 4 |
| 18 | Idle latency | 0 | 2 |
| 19 | Upload | 25,000,000 | 4 |
| 20 | Idle latency | 0 | 2 |
| 21 | Download | 100,000,000 | 3 |
| 22 | Idle latency | 0 | 2 |
| 23 | Upload | 50,000,000 | 3 |
| 24 | Idle latency | 0 | 2 |
| 25 | Download | 250,000,000 | 2 |

The packet-loss phase is inserted after step 9, the two idle probes after the 100,000-byte upload, and before the 1,000,000-byte upload. The HTTP plan contains 42 idle probes, 33 download attempts, and 25 upload attempts before early finish or request failures.

The initial 100,000-byte download phase bypasses early finish so that a short first sample does not end the direction. After each other bandwidth phase, the CLI marks that direction finished when the shortest successful sample in that phase exceeds 1,000 ms. Later bandwidth phases for a finished direction are skipped. A bandwidth phase with no successful samples also stops later phases for that direction. A partially failed phase with at least one successful sample does not by itself stop the direction.

`-download` and `-upload` select bandwidth phases. Idle-latency phases remain in their original plan positions, including when a direction is omitted. `-lite` removes only download or upload phases larger than 10,000,000 bytes; it is a per-request-size ceiling, not a total-transfer ceiling. Without explicit direction flags, `-lite-download` and `-lite-upload` select only their named direction. With explicit directions, either flag limits only its named direction, which must be selected; other selected directions remain full unless limited by `-lite` or their own lite flag.

### Planned HTTP payload

These sums include one transfer for every scheduled bandwidth attempt and exclude early finish. Units are decimal bytes. They are not hard network caps.

| Selection | Download payload | Upload payload | Combined payload |
| --- | ---: | ---: | ---: |
| Full plan | 969 MB | 296.8 MB | 1,265.8 MB |
| Lite, both directions | 69 MB | 46.8 MB | 115.8 MB |
| Lite download only | 69 MB | 0 MB | 69 MB |
| Lite upload only | 0 MB | 46.8 MB | 46.8 MB |
| Lite download, full upload | 69 MB | 296.8 MB | 365.8 MB |

The combined figure adds bytes received and bytes sent; it is not a count of unique bytes. Traffic can be lower because a direction finishes early. A 429 retry can repeat an HTTP request, and the upload body may already have crossed the network before the server returns 429. Each bandwidth request has up to three 429 retries after its first attempt. These sums exclude HTTP latency probes, metadata, IP/TCP/TLS overhead, retries, retransmissions, and TURN/WebRTC signaling and transport traffic. The packet-loss phase sends 1,000 application messages by default, but those messages are not a fixed number of IP datagrams.

## HTTP timing and bandwidth formulas

The Go client records request timing with `httptrace`. It starts the measurement at `WroteHeaders`; `GotConn` records whether the connection is new or reused and does not start the timing interval. The first-response-byte event marks arrival of the response headers. This excludes DNS, TCP connection setup, and TLS handshake time. For uploads, the timer begins before the request body is sent, so the interval to response headers includes body transmission and the server's wait before responding. The implementation does not use a browser navigation start or resource timing API.

Let `T` be the measured interval from `WroteHeaders` to first response byte, in milliseconds. If Cloudflare `Server-Timing` provides a valid server duration `S`, the client subtracts it and a learned client/server timing offset `D` to estimate latency. The parser accepts a valid `cfReqDur`, `cfRequestDur`, `cfReqDuration`, `cfRequestDuration`, or `cfSpeed*` duration; it uses the first valid request-duration metric, or sums valid speed metrics when there is no request-duration metric. A missing server duration is zero.

The client learns `D` only on a new HTTP/1 connection when `S` is above 0.01 ms and at most 150 ms. It estimates server time as `max(0, T - TCP connection duration)`, excluding TLS negotiation, and accepts the difference from `S` only when it is positive, at most 15 ms, and no greater than `S`. An accepted difference updates `D` with 75 percent of the new difference and 25 percent of its previous value. Other requests reuse the last offset.

The corrected latency is `L = T - S - D`. If `L` is at most 1 ms, the implementation falls back to `max(0, T - S)`. Non-finite corrected latency is rejected.

- Upload duration is the response-header interval `T`.
- Download duration is corrected latency `L` plus response-body read time, from the first response byte until the body is drained.
- Idle-latency samples use the corrected latency `L`.

Each positive-size bandwidth sample estimates transfer size as `N * 1.005`, where `N` is the validated payload byte count. It then calculates:

```text
speed bits/second = 8 * (N * 1.005) / (duration milliseconds / 1000)
```

The 1.005 factor is a 0.5 percent transfer-overhead approximation. Go cannot read the browser's exact `transferSize`; these values are not byte-for-byte browser measurements. Displayed decimal Mbps is `bits/second / 1,000,000`.

A phase result shows the median of that phase's finite positive speed samples whose duration is at least 10 ms. The final directional speed pools all eligible samples for the selected direction across request sizes, sorts their speeds, and returns the 90th percentile using Type 7 interpolation:

```text
h = (n - 1) * 0.90
P90 = x[floor(h)] * (1 - (h - floor(h))) + x[ceil(h)] * (h - floor(h))
```

Only finite positive speeds with finite duration at least 10 ms enter this calculation. The input sample list is not modified. If no sample passes the filter, the result is unavailable and prints `N/A`.

### Latency and jitter

Unloaded latency is the median of finite nonnegative corrected latency samples. Jitter is the mean absolute difference between adjacent valid latency samples in their measurement order:

```text
jitter = mean(abs(latency[i] - latency[i-1]))
```

Invalid values are omitted. An empty set makes latency unavailable. Fewer than two valid points makes jitter unavailable; the CLI does not print zero for missing jitter.

When `-loaded-latency` is enabled, each bandwidth phase starts background latency probes after 20 ms, then waits 400 ms after each completed probe before starting the next. The probe includes the active direction in `during=download` or `during=upload`. Ending the bandwidth phase cancels and joins its probe loop. Failed probes are excluded from statistics and reported as loaded-latency failures. Expected cancellation at normal phase completion is not a failure.

Loaded results are grouped by direction and request size. A size group is eligible only when it has a successful bandwidth sample and the shortest successful sample in the group lasted at least 250 ms. The CLI combines the loaded probes from eligible groups, orders them by start time, retains the latest 20, and applies the same median-latency and jitter formulas. Missing eligible data prints `N/A`. If loaded latency is disabled, loaded values and NQS dependent on them are unavailable.

## HTTP errors, retries, and cancellation

The default `-timeout` is 30 seconds per HTTP operation. It bounds HTTP operations, including the wait before a 429 retry. The client retries HTTP 429 no more than three times after the original attempt. It interprets `Retry-After` as seconds or an HTTP date. An absent or unparsable value uses a five-second delay; a negative delay or expired date causes no wait. Context cancellation interrupts a request or retry wait.

Non-2xx status, failed body reads or drains, a download length different from the requested size, invalid timing, or an invalid speed rejects that sample. A rejected sample cannot contribute to speed, latency, or NQS. Metadata failures produce a warning and `N/A` metadata without independently failing the HTTP test. Enabled measurement failures or missing required results make the CLI exit nonzero. An interrupt or canceled run exits with code 130; invalid CLI arguments exit with code 2.

## Packet loss and TURN

The pinned reference requests `GET https://speed.cloudflare.com/turn-creds` without a request body, cookie requirement, or client-owned secret. Its parser reads flat `username` and `credential` strings, optional `server`, and `error`, ignoring unrelated fields. The CLI validates status, response size, JSON, required strings, and server authority; a non-empty `error` rejects credentials without exposing its text.

The current public endpoint returns flat `username` and `credential` plus `urls`. The native parser supports this observed format. A valid explicit `server` wins; otherwise it selects the first valid UDP `turn:` URI from a `urls` string or array. TCP `turn:`, TLS `turns:`, and STUN entries are not used. If both server fields are absent, it uses the configured fallback `turn.speed.cloudflare.com:50000`. URL authorities require a host and port. It always constructs `turn:<authority>?transport=udp`. Unknown fields are ignored, but nested `iceServers` wrappers do not provide the required flat credentials. `-turn-creds-url` overrides the endpoint; `-turn-server` changes only the fallback. Neither flag accepts static secrets. Credentials and raw responses are never logged.

The packet-loss phase creates two local Pion WebRTC peers with the same TURN credentials. It gathers UDP candidates only, requires a relay ICE policy and a selected UDP relay pair, and exchanges offer, answer, and candidates locally. It sends text messages `"1"` through `"1000"` over an unordered data channel configured with zero allowed retransmissions. The sender traverses its access network to the TURN relay and the receiver traverses that user's network back from the relay. This is application-message non-arrival on a TURN-relayed WebRTC/SCTP path, not one-way IP packet loss and not a guarantee of 1,000 IP datagrams.

Relay-only peers disable multicast DNS host discovery. It is unnecessary for the selected path and avoids an observed mDNS shutdown hang on the Go 1.26/Pion stack. Both peers close gracefully outside callbacks. Cleanup has a separate two-second grace and checks closure errors and final peer states.

The default phase sends 1,000 messages in batches of 10, waits 10 ms between batches, and then uses a 3,000 ms receive-silence window. Peer setup has a 5,000 ms limit after credential retrieval. The measurement window is capped by `-timeout`, which defaults to 30 seconds, with a separate bounded shutdown grace. UDP access to a TURN relay is required; a TCP-only proxy or a firewall that blocks UDP may make the result unavailable. Raw TURN UDP or an echo server would measure a different path and is not an equivalent WebRTC test.

The result counts only unique, canonical IDs that were sent. Duplicates and malformed, unexpected, or unsent IDs do not count as received messages. The final result is:

```text
loss ratio = unique sent IDs not received / actual sent ID count
packet-loss percent = loss ratio * 100
```

A successful connected session with no received messages is 100 percent loss. Zero sent IDs, credential/setup failure, send failure, timeout, or cancellation is unavailable, never a fabricated 0 or 100 percent. The client snapshots final sent and received IDs at completion, even if no message arrived after the last send. It may finish immediately once all messages have been sent and received; the reference's delayed finish quirk is not required.

Credential, TURN, or peer setup failure warns and leaves packet loss unavailable. The HTTP phases continue, but an enabled packet-loss failure makes the run incomplete. `-packet-loss=false` skips the phase; this does not turn packet loss into a measured zero.

## Network Quality Score formulas

The CLI reports experience-specific NQS categories, not a single normalized score from 0 to 100. It uses validated finite measurements and preserves availability separately from numeric values. Threshold equality advances to the next bucket. Metric points are:

| Metric | Input unit | Thresholds | Points by bucket |
| --- | --- | --- | --- |
| Packet loss | ratio from 0 to 1 | `0.01, 0.05, 0.25, 0.5` | `10, 5, 0, -10, -20` |
| Latency | ms | `10, 20, 50, 100, 500` | `20, 10, 5, 0, -10, -20` |
| Loaded latency increase | ms | `10, 20, 50, 100, 500` | `20, 10, 5, 0, -10, -20` |
| Jitter | ms | `10, 20, 100, 500` | `10, 5, 0, -10, -20` |
| Download speed | bits/s | `1e6, 10e6, 50e6, 100e6` | `0, 5, 10, 20, 30` |
| Upload speed | bits/s | `1e6, 10e6, 50e6, 100e6` | `0, 5, 10, 20, 30` |

Loaded latency increase is:

```text
max(download-loaded latency, upload-loaded latency) - unloaded latency
```

It is not a percentage, ratio, jitter, or sum. A negative increase is valid and falls in the best metric bucket. The CLI requires both finite loaded directions and a valid idle latency for this metric; it does not reproduce the pinned JavaScript truthiness/NaN behavior for incomplete directions.

Each experience sums its listed metric points and clamps the total to a minimum of zero. It does not average, weight, normalize to 100, or apply an upper clamp.

| Experience | Required metric points | Category thresholds |
| --- | --- | --- |
| Streaming | Latency + packet loss + download + loaded latency increase | `15, 20, 40, 60` |
| Gaming | Latency + packet loss + loaded latency increase | `5, 15, 25, 30` |
| RTC | Latency + jitter + packet loss + loaded latency increase | `5, 15, 25, 40` |

The category bands map to `bad`, `poor`, `average`, `good`, and `great`, in order. For example, streaming totals of 15, 20, 40, and 60 advance to `poor`, `average`, `good`, and `great`. Gaming and RTC use their respective thresholds from the table. There is no aggregate overall category. Upload speed is scored by the source's helper but is not an input to the three default experiences. Loaded jitter and RPKI are also not score inputs.

Missing packet loss contributes the source's neutral zero metric points; it does not create a measured loss ratio of zero. A real measured ratio of zero receives 10 packet-loss points. The output still labels missing packet loss `N/A`. Missing or invalid latency or loaded increase makes the dependent category unavailable. Missing jitter omits RTC only; missing download omits streaming only. A direction-only run or disabled loaded latency lacks both loaded directions, so all three categories are unavailable. Non-finite and out-of-range values are rejected instead of receiving favorable threshold buckets.

The pinned score fixture uses 20 ms latency, 10 ms jitter, 0.01 loss, 50 Mbps download, 10 Mbps upload, 50 ms download-loaded latency, and 40 ms upload-loaded latency. It produces 35 streaming points, 15 gaming points, and 20 RTC points. All three classify as `average`.

## Output, flags, and exit behavior

The CLI prints connection metadata when available, phase speed summaries, then final metrics. An unavailable metric prints `N/A`; it is never replaced by a measured zero. Metadata errors print a warning to standard error but do not alone change success status. Results go to standard output, warnings and terminal progress go to standard error. Progress is shown only when standard error is a terminal and is disabled by `-no-progress`. Colors require a terminal on standard output and are disabled by `-no-color` or any set `NO_COLOR` environment variable.

Progress counts two metadata operations, each planned HTTP sample attempt, and one packet-loss session. Completed, failed, and skipped units advance the same denominator. Sending individual WebRTC messages updates the status label, not the work percentage. This is planned-work progress, not elapsed-time or byte progress.

| Exit code | Meaning |
| ---: | --- |
| `0` | Required measurements completed |
| `1` | An enabled measurement failed or a required result was unavailable |
| `2` | Invalid flags, incompatible selection, or positional arguments |
| `130` | Context cancellation or interruption |

`-help` and `-version` complete without network access. The version command prints the binary's version and exits 0.

## Acceptance tests

The source implementation and tests should cover these behaviors with local fixtures. No test should contact Cloudflare or require real public credentials.

- HTTP: status validation, 429 retries with seconds and HTTP-date `Retry-After`, retry limit, cancellation during retry delay, timeout across a request and its retry waits, fresh upload body on retry, checked drains, response size mismatch and truncation, successful empty/control response bodies, malformed or multiple `Server-Timing` metrics, and response timing that excludes body-drain time from upload TTFB.
- Statistics: empty and non-finite values, no input mutation, positive-speed and 10 ms filters, Type 7 P90 interpolation, regression fixtures with expected P90 values 56.31, 37.37, and 28.28, sample ordering, latency median and consecutive-difference jitter, loaded minimum-duration filtering, latest-20 selection, and `shouldFinish` at and above the 1,000 ms boundary. Confirm the initial 100,000-byte download bypasses early finish.
- Packet loss: flat credential schema and server fallback/override, status and parse failures, oversized response, sanitized errors, UDP relay-only peer selection, text IDs, batches, duplicates and invalid IDs, no-arrival 100 percent result, zero-sent unavailable result, final snapshot, cancellation during setup/send/wait, session and setup deadlines, and cleanup on every return path. A local Pion TURN server fixture should test a real relay path without an external service.
- NQS: every metric/category threshold immediately below, at, and above the boundary; the pinned 35/15/20 fixture; missing loss versus measured zero; each missing input's category effect; one-sided or missing loaded latency; real zero unloaded latency; invalid, non-finite, and out-of-range values; no input mutation.
- CLI: offline help/version, accepted and rejected direction/lite combinations, no positional arguments, timeout validation, all-failure exit status, packet-loss skip and setup-failure behavior, N/A output, metadata warning behavior, color and TTY progress routing, progress completion when phases skip or fail, and signal/context cancellation.
