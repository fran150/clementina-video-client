BINARY_NAME := clementina-video-client
CMD := ./cmd/$(BINARY_NAME)
BUILD_DIR := build
BUILD_OUTPUT := $(BUILD_DIR)/$(BINARY_NAME)

.PHONY: build clean test dev-install

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_OUTPUT) $(CMD)

# Dev builds go into the folder the Clementina SDK's `clementina-dev on` puts
# first on PATH; see docs/dev-install.md in clementina-sdk.
DEV_BIN ?= $(if $(CLEMENTINA_DEV_BIN),$(CLEMENTINA_DEV_BIN),$(HOME)/.clementina/dev/bin)

dev-install:
	mkdir -p "$(DEV_BIN)"
	go build -o "$(DEV_BIN)/clementina-video-client" ./cmd/clementina-video-client
	go build -o "$(DEV_BIN)/clementina-render" ./cmd/clementina-render
	"$(DEV_BIN)/clementina-render" --version

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
