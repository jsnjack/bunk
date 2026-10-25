BINARY  := bunk
PWD     := $(shell pwd)
PKG     := ./...
VERSION := 0.0.0
MONOVA  := $(shell which monova 2> /dev/null)
LDFLAGS  = -ldflags="-X main.Version=$(VERSION)"
# The repo's own Go files: not build output, vendored code or agent worktrees.
GOFILES  = $(shell find . -name '*.go' -not -path './bin/*' -not -path './.claude/*' -not -path '*/vendor/*')

# Formatting, tests, and binaries share files even with make -j.
.NOTPARALLEL:

export PATH := $(PATH):$(shell go env GOPATH)/bin

version:
ifdef MONOVA
override VERSION = $(shell monova)
override LDFLAGS = -ldflags="-X main.Version=$(VERSION)"
else
	$(info "Install monova with: grm install jsnjack/monova")
endif

test:
	go test $(PKG) github.com/gdamore/tcell/v2/... -count=1 -race

vet:
	go vet $(PKG)

fmt:
	@command -v goimports >/dev/null 2>&1 || { \
	  echo "goimports is not installed. Install it with:"; \
	  echo "  go install golang.org/x/tools/cmd/goimports@latest"; \
	  exit 1; \
	}
	goimports -w $(GOFILES)

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint is not installed. Install it with:"; \
	  echo "  grm install golangci/golangci-lint"; \
	  exit 1; \
	}
	golangci-lint run

check: fmt vet build lint
	@echo "==> make check: all green"

standards:
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.universal.md \
	    -o AGENTS.universal.md
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.go.md \
	    -o AGENTS.go.md

bin/$(BINARY): bin/$(BINARY)_linux_amd64
	cp $< $@
	ln -sf bin/$(BINARY) $(BINARY)
bin/$(BINARY)_linux_amd64: test version
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $@
bin/$(BINARY)_linux_arm64: test version
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $@
bin/$(BINARY)_darwin_amd64: test version
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $@
bin/$(BINARY)_darwin_arm64: test version
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $@

build: bin/$(BINARY) bin/$(BINARY)_linux_amd64 bin/$(BINARY)_linux_arm64 bin/$(BINARY)_darwin_amd64 bin/$(BINARY)_darwin_arm64

release: build
	tar -czf bin/$(BINARY)_linux_amd64.tar.gz  --transform 's|.*/$(BINARY)_.*|$(BINARY)|' bin/$(BINARY)_linux_amd64
	tar -czf bin/$(BINARY)_linux_arm64.tar.gz  --transform 's|.*/$(BINARY)_.*|$(BINARY)|' bin/$(BINARY)_linux_arm64
	tar -czf bin/$(BINARY)_darwin_amd64.tar.gz --transform 's|.*/$(BINARY)_.*|$(BINARY)|' bin/$(BINARY)_darwin_amd64
	tar -czf bin/$(BINARY)_darwin_arm64.tar.gz --transform 's|.*/$(BINARY)_.*|$(BINARY)|' bin/$(BINARY)_darwin_arm64
	grm release jsnjack/$(BINARY) \
		-f bin/$(BINARY)_linux_amd64.tar.gz \
		-f bin/$(BINARY)_linux_arm64.tar.gz \
		-f bin/$(BINARY)_darwin_amd64.tar.gz \
		-f bin/$(BINARY)_darwin_arm64.tar.gz \
		-t "v`monova`"

run: test
	go build -o $(BINARY) .
	> /tmp/bunk.log
	BUNK=1 gnome-terminal -- bash -c 'cd $(PWD) && BUNK= ./$(BINARY) --trace'

# bunk-gui is a separate Go module (cgo, GTK4); check never builds it.
GUI_DIR := cmd/bunk-gui
RPM_TOP := $(PWD)/bin/rpmbuild
APP_ID  := io.github.jsnjack.BunkGui

gui:
	cd $(GUI_DIR) && go build -o ../../bin/bunk-gui .

# Window tests run on a private headless compositor, never on the desktop.
gui-test:
	cd $(GUI_DIR) && go vet ./... && \
		../../scripts/headless-gui.sh go test -race -count=1 ./...
	desktop-file-validate packaging/$(APP_ID).desktop
	appstreamcli validate --no-net packaging/$(APP_ID).metainfo.xml

# Everything: bunk's gate, then bunk-gui and its packaging.
check-all: check gui-test

rpm: version
	rm -rf $(RPM_TOP)
	packaging/fedora/sources.sh $(VERSION) $(RPM_TOP)/SOURCES
	rpmbuild -ba --define "_topdir $(RPM_TOP)" \
		--define "bunk_version $(VERSION)" \
		--define "snapinfo git$$(git rev-parse --short HEAD)" \
		packaging/fedora/bunk-gui.spec
	packaging/fedora/check-rpm.sh $(RPM_TOP)/RPMS/*/bunk-gui-$(VERSION)-*.$$(uname -m).rpm \
		$(RPM_TOP)/SOURCES/bunk-gui-$(VERSION).tar.gz
	@ls $(RPM_TOP)/RPMS/*/bunk-gui-*.rpm $(RPM_TOP)/SRPMS/*.rpm

clean:
	rm -rf bin/ $(BINARY)

.PHONY: version build release test vet fmt lint check standards run clean gui gui-test check-all rpm
