# Core module maintenance helpers.
#
# Core has no caerus-framework-* requires — use deps-others / deps-upgrade to
# refresh third-party modules (and the tool directive graph).
#
# GOWORK=off so a parent go.work does not pin you to local checkouts.

.PHONY: help deps-latest deps-others deps-upgrade tidy test vet

GO ?= go

help:
	@echo "Targets:"
	@echo "  deps-others   bump direct non-caerus requires to @latest"
	@echo "  deps-upgrade  go get -u ./... (+ tools) — all used modules"
	@echo "  tidy          go mod tidy"
	@echo "  test          go test ./..."
	@echo "  vet           go vet ./..."

# Direct (non-indirect) require paths from go.mod.
DIRECT_MODS = awk ' \
	/^require[ \t]+\(/ { inreq=1; next } \
	/^\)/ { inreq=0; next } \
	/^require[ \t]+[^(\t ]/ { \
		if ($$0 !~ /\/\/[ \t]*indirect/) print $$2; \
		next \
	} \
	inreq { \
		if ($$0 ~ /\/\/[ \t]*indirect/) next; \
		print $$1 \
	} \
	' go.mod

deps-latest:
	@echo "core has no caerus-framework-* deps; use deps-others or deps-upgrade"

deps-others:
	@set -eu; \
	mods=$$($(DIRECT_MODS) | grep -v '^github\.com/caerus-framework/' || true); \
	if [ -z "$$mods" ]; then echo "no non-caerus direct deps"; exit 0; fi; \
	args=""; \
	for m in $$mods; do echo "→ $$m@latest"; args="$$args $$m@latest"; done; \
	GOWORK=off $(GO) get $$args; \
	GOWORK=off $(GO) mod tidy; \
	echo "now:"; \
	GOWORK=off $(GO) list -m $$mods

deps-upgrade:
	@echo "→ go get -u ./..."
	GOWORK=off $(GO) get -u ./...
	@echo "→ go get -u tool"
	GOWORK=off $(GO) get -u tool
	GOWORK=off $(GO) mod tidy

tidy:
	$(GO) mod tidy

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
