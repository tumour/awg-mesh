.PHONY: build build-prod build-all clean vet test help

VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)
GOFLAGS = -trimpath -ldflags="$(LDFLAGS)"

BIN_DIR = bin

help:
	@echo "Targets:"
	@echo "  make build       # quick dev build (with debug symbols)"
	@echo "  make build-prod  # stripped production build for current arch"
	@echo "  make build-all   # cross-compile linux amd64/arm64/armv7"
	@echo "  make vet         # go vet"
	@echo "  make test        # go test"
	@echo "  make clean       # remove bin/"

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

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
