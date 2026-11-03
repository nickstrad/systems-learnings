//go:build e2e

// Package e2e runs the real lsmctl binary against stores on disk. Every
// command is a separate process, so each test also checks that data
// persists across opens. Where a test needs ground truth, it opens the
// store with the lsmtree library directly instead of trusting lsmctl's
// report about itself.
//
// Run with: go test -tags e2e -v ./e2e
package e2e

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
)

var binDir string

// TestMain builds lsmctl once for the whole suite.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lsm-e2e-bin")
	if err != nil {
		panic(err)
	}
	binDir = dir
	cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, "lsmctl"), "../cmd/lsmctl")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build lsmctl: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(binDir)
	os.Exit(code)
}

var logIDRE = regexp.MustCompile(`\[log ID [0-9a-f]{8}\]`)

// env is one isolated store: <root>/data, with lsmctl.log beside it.
type env struct {
	t    *testing.T
	root string
	// opts is the store's tuning. Every command gets it as flags, and
	// inspect opens the store with it, so both see the same targets.
	opts lsmtree.Options
}

func newEnv(t *testing.T, opts lsmtree.Options) *env {
	return &env{t: t, root: t.TempDir(), opts: opts}
}

func (e *env) dir() string { return filepath.Join(e.root, "data") }

// flags turns e.opts into lsmctl's global tuning flags. Zero fields are
// left out, so lsmctl and the library fill the same defaults.
func (e *env) flags() []string {
	var f []string
	add := func(name string, v int64) {
		if v != 0 {
			f = append(f, name, fmt.Sprint(v))
		}
	}
	o := e.opts
	add("-memtable", o.MemtableSize)
	add("-block", int64(o.BlockSize))
	add("-table", o.TableSize)
	add("-bloom", int64(o.BloomBitsPerKey))
	add("-l0-trigger", int64(o.L0CompactionTrigger))
	add("-base", o.BaseLevelSize)
	add("-ratio", int64(o.LevelSizeRatio))
	if o.NoBloom {
		f = append(f, "-no-bloom")
	}
	if o.MaxLevels != 0 || o.LevelTargets != nil || o.SyncWrites || o.FS != nil || o.Events != nil {
		e.t.Fatalf("env options %+v have no lsmctl flag", o)
	}
	return f
}

func (e *env) command(args ...string) *exec.Cmd {
	argv := append([]string{"-dir", e.dir()}, e.flags()...)
	return exec.Command(filepath.Join(binDir, "lsmctl"), append(argv, args...)...)
}

// lsmctl runs the CLI and returns stdout. It fails the test on a non-zero
// exit.
func (e *env) lsmctl(args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("lsmctl %s: exit %d\n%s%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// run runs the CLI and returns stdout, stderr, and the exit code, for
// tests that expect failure.
func (e *env) run(args ...string) (stdout, stderr string, code int) {
	e.t.Helper()
	cmd := e.command(args...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if _, exited := err.(*exec.ExitError); err != nil && !exited {
		e.t.Fatalf("lsmctl %s: %v", strings.Join(args, " "), err)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

// wantError runs a command that must fail as a known edge case: exit 1,
// a message containing want, a log ID, and no bug report or stack.
func (e *env) wantError(want string, args ...string) {
	e.t.Helper()
	_, stderr, code := e.run(args...)
	if code != 1 || !strings.Contains(stderr, want) || !logIDRE.MatchString(stderr) ||
		strings.Contains(stderr, "bug") || strings.Contains(stderr, "goroutine") {
		e.t.Fatalf("lsmctl %s: exit %d, stderr %q; want exit 1, %q, and a log ID",
			strings.Join(args, " "), code, stderr, want)
	}
}

// scan reads the whole store through the CLI into a map.
func (e *env) scan() map[string]string {
	e.t.Helper()
	m := map[string]string{}
	for _, line := range lines(e.lsmctl("scan")) {
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			e.t.Fatalf("scan line without a tab: %q", line)
		}
		m[k] = v
	}
	return m
}

// inspect opens the store with the library, outside lsmctl, and returns
// its stats. It uses the same options as the commands, so level targets
// match. The store must not be in use.
func (e *env) inspect() lsmtree.Stats {
	e.t.Helper()
	db, err := lsmtree.Open(e.dir(), e.opts)
	if err != nil {
		e.t.Fatalf("open the store directly: %v", err)
	}
	defer db.Close()
	return db.Stats()
}

// loader is a running `lsmctl -sync load`, streaming acknowledged keys.
type loader struct {
	cmd    *exec.Cmd
	acks   chan string   // each acknowledged key, in order
	done   chan struct{} // closed when stdout reaches EOF
	stderr *strings.Builder
}

// startLoad starts `lsmctl -sync load <n> args...` and streams the keys
// it prints. Only whole lines count: a key is acknowledged once its
// newline is out.
func (e *env) startLoad(n int, args ...string) *loader {
	e.t.Helper()
	cmd := e.command(append([]string{"-sync", "load", fmt.Sprint(n)}, args...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		e.t.Fatal(err)
	}
	l := &loader{cmd: cmd, acks: make(chan string, 1<<16), done: make(chan struct{}), stderr: &strings.Builder{}}
	cmd.Stderr = l.stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() {
		defer close(l.done)
		r := bufio.NewReader(out)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return // EOF; a partial last line was never acknowledged
			}
			if strings.HasPrefix(line, "key-") {
				l.acks <- strings.TrimSuffix(line, "\n")
			}
		}
	}()
	e.t.Cleanup(func() { l.kill() })
	return l
}

// waitAcks returns the first n acknowledged keys, failing the test if
// the process ends first.
func (l *loader) waitAcks(t *testing.T, n int) []string {
	t.Helper()
	var keys []string
	for len(keys) < n {
		select {
		case k := <-l.acks:
			keys = append(keys, k)
		case <-l.done:
			t.Fatalf("load ended after %d acks, before %d:\n%s", len(keys), n, l.stderr)
		}
	}
	return keys
}

// kill sends SIGKILL and returns every key acknowledged before it died,
// including any still in the pipe. A no-op after the process has exited.
func (l *loader) kill() []string {
	l.cmd.Process.Kill() // an error means it already exited
	<-l.done             // read to EOF first: Wait closes the pipe
	l.cmd.Wait()
	var rest []string
	for {
		select {
		case k := <-l.acks:
			rest = append(rest, k)
		default:
			return rest
		}
	}
}

// loadValue mirrors lsmctl's load: "<key>@<seed>" padded with '.' to at
// least size bytes. It is repeated here so the test checks values
// independently of the binary.
func loadValue(key string, seed uint64, size int) string {
	v := fmt.Sprintf("%s@%d", key, seed)
	if len(v) < size {
		v += strings.Repeat(".", size-len(v))
	}
	return v
}

// lines splits output into lines, dropping the final newline.
func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
