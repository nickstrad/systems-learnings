# Coding approach

How projects in this repo are structured and written. They are learning
projects, written to be read and blogged about, so clarity beats
cleverness. `jumphash/` is the worked example for everything here.

Sources: *Effective Software Testing* (Aniche), chapters 4 and 7, and
*Concurrency in Go* (Cox-Buday), chapters 4 and 5. Research notes are in
`~/AI/writeups/`.

## 1. Domain vs integration

Split every project into two kinds of package.

| Kind | Holds | Rules |
|------|-------|-------|
| **Domain** | The logic: data structures, algorithms, business rules | No I/O. No `net/http`, `os`, `encoding/json`, or logging. Reaches the outside world only through interfaces it defines |
| **Integration** (adapters) | HTTP servers and clients, files, databases, CLIs | Thin. Translates between the outside world and domain types, and validates input |

- **The domain defines the interfaces it needs**, and adapters implement
  them (ports and adapters). Keep interfaces small: only the methods the
  domain calls.
- **Prefer pure functions for decisions.** Example: "given what each node
  holds and the new membership, which keys move?" A pure function has no
  I/O, so you can test it with plain maps. Keep I/O in a thin orchestration
  layer around it.
- **Wiring lives in `cmd/`.** `main` connects the domain to its adapters,
  parses flags, and prints output. Nothing else.
- **Layout follows Go convention.** A reusable library sits at the module
  root. App-only packages go under `internal/`. Binaries go in
  `cmd/<name>/`. Test helpers shared across packages, such as fakes and
  contract suites, go in a `<pkg>test` package (like `httptest`).

## 2. Validation vs contracts

Two different mechanisms for two different problems (Aniche, chapter 4).

| | Validation | Contracts |
|--|------------|-----------|
| **Protects against** | Bad input from users, the network, or files | Bugs in our own code |
| **Where** | At the edges: the domain's public API, HTTP handlers, file loaders | Inside: preconditions, postconditions, invariants |
| **On failure** | Return an error, or a 4xx over HTTP | Panic through `assert.That`, halting before bad data spreads |
| **Tests** | Always tested | Mostly covered by other tests; a few tests show a key check can fail |

- **Validate once, at the edge, then trust the data inside.** Repeat a
  check as a contract only where the contract is worth guarding (Aniche
  4.5.2), for example on an interface any future adapter could break.
- **Never assert on input.** If user input can reach a check, the check
  must return an error. The test: "could a typo on the command line trip
  this?"
- **Write contracts down.** Every function with a non-obvious contract
  documents it:

  ```go
  //  pre:  m.Valid()
  //  post: err == nil ⇒ the node count changed by exactly one
  ```

- **Invariants** go in a `checkInvariant()` method, called at the end of
  every method that changes state. Don't call it on reads. Guard expensive
  checks with `if assert.Enabled { ... }`.
- **Assertions are on by default.** Build with `-tags noassert` to compile
  them out, and test both builds.
- **Contracts on interfaces:**
  - Document each method's pre- and postconditions on the interface.
  - Wrap implementations in a checker that asserts them at runtime.
  - Run one shared **contract test suite** against every implementation,
    real and fake (Liskov substitution).

  An implementation may accept more input than its interface (a weaker
  precondition), and may promise more (a stronger postcondition). It may
  never do the reverse.

## 3. Error handling

Follows Cox-Buday's error propagation (chapter 5).

**Every error a user might see carries:**
1. what happened (the cause)
2. when and where: a UTC time, a stack trace, context such as a URL or path
3. a friendly one-line message
4. how to get more information: a log ID

**Wrap at module boundaries.** Every error that leaves a package's public
API is that package's well-formed error type, e.g.
`errs.Wrap("cluster", err, "cannot add %s, nothing changed: %v", node, err)`.
- `Error()` returns only the message, so each message must make sense on
  its own. Include the cause's message where it helps.
- Say whether anything changed and whether it's safe to retry.
- Support `Unwrap`, so `errors.Is` and `errors.As` still find sentinel
  errors through the wrapping.
- Wrap only at public boundaries, or where you add real context. Don't
  wrap at every call.

**The top level decides between edge case and bug:**
- **The outermost error is well-formed** (wrapped by the module the top
  level talks to): a known edge case. Show the message.
- **Anything else** (a raw error, or a panic such as a failed assertion):
  a bug. Show "something unexpected went wrong; this is a bug", never a
  stack trace.
- Either way, log the full error under a fresh log ID and show the ID.
- Use distinct exit codes: 1 for an error, 2 for bad usage, 70 for a bug.

## 4. Logging

- **Format:** structured JSON lines through `log/slog`, with UTC timestamps.
- **Who logs:** only integration code. Domain code returns errors and
  reports (values that describe what happened); the caller decides what to
  log.
- **What to log:**
  - every error, with its log ID, the whole error chain, and the stack
  - state changes worth an audit trail
  - one line per request on servers
- **Correlate across processes.** A server gives each request an ID and
  returns it in a header. Clients copy it into their errors, so a client's
  log line can be matched to the server's.
- **Failures where they happen.** A component that can't act on an error
  (e.g. an HTTP handler whose client hung up) hands it to the layer that
  logs, instead of dropping or printing it.

## 5. Concurrency

Follows Cox-Buday, chapter 4.

- **Goroutines report; parents decide.** A goroutine sends its result and
  its error together, as one value on one channel (the `Result` pattern).
  Policy, such as "stop at the first error" or "report all", lives in the
  parent.
- **No leaks.** Whoever starts a goroutine makes sure it can finish:
  - pass a `context.Context`, and cancel it when you stop early
  - buffer result channels so a sender never blocks after the reader leaves
  - close channels from a single owner
- **Read every result, even after the first error.** Cancel the rest, but
  keep reading what comes back, so a panic or error behind it isn't lost.
- **Panics don't cross goroutines.** A panic in a goroutine kills the
  process, and `main` can't recover it. Recover inside the goroutine,
  include the panic in the result, and re-panic in the parent.
- **Cancellation:** wire Ctrl-C to the root context with
  `signal.NotifyContext`.

## 6. Data and state changes

For anything that moves or rewrites data:

- **Order the steps so a failure can't lose data.** Copy, then commit,
  then clean up. Each step's error says what state the system is in.
- **Decide on a single source of truth when copies can disagree.** Never
  let a stale copy overwrite a newer one. Delete stale copies; never copy
  them.
- **Write limits down.** If the design isn't safe under some condition,
  such as concurrent writes during a migration, say so in the doc comment
  and the README. Don't leave it implicit.

## 7. Style

- **Comments:** short, plain, and they explain *why*. Write doc comments
  on every exported identifier. Use `pre:`, `post:`, and `Invariant:` lines
  for contracts.
- **Interfaces:** define them where they're used. Avoid package-level
  mutable state; a function variable used as a test seam is the exception.
- **Exported names:** export only what another package needs. To test
  unexported code from an external test package, use `export_test.go`.
- **Before calling a change done:** run `gofmt`, `go vet`, and
  `go test -race`, in both assertion builds.
