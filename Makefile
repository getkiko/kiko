.PHONY: test core macos-app
test:
	cd app && go test ./...
	$(MAKE) -C app/mygo test

# kiko-core is built from the core/ submodule (a sing-box fork). Its version
# output keeps the upstream "sing-box version <x>" format.
core:
	mkdir -p app/build/core
	core_version="$$(cd core && CGO_ENABLED=0 go run ./cmd/internal/read_tag)" && \
	cd core && go build -trimpath \
		-tags "$$(cat release/DEFAULT_BUILD_TAGS_OTHERS)" \
		-ldflags "-X github.com/sagernet/sing-box/constant.Version=$$core_version $$(cat release/LDFLAGS) -s -w -buildid=" \
		-o ../app/build/core/kiko-core ./cmd/sing-box && \
	built_core_version="$$(../app/build/core/kiko-core version | sed -n '1s/^[^ ]* version //p')" && \
	test "$$built_core_version" = "$$core_version" || { echo "kiko-core version mismatch: want $$core_version, got $${built_core_version:-unknown}" >&2; exit 1; }

macos-app: core
	$(MAKE) -C app oneclick-macos KIKO_CORE=$(CURDIR)/app/build/core/kiko-core
	$(MAKE) -C app mygo-app
