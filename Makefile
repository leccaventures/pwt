.PHONY: build clean

.PHONY: build build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 prepare clean

BINDIR ?= $(shell go env GOPATH)/bin
BINARY ?= pharos-watchtower
CMD ?= ./cmd/monitor
CONFIG_EXAMPLE ?= $(CURDIR)/config.example.yml
CONFIG ?= $(HOME)/.pwt/config.yml

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

build: prepare
	@mkdir -p "$(BINDIR)"
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o "$(BINDIR)/$(BINARY)" $(CMD)

define build_target
build-$(1)-$(2): prepare
	@mkdir -p "$(BINDIR)"
	GOOS=$(1) GOARCH=$(2) go build -o "$(BINDIR)/$(BINARY)-$(1)-$(2)" $(CMD)
endef

$(eval $(call build_target,linux,amd64))
$(eval $(call build_target,linux,arm64))
$(eval $(call build_target,darwin,amd64))
$(eval $(call build_target,darwin,arm64))

prepare:
	@mkdir -p "$(HOME)/.pwt/data"
	@mkdir -p "$(dir $(CONFIG))"
	@if [ ! -f "$(CONFIG)" ] && [ -f "$(CONFIG_EXAMPLE)" ]; then \
		cp "$(CONFIG_EXAMPLE)" "$(CONFIG)"; \
	fi

clean:
	@rm -f "$(BINDIR)/$(BINARY)" "$(BINDIR)/$(BINARY)-"*
