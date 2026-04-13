// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type watchOptions struct {
	Build    buildOptions
	Debounce time.Duration
}

func runWatch(ctx context.Context, opts watchOptions) error {
	if _, err := exec.LookPath("fswatch"); err != nil {
		return fmt.Errorf("fswatch is required for watch mode: %w", err)
	}

	root, err := detectProjectRoot()
	if err != nil {
		return err
	}
	if opts.Build.Explain {
		fmt.Fprintf(os.Stderr, "gofast: watch root=%s\n", root)
	}

	if err := runBuild(ctx, opts.Build); err != nil {
		return err
	}

	args := []string{
		"-0",
		"-r",
		"--exclude", `(^|/)\.git(/|$)`,
		"--exclude", `(^|/)vendor(/|$)`,
		"--exclude", `(^|/)(bin|dist|tmp)(/|$)`,
		root,
	}
	cmd := exec.CommandContext(ctx, "fswatch", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Process.Kill()

	events := make(chan string, 64)
	errs := make(chan error, 1)
	go func() {
		errs <- readNullEvents(stdout, events)
	}()

	var timer *time.Timer
	var timerCh <-chan time.Time
	pending := false
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return ctx.Err()
		case err := <-errs:
			if err != nil && err != io.EOF {
				return err
			}
			return cmd.Wait()
		case ev := <-events:
			if !isRelevantFile(ev) {
				continue
			}
			if opts.Build.Explain {
				fmt.Fprintf(os.Stderr, "gofast: event %s\n", ev)
			}
			pending = true
			if timer == nil {
				timer = time.NewTimer(opts.Debounce)
				timerCh = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(opts.Debounce)
			}
		case <-timerCh:
			if pending {
				if err := runBuild(ctx, opts.Build); err != nil {
					fmt.Fprintf(os.Stderr, "gofast: build failed: %v\n", err)
				}
				pending = false
			}
			timerCh = nil
			timer = nil
		}
	}
}

func readNullEvents(r io.Reader, out chan<- string) error {
	br := bufio.NewReader(r)
	for {
		b, err := br.ReadBytes(0)
		if len(b) > 0 {
			out <- string(bytes.TrimSuffix(b, []byte{0}))
		}
		if err != nil {
			return err
		}
	}
}
