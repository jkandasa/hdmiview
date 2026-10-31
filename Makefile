VERSION    ?= $(shell git describe --tags --always --dirty --exclude devel 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG     := github.com/jkandasa/hdmiview/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildDate=$(BUILD_DATE)

# Release targets (os/arch). Linux arm is built for ARMv7 (GOARM=7).
DIST_TARGETS ?= linux/amd64 linux/arm64 linux/arm

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

# One archive per tool and target in dist/, plus SHA256SUMS:
#   dist/hdmiview-<version>-<os>-<arch>.tar.gz
#   dist/ms2109tool-<version>-<os>-<arch>.tar.gz (includes the udev rule)
dist:
	rm -rf dist && mkdir -p dist
	set -e; for target in $(DIST_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		for tool in hdmiview ms2109tool; do \
			name=$$tool-$(VERSION)-$$os-$$arch; dir=dist/$$name; \
			mkdir -p $$dir; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch GOARM=7 \
				go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/$$tool ./cmd/$$tool; \
			cp README.md LICENSE $$dir/; \
			if [ $$tool = ms2109tool ]; then cp udev/70-ms2109.rules $$dir/; fi; \
			tar -C dist -czf dist/$$name.tar.gz $$name; \
			rm -rf $$dir; \
		done; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS

clean:
	rm -rf bin dist
