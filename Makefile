VERSION    ?= $(shell git describe --tags --always --dirty --exclude devel 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG     := github.com/jkandasa/hdmiview/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildDate=$(BUILD_DATE)

# Release targets. hdmiview needs a 64-bit kernel ABI (V4L2 struct layout).
DIST_ARCHES ?= amd64 arm64

.PHONY: all build hdmiview ms2109tool test vet dist clean

all: build

build: hdmiview ms2109tool

hdmiview:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/hdmiview ./cmd/hdmiview

ms2109tool:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ms2109tool ./cmd/ms2109tool

test:
	go test ./...

vet:
	go vet ./...

# dist/hdmiview-<version>-linux-<arch>.tar.gz plus SHA256SUMS
dist:
	rm -rf dist && mkdir -p dist
	set -e; for arch in $(DIST_ARCHES); do \
		name=hdmiview-$(VERSION)-linux-$$arch; dir=dist/$$name; \
		mkdir -p $$dir; \
		for tool in hdmiview ms2109tool; do \
			CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/$$tool ./cmd/$$tool; \
		done; \
		cp README.md LICENSE udev/70-ms2109.rules $$dir/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf $$dir; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS

clean:
	rm -rf bin dist
