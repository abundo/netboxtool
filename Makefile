# Build directory
BUILD_DIR := build

# Install directory
INSTALL_DIR := /usr/bin

# Go build flags (for release)
GO_BUILD_FLAGS := -ldflags="-s -w"

build: netboxtool

netboxtool:
	@mkdir -p $(BUILD_DIR)
	@go build $(GO_BUILD_FLAGS) -o $(BUILD_DIR)/netboxtool  cmd/netboxtool_cli.go

install: build
	install -m 755 $(BUILD_DIR)/netboxtool $(INSTALL_DIR)
