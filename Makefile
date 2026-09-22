NAMESPACE			= flomation.app/automate/api
DATE				= $(shell date -u +%Y%m%d_%H%M%S)
NAME				?= automate/api

BRANCH 				:= $(shell git rev-parse --abbrev-ref HEAD)
GITHASH 			?= $(shell git rev-parse HEAD)
CI_PIPELINE_ID 		?= dev
VERSION 			?= 1.0.${CI_PIPELINE_ID}
REGISTRY 			?= local

OS_ARCHS ?= linux/amd64

# Lint tool versions are PINNED, not @latest.
#
# @latest re-resolves and re-downloads the tool on every single run, which costs
# time and — worse — means an unchanged commit can start failing because the
# tool changed underneath it. A new gosec rule or vuln database entry then
# arrives as "your MR broke lint", with nothing in the diff to explain it.
#
# Bumping these is a deliberate, reviewable one-line change.
GOSEC_VERSION        ?= v2.28.0
GOVULNCHECK_VERSION  ?= v1.7.0

# ── govulncheck runs in BINARY mode, in CI, after compile ──────────────────
#
# This is the fix the previous "temporarily disabled" note asked for. It is
# enabled and blocking again; see the `vulncheck` job in .gitlab-ci.yml.
#
# SOURCE mode is not viable and never will be at this module size. It builds an
# SSA call graph over the entire transitive dependency tree, and measured peak
# RSS on this module is 12.51 GiB in 2m32s at GOMAXPROCS=1 — above even a 10Gi
# runner ceiling. GOMEMLIMIT cannot rescue it: that bounds the Go heap, while
# govulncheck's footprint is largely OFF-heap (mmap'd package export data, SSA).
#
# BINARY mode reads the shipped symbol table instead. Measured on the same
# module: 20 MiB peak RSS in 0.55s — roughly 640x less memory. It also scans
# exactly what ships, which is the thing we actually care about.
#
# The trade-off, stated honestly: binary mode reports symbols PRESENT in the
# binary and does not do source mode's call-graph reachability analysis, so it
# can flag code that is linked but never called. That is what GOVULNCHECK_ALLOW
# below exists for. Source mode stays available for local use.
#
# Local use:
#   make govulncheck-binary   # needs `make build` first; what CI runs
#   make lint GOVULNCHECK_ENABLED=1   # source mode; budget ~13 GiB of RAM
GOVULNCHECK_ENABLED    ?= 0
GOVULNCHECK_GOMAXPROCS ?= 1
GOVULNCHECK_GOMEMLIMIT ?= 2000MiB

# Findings accepted with a written justification. Anything NOT on this list
# fails the build. Keep each entry's reasoning with it, and re-check on any
# dependency bump.
#
# GO-2026-5932  golang.org/x/crypto/openpgp — unmaintained and unsafe by
#               design, with "Fixed in: N/A", so there is no version to move to.
#               `go mod why golang.org/x/crypto/openpgp` reports "main module
#               does not need package", and SOURCE-mode govulncheck does not
#               report it at all — i.e. it is linked into the binary but is not
#               reachable from our code. This is precisely the binary-mode
#               false-positive class described above.
#               Re-evaluate if we ever call openpgp directly.
GOVULNCHECK_ALLOW      ?= GO-2026-5932

# Install a pinned tool only when the required version is not already present,
# so a warm GOPATH/bin (see the CI cache) skips the download entirely.
# `go version -m` reports the module version a binary was built from.
define ensure_tool
	@if ! command -v $(1) >/dev/null 2>&1 || ! go version -m "$$(command -v $(1))" 2>/dev/null | grep -q "$(3)"; then \
		echo "installing $(1)@$(3)"; \
		go install $(2)@$(3); \
	else \
		echo "$(1)@$(3) already present"; \
	fi
endef

lint:
	go mod tidy
	goimports -l .
	golangci-lint run --timeout=5m ./...
	go vet ./...
	$(call ensure_tool,gosec,github.com/securego/gosec/v2/cmd/gosec,$(GOSEC_VERSION))
	gosec -exclude=G117,G704 ./...
	@if [ "$(GOVULNCHECK_ENABLED)" = "1" ]; then \
		$(MAKE) --no-print-directory govulncheck; \
	else \
		echo "govulncheck source mode skipped here — CI runs it in BINARY mode after compile (make govulncheck-binary). Set GOVULNCHECK_ENABLED=1 for source mode locally; budget ~13 GiB."; \
	fi

# SOURCE mode. Local use only — measured 12.51 GiB peak RSS on this module, so
# it does not fit any runner we have. Kept because its call-graph reachability
# analysis is more precise than binary mode's, which makes it the tool to reach
# for when triaging whether a binary-mode finding is real.
govulncheck:
	$(call ensure_tool,govulncheck,golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))
	GOMAXPROCS=$(GOVULNCHECK_GOMAXPROCS) GOMEMLIMIT=$(GOVULNCHECK_GOMEMLIMIT) govulncheck ./...

# BINARY mode. This is what CI runs, in the `vulncheck` job after `compile`.
# Scans every artefact `make build` produced, so the gencerts binary that ships
# alongside the service is covered too.
#
# Findings on GOVULNCHECK_ALLOW are reported and skipped; anything else fails.
# We deliberately do not use govulncheck's exit code directly, because it does
# not distinguish an allowed finding from a new one.
govulncheck-binary:
	$(call ensure_tool,govulncheck,golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))
	@export PATH="$$(go env GOPATH)/bin:$$PATH"; \
	command -v govulncheck >/dev/null 2>&1 || { \
		echo "ERROR: govulncheck is not on PATH after install — refusing to report a pass"; \
		exit 1; \
	}; \
	bins=$$(find ./dist -type f -name 'flomation-*-amd64-linux-*' 2>/dev/null); \
	if [ -z "$$bins" ]; then \
		echo "ERROR: no binaries under ./dist — run 'make build' first (CI: needs the compile job's artifacts)"; \
		exit 1; \
	fi; \
	status=0; \
	for bin in $$bins; do \
		echo "── govulncheck -mode=binary $$bin"; \
		out=$$(govulncheck -mode=binary "$$bin" 2>&1); rc=$$?; \
		echo "$$out"; \
		if [ $$rc -ne 0 ] && [ $$rc -ne 3 ]; then \
			echo "ERROR: govulncheck could not scan $$bin (exit $$rc). Failing — a tool that did not run is not a pass."; \
			exit 1; \
		fi; \
		ids=$$(echo "$$out" | sed -n 's/^Vulnerability #[0-9]*: \(GO-[0-9][0-9-]*\).*/\1/p' | sort -u); \
		if [ $$rc -eq 3 ] && [ -z "$$ids" ]; then \
			echo "ERROR: govulncheck reported findings (exit 3) but none could be parsed from its output."; \
			echo "       The output format has probably changed — fix the parser rather than trusting this."; \
			exit 1; \
		fi; \
		for id in $$ids; do \
			case " $(GOVULNCHECK_ALLOW) " in \
				*" $$id "*) echo "  ALLOWED  $$id (see GOVULNCHECK_ALLOW in the Makefile)";; \
				*)          echo "  BLOCKING $$id"; status=1;; \
			esac; \
		done; \
	done; \
	if [ $$status -eq 0 ]; then echo "govulncheck: no unaccepted findings"; fi; \
	exit $$status

build:
	rm -rf dist/
	@for platform in $(OS_ARCHS); do \
		os=$$(echo $$platform | cut -d'/' -f1); \
		arch=$$(echo $$platform | cut -d'/' -f2); \
		echo "Building for $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "-s -X $(NAMESPACE)/internal/version.Version=$(VERSION) -X $(NAMESPACE)/internal/version.Hash=$(GITHASH) -X $(NAMESPACE)/internal/version.BuiltDate=$(DATE)" -o ./dist/flomation-${NAME}-$$arch-$$os-${VERSION} $(NAMESPACE)/cmd; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "-s" -o ./dist/flomation-gencerts-$$arch-$$os-${VERSION} $(NAMESPACE)/tools/gencerts; \
	done
	cd dist && zip -r ../build.zip .

dev-certs:
	go run tools/gencerts/main.go -out certs/dev

test:
	go test ./... -coverprofile cover.out
	go tool cover -func cover.out

publish:
	aws ecr get-login-password --region eu-west-2 | docker login --username AWS --password-stdin ${REGISTRY}
	docker push ${REGISTRY}/${NAME}:latest
	docker push ${REGISTRY}/${NAME}:${GITHASH}
