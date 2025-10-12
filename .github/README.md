# GitHub Actions Workflows

This directory contains the GitHub Actions workflows for the Go Speed Cloudflare CLI tool.

## Release Workflow

### Releasing a New Version

To create a new release:

1. Create and push a new tag with the desired version:
   ```
   git tag -a vX.Y.Z -m "Version X.Y.Z"
   git push origin vX.Y.Z
   ```
2. The GitHub Actions workflow will automatically:
   - Update the version number in `src/main.go` to match the tag
   - Commit and push this change to the branch
   - Build binaries for multiple platforms (Linux, macOS, Windows)
   - Create a GitHub release with the tag name
   - Upload the binaries as release assets

### Workflow Details

The workflow is defined in `release.yml` and uses GoReleaser to build and publish the release. It's triggered whenever a tag matching the pattern `v*.*.*` is pushed.

The workflow:
1. Extracts the version number from the tag
2. Updates the version constant in the source code
3. Commits and pushes this change
4. Builds and releases the binaries using GoReleaser

## Testing Workflow

### Automated Testing

The testing workflow automatically runs tests on:
- Pushes to the `dev` branch
- Pull requests to the `dev` branch

### Workflow Details

The workflow is defined in `test.yml` and performs the following actions:
1. Checks out the code
2. Sets up the Go environment
3. Downloads dependencies
4. Runs the test suite
5. Performs linting checks

## Requirements

For these workflows to function correctly:
- The repository needs a `GITHUB_TOKEN` secret (automatically provided by GitHub)
- GoReleaser configuration is in the `.goreleaser.yml` file in the repository root
- The release workflow needs write permissions to push the version update commit 