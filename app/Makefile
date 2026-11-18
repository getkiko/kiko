GO ?= go
BUILD_VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)-$(shell date +%Y%m%d%H%M%S)
MAC_ARCH := $(shell $(GO) env GOHOSTARCH)
MAC_PACKAGE_DIR := build/package-macos-$(MAC_ARCH)
MAC_ARCHIVE := build/kiko-server-macos-$(MAC_ARCH)-oneclick.tar.gz
KIKO_CORE ?= $(CURDIR)/$(MAC_PACKAGE_DIR)/kiko-core
REPO_ROOT := $(abspath ../core)
CORE_TAGS := $(shell cat $(REPO_ROOT)/release/DEFAULT_BUILD_TAGS_OTHERS)
CORE_LDFLAGS := $(shell cat $(REPO_ROOT)/release/LDFLAGS)
CORE_VERSION := $(shell cd $(REPO_ROOT) && CGO_ENABLED=0 go run ./cmd/internal/read_tag)
LINUX_ARCH ?= arm64
LINUX_DEPLOY_DIR = $(CURDIR)/build/deploy-linux-$(LINUX_ARCH)
ANDROID_NDK ?= $(lastword $(sort $(wildcard $(HOME)/Library/Android/sdk/ndk/* $(HOME)/Android/Sdk/ndk/*)))
ANDROID_API ?= 28
ANDROID_NDK_HOST ?= $(if $(filter Darwin,$(shell uname -s)),darwin-x86_64,linux-x86_64)
ANDROID_CC ?= $(ANDROID_NDK)/toolchains/llvm/prebuilt/$(ANDROID_NDK_HOST)/bin/aarch64-linux-android$(ANDROID_API)-clang
ANDROID_DEPLOY_DIR := $(CURDIR)/build/deploy-android-arm64

.PHONY: android-arm64 android-ndk-check
android-ndk-check:
	@test -x "$(ANDROID_CC)" || { echo "Android NDK compiler not found: $(ANDROID_CC)"; echo "usage: make android-arm64 ANDROID_NDK=/absolute/path/to/ndk"; exit 1; }

android-arm64: android-ndk-check
	mkdir -p "$(ANDROID_DEPLOY_DIR)"
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$(ANDROID_CC)" $(GO) build -trimpath -ldflags="-s -w -X main.version=$(BUILD_VERSION)" -o "$(ANDROID_DEPLOY_DIR)/kiko-server" .
	cd "$(REPO_ROOT)" && CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$(ANDROID_CC)" $(GO) build -trimpath -tags "$(CORE_TAGS)" -ldflags="-X 'github.com/sagernet/sing-box/constant.Version=$(CORE_VERSION)' $(CORE_LDFLAGS) -s -w -buildid=" -o "$(ANDROID_DEPLOY_DIR)/kiko-core" ./cmd/sing-box
	@echo "Android arm64 build done: $(ANDROID_DEPLOY_DIR)"

.PHONY: build test oneclick-macos package-linux package-macos package-windows darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64
.PHONY: linux-deploy-build
linux-deploy-build:
	case "$(LINUX_ARCH)" in amd64|arm64) ;; *) echo "unsupported Linux architecture: $(LINUX_ARCH)"; exit 1 ;; esac
	mkdir -p "$(LINUX_DEPLOY_DIR)"
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) $(GO) build -trimpath -ldflags="-s -w -X main.version=$(BUILD_VERSION)" -o "$(LINUX_DEPLOY_DIR)/kiko-server" .
	cd "$(REPO_ROOT)" && CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) $(GO) build -trimpath -tags "$(CORE_TAGS)" -ldflags="-X 'github.com/sagernet/sing-box/constant.Version=$(CORE_VERSION)' $(CORE_LDFLAGS) -s -w -buildid=" -o "$(LINUX_DEPLOY_DIR)/kiko-core" ./cmd/sing-box

build:
	$(GO) build -o build/bin/kiko-server .

test:
	$(GO) test .

darwin-amd64:
	GOOS=darwin GOARCH=amd64 $(GO) build -o build/bin/kiko-server-darwin-amd64 .
darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build -o build/bin/kiko-server-darwin-arm64 .
linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -o build/bin/kiko-server-linux-amd64 .
linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -o build/bin/kiko-server-linux-arm64 .
windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build -o build/bin/kiko-server-windows-amd64.exe .
windows-arm64:
	GOOS=windows GOARCH=arm64 $(GO) build -o build/bin/kiko-server-windows-arm64.exe .

package-linux:
	mkdir -p build/package-linux-amd64 build/package-linux-arm64
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o build/package-linux-amd64/kiko-server .
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o build/package-linux-arm64/kiko-server .
	cd "$(REPO_ROOT)" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -tags "$(CORE_TAGS)" -ldflags="-X 'github.com/sagernet/sing-box/constant.Version=$(CORE_VERSION)' $(CORE_LDFLAGS) -s -w -buildid=" -o "$(CURDIR)/build/package-linux-amd64/kiko-core" ./cmd/sing-box
	cd "$(REPO_ROOT)" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -tags "$(CORE_TAGS)" -ldflags="-X 'github.com/sagernet/sing-box/constant.Version=$(CORE_VERSION)' $(CORE_LDFLAGS) -s -w -buildid=" -o "$(CURDIR)/build/package-linux-arm64/kiko-core" ./cmd/sing-box
	cp packaging/linux/start.sh packaging/linux/install-service.sh packaging/linux/README.txt build/package-linux-amd64/
	cp packaging/linux/start.sh packaging/linux/install-service.sh packaging/linux/README.txt build/package-linux-arm64/
	chmod +x build/package-linux-amd64/kiko-server build/package-linux-amd64/kiko-core build/package-linux-amd64/*.sh
	chmod +x build/package-linux-arm64/kiko-server build/package-linux-arm64/kiko-core build/package-linux-arm64/*.sh
	tar -C build -czf build/kiko-server-linux-amd64.tar.gz package-linux-amd64
	tar -C build -czf build/kiko-server-linux-arm64.tar.gz package-linux-arm64

package-windows:
	mkdir -p build/package-windows-amd64 build/package-windows-arm64
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o build/package-windows-amd64/kiko-server.exe .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o build/package-windows-arm64/kiko-server.exe .
	cd "$(REPO_ROOT)" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -tags "$(CORE_TAGS)" -ldflags="-X 'github.com/sagernet/sing-box/constant.Version=$(CORE_VERSION)' $(CORE_LDFLAGS) -s -w -buildid=" -o "$(CURDIR)/build/package-windows-amd64/kiko-core.exe" ./cmd/sing-box
	cd "$(REPO_ROOT)" && CGO_ENABLED=0 GOOS=windows GOARCH=arm64 $(GO) build -trimpath -tags "$(CORE_TAGS)" -ldflags="-X 'github.com/sagernet/sing-box/constant.Version=$(CORE_VERSION)' $(CORE_LDFLAGS) -s -w -buildid=" -o "$(CURDIR)/build/package-windows-arm64/kiko-core.exe" ./cmd/sing-box
	cp packaging/windows/start.cmd packaging/windows/README.txt build/package-windows-amd64/
	cp packaging/windows/start.cmd packaging/windows/README.txt build/package-windows-arm64/
	rm -f build/kiko-server-windows-amd64.zip build/kiko-server-windows-arm64.zip
	cd build && zip -q -r kiko-server-windows-amd64.zip package-windows-amd64
	cd build && zip -q -r kiko-server-windows-arm64.zip package-windows-arm64

package-macos:
	mkdir -p build/package-macos-amd64 build/package-macos-arm64
	GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o build/package-macos-amd64/kiko-server .
	GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o build/package-macos-arm64/kiko-server .
	cp packaging/macos/start.command packaging/macos/install-launchagent.command packaging/macos/README.txt build/package-macos-amd64/
	cp packaging/macos/start.command packaging/macos/install-launchagent.command packaging/macos/README.txt build/package-macos-arm64/
	chmod +x build/package-macos-amd64/kiko-server build/package-macos-amd64/*.command
	chmod +x build/package-macos-arm64/kiko-server build/package-macos-arm64/*.command
	tar -C build -czf build/kiko-server-macos-amd64.tar.gz package-macos-amd64/kiko-server package-macos-amd64/start.command package-macos-amd64/install-launchagent.command package-macos-amd64/README.txt
	tar -C build -czf build/kiko-server-macos-arm64.tar.gz package-macos-arm64/kiko-server package-macos-arm64/start.command package-macos-arm64/install-launchagent.command package-macos-arm64/README.txt

# Build a complete package for the current Mac, including kiko-core.
# Usage:
#   make oneclick-macos
#   make oneclick-macos KIKO_CORE=/absolute/path/to/kiko-core
oneclick-macos: test
	case "$(MAC_ARCH)" in arm64|amd64) ;; *) echo "unsupported macOS architecture: $(MAC_ARCH)"; exit 1 ;; esac
	test -x "$(KIKO_CORE)" || { echo "kiko-core executable not found: $(KIKO_CORE)"; exit 1; }
	mkdir -p "$(MAC_PACKAGE_DIR)"
	GOOS=darwin GOARCH=$(MAC_ARCH) $(GO) build -trimpath -ldflags="-s -w" -o "$(MAC_PACKAGE_DIR)/kiko-server" .
	CGO_ENABLED=0 GOOS=darwin GOARCH=$(MAC_ARCH) $(GO) build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" -o "$(MAC_PACKAGE_DIR)/kiko-helper" ./helper
	cp packaging/macos/start.command packaging/macos/install-launchagent.command packaging/macos/README.txt "$(MAC_PACKAGE_DIR)/"
	if test "$(KIKO_CORE)" != "$(CURDIR)/$(MAC_PACKAGE_DIR)/kiko-core"; then cp "$(KIKO_CORE)" "$(MAC_PACKAGE_DIR)/kiko-core"; fi
	chmod +x "$(MAC_PACKAGE_DIR)/kiko-server" "$(MAC_PACKAGE_DIR)/kiko-core" "$(MAC_PACKAGE_DIR)"/*.command
	tar -C build -czf "$(MAC_ARCHIVE)" \
		"package-macos-$(MAC_ARCH)/kiko-server" \
		"package-macos-$(MAC_ARCH)/kiko-core" \
		"package-macos-$(MAC_ARCH)/start.command" \
		"package-macos-$(MAC_ARCH)/install-launchagent.command" \
		"package-macos-$(MAC_ARCH)/README.txt"
	@echo "package written: $(CURDIR)/$(MAC_ARCHIVE)"

.PHONY: mygo-dev mygo-app mygo-test
mygo-dev:
	$(MAKE) -C mygo dev

mygo-app:
	$(MAKE) -C mygo build

mygo-test:
	$(MAKE) -C mygo test
