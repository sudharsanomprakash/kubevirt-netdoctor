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
GOVULNCHECK_VERSION ?= v1.10.0

.PHONY: all build test race vet fmt fmt-check lint vulncheck verify golden integration bpf-generate bpf-check bpf-test dist clean

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

fmt-check:
	@test -z "$$(gofmt -s -l .)" || { gofmt -s -l .; echo "files need gofmt -s"; exit 1; }

vulncheck: ## Check for known vulnerabilities in the code paths the project calls
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

verify: fmt-check vet lint race vulncheck ## Every gate a change must pass before it is pushed

bpf-generate: ## Recompile bpf/*.c and regenerate the Go bindings (needs clang)
	go generate ./internal/ebpf

bpf-check: ## Compile the BPF sources with -Werror without touching the committed objects
	clang -O2 -g -Wall -Werror -target bpf -I bpf -c bpf/netdoctor.bpf.c -o /dev/null

bpf-test: ## Load the tracer into the running kernel and trace real traffic in network namespaces (root)
	go test -count=1 -v -run 'Trace' ./internal/ebpf ./internal/cli

integration: ## Host collector test against real iproute2 in a disposable netns (root)
	./hack/integration-netns.sh

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
