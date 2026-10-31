VERSION    ?= $(shell git describe --tags --always --dirty --exclude devel 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG     := github.com/jkandasa/hdmiview/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildDate=$(BUILD_DATE)

# Release targets (os/arch). Linux arm is ARMv7 (GOARM=7). macOS needs cgo
# (IOKit) and Apple's clang, so darwin/* must be built on macOS:
#   make dist DIST_TARGETS="darwin/amd64 darwin/arm64"
DIST_TARGETS ?= linux/amd64 linux/arm64 linux/arm windows/amd64

SHA256 := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

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
#   <tool>-<version>-<os>-<arch>.tar.gz (.zip on Windows)
# The Linux ms2109tool archive includes the udev rule.
dist:
	rm -rf dist && mkdir -p dist
	set -e; for target in $(DIST_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=; cgo=0; cc=; \
		if [ $$os = windows ]; then ext=.exe; fi; \
		if [ $$os = darwin ]; then \
			cgo=1; cc="clang -arch $$( [ $$arch = amd64 ] && echo x86_64 || echo arm64 )"; \
		fi; \
		for tool in hdmiview ms2109tool; do \
			name=$$tool-$(VERSION)-$$os-$$arch; dir=dist/$$name; \
			mkdir -p $$dir; \
			CGO_ENABLED=$$cgo CC="$$cc" GOOS=$$os GOARCH=$$arch GOARM=7 \
				go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/$$tool$$ext ./cmd/$$tool; \
			cp README.md LICENSE $$dir/; \
			if [ $$tool = ms2109tool ] && [ $$os = linux ]; then cp udev/70-ms2109.rules $$dir/; fi; \
			if [ $$os = windows ]; then \
				(cd dist && zip -qr $$name.zip $$name); \
			else \
				tar -C dist -czf dist/$$name.tar.gz $$name; \
			fi; \
			rm -rf $$dir; \
		done; \
	done
	cd dist && $(SHA256) *.tar.gz *.zip 2>/dev/null > SHA256SUMS || true

clean:
	rm -rf bin dist
