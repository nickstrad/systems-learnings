.PHONY: all test race vet fmt noassert check e2e demo

all: check

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Builds and tests with assertions compiled out, to keep that build working.
noassert:
	go vet -tags noassert ./...
	go test -tags noassert ./...

# Fails if any file needs formatting, then vets and runs tests with the race detector.
check: vet race noassert
	@test -z "$$(gofmt -l .)" || (echo "needs gofmt:"; gofmt -l .; exit 1)

# shared is a library with no binaries, so it has no e2e suite or demo.
# The targets exist because the root Makefile runs them for every module.
e2e:
	@echo "shared: no e2e suite (library only)"

demo:
	@echo "shared: no demo (library only)"
