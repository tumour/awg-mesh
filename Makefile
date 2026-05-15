.PHONY: build build-prod build-all vet test clean help package package-all

VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)
GOFLAGS = -trimpath -ldflags="$(LDFLAGS)"

BIN_DIR = bin
DIST_DIR = dist

help:
	@echo "Build targets:"
	@echo "  make build         # quick dev build (with debug symbols)"
	@echo "  make build-prod    # stripped production build for current arch"
	@echo "  make build-all     # cross-compile linux amd64/arm64/armv7"
	@echo ""
	@echo "Package targets (требуют nfpm: https://nfpm.goreleaser.com/install/):"
	@echo "  make package       # build .deb for amd64 (current build-all)"
	@echo "  make package-all   # build .deb for amd64 + arm64"
	@echo ""
	@echo "  make vet           # go vet"
	@echo "  make test          # go test"
	@echo "  make clean         # remove bin/ + dist/"

build:
	go build -o $(BIN_DIR)/meshd ./cmd/meshd

build-prod:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BIN_DIR)/meshd ./cmd/meshd
	@ls -la $(BIN_DIR)/meshd

build-all: $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64           go build $(GOFLAGS) -o $(BIN_DIR)/meshd-linux-amd64 ./cmd/meshd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64           go build $(GOFLAGS) -o $(BIN_DIR)/meshd-linux-arm64 ./cmd/meshd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7     go build $(GOFLAGS) -o $(BIN_DIR)/meshd-linux-armv7 ./cmd/meshd
	@ls -la $(BIN_DIR)/

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

$(DIST_DIR):
	mkdir -p $(DIST_DIR)

# Сборка .deb для одной арки (по умолчанию amd64).
# Требует чтобы bin/meshd-linux-${ARCH} был уже собран (через build-all).
# nfpm не expand'ит ${ARCH} в contents.src — используем envsubst preprocessing.
package: $(DIST_DIR)
	@command -v nfpm >/dev/null || (echo "nfpm not installed; see https://nfpm.goreleaser.com/install/"; exit 1)
	@command -v envsubst >/dev/null || (echo "envsubst not installed (apt install gettext-base)"; exit 1)
	@test -f $(BIN_DIR)/meshd-linux-amd64 || (echo "run 'make build-all' first"; exit 1)
	@VERSION_CLEAN=$$(echo $(VERSION) | sed 's/^v//'); \
	export ARCH=amd64 VERSION=$$VERSION_CLEAN; \
	envsubst < nfpm.yaml > $(DIST_DIR)/nfpm-amd64.yaml; \
	nfpm pkg --config $(DIST_DIR)/nfpm-amd64.yaml --packager deb \
		--target $(DIST_DIR)/meshd_$${VERSION_CLEAN}_amd64.deb
	@ls -la $(DIST_DIR)/

# Сборка под все целевые архитектуры (amd64 + arm64).
package-all: $(DIST_DIR) build-all
	@command -v nfpm >/dev/null || (echo "nfpm not installed; see https://nfpm.goreleaser.com/install/"; exit 1)
	@command -v envsubst >/dev/null || (echo "envsubst not installed (apt install gettext-base)"; exit 1)
	@VERSION_CLEAN=$$(echo $(VERSION) | sed 's/^v//'); \
	for arch in amd64 arm64; do \
		echo "==> packaging $$arch..."; \
		export ARCH=$$arch VERSION=$$VERSION_CLEAN; \
		envsubst < nfpm.yaml > $(DIST_DIR)/nfpm-$$arch.yaml; \
		nfpm pkg --config $(DIST_DIR)/nfpm-$$arch.yaml --packager deb \
			--target $(DIST_DIR)/meshd_$${VERSION_CLEAN}_$${arch}.deb || exit 1; \
	done
	@ls -la $(DIST_DIR)/

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
