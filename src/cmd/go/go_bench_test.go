// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"internal/testenv"
)

func BenchmarkBuildWithAndWithoutCache(b *testing.B) {
	testenv.MustHaveGoBuild(b)
	if testGo == "" {
		b.Skip("go tool is unavailable")
	}

	tmpDir := b.TempDir()
	gopath := filepath.Join(tmpDir, "gopath")
	srcDir := filepath.Join(gopath, "src", "benchbuild")
	libDir := filepath.Join(srcDir, "lib")
	if err := os.MkdirAll(libDir, 0o777); err != nil {
		b.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(`package main
import (
	"benchbuild/lib"
	"fmt"
)
func main() { fmt.Println(lib.Value()) }
`), 0o666); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "lib.go"), []byte(`package lib
import (
	"crypto/sha256"
	"encoding/json"
)
type payload struct {
	A int `+"`json:\"a\"`"+`
	B string `+"`json:\"b\"`"+`
}
func Value() string {
	b, _ := json.Marshal(payload{A: 42, B: "cache"})
	sum := sha256.Sum256(b)
	return string(sum[:])
}
`), 0o666); err != nil {
		b.Fatal(err)
	}

	env := append([]string(nil), os.Environ()...)
	env = setEnv(env, "GOROOT", testGOROOT)
	env = setEnv(env, "GO111MODULE", "off")
	env = setEnv(env, "GOPATH", gopath)
	env = setEnv(env, "GOCACHE", filepath.Join(tmpDir, "gocache"))

	out := filepath.Join(tmpDir, "benchbuild"+exeSuffix)
	runGoCommandBenchmark(b, env, srcDir, "build", "-o", out, "benchbuild")

	b.Run("cache_enabled", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			runGoCommandBenchmark(b, env, srcDir, "build", "-o", out, "benchbuild")
		}
	})

	b.Run("cache_disabled", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			runGoCommandBenchmark(b, env, srcDir, "clean", "-cache")
			b.StartTimer()
			runGoCommandBenchmark(b, env, srcDir, "build", "-o", out, "benchbuild")
		}
	})
}

func runGoCommandBenchmark(b *testing.B, env []string, dir string, args ...string) {
	b.Helper()
	cmd := testenv.Command(b, testGo, args...)
	cmd.Env = env
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("go %v failed: %v\n%s", args, err, out)
	}
}

func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}
