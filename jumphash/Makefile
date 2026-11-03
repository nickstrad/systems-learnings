.PHONY: all test race bench cover fmt vet check clean e2e demo build noassert experiments

all: check

test:
	go test ./...

race:
	go test -race ./...

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

# Builds kvnode and kvctl into ./bin.
build:
	go build -o bin/ ./cmd/...

# End-to-end suite: real kvnode processes driven through the kvctl binary.
e2e:
	go test -tags e2e -count=1 -v ./e2e

# Manual walkthrough with 4 nodes on ports 8081-8084.
demo:
	./scripts/demo.sh

# Measures the paper's claims (experiments/, -tags experiments): CSVs to
# results/<run>/, then the DuckDB tables in scripts/experiments.sql.
# RUN=H1 runs only the matching experiments.
experiments:
	../shared/scripts/experiments.sh . "$(RUN)"

# Builds and tests with assertions compiled out, to keep that build working.
noassert:
	go vet -tags noassert ./...
	go vet -tags 'experiments noassert' ./experiments
	go test -tags noassert ./...

# Fails if any file needs formatting, then vets and runs tests with the race detector.
check: vet race noassert
	@test -z "$$(gofmt -l .)" || (echo "needs gofmt:"; gofmt -l .; exit 1)

clean:
	rm -rf coverage.out bin *.log
