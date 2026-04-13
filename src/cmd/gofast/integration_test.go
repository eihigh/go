// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGoFastBuildCommandEndToEnd(t *testing.T) {
	goroot := runtime.GOROOT()
	goBin := filepath.Join(goroot, "bin", "go")
	gofastBin := filepath.Join(t.TempDir(), "gofast")

	cmd := exec.Command(goBin, "build", "-o", gofastBin, "cmd/gofast")
	cmd.Dir = filepath.Join(goroot, "src")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build gofast: %v\n%s", err, out)
	}

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/e2e\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"ok\")}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outBin := filepath.Join(work, "app")

	env := append([]string{}, os.Environ()...)
	env = append(env, "PATH="+filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "GOROOT="+goroot)
	env = append(env, "CGO_ENABLED=0")
	env = append(env, "XDG_CACHE_HOME="+t.TempDir())

	first := exec.Command(gofastBin, "build", "--explain-cache", "-o", outBin, ".")
	first.Dir = work
	first.Env = env
	firstOut, err := first.CombinedOutput()
	if err != nil {
		t.Fatalf("first gofast build failed: %v\n%s", err, firstOut)
	}

	second := exec.Command(gofastBin, "build", "--explain-cache", "-o", outBin, ".")
	second.Dir = work
	second.Env = env
	secondOut, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("second gofast build failed: %v\n%s", err, secondOut)
	}
	if !strings.Contains(string(secondOut), "cache hit") {
		t.Fatalf("expected cache hit on second build, got:\n%s\n(first build output)\n%s", secondOut, firstOut)
	}

	run := exec.Command(outBin)
	run.Dir = work
	runOut, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("built binary failed: %v\n%s", err, runOut)
	}
	if strings.TrimSpace(string(runOut)) != "ok" {
		t.Fatalf("unexpected binary output: %q", runOut)
	}
}
