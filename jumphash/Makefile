.PHONY: all test race bench cover fmt vet check clean e2e demo build noassert

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

# Builds kvnode and kvctl into ./bin.
build:
	go build -o bin/ ./cmd/...

# End-to-end suite: real kvnode processes driven through the kvctl binary.
e2e:
	go test -tags e2e -count=1 -v ./e2e

# Manual walkthrough with 4 nodes on ports 8081-8084.
demo:
	./scripts/demo.sh

# Builds and tests with assertions compiled out, to keep that build working.
noassert:
	go vet -tags noassert ./...
	go test -tags noassert ./...

# Fails if any file needs formatting, then vets and runs tests with the race detector.
check: vet race noassert
	@test -z "$$(gofmt -l .)" || (echo "needs gofmt:"; gofmt -l .; exit 1)

clean:
	rm -rf coverage.out bin *.log
