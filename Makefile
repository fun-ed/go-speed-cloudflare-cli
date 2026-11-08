GO ?= go
GOFMT ?= gofmt
# Shared by local checks and .github/workflows/test.yml.
GOLANGCI_LINT_VERSION ?= v2.14.0
PREFIX ?= /usr/local
DESTDIR ?=
BINDIR ?= $(PREFIX)/bin

BINARY := build/go-speed-cloudflare-cli
SOURCES := $(filter-out %_test.go,$(wildcard src/*.go)) src/go.mod src/go.sum

.DEFAULT_GOAL := build
.PHONY: help build install install-local verify-install test race coverage vet lint fmt fmt-check check version clean

help:
	@printf '%s\n' \
	  'make build           Build the CLI in build/ (default)' \
	  'make test            Run local unit and integration tests' \
	  'make race            Run tests with the race detector' \
	  'make coverage        Report test coverage' \
	  'make vet             Run go vet' \
	  'make lint            Run the CI-pinned golangci-lint via go run' \
	  'make fmt             Format Go source and tests' \
	  'make fmt-check       Fail if Go files need formatting' \
	  'make check           Check formatting, tests, race, vet, lint and build' \
	  'make version         Print the locally built version' \
	  'make install         Install to PREFIX/bin (default /usr/local)' \
	  'make install-local   Install to ~/.local/bin without sudo' \
	  'make verify-install  Show the CLI resolved by PATH and its version' \
	  'make clean           Remove the locally built executable' \
	  '' \
	  'Overrides: GO, GOFMT, GOLANGCI_LINT_VERSION, PREFIX, BINDIR, DESTDIR' \
	  'mise: make GO="mise exec -- go" GOFMT="mise exec -- gofmt" check'

build: $(BINARY)

$(BINARY): $(SOURCES) Makefile
	mkdir -p "$(dir $(BINARY))"
	cd src && CGO_ENABLED=0 $(GO) build -trimpath -o "$(abspath $(BINARY))" .

install: $(BINARY)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BINARY)" "$(DESTDIR)$(BINDIR)/go-speed-cloudflare-cli"

install-local:
	$(MAKE) install PREFIX="$(HOME)/.local"

verify-install:
	command -v go-speed-cloudflare-cli
	go-speed-cloudflare-cli -version

test:
	cd src && $(GO) test ./...

race:
	cd src && $(GO) test -race ./...

coverage:
	cd src && $(GO) test -cover ./...

vet:
	cd src && $(GO) vet ./...

lint:
	cd src && $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --timeout=5m

fmt:
	cd src && $(GOFMT) -w *.go

fmt-check:
	@cd src && files="$$($(GOFMT) -l *.go)" && \
	  if [ -n "$$files" ]; then \
	    printf 'Go files need formatting:\n%s\n' "$$files"; exit 1; \
	  fi

check: fmt-check test race vet lint build

version: $(BINARY)
	"$(BINARY)" -version

clean:
	$(RM) "$(BINARY)"
