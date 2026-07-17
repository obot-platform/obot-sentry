# Makefile for Go project

default: build

# Build the project

GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null | xargs -I {} echo -X 'github.com/obot-platform/obot-sentry/pkg/version.Tag={}')
GO_LD_FLAGS := "-s -w $(GIT_TAG)"
build:
	go build -ldflags=$(GO_LD_FLAGS) -o bin/obot-sentry .

clean:
	rm -rf bin dist

# Build the MDM assets remotely: dispatch the build.yaml workflow on the
# current branch of your fork via gh, wait, and download the result into
# dist/mdm-assets. e.g. make mdm [VERSION=1.2.3]
mdm:
	scripts/mdm-remote.sh $(VERSION)

# Lint the project
lint: lint-go

tidy:
	go mod tidy

GOLANGCI_LINT_VERSION ?= v2.11.4
setup-env:
	if ! command -v golangci-lint &> /dev/null; then \
  		echo "Could not find golangci-lint, installing version $(GOLANGCI_LINT_VERSION)."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
	fi

lint-go: setup-env
	golangci-lint run

# Catches Windows-only compile errors without a Windows box.
vet-windows:
	GOOS=windows go vet ./...

test:
	go test -v -cover ./...

# Runs Go linters and validates that the repo is clean.
validate-go-code: tidy lint-go vet-windows no-changes

no-changes:
	@if [ -n "$$(git status --porcelain)" ]; then \
		git status --porcelain; \
		git --no-pager diff; \
		echo "Encountered dirty repo!"; \
		exit 1; \
	fi

.PHONY: default build clean mdm lint lint-go vet-windows tidy setup-env test validate-go-code no-changes
