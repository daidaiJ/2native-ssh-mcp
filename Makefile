# Run this before every commit: make check
#
# CI runs the same two steps (.github/workflows/ci.yml). The structure gate in
# complexity_test.go — file length, function length, cyclomatic complexity —
# runs as part of `go test`, so `make test` covers it.
#
# golangci-lint v2 is required:
#   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
SHELL := /bin/bash
GOLANGCI ?= golangci-lint

.PHONY: check lint test fmt-report

check: lint test

lint:
	$(GOLANGCI) run --timeout 5m ./...

test:
	go test ./...

# Not part of `check` yet: 25 files in the tree predate any gofmt gate and
# would change. Run this to list them; reformat them in a dedicated commit and
# then move this into `check` and into CI. CR is stripped from both sides
# because a Windows checkout with core.autocrlf gives every file CRLF.
fmt-report:
	@for f in $$(git ls-files '*.go'); do \
		if ! diff -q <(tr -d '\r' < "$$f") <(tr -d '\r' < "$$f" | gofmt) >/dev/null; then \
			echo "$$f"; \
		fi; \
	done
