# GitHub Actions Workflows

This directory contains CI and release workflows for Go Speed Cloudflare CLI.

## Testing

`workflows/test.yml` runs on pushes to every branch and on pull requests. It sets
up the Go version declared in `src/go.mod`, runs the Go unit tests from `src/`,
and runs golangci-lint there.

## Releases

`workflows/release.yml` runs when a `v*.*.*` tag is pushed. Before building a
release, it checks out the tag, installs the Go version from `src/go.mod`, and
runs `go test ./...` from `src/`. GoReleaser runs only if those tests pass.

GoReleaser runs from the nested Go module in `src/` and loads the root
`.goreleaser.yml`. It builds the shared source for Linux, macOS, and Windows.
The tag version is injected into the binary at link time; the workflow does not
edit, commit, or push source files. GoReleaser publishes the release assets
using the automatically provided `GITHUB_TOKEN`. The workflow also attaches
the generated `src/dist/` artifacts to the Actions run.

The application’s default version remains in `src/main.go` for local builds.
Release binaries use the pushed tag version without changing that source value.

## Permissions

The release workflow requires `contents: write` to create the GitHub release.
No manually configured token secret or package-publishing permission is needed.