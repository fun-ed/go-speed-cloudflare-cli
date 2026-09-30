GO ?= go
PREFIX ?= /usr/local
DESTDIR ?=
BINDIR ?= $(PREFIX)/bin

BINARY := build/go-speed-cloudflare-cli
SOURCES := $(filter-out %_test.go,$(wildcard src/*.go)) src/go.mod src/go.sum

.PHONY: build install clean

build: $(BINARY)

$(BINARY): $(SOURCES) Makefile
	mkdir -p "$(dir $(BINARY))"
	cd src && CGO_ENABLED=0 $(GO) build -trimpath -o "$(abspath $(BINARY))" .

install: $(BINARY)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BINARY)" "$(DESTDIR)$(BINDIR)/go-speed-cloudflare-cli"

clean:
	$(RM) "$(BINARY)"
