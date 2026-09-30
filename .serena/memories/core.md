# Project core

- One executable package in the nested `src/` module; no root module or service. Repository guidance is `AGENTS.md`; measurement source of truth is `specs/speedtest.md` plus the pinned Cloudflare upstream source.
- `main.go` orchestrates interleaved HTTP phases, optional TURN loss, availability, exits and terminal UI. `measurement.go` owns shared HTTP timing/retries and reducers; `packetloss.go` owns two native WebRTC peers; `scores.go` owns pure experience scores.
- Upload uses header-write-to-first-response-byte TTFB. Download uses corrected ping plus body duration. Browser transferSize is approximated with validated payload bytes times1.005; native clients are not browser-identical.
- Final bandwidth pools eligible sizes into P90. Loaded probes run concurrently and must cancel/join at phase end. Packet loss means unreliable WebRTC application-message non-arrival over a UDP relay, not a raw-UDP ping or directional IP loss.
- Lite direction flags select their direction when no explicit direction flags exist. Lite limits individual requests, not total traffic. Live runs consume bandwidth and reveal public IP/location; never retain those values or TURN credentials.
- For dependency/packaging boundaries read `mem:tech_stack`; for local commands and Docker CMD handling read `mem:suggested_commands`; for measurement/statistics/concurrency invariants read `mem:conventions`; for completion gates read `mem:task_completion`.
- Before changing memory structure read `mem:memory_maintenance`. Existing project-local memory is not permission to create external canonical stores.
