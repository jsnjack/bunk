BINARY  := bunk
PWD     := $(shell pwd)
PKG     := ./...
VERSION := 0.0.0
MONOVA  := $(shell which monova 2> /dev/null)
LDFLAGS  = -ldflags="-X main.Version=$(VERSION)"
# The repo's own Go files: not build output, vendored code or agent worktrees.
GOFILES  = $(shell find . -name '*.go' -not -path './bin/*' -not -path './.claude/*' -not -path '*/vendor/*')
# Go files changed since HEAD or untracked: goimports takes ~25 s on every
# file but only ~0.1 s per file, and committed files already passed it.
CHANGED_GOFILES = $(shell { git diff --name-only --diff-filter=ACMR HEAD -- '*.go'; \
	git ls-files -o --exclude-standard -- '*.go'; } 2>/dev/null | grep -v -E '^(bin|\.claude)/|/vendor/' | sort -u)

# FRESH=1 is the full gate before a merge: tests run uncached and goimports
# sees every file. Without it, go test reuses results for packages whose
# code, read files and env vars are unchanged.
ifdef FRESH
TEST_COUNT  := -count=1
IMPORTS_FILES = $(GOFILES)
else
IMPORTS_FILES = $(CHANGED_GOFILES)
endif
# bunk for every platform; make build adds bin/bunk-gui.
BUNK_BINS := bin/$(BINARY) bin/$(BINARY)_linux_amd64 bin/$(BINARY)_linux_arm64 bin/$(BINARY)_darwin_amd64 bin/$(BINARY)_darwin_arm64
# Binaries wait for their tests; check-all clears these after running them.
TEST_GATE     ?= test
GUI_TEST_GATE ?= gui-test

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
	go test $(PKG) github.com/gdamore/tcell/v2/... $(TEST_COUNT) -race

vet:
	go vet $(PKG)

fmt:
	@command -v goimports >/dev/null 2>&1 || { \
	  echo "goimports is not installed. Install it with:"; \
	  echo "  go install golang.org/x/tools/cmd/goimports@latest"; \
	  exit 1; \
	}
	$(if $(strip $(GOFILES)),gofmt -w $(GOFILES))
	$(if $(strip $(IMPORTS_FILES)),goimports -w $(IMPORTS_FILES))

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint is not installed. Install it with:"; \
	  echo "  grm install golangci/golangci-lint"; \
	  exit 1; \
	}
	golangci-lint run

check: fmt vet $(BUNK_BINS) lint
	@echo "==> make check: all green"

standards:
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.universal.md \
	    -o AGENTS.universal.md
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.go.md \
	    -o AGENTS.go.md

bin/$(BINARY): bin/$(BINARY)_linux_amd64
	cp $< $@
	ln -sf bin/$(BINARY) $(BINARY)
bin/$(BINARY)_linux_amd64: $(TEST_GATE) version
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $@
bin/$(BINARY)_linux_arm64: $(TEST_GATE) version
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $@
bin/$(BINARY)_darwin_amd64: $(TEST_GATE) version
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $@
bin/$(BINARY)_darwin_arm64: $(TEST_GATE) version
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $@

# Every binary: bunk for four platforms and bunk-gui for this machine (cgo
# and GTK4 rule out cross builds), each after its own tests.
build: $(BUNK_BINS) bin/bunk-gui

release: $(BUNK_BINS)
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

# bin/bunk-gui waits for the window tests; make gui builds it without them.
bin/bunk-gui: $(GUI_TEST_GATE)
	cd $(GUI_DIR) && go build -o ../../bin/bunk-gui .

gui:
	cd $(GUI_DIR) && go build -o ../../bin/bunk-gui .

# Window tests run on a private headless compositor, never on the desktop.
gui-test:
	cd $(GUI_DIR) && go vet ./... && \
		../../scripts/headless-gui.sh go test -race $(TEST_COUNT) ./...
	desktop-file-validate packaging/$(APP_ID).desktop
	appstreamcli validate --no-net packaging/$(APP_ID).metainfo.xml

# README screenshots of bunk-gui, rendered headlessly into assets/.
screenshots:
	scripts/screenshots.sh

# Everything: format and vet, then bunk's tests and bunk-gui's window tests
# at the same time (separate modules; logs print after both finish), then
# the binaries and lint without running the tests again.
check-all: fmt vet
	@mkdir -p bin; \
	$(MAKE) --no-print-directory test > bin/check-test.log 2>&1 & t=$$!; \
	$(MAKE) --no-print-directory gui-test > bin/check-gui.log 2>&1 & g=$$!; \
	wait $$t; tr=$$?; wait $$g; gr=$$?; \
	cat bin/check-test.log bin/check-gui.log; \
	test $$tr -eq 0 && test $$gr -eq 0
	@$(MAKE) --no-print-directory build lint TEST_GATE= GUI_TEST_GATE=
	@echo "==> make check-all: all green"

# Runs every Fuzz target in turn for FUZZTIME each. Not part of check or
# check-all: make test already runs the seed corpora in testdata/fuzz.
FUZZTIME ?= 30s

fuzz:
	@list=$$(go test -list '^Fuzz' $(PKG)) || { echo "$$list"; exit 1; }; \
	echo "$$list" | awk '/^Fuzz/ { names[++n] = $$1; next } \
		/^ok/ { for (i = 1; i <= n; i++) print $$2, names[i]; n = 0 }' | \
	while read -r pkg name; do \
		echo "==> $$pkg $$name ($(FUZZTIME))"; \
		go test "$$pkg" -run '^$$' -fuzz "^$$name\$$" -fuzztime $(FUZZTIME) < /dev/null || exit 1; \
	done

# Leak and soak tests at full length (SOAK=1): 2000 sessions, 500 MiB of
# output, 1,000,000 unique links, 200 TUI cycles, 400 notify sessions, 200
# bunk-gui tabs. Not part
# of check or check-all, which run the short versions with -race.
# SOAKFLAGS=-race adds the race detector (about ten times slower).
SOAKFLAGS ?=
SOAK_TESTS := ^Test(SessionLeaks|OutputSoak|LinkSoak|TUISoak|NotifySessionLeaks)$$

soak:
	SOAK=1 go test $(SOAKFLAGS) -count=1 -timeout 60m -v -run '$(SOAK_TESTS)' . ./internal/session ./internal/notify
	cd $(GUI_DIR) && SOAK=1 ../../scripts/headless-gui.sh \
		go test $(SOAKFLAGS) -count=1 -timeout 30m -v -run '^TestGUITabLeaks$$' .

# Performance gate. bench-check runs the key benchmarks (scripts/benchcheck)
# pinned to BENCH_CPU, BENCH_COUNT interleaved rounds, and fails when a
# median time grows by more than BENCH_THRESHOLD percent with p < 0.05
# against BENCH_BASELINE. bench-baseline records that file. Neither is part
# of check or check-all: timings depend on machine load.
BENCH_THRESHOLD ?= 10
BENCH_COUNT     ?= 10
BENCH_CPU       ?= 0
BENCH_MAX_LOAD  ?= 4
BENCH_TIME      ?=
BENCH_SUITES    ?=
BENCH_BASELINE  ?= testdata/bench-baseline.txt
BENCH_CURRENT   ?= bin/bench-current.txt
BENCH_RUN        = go run ./scripts/benchcheck run -count $(BENCH_COUNT) -cpu '$(BENCH_CPU)' \
	-max-load $(BENCH_MAX_LOAD) -benchtime '$(BENCH_TIME)' -suites '$(BENCH_SUITES)'

bench-check:
	BENCHCHECK_E2E=1 go test -count=1 -run '^TestRunEndToEnd$$' ./scripts/benchcheck
	$(BENCH_RUN) -o $(BENCH_CURRENT)
	go run ./scripts/benchcheck compare -threshold $(BENCH_THRESHOLD) -max-load $(BENCH_MAX_LOAD) \
		$(BENCH_BASELINE) $(BENCH_CURRENT)

bench-baseline:
	$(BENCH_RUN) -o $(BENCH_BASELINE)

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

# bin/bunk-gui is phony so a build without its test gate still rebuilds it.
.PHONY: version build release test vet fmt lint check standards run clean gui gui-test screenshots check-all rpm fuzz soak \
	bench-check bench-baseline bin/bunk-gui
