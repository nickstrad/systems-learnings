package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/kvhttp"
	"github.com/nickstrad/systems-learnings/jumphash/internal/memberfile"
)

// run executes one command. It returns errors from module "cluster", or
// from this module (userError) for bad command-line input. Any other error
// is a bug.
func run(ctx context.Context, logger *slog.Logger, out io.Writer, path string, verbose bool, cmd string, args []string) error {
	dial := func(addr string) cluster.Node { return kvhttp.NewClient(addr) }
	c, err := cluster.Open(memberfile.File{Path: path}, dial)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	need := func(n int) error {
		if len(args) != n {
			return userError("%s: want %d argument(s), got %d (see kvctl -h)", cmd, n, len(args))
		}
		return nil
	}

	switch cmd {
	case "add-node", "remove-node":
		if err := need(1); err != nil {
			return err
		}
		change := c.AddNode
		if cmd == "remove-node" {
			change = c.RemoveNode
		}
		start := time.Now()
		rep, err := change(ctx, kvhttp.NormalizeAddr(args[0]))
		if err != nil {
			return err
		}
		took := time.Since(start)
		// Membership changes are the events worth an audit trail.
		logger.Info("membership changed", "node", rep.Node, "scanned", rep.Scanned,
			"moved", len(rep.Moves), "stale_dropped", len(rep.Drops),
			"nodes", c.Nodes(), "duration_ms", took.Milliseconds())
		printReport(out, cmd, rep, verbose, took)

	case "nodes":
		for i, n := range c.Nodes() {
			fmt.Fprintf(out, "%d  %s\n", i, n)
		}

	case "stats":
		total := 0
		for _, s := range c.Stats(ctx) {
			if s.Unreachable != nil {
				fmt.Fprintf(out, "%s  UNREACHABLE: %v\n", s.Node, s.Unreachable)
				logger.Warn("node unreachable", "node", s.Node, "error", s.Unreachable.Error())
				continue
			}
			fmt.Fprintf(out, "%s  keys=%d misplaced=%d\n", s.Node, s.Keys, s.Misplaced)
			total += s.Keys
		}
		fmt.Fprintf(out, "total keys=%d\n", total)

	case "put":
		if err := need(2); err != nil {
			return err
		}
		node, err := c.Put(ctx, args[0], args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "OK (%s)\n", node)

	case "get":
		if err := need(1); err != nil {
			return err
		}
		v, ok, err := c.Get(ctx, args[0])
		if err != nil {
			return err
		}
		if !ok {
			return userError("key %q not found", args[0])
		}
		fmt.Fprintln(out, v)

	case "del":
		if err := need(1); err != nil {
			return err
		}
		if err := c.Delete(ctx, args[0]); err != nil {
			return err
		}
		fmt.Fprintln(out, "OK")

	case "where":
		if err := need(1); err != nil {
			return err
		}
		n, err := c.Owner(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(out, n)

	case "fill":
		if len(args) < 1 || len(args) > 2 {
			return userError("fill: want <n> [prefix] (see kvctl -h)")
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			return userError("fill: %q is not a non-negative number", args[0])
		}
		prefix := "key"
		if len(args) == 2 {
			prefix = args[1]
		}
		for i := range n {
			k := fmt.Sprintf("%s-%d", prefix, i)
			if _, err := c.Put(ctx, k, "value-"+k); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "wrote %d keys\n", n)

	default:
		return userError("unknown command %q (see kvctl -h)", cmd)
	}
	return nil
}

func printReport(out io.Writer, cmd string, rep cluster.Report, verbose bool, took time.Duration) {
	pct := 0.0
	if rep.Scanned > 0 {
		pct = 100 * float64(len(rep.Moves)) / float64(rep.Scanned)
	}
	fmt.Fprintf(out, "%s %s: scanned %d keys, moved %d (%.1f%%) in %s\n",
		cmd, rep.Node, rep.Scanned, len(rep.Moves), pct, took.Round(time.Millisecond))

	from, to := map[string]int{}, map[string]int{}
	for _, m := range rep.Moves {
		from[m.From]++
		to[m.To]++
	}
	for _, n := range slices.Sorted(maps.Keys(from)) {
		fmt.Fprintf(out, "  from %s: %d\n", n, from[n])
	}
	for _, n := range slices.Sorted(maps.Keys(to)) {
		fmt.Fprintf(out, "  to   %s: %d\n", n, to[n])
	}
	if len(rep.Drops) > 0 {
		fmt.Fprintf(out, "  removed %d stale copies left by earlier failures\n", len(rep.Drops))
	}
	if verbose {
		for _, m := range rep.Moves {
			fmt.Fprintf(out, "  %s: %s -> %s\n", m.Key, m.From, m.To)
		}
	}
}
