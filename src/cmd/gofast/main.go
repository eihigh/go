// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "build":
		if err := runBuildCommand(ctx, os.Args[2:]); err != nil {
			fail(err)
		}
	case "watch":
		if err := runWatchCommand(ctx, os.Args[2:]); err != nil {
			fail(err)
		}
	case "clear":
		if err := clearCache(); err != nil {
			fail(err)
		}
	case "verify-layout":
		ok, details, err := verifyStdlibLayout(ctx)
		if err != nil {
			fail(err)
		}
		if !ok {
			fail(errors.New(details))
		}
		fmt.Fprintln(os.Stderr, details)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: gofast <command> [args]

Commands:
  build [--explain-cache] [go build args...]
  watch [--explain-cache] [--debounce duration] [go build args...]
  clear
  verify-layout
`)
	os.Exit(2)
}

func runBuildCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	explain := fs.Bool("explain-cache", false, "print cache decision details")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runBuild(ctx, buildOptions{
		GoArgs:  fs.Args(),
		Explain: *explain,
	})
}

func runWatchCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	explain := fs.Bool("explain-cache", false, "print cache decision details")
	debounce := fs.Duration("debounce", 400*time.Millisecond, "event debounce interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runWatch(ctx, watchOptions{
		Build: buildOptions{
			GoArgs:  fs.Args(),
			Explain: *explain,
		},
		Debounce: *debounce,
	})
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "gofast: %v\n", err)
	os.Exit(1)
}
