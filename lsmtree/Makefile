.PHONY: all test race bench cover fmt vet check clean e2e demo build noassert experiments

all: check

test:
	go test ./...

# -short shrinks the two long root tests (TestEveryFaultLeavesARecoverableStore
# samples fewer faults per op, TestModelLongRun runs fewer steps), which
# are ~10x slower under the race detector. The noassert pass below runs
# their full versions, so check still covers every fault and step.
race:
	go test -race -short ./...

bench:
	go test -run=^$$ -bench=. -benchmem ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

fmt:
	gofmt -w .

vet:
	go vet ./...
	go vet -tags e2e ./e2e
	go vet -tags experiments ./experiments

# Builds lsmctl into ./bin.
build:
	go build -o bin/ ./cmd/...

# Builds and tests with assertions compiled out, to keep that build working.
noassert:
	go vet -tags noassert ./...
	go test -tags noassert ./...

# Fails if any file needs formatting, then vets and runs tests with the race detector.
check: vet race noassert
	@test -z "$$(gofmt -l .)" || (echo "needs gofmt:"; gofmt -l .; exit 1)

# End-to-end suite: the real lsmctl binary, one process per command,
# including a SIGKILL crash test.
e2e:
	go test -tags e2e -count=1 -v ./e2e

# Narrated walkthrough in a temporary store, removed when it ends.
demo:
	./scripts/demo.sh

# Measures the paper's claims and writes CSV + JSON lines to results/<run>/,
# then prints the DuckDB summary tables (DESIGN.md §8). RUN=E3 runs only
# the matching experiments.
experiments:
	../shared/scripts/experiments.sh . "$(RUN)"

clean:
	rm -rf coverage.out bin *.log
