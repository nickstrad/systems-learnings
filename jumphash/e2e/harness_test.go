//go:build e2e

// Package e2e runs the real binaries: it starts kvnode processes on free
// ports and drives them only through the kvctl CLI. Assertions check
// placement by asking each node directly what it holds.
//
// Run with: go test -tags e2e -v ./e2e
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nickstrad/systems-learnings/jumphash/internal/kvhttp"
)

var binDir string

// TestMain builds kvnode and kvctl once for the whole suite.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kv-e2e-bin")
	if err != nil {
		panic(err)
	}
	binDir = dir
	for _, name := range []string{"kvnode", "kvctl"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, name), "../cmd/"+name)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(binDir)
	os.Exit(code)
}

// node is one running kvnode process.
type node struct {
	URL string
	cmd *exec.Cmd

	done    chan struct{} // closed once the process has exited
	waitErr error         // result of cmd.Wait; valid after done is closed
}

// watch reaps the process in the background, so Stop and Kill can be called
// any number of times, in any order.
func (n *node) watch() {
	n.done = make(chan struct{})
	go func() {
		n.waitErr = n.cmd.Wait()
		close(n.done)
	}()
}

// Kill stops the process with SIGKILL to simulate a crash. It is a no-op
// once the process has exited.
func (n *node) Kill() {
	n.cmd.Process.Kill() // error means it already exited
	<-n.done
}

// Stop asks the process to shut down with SIGTERM and waits for it to exit,
// falling back to Kill after a timeout. It returns an error if the process
// did not exit with status 0 on its own. A no-op after Kill or Stop.
func (n *node) Stop() error {
	n.cmd.Process.Signal(syscall.SIGTERM) // error means it already exited
	select {
	case <-n.done:
		return n.waitErr
	case <-time.After(10 * time.Second):
		n.Kill()
		return fmt.Errorf("no exit within 10s of SIGTERM; killed")
	}
}

// Contents asks the node directly what it holds, bypassing the cluster.
func (n *node) Contents(t *testing.T) map[string]string {
	t.Helper()
	m, err := kvhttp.NewClient(n.URL).All(context.Background())
	if err != nil {
		t.Fatalf("read %s directly: %v", n.URL, err)
	}
	return m
}

// env is one isolated cluster: its own membership file and its own nodes.
type env struct {
	t       *testing.T
	cluster string
}

func newEnv(t *testing.T) *env {
	return &env{t: t, cluster: filepath.Join(t.TempDir(), "cluster.json")}
}

// startNode launches a kvnode on a free port and waits until it's healthy.
func (e *env) startNode() *node {
	e.t.Helper()
	port := freePort(e.t)
	logs := &bytes.Buffer{}
	cmd := exec.Command(filepath.Join(binDir, "kvnode"), "-addr", "127.0.0.1:"+port, "-quiet")
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	n := &node{URL: "http://127.0.0.1:" + port, cmd: cmd}
	n.watch()
	e.t.Cleanup(func() {
		n.Stop()
		if e.t.Failed() && logs.Len() > 0 {
			e.t.Logf("logs from %s:\n%s", n.URL, logs)
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := kvhttp.NewClient(n.URL).Health(context.Background()); err == nil {
			return n
		} else if time.Now().After(deadline) {
			e.t.Fatalf("node %s never became healthy: %v\n%s", n.URL, err, logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) startNodes(k int) []*node {
	e.t.Helper()
	out := make([]*node, k)
	for i := range out {
		out[i] = e.startNode()
	}
	return out
}

// kvctl runs the CLI and returns stdout. It fails the test on a non-zero exit.
func (e *env) kvctl(args ...string) string {
	e.t.Helper()
	out, err := e.tryKvctl(args...)
	if err != nil {
		e.t.Fatalf("kvctl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// tryKvctl runs the CLI and returns combined output and the exit error, for
// tests that expect failure.
func (e *env) tryKvctl(args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(binDir, "kvctl"), append([]string{"-cluster", e.cluster}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var (
	movedRE     = regexp.MustCompile(`scanned (\d+) keys, moved (\d+)`)
	misplacedRE = regexp.MustCompile(`misplaced=[1-9]`)
)

// addNode and removeNode run the CLI and return (scanned, moved) parsed from its report.
func (e *env) addNode(n *node) (scanned, moved int)    { return e.membership("add-node", n) }
func (e *env) removeNode(n *node) (scanned, moved int) { return e.membership("remove-node", n) }

func (e *env) membership(cmd string, n *node) (int, int) {
	e.t.Helper()
	out := e.kvctl(cmd, n.URL)
	e.t.Logf("$ kvctl %s %s\n%s", cmd, n.URL, strings.TrimSpace(out))
	m := movedRE.FindStringSubmatch(out)
	if m == nil {
		e.t.Fatalf("could not parse report: %q", out)
	}
	s, _ := strconv.Atoi(m[1])
	mv, _ := strconv.Atoi(m[2])
	return s, mv
}

// held is one stored copy of a key: the node holding it, and its value.
type held struct{ node, value string }

// holdings asks each node directly what it holds, so it doesn't depend on
// kvctl's routing. It fails if any key is on two nodes, which would mean a
// clean-up step was missed.
func holdings(t *testing.T, nodes ...*node) map[string]held {
	t.Helper()
	out := map[string]held{}
	for _, n := range nodes {
		for k, v := range n.Contents(t) {
			if prev, dup := out[k]; dup {
				t.Fatalf("key %q is on both %s and %s", k, prev.node, n.URL)
			}
			out[k] = held{node: n.URL, value: v}
		}
	}
	return out
}

// placement maps each key to the URL of the node that holds it.
func placement(t *testing.T, nodes ...*node) map[string]string {
	t.Helper()
	where := map[string]string{}
	for k, h := range holdings(t, nodes...) {
		where[k] = h.node
	}
	return where
}

// assertAllReadable reads every key back through the CLI's routing and
// checks the value. It also runs `kvctl stats` and requires zero misplaced keys.
func (e *env) assertAllReadable(prefix string, count int) {
	e.t.Helper()
	// Spot-check through the CLI binary (one process per call is slow).
	for i := 0; i < count; i += max(1, count/25) {
		k := fmt.Sprintf("%s-%d", prefix, i)
		if got := strings.TrimSpace(e.kvctl("get", k)); got != "value-"+k {
			e.t.Fatalf("get %s = %q", k, got)
		}
	}
	stats := e.kvctl("stats")
	if !strings.Contains(stats, fmt.Sprintf("total keys=%d", count)) {
		e.t.Fatalf("stats total mismatch, want %d:\n%s", count, stats)
	}
	if misplacedRE.MatchString(stats) {
		e.t.Fatalf("misplaced keys after rebalance:\n%s", stats)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func near(got, want int, tol float64) bool {
	d := float64(got - want)
	return d >= -tol*float64(want) && d <= tol*float64(want)
}
