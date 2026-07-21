GO ?= go

version := $(shell git describe --tags 2>/dev/null || echo "v0.0.0-dev")
commit := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")

build := github.com/rewantsoni/dr-network-check/pkg/build
ldflags := -X '$(build).Version=$(version)' -X '$(build).Commit=$(commit)'

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all dr-network-check cross test lint clean

all: dr-network-check

dr-network-check:
	CGO_ENABLED=0 $(GO) build -ldflags="$(ldflags)" -o bin/dr-network-check ./cmd/dr-network-check/

cross:
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "Building bin/dr-network-check-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -ldflags="$(ldflags)" \
			-o bin/dr-network-check-$$os-$$arch$$ext ./cmd/dr-network-check/; \
	done

test:
	$(GO) test -ldflags="$(ldflags)" -v ./...

lint:
	golangci-lint run ./...

clean:
	rm -f bin/dr-network-check bin/dr-network-check-*
