BINARY  := kubectl-netdoctor
PKG     := github.com/sudharsanomprakash/kubevirt-netdoctor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test race vet fmt lint golden dist clean

all: fmt vet lint test build

build: ## Build the plugin binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/kubectl-netdoctor

test: ## Run unit and golden tests
	go test ./...

race: ## Run tests with the race detector
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

lint: ## Requires golangci-lint v2
	golangci-lint run ./...

golden: ## Regenerate CLI golden files after an intended output change
	go test ./internal/cli -update

dist: ## Cross-compile release archives and checksums into dist/
	rm -rf dist && mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		dir=dist/$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		mkdir -p $$dir; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $$dir/$(BINARY)$$ext ./cmd/kubectl-netdoctor; \
		cp LICENSE README.md $$dir/; \
		tar -C dist -czf $$dir.tar.gz $$(basename $$dir); \
		rm -rf $$dir; \
	done
	cd dist && sha256sum *.tar.gz > checksums.txt

clean:
	rm -rf bin dist
