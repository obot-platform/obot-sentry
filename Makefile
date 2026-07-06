# Makefile for Go project

default: build

# Build the project

GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null | xargs -I {} echo -X 'github.com/obot-platform/obocop/pkg/version.Tag={}')
GO_LD_FLAGS := "-s -w $(GIT_TAG)"
build:
	go build -ldflags=$(GO_LD_FLAGS) -o bin/obocop .

# Cross-compiled Windows binaries.
build-windows:
	GOOS=windows GOARCH=amd64 go build -ldflags=$(GO_LD_FLAGS) -o bin/obocop.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags=$(GO_LD_FLAGS) -o bin/obocop-arm64.exe .

clean:
	rm -rf bin dist

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

.PHONY: default build build-windows clean lint lint-go vet-windows tidy setup-env test validate-go-code no-changes
