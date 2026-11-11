.PHONY: fmt-check build test vet validate check-lock all

# Everything CI runs, in the order it runs it.
all: fmt-check build vet test validate check-lock

# Fails and lists the files when gofmt would change any, and fails when gofmt
# itself does, as on a file it cannot parse.
fmt-check:
	@unformatted="$$(gofmt -l .)" || exit 1; \
	test -z "$$unformatted" || { echo "$$unformatted"; exit 1; }

build:
	go build ./...

vet:
	go vet ./...

test:
	go test -race ./...

validate:
	go run ./cmd/meshmedic validate

# The gate that fails when an approved entry was edited, or when one was
# removed without its approval being retired.
check-lock:
	go run ./cmd/meshmedic validate --no-drift
