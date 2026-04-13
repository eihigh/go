// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestParseBuildConfig(t *testing.T) {
	t.Parallel()
	cfg := parseBuildConfig([]string{"-race", "-o", "bin/app", "./cmd/app"})
	if filepath.Base(cfg.OutputPath) != "app" {
		t.Fatalf("unexpected output path: %q", cfg.OutputPath)
	}
	if len(cfg.PkgArgs) != 1 || cfg.PkgArgs[0] != "./cmd/app" {
		t.Fatalf("unexpected package args: %#v", cfg.PkgArgs)
	}
}

func TestDisableReasons(t *testing.T) {
	t.Parallel()
	reasons := disableReasons([]string{"-buildmode=plugin", "-ldflags=-linkmode external"}, "1")
	if len(reasons) < 2 {
		t.Fatalf("expected multiple reasons, got %#v", reasons)
	}
}

func TestChooseDecision(t *testing.T) {
	t.Parallel()
	state := &targetState{
		EnvKey:    "env",
		WatchKey:  "watch1",
		StdlibKey: "std1",
		EntryID:   "entry-old",
	}
	in := buildInputs{
		EnvKey:    "env",
		WatchKey:  "watch1",
		StdlibKey: "std1",
		EntryID:   "entry-new",
	}
	d := chooseDecision(state, in)
	if d.stage != "watch" || d.entryID != "entry-old" {
		t.Fatalf("unexpected decision: %#v", d)
	}
	in.WatchKey = "watch2"
	d = chooseDecision(state, in)
	if d.stage != "stdlib" || d.entryID != "entry-new" {
		t.Fatalf("unexpected stage2 decision: %#v", d)
	}
}

func TestIsRelevantFile(t *testing.T) {
	t.Parallel()
	if !isRelevantFile("/repo/go.mod") {
		t.Fatal("go.mod should be relevant")
	}
	if !isRelevantFile("/repo/main.go") {
		t.Fatal(".go should be relevant")
	}
	if isRelevantFile("/repo/README.md") {
		t.Fatal("README.md should be irrelevant")
	}
}

func TestDedupe(t *testing.T) {
	t.Parallel()
	in := []string{"a", "a", "b", "b", "c"}
	out := dedupe(in)
	if !slices.Equal(out, []string{"a", "b", "c"}) {
		t.Fatalf("unexpected output: %#v", out)
	}
}
