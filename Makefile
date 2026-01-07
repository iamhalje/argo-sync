.PHONY: all build build-local build-docker clean crossbuild

PKG_PREFIX := github.com/iamhalje/argo-sync

BIN ?= argo-sync
PKG ?= ./cmd/argo-sync
GO  ?= go
DOCKER ?= docker
OUTDIR ?= ./bin

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo unknown)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
REPO ?= $(shell git config --get remote.origin.url 2>/dev/null || echo $(PKG_PREFIX))

LDFLAGS ?= -s -w \
	-X '$(PKG_PREFIX)/internal/buildinfo.Version=$(VERSION)' \
	-X '$(PKG_PREFIX)/internal/buildinfo.Commit=$(COMMIT)' \
	-X '$(PKG_PREFIX)/internal/buildinfo.BuildDate=$(BUILD_DATE)' \
	-X '$(PKG_PREFIX)/internal/buildinfo.Repo=$(REPO)'

TARGETOS ?= $(shell $(GO) env GOOS 2>/dev/null || echo linux)
TARGETARCH ?= $(shell $(GO) env GOARCH 2>/dev/null || echo amd64)

all: build

build:
	@set -e; \
	if command -v "$(DOCKER)" >/dev/null 2>&1; then \
		$(MAKE) build-docker; \
	else \
		$(MAKE) build-local; \
	fi

build-local:
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o "$(CURDIR)/$(BIN)" $(PKG)

build-docker:
	@set -e; \
	img="argo-sync-build-$$(date +%s)-$$RANDOM"; \
	cid=""; \
	printf '%s\n' "Building $$img for $(TARGETOS)/$(TARGETARCH)..."; \
	$(DOCKER) build --pull -t "$$img" --target builder \
		--build-arg TARGETOS="$(TARGETOS)" \
		--build-arg TARGETARCH="$(TARGETARCH)" \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg REPO="$(REPO)" \
		-f Dockerfile .; \
	cid=$$($(DOCKER) create "$$img"); \
	$(DOCKER) cp "$$cid:/out/argo-sync" "$(CURDIR)/$(BIN)"; \
	$(DOCKER) rm -f "$$cid" >/dev/null; \
	$(DOCKER) rmi "$$img" >/dev/null

argo-sync-crossbuild: \
	argo-sync-linux-amd64 \
	argo-sync-linux-386 \
	argo-sync-linux-arm64 \
	argo-sync-linux-arm \
	argo-sync-linux-ppc64le \
	argo-sync-darwin-amd64 \
	argo-sync-darwin-arm64 \
	argo-sync-freebsd-amd64 \
	argo-sync-openbsd-amd64 \
	argo-sync-windows-amd64

argo-sync-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(MAKE) build-goos-goarch
argo-sync-linux-386:
	CGO_ENABLED=0 GOOS=linux GOARCH=386 $(MAKE) build-goos-goarch
argo-sync-linux-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm $(MAKE) build-goos-goarch
argo-sync-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(MAKE) build-goos-goarch
argo-sync-linux-ppc64le:
	CGO_ENABLED=0 GOOS=linux GOARCH=ppc64le $(MAKE) build-goos-goarch
argo-sync-darwin-amd64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(MAKE) build-goos-goarch
argo-sync-darwin-arm64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(MAKE) build-goos-goarch
argo-sync-freebsd-amd64:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 $(MAKE) build-goos-goarch
argo-sync-openbsd-amd64:
	CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 $(MAKE) build-goos-goarch
argo-sync-windows-amd64:
	GOOS=windows GOARCH=amd64 $(MAKE) build-windows-goarch

build-goos-goarch:
	@set -e; \
	mkdir -p "$(OUTDIR)"; \
	out="$(OUTDIR)/$(BIN)-$${GOOS}-$${GOARCH}"; \
	printf '%s\n' "Building $$out..."; \
	CGO_ENABLED="$${CGO_ENABLED:-0}" GOOS="$$GOOS" GOARCH="$$GOARCH" \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o "$$out" $(PKG)

build-windows-goarch:
	@set -e; \
	mkdir -p "$(OUTDIR)"; \
	out="$(OUTDIR)/$(BIN)-$${GOOS}-$${GOARCH}.exe"; \
	printf '%s\n' "Building $$out..."; \
	CGO_ENABLED="$${CGO_ENABLED:-0}" GOOS="$$GOOS" GOARCH="$$GOARCH" \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o "$$out" $(PKG)

clean:
	rm -f "$(CURDIR)/$(BIN)"

golangci-lint: install-golangci-lint
	GOEXPERIMENT=synctest golangci-lint run

install-golangci-lint:
	which golangci-lint || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin v2.7.2

remove-golangci-lint:
	rm -rf `which golangci-lint`

govulncheck: install-govulncheck
	govulncheck -show verbose ./...

install-govulncheck:
	which govulncheck || go install golang.org/x/vuln/cmd/govulncheck@latest

remove-govulncheck:
	rm -rf `which govulncheck`

fmt:
	gofmt -l -w -s ./internal
	gofmt -l -w -s ./cmd

tests:
	GO111MODULE=on go test -race -mod=mod ./...

check-all: tests fmt golangci-lint
