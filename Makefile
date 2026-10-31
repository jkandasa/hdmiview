VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG     := github.com/jkandasa/hdmiview/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildDate=$(BUILD_DATE)

.PHONY: all build hdmiview ms2109tool test vet clean

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

clean:
	rm -rf bin
