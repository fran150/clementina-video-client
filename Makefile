BINARY_NAME := clementina-video-client
CMD := ./cmd/$(BINARY_NAME)
BUILD_DIR := build
BUILD_OUTPUT := $(BUILD_DIR)/$(BINARY_NAME)

.PHONY: build clean test

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_OUTPUT) $(CMD)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
