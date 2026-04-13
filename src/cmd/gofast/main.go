// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
	explain, goArgs, err := parseBuildArgs(args)
	if err != nil {
		return err
	}
	return runBuild(ctx, buildOptions{
		GoArgs:  goArgs,
		Explain: explain,
	})
}

func runWatchCommand(ctx context.Context, args []string) error {
	explain, debounce, goArgs, err := parseWatchArgs(args)
	if err != nil {
		return err
	}
	return runWatch(ctx, watchOptions{
		Build: buildOptions{
			GoArgs:  goArgs,
			Explain: explain,
		},
		Debounce: debounce,
	})
}

func parseBuildArgs(args []string) (explain bool, goArgs []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--":
			return explain, append(goArgs, args[i+1:]...), nil
		case "--explain-cache":
			explain = true
		default:
			goArgs = append(goArgs, a)
		}
	}
	return explain, goArgs, nil
}

func parseWatchArgs(args []string) (explain bool, debounce time.Duration, goArgs []string, err error) {
	debounce = 400 * time.Millisecond
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return explain, debounce, append(goArgs, args[i+1:]...), nil
		case a == "--explain-cache":
			explain = true
		case a == "--debounce":
			if i+1 >= len(args) {
				return false, 0, nil, errors.New("watch: --debounce requires a value")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return false, 0, nil, fmt.Errorf("watch: invalid --debounce value: %w", err)
			}
			debounce = d
			i++
		case strings.HasPrefix(a, "--debounce="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--debounce="))
			if err != nil {
				return false, 0, nil, fmt.Errorf("watch: invalid --debounce value: %w", err)
			}
			debounce = d
		default:
			goArgs = append(goArgs, a)
		}
	}
	return explain, debounce, goArgs, nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "gofast: %v\n", err)
	os.Exit(1)
}
