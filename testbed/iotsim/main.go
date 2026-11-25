package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: iotsim http|pub|sub [flags]")
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	out := newLineWriter(os.Stdout)
	switch args[0] {
	case "http":
		c, err := parseHTTPFlags(args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "iotsim http:", err)
			return 2
		}
		runHTTP(ctx, out, c.Name, c.URL, c.Interval, c.Timeout)
	case "pub":
		c, err := parsePubFlags(args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "iotsim pub:", err)
			return 2
		}
		runPub(ctx, out, c)
	case "sub":
		c, err := parseSubFlags(args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "iotsim sub:", err)
			return 2
		}
		runSub(ctx, out, c)
	default:
		usage()
		return 2
	}
	return 0
}
