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

# govulncheck is the memory peak of the whole lint suite: it builds an SSA call
# graph over the entire transitive dependency tree to prove which vulnerable
# symbols are actually reachable. As the SDK surface grew it began OOM-killing
# at the runner's 4Gi cgroup ceiling, and that ceiling cannot be raised without
# runner config access.
#
# The CI job's GOMAXPROCS/GOMEMLIMIT apply to every command in the target, so a
# single value has to serve both tools — and they want opposite things.
# GOMAXPROCS=1 was previously found to fix the govulncheck OOM, but it starved
# golangci-lint's parallel package loading badly enough to blow its timeout, so
# the job settled on 2 and govulncheck kept dying.
#
# Setting them PER COMMAND resolves that: govulncheck gets the single-threaded,
# tightly-capped environment it needs, while golangci-lint keeps the job-level
# parallelism. Fewer analysis goroutines means fewer concurrent SSA package
# graphs alive at once, which is where the peak comes from; the lower GOMEMLIMIT
# forces the GC to work harder and leaves ~2Gi of the 4Gi for govulncheck's
# large OFF-heap footprint (mmap'd export data), which GOMEMLIMIT does not bound.
GOVULNCHECK_GOMAXPROCS ?= 1
GOVULNCHECK_GOMEMLIMIT ?= 2000MiB

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
	$(call ensure_tool,govulncheck,golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))
	GOMAXPROCS=$(GOVULNCHECK_GOMAXPROCS) GOMEMLIMIT=$(GOVULNCHECK_GOMEMLIMIT) govulncheck ./...

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
