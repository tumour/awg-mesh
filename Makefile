.PHONY: build build-prod build-all vet test clean help package package-all package-openwrt

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
	@echo "  make package         # build .deb for amd64 (current build-all)"
	@echo "  make package-all     # build .deb for amd64 + arm64"
	@echo "  make package-openwrt # build .apk (OpenWrt 25.12+) for all arches"
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
	# mips/mipsle — OpenWrt-роутеры (ath79 BE / ramips LE). 24kc без FPU -> softfloat.
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(GOFLAGS) -o $(BIN_DIR)/meshd-linux-mipsle ./cmd/meshd
	CGO_ENABLED=0 GOOS=linux GOARCH=mips   GOMIPS=softfloat go build $(GOFLAGS) -o $(BIN_DIR)/meshd-linux-mips ./cmd/meshd
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

# Сборка .apk для OpenWrt 25.12+ (apk-tools v3) под все целевые арки.
# apk строг к формату версии (digits.digits...), поэтому из git describe
# берём только числовую часть: "0.1.3-1-gb09bb3d" -> "0.1.3", "dev" -> "0.0.0".
package-openwrt: $(DIST_DIR) build-all
	@command -v nfpm >/dev/null || (echo "nfpm not installed; see https://nfpm.goreleaser.com/install/"; exit 1)
	@command -v envsubst >/dev/null || (echo "envsubst not installed (apt install gettext-base)"; exit 1)
	@APK_VERSION=$$(echo $(VERSION) | sed 's/^v//' | grep -oE '^[0-9]+(\.[0-9]+)*' || true); \
	[ -n "$$APK_VERSION" ] || APK_VERSION=0.0.0; \
	for goarch in amd64 arm64 armv7 mipsle mips; do \
		echo "==> packaging openwrt-$$goarch..."; \
		export GOARCH=$$goarch VERSION=$$APK_VERSION; \
		envsubst < nfpm-openwrt.yaml > $(DIST_DIR)/nfpm-openwrt-$$goarch.yaml; \
		nfpm pkg --config $(DIST_DIR)/nfpm-openwrt-$$goarch.yaml --packager apk \
			--target $(DIST_DIR)/meshd_$${APK_VERSION}_openwrt-$$goarch.apk || exit 1; \
	done
	@ls -la $(DIST_DIR)/*.apk

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
