# Testing approach

How projects in this repo are tested, using the definitions from Aniche's
*Effective Software Testing* (research notes:
`~/AI/writeups/aniche_effective_software_testing.md`). `jumphash/` is the
worked example.

The techniques build on each other:
1. Derive tests from the spec.
2. Use coverage to find what the spec missed.
3. Turn examples into properties and fuzz them.
4. Add a few large tests on top.

## The pyramid

| Level | What runs | Speed | Use for |
|-------|-----------|-------|---------|
| **Unit** | Domain code, with in-memory fakes for its interfaces | ms | All business rules (Aniche: "I favor unit tests as much as possible") |
| **Integration** | One adapter against real infrastructure: an `httptest` server, a temp dir | ms–s | The edges: HTTP encoding, file formats, error wrapping |
| **System / e2e** | The real binaries as separate processes, driven only through their public interface (CLI, HTTP) | seconds | The main and riskiest flows, end to end |

Aniche calls the top level *system tests*; "end-to-end" describes them.
Here, *e2e* means real processes, built with a build tag:

```sh
go test -race ./...               # unit + integration
go test -tags noassert ./...      # the same, with assertions compiled out
go test -tags e2e -v ./e2e        # system / e2e
```

## 1. Specification-based tests (Aniche, chapter 2)

Derive tests from the requirements before looking at the code.

1. Understand the requirement: its inputs and outputs.
2. Explore what the program does with a few inputs.
3. **Find the partitions:** each input on its own, then combinations,
   then outputs. One test per partition.
4. **Find the boundaries.** Test the **on point** (exactly on the
   boundary) and the **off point** (the nearest value that flips the
   outcome). Common boundaries:
   - 0, 1, many
   - empty versus non-empty
   - the size limit, and one over it
5. Write the test cases.
6. Automate them, as table-driven tests where several cases share a shape.
7. Add cases from experience: odd characters in keys, a dependency that
   is down, a retry after a failure.

**A spec for each public operation** names its valid inputs, its error
cases, and its guarantees. Write it in the doc comment or README, then
test each part. Keep these tests black box: build the test from the
documented behaviour, not from the implementation.

## 2. Structural tests (Aniche, chapter 3)

Use coverage to **find** partitions the specification-based tests missed,
then decide whether each one needs a test. Coverage is a tool, not a
target.

**Measure it in three passes:**

```sh
# Unit and integration. -coverpkg counts code exercised from other packages.
go test -coverpkg=./... -coverprofile=unit.out ./...

# e2e: build the binaries with coverage on, and collect what they write.
go test -tags e2e -c -o e2e.test ./e2e
(cd e2e && GOFLAGS=-cover GOCOVERDIR=$PWD/../e2ecov ../e2e.test)
go tool covdata textfmt -i=e2ecov -o=e2e.out
```

Then combine the two profiles: a block counts as covered if either run
covered it. Look at the **uncovered blocks**, not the percentage.

**Two gotchas:**
- A process killed with SIGKILL never writes its coverage data. Stop
  processes gracefully if you want their coverage.
- `go test` sets its own `GOCOVERDIR`. Run the compiled test binary
  directly, as above, so the child processes write to yours.

**What uncovered blocks usually mean:**
- **Error branches:** use a fake to inject the failure.
- **Code that should be impossible:** consider an assertion instead of a
  branch.
- **A check whose failing side never runs:** an assertion that has never
  failed proves nothing. Make sure at least one test can trip it. This is
  what mutation testing measures.

**Criteria:** aim for branch coverage of the domain code. Use MC/DC
(each condition shown to change the outcome on its own) for complex
conditions that matter. Test loops with 0, 1, and many iterations.

## 3. Property-based tests, with Go fuzzing (Aniche, chapter 5)

Instead of hand-picked examples, state a **property** that must hold for
every input, and let the tool search for an input that breaks it.

Go's built-in fuzzer is our property-testing tool:

```go
func FuzzRoundTrip(f *testing.F) {
	f.Add("plain", "v")       // seed corpus: also run by plain `go test`
	f.Add("with/slash", "")
	f.Fuzz(func(t *testing.T, key, value string) {
		if key == "" {
			t.Skip() // precondition: like jqwik's assume
		}
		// ... put then get ...
		if got != value {
			t.Fatalf("round trip lost data: %q -> %q", value, got)
		}
	})
}
```

```sh
go test ./...                                     # runs the seeds only (fast, in CI)
go test -run='^$' -fuzz=FuzzRoundTrip -fuzztime=30s ./pkg
```

**How Go fuzzing maps onto property-based testing:**
- **Generators:** the fuzzer creates and mutates `[]byte`, string, int,
  and bool inputs. For structured inputs, use bytes as a "script" (below).
- **Search:** it is coverage-guided, so it favours inputs that reach new
  code.
- **Shrinking:** it reduces a failing input before reporting it.
- **Regression tests:** a failure is saved under
  `testdata/fuzz/<FuzzName>/`. Commit it, and it then runs with every
  `go test`.

**Kinds of property to look for:**
- **Round trip:** `decode(encode(x)) == x`. Examples: HTTP put then get,
  save then load, escape then unescape.
- **Invariants:** something that must always be true after any operation.
- **Model-based (stateful):** read the fuzz bytes as a script of
  operations (add, remove, put, delete, ...). Run the script against the
  real system and a trivial model, such as a plain map, and check that
  they always agree. This finds bugs in sequences no one thought to write
  by hand. **This is the most valuable kind.**
- **Metamorphic:** a relation between two runs, e.g. "removing a node,
  then adding it back, restores the mapping".

**Assertions are oracles.** With assertions on, every contract in the
code is checked on every fuzzed input, for free (Aniche 4.4).

## 4. Contract tests

When an interface has several implementations (real adapters and test
fakes), write **one** test suite for the interface's contract and run it
against each one. That keeps the fakes honest: a unit test that passes
against a fake then says something real about the system.

## 5. Integration tests

- **Run real infrastructure in-process:** `httptest.NewServer`,
  `t.TempDir()`.
- **Test the adapter's job:** encoding, escaping, status codes, file
  formats, and that every error leaving the adapter is well-formed (right
  module, useful context).
- **Cover the failure paths too:** a server that is down, a corrupt file,
  a failed write.

## 6. System / e2e tests

- **Build the real binaries once** in `TestMain`. Start processes on free
  ports and wait until they're healthy. Clean up with `t.Cleanup`.
- **Drive them only through the public interface,** but check results by
  querying each component directly. Don't trust the system's report about
  itself.
- **Keep them few:** the main flows and the riskiest failure modes, such
  as a dependency that is down, plus a check that errors show a log ID.
- **Check against a model.** One fixed-seed workload, kept in step with a
  plain map, checks the main rule (no lost writes, no stale reads, no
  resurrected deletes) through the real processes. Keep it deterministic:
  random exploring belongs to the domain-level fuzz test, where faults can
  be injected precisely.
- **Make them deterministic:** stable inputs, no sleeps for correctness,
  and statistical checks with tolerances.
- **Log what helps a reader.** Tables written with `t.Log` double as blog
  material.

## Checklist for a new feature

- [ ] Specification-based tests for each partition and boundary
- [ ] Validation errors tested; contracts written down and asserted
- [ ] Coverage checked; every uncovered branch either tested or explained
- [ ] A property or fuzz test, if the feature has a round trip, an invariant, or state
- [ ] A contract-suite run, if the feature adds an interface implementation
- [ ] An e2e flow, only if the feature changes a main or risky user flow
