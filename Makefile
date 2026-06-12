# agent-minder Makefile.
#
# These targets back the lefthook hooks and the CI workflow. The repo's
# canonical commands (go build / go test / golangci-lint) are still safe to
# invoke directly; the Makefile only adds names for the integration suite and
# any other multi-step recipe we want to keep in one place.

.PHONY: build test integration vet lint fmt-check all

build:
	go build ./...

test:
	go test ./... -timeout 5m

# integration runs the fast supervisor scenario suite — DB-final-state asserts
# against the seams where unit tests can't catch write-ordering regressions
# (e.g. #517's handleBailReport / finalizeBail clobber). Wired into the
# pre-push lefthook and surfaced as its own CI check.
integration:
	go test ./internal/supervisor/... -run TestScenarios -count=1 -timeout 2m

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt-check:
	@UNFMTED=$$(gofmt -l . 2>&1); \
	if [ -n "$$UNFMTED" ]; then \
	  echo "Files not formatted:"; \
	  echo "$$UNFMTED"; \
	  exit 1; \
	fi

all: build vet lint fmt-check test
