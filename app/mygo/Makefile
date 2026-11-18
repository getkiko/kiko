GO ?= go
GOOS ?= $(shell $(GO) env GOHOSTOS)
GOARCH ?= $(shell $(GO) env GOHOSTARCH)
TARGET_OS ?= $(shell $(GO) env GOHOSTOS)
TARGET_ARCH ?= $(shell $(GO) env GOHOSTARCH)
PACKAGE_OS := $(if $(filter darwin,$(GOOS)),macos,$(GOOS))
EXE := $(if $(filter windows,$(GOOS)),.exe)
SERVER_ROOT := $(abspath ..)
SHARED_ROOT := $(abspath ../..)
ASSET_SERVER_ROOT ?= $(SHARED_ROOT)/app
BINARY_DIR ?= $(or $(firstword $(wildcard $(SERVER_ROOT)/build/package-$(PACKAGE_OS)-$(GOARCH) $(ASSET_SERVER_ROOT)/build/package-$(PACKAGE_OS)-$(GOARCH))),$(SERVER_ROOT)/build/package-$(PACKAGE_OS)-$(GOARCH))
# mygo build ships resources/<os>-<arch> with the app of that platform.
STAGE := resources/$(GOOS)-$(GOARCH)
PLATFORM_RESOURCES := $(STAGE)
BUILD_FLAGS ?=
# Local builds are ad hoc signed; CI sets a Developer ID.
SIGN_FLAGS := $(if $(MACOS_SIGNING_IDENTITY),-sign "$(MACOS_SIGNING_IDENTITY)")

.PHONY: prepare build dev test

# Prepared binaries may live in the shared checkout.
# Override BINARY_DIR to use another prepared package.
prepare:
	test -x "$(BINARY_DIR)/kiko-server$(EXE)"
	test -x "$(BINARY_DIR)/kiko-core$(EXE)"
	rm -rf "$(STAGE)"
	mkdir -p "$(STAGE)/bin" "$(STAGE)/helper"
	cp "$(BINARY_DIR)/kiko-server$(EXE)" "$(BINARY_DIR)/kiko-core$(EXE)" "$(PLATFORM_RESOURCES)/bin/"
	cp ../helperpaths/paths.json "$(STAGE)/helper/paths.json"
	cp ../helperpaths/versions.json "$(STAGE)/helper/versions.json"
	cp ../packaging/mygo/install-helper.sh "$(STAGE)/helper/install-helper.sh"
ifeq ($(GOOS),darwin)
	test -x "$(BINARY_DIR)/kiko-helper"
	mkdir -p "$(PLATFORM_RESOURCES)/helper"
	cp "$(BINARY_DIR)/kiko-helper" "$(PLATFORM_RESOURCES)/helper/"
endif

build:
	$(MAKE) prepare GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH)
	$(GO) tool mygo build -platform $(TARGET_OS)/$(TARGET_ARCH) $(SIGN_FLAGS) $(BUILD_FLAGS)
ifeq ($(TARGET_OS),darwin)
	@echo "App: $(abspath ../build/$(TARGET_OS)-$(TARGET_ARCH)/Kiko.app)"
endif

dev: prepare
	$(GO) tool mygo dev

test:
	$(GO) vet ./...
	$(GO) test ./...
