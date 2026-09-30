# Development commands

Run Go commands from `src/` using the existing module:

```sh
cd src
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
goreleaser check --config ../.goreleaser.yml
```

- Format edited files only. Build binaries into temporary locations; arbitrary local linter versions may not match the Go toolchain.
- Live isolated HTTP checks: `go run . -lite-download -packet-loss=false` or `go run . -lite-upload -packet-loss=false`. These use real bandwidth and print connection metadata. Help/version remain offline.
- Cross-build from src: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/go-speed-cloudflare-cli-linux-amd64 .`.
- From repository root: `docker build --target final-alpine -t go-speed-cloudflare-cli:local .`; offline run `docker run --rm go-speed-cloudflare-cli:local ./main -version`. Images use CMD, so preserve `./main` when passing arguments.
- Snapshot packaging is local only; run GoReleaser from src with the root config and a unique external dist path. Publishing requires a separate explicit request.
