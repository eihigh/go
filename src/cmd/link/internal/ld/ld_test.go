// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"bytes"
	"debug/pe"
	"encoding/json"
	"fmt"
	"internal/testenv"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUndefinedRelocErrors(t *testing.T) {
	testenv.MustHaveGoBuild(t)

	// When external linking, symbols may be defined externally, so we allow
	// undefined symbols and let external linker resolve. Skip the test.
	//
	// N.B. go build below explictly doesn't pass through
	// -asan/-msan/-race, so we don't care about those.
	testenv.MustInternalLink(t, testenv.NoSpecialBuildTypes)

	t.Parallel()

	out, err := testenv.Command(t, testenv.GoToolPath(t), "build", "./testdata/issue10978").CombinedOutput()
	if err == nil {
		t.Fatal("expected build to fail")
	}

	wantErrors := map[string]int{
		// Main function has dedicated error message.
		"function main is undeclared in the main package": 1,

		// Single error reporting per each symbol.
		// This way, duplicated messages are not reported for
		// multiple relocations with a same name.
		"main.defined1: relocation target main.undefined not defined": 1,
		"main.defined2: relocation target main.undefined not defined": 1,
	}
	unexpectedErrors := map[string]int{}

	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "#") || l == "" {
			continue
		}
		matched := ""
		for want := range wantErrors {
			if strings.Contains(l, want) {
				matched = want
				break
			}
		}
		if matched != "" {
			wantErrors[matched]--
		} else {
			unexpectedErrors[l]++
		}
	}

	for want, n := range wantErrors {
		switch {
		case n > 0:
			t.Errorf("unmatched error: %s (x%d)", want, n)
		case n < 0:
			if runtime.GOOS == "android" && runtime.GOARCH == "arm64" {
				testenv.SkipFlaky(t, 58807)
			}
			t.Errorf("extra errors: %s (x%d)", want, -n)
		}
	}
	for unexpected, n := range unexpectedErrors {
		t.Errorf("unexpected error: %s (x%d)", unexpected, n)
	}
}

func TestParseCachedArchive(t *testing.T) {
	testenv.MustHaveGoBuild(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cachetest\n\ngo 1.24\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte("package cachetest\n\nfunc F() int { return 1 }\n"), 0666); err != nil {
		t.Fatal(err)
	}
	pkgfile := filepath.Join(dir, "cachetest.a")
	cmd := testenv.Command(t, testenv.GoToolPath(t), "build", "-buildmode=archive", "-o", pkgfile, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	archive, ok := parseCachedArchive(pkgfile)
	if !ok {
		t.Fatalf("parseCachedArchive(%q) failed", pkgfile)
	}
	if len(archive.members) == 0 {
		t.Fatalf("parseCachedArchive(%q) returned no members", pkgfile)
	}
}

func TestDebugLinkServer(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)

	dir := t.TempDir()
	stateFile := filepath.Join(dir, "linkserver.json")
	goTool := testenv.GoToolPath(t)
	writeDebugLinkServerModule(t, dir)
	t.Cleanup(func() {
		cleanupDebugLinkServer(t, stateFile)
	})
	writeDebugLinkServerProgram(t, dir, "hello")
	buildDebugLinkServerProgram(t, goTool, dir, stateFile, filepath.Join(dir, "hello.exe"))
	state1 := readLinkServiceStateForTest(t, stateFile)
	if state1.Addr == "" || state1.PID == 0 {
		t.Fatalf("unexpected link server state after first build: %+v", state1)
	}
	buildDebugLinkServerProgram(t, goTool, dir, stateFile, filepath.Join(dir, "hello.exe"))
	state2 := readLinkServiceStateForTest(t, stateFile)
	if state1 != state2 {
		t.Fatalf("link server state changed across rebuilds: before=%+v after=%+v", state1, state2)
	}

	resp, err := http.Get(state1.Addr + "/healthz")
	if err != nil {
		t.Fatalf("link server health check failed: %v", err)
	}
	resp.Body.Close()
}

func TestDebugLinkServerRebuildAfterEdit(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)

	dir := t.TempDir()
	stateFile := filepath.Join(dir, "linkserver.json")
	exe := filepath.Join(dir, "hello.exe")
	goTool := testenv.GoToolPath(t)
	writeDebugLinkServerModule(t, dir)
	t.Cleanup(func() {
		cleanupDebugLinkServer(t, stateFile)
	})

	writeDebugLinkServerProgram(t, dir, "hello")
	buildDebugLinkServerProgram(t, goTool, dir, stateFile, exe)
	if got, want := runDebugLinkServerProgram(t, exe), "hello\n"; got != want {
		t.Fatalf("unexpected program output after first build: got %q want %q", got, want)
	}

	writeDebugLinkServerProgram(t, dir, "hello again")
	buildDebugLinkServerProgram(t, goTool, dir, stateFile, exe)
	state := readLinkServiceStateForTest(t, stateFile)
	if state.Addr == "" || state.PID == 0 {
		t.Fatalf("unexpected link server state after rebuild: %+v", state)
	}
	if got, want := runDebugLinkServerProgram(t, exe), "hello again\n"; got != want {
		t.Fatalf("unexpected program output after rebuild: got %q want %q", got, want)
	}
}

func BenchmarkDebugLinkServerRebuildAfterEdit(b *testing.B) {
	testenv.MustHaveGoBuild(b)
	testenv.MustHaveExec(b)

	dir := b.TempDir()
	stateFile := filepath.Join(dir, "linkserver.json")
	exe := filepath.Join(dir, "hello.exe")
	goTool := testenv.GoToolPath(b)
	writeDebugLinkServerModule(b, dir)
	b.Cleanup(func() {
		cleanupDebugLinkServer(b, stateFile)
	})

	message := "hello 0"
	writeDebugLinkServerProgram(b, dir, message)
	buildDebugLinkServerProgram(b, goTool, dir, stateFile, exe)
	state := readLinkServiceStateForTest(b, stateFile)
	if state.Addr == "" || state.PID == 0 {
		b.Fatalf("unexpected link server state after initial build: %+v", state)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		message = fmt.Sprintf("hello %d", i+1)
		writeDebugLinkServerProgram(b, dir, message)
		b.StartTimer()
		buildDebugLinkServerProgram(b, goTool, dir, stateFile, exe)
	}
	b.StopTimer()

	if got, want := runDebugLinkServerProgram(b, exe), message+"\n"; got != want {
		b.Fatalf("unexpected program output after benchmark rebuilds: got %q want %q", got, want)
	}
}

type debugLinkServerState struct {
	Addr string `json:"addr"`
	PID  int    `json:"pid"`
}

func readLinkServiceStateForTest(tb testing.TB, file string) debugLinkServerState {
	tb.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		tb.Fatal(err)
	}
	var state debugLinkServerState
	if err := json.Unmarshal(data, &state); err != nil {
		tb.Fatal(err)
	}
	return state
}

func writeDebugLinkServerModule(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/linkserver\n\ngo 1.24\n"), 0666); err != nil {
		tb.Fatal(err)
	}
}

func writeDebugLinkServerProgram(tb testing.TB, dir, message string) {
	tb.Helper()
	source := fmt.Sprintf("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(%q) }\n", message)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0666); err != nil {
		tb.Fatal(err)
	}
}

func buildDebugLinkServerProgram(tb testing.TB, goTool, dir, stateFile, exe string) {
	tb.Helper()
	cmd := testenv.Command(tb, goTool, "build", "-debug-linkserver="+stateFile, "-o", exe, ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("go build failed: %v\n%s", err, out)
	}
}

func runDebugLinkServerProgram(tb testing.TB, exe string) string {
	tb.Helper()
	cmd := testenv.Command(tb, exe)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("running %s failed: %v\n%s", exe, err, out)
	}
	return string(out)
}

func cleanupDebugLinkServer(tb testing.TB, stateFile string) {
	tb.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return
	}
	var state debugLinkServerState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}
	if proc, err := os.FindProcess(state.PID); err == nil {
		_ = proc.Kill()
	}
}

const carchiveSrcText = `
package main

//export GoFunc
func GoFunc() {
	println(42)
}

func main() {
}
`

func TestArchiveBuildInvokeWithExec(t *testing.T) {
	t.Parallel()
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveCGO(t)

	// run this test on just a small set of platforms (no need to test it
	// across the board given the nature of the test).
	pair := runtime.GOOS + "-" + runtime.GOARCH
	switch pair {
	case "darwin-amd64", "darwin-arm64", "linux-amd64", "freebsd-amd64":
	default:
		t.Skip("no need for test on " + pair)
	}
	switch runtime.GOOS {
	case "openbsd", "windows":
		t.Skip("c-archive unsupported")
	}
	dir := t.TempDir()

	srcfile := filepath.Join(dir, "test.go")
	arfile := filepath.Join(dir, "test.a")
	if err := os.WriteFile(srcfile, []byte(carchiveSrcText), 0666); err != nil {
		t.Fatal(err)
	}

	ldf := fmt.Sprintf("-ldflags=-v -tmpdir=%s", dir)
	argv := []string{"build", "-buildmode=c-archive", "-o", arfile, ldf, srcfile}
	out, err := testenv.Command(t, testenv.GoToolPath(t), argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("build failure: %s\n%s\n", err, string(out))
	}

	found := false
	const want = "invoking archiver with syscall.Exec"
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, want) {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected '%s' in -v output, got:\n%s\n", want, string(out))
	}
}

func TestLargeTextSectionSplitting(t *testing.T) {
	switch runtime.GOARCH {
	case "ppc64", "ppc64le", "arm":
	case "arm64":
		if runtime.GOOS == "darwin" {
			break
		}
		fallthrough
	default:
		t.Skipf("text section splitting is not done in %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	testenv.MustHaveGoBuild(t)
	testenv.MustHaveCGO(t)
	t.Parallel()
	dir := t.TempDir()

	// NB: the use of -ldflags=-debugtextsize=1048576 tells the linker to
	// split text sections at a size threshold of 1M instead of the
	// architected limit of 67M or larger. The choice of building cmd/go
	// is arbitrary; we just need something sufficiently large that uses
	// external linking.
	exe := filepath.Join(dir, "go.exe")
	out, err := testenv.Command(t, testenv.GoToolPath(t), "build", "-o", exe, "-ldflags=-linkmode=external -debugtextsize=1048576", "cmd/go").CombinedOutput()
	if err != nil {
		t.Fatalf("build failure: %s\n%s\n", err, string(out))
	}

	// Check that we did split text sections.
	out, err = testenv.Command(t, testenv.GoToolPath(t), "tool", "nm", exe).CombinedOutput()
	if err != nil {
		t.Fatalf("nm failure: %s\n%s\n", err, string(out))
	}
	if !bytes.Contains(out, []byte("runtime.text.1")) {
		t.Errorf("runtime.text.1 not found, text section not split?")
	}

	// Result should be runnable.
	_, err = testenv.Command(t, exe, "version").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsBuildmodeCSharedASLR(t *testing.T) {
	platform := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	switch platform {
	case "windows/amd64", "windows/386":
	default:
		t.Skip("skipping windows amd64/386 only test")
	}

	testenv.MustHaveCGO(t)

	t.Run("aslr", func(t *testing.T) {
		testWindowsBuildmodeCSharedASLR(t, true)
	})
	t.Run("no-aslr", func(t *testing.T) {
		testWindowsBuildmodeCSharedASLR(t, false)
	})
}

func testWindowsBuildmodeCSharedASLR(t *testing.T, useASLR bool) {
	t.Parallel()
	testenv.MustHaveGoBuild(t)

	dir := t.TempDir()

	srcfile := filepath.Join(dir, "test.go")
	objfile := filepath.Join(dir, "test.dll")
	if err := os.WriteFile(srcfile, []byte(`package main; func main() { print("hello") }`), 0666); err != nil {
		t.Fatal(err)
	}
	argv := []string{"build", "-buildmode=c-shared"}
	if !useASLR {
		argv = append(argv, "-ldflags", "-aslr=false")
	}
	argv = append(argv, "-o", objfile, srcfile)
	out, err := testenv.Command(t, testenv.GoToolPath(t), argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("build failure: %s\n%s\n", err, string(out))
	}

	f, err := pe.Open(objfile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var dc uint16
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		dc = oh.DllCharacteristics
	case *pe.OptionalHeader64:
		dc = oh.DllCharacteristics
		hasHEVA := (dc & pe.IMAGE_DLLCHARACTERISTICS_HIGH_ENTROPY_VA) != 0
		if useASLR && !hasHEVA {
			t.Error("IMAGE_DLLCHARACTERISTICS_HIGH_ENTROPY_VA flag is not set")
		} else if !useASLR && hasHEVA {
			t.Error("IMAGE_DLLCHARACTERISTICS_HIGH_ENTROPY_VA flag should not be set")
		}
	default:
		t.Fatalf("unexpected optional header type of %T", f.OptionalHeader)
	}
	hasASLR := (dc & pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE) != 0
	if useASLR && !hasASLR {
		t.Error("IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE flag is not set")
	} else if !useASLR && hasASLR {
		t.Error("IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE flag should not be set")
	}
}

// TestMemProfileCheck tests that cmd/link sets
// runtime.disableMemoryProfiling if the runtime.MemProfile
// symbol is unreachable after deadcode (and not dynlinking).
// The runtime then uses that to set the default value of
// runtime.MemProfileRate, which this test checks.
func TestMemProfileCheck(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	t.Parallel()

	tests := []struct {
		name    string
		prog    string
		wantOut string
	}{
		{
			"no_memprofile",
			`
package main
import "runtime"
func main() {
	println(runtime.MemProfileRate)
}
`,
			"0",
		},
		{
			"with_memprofile",
			`
package main
import "runtime"
func main() {
	runtime.MemProfile(nil, false)
	println(runtime.MemProfileRate)
}
`,
			"524288",
		},
		{
			"with_memprofile_indirect",
			`
package main
import "runtime"
var f = runtime.MemProfile
func main() {
	if f == nil {
		panic("no f")
	}
	println(runtime.MemProfileRate)
}
`,
			"524288",
		},
		{
			"with_memprofile_runtime_pprof",
			`
package main
import "runtime"
import "runtime/pprof"
func main() {
	_ = pprof.Profiles()
	println(runtime.MemProfileRate)
}
`,
			"524288",
		},
		{
			"with_memprofile_runtime_pprof_writeheap",
			`
package main
import "io"
import "runtime"
import "runtime/pprof"
func main() {
	_ = pprof.WriteHeapProfile(io.Discard)
	println(runtime.MemProfileRate)
}
`,
			"524288",
		},
		{
			"with_memprofile_runtime_pprof_lookupheap",
			`
package main
import "runtime"
import "runtime/pprof"
func main() {
	_ = pprof.Lookup("heap")
	println(runtime.MemProfileRate)
}
`,
			"524288",
		},
		{
			"with_memprofile_http_pprof",
			`
package main
import "runtime"
import _ "net/http/pprof"
func main() {
	println(runtime.MemProfileRate)
}
`,
			"524288",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			src := filepath.Join(tempDir, "x.go")
			if err := os.WriteFile(src, []byte(tt.prog), 0644); err != nil {
				t.Fatal(err)
			}
			cmd := testenv.Command(t, testenv.GoToolPath(t), "run", src)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(string(out))
			if got != tt.wantOut {
				t.Errorf("got %q; want %q", got, tt.wantOut)
			}
		})
	}
}

func TestRISCVTrampolines(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	t.Parallel()

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "x.s")

	// Calling b from a or c should not use trampolines, however
	// calling from d to a will require one.
	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "TEXT a(SB),$0-0\n")
	for i := 0; i < 1<<17; i++ {
		fmt.Fprintf(buf, "\tADD $0, X0, X0\n")
	}
	fmt.Fprintf(buf, "\tCALL b(SB)\n")
	fmt.Fprintf(buf, "\tRET\n")
	fmt.Fprintf(buf, "TEXT b(SB),$0-0\n")
	fmt.Fprintf(buf, "\tRET\n")
	fmt.Fprintf(buf, "TEXT c(SB),$0-0\n")
	fmt.Fprintf(buf, "\tCALL b(SB)\n")
	fmt.Fprintf(buf, "\tRET\n")
	fmt.Fprintf(buf, "TEXT ·d(SB),0,$0-0\n")
	for i := 0; i < 1<<17; i++ {
		fmt.Fprintf(buf, "\tADD $0, X0, X0\n")
	}
	fmt.Fprintf(buf, "\tCALL a(SB)\n")
	fmt.Fprintf(buf, "\tCALL c(SB)\n")
	fmt.Fprintf(buf, "\tRET\n")
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to write assembly file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module riscvtramp"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v\n", err)
	}
	main := `package main
func main() {
	d()
}

func d()
`
	if err := os.WriteFile(filepath.Join(tmpDir, "x.go"), []byte(main), 0644); err != nil {
		t.Fatalf("failed to write main: %v\n", err)
	}
	cmd := testenv.Command(t, testenv.GoToolPath(t), "build", "-ldflags=-linkmode=internal")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "GOARCH=riscv64", "GOOS=linux")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Build failed: %v, output: %s", err, out)
	}

	// Check what trampolines exist.
	cmd = testenv.Command(t, testenv.GoToolPath(t), "tool", "nm", filepath.Join(tmpDir, "riscvtramp"))
	cmd.Env = append(os.Environ(), "GOARCH=riscv64", "GOOS=linux")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nm failure: %s\n%s\n", err, string(out))
	}
	if !bytes.Contains(out, []byte(" T a-tramp0")) {
		t.Errorf("Trampoline a-tramp0 is missing")
	}
	if bytes.Contains(out, []byte(" T b-tramp0")) {
		t.Errorf("Trampoline b-tramp0 exists unnecessarily")
	}
}
