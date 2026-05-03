// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"cmd/internal/sys"
	"cmd/link/internal/loader"
	"cmd/link/internal/sym"
	"internal/buildcfg"
	"internal/testenv"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type loadobjSnapshot struct {
	unitFiles [][]string
	syms      []loadedSym
}

type loadedSym struct {
	name    string
	version int
	kind    sym.SymKind
}

func TestArchiveTemplateReplayMatchesLoadobjfile(t *testing.T) {
	testenv.MustHaveGoBuild(t)

	archive, pkg := buildArchiveTemplateTestPackage(t)
	lib := &sym.Library{File: archive, Pkg: pkg}

	wantCtxt := newLoadcacheTestContext(t)
	loadobjfile(wantCtxt, lib)
	want := snapshotLoadedLibrary(t, wantCtxt, lib)

	tmpl, ok := readArchiveTemplate(archive)
	if !ok {
		t.Fatalf("readArchiveTemplate(%q) = !ok", archive)
	}

	gotCtxt := newLoadcacheTestContext(t)
	gotLib := &sym.Library{File: archive, Pkg: pkg}
	loadobjfileTemplate(gotCtxt, gotLib, tmpl)
	got := snapshotLoadedLibrary(t, gotCtxt, gotLib)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadobjfile template replay mismatch:\nwant %#v\ngot  %#v", want, got)
	}
}

func BenchmarkLoadobjfileGOROOTArchiveCache(b *testing.B) {
	stdlibDir := filepath.Join(buildcfg.GOROOT, "pkg", runtime.GOOS+"_"+runtime.GOARCH)
	archive := filepath.Join(stdlibDir, "fmt.a")
	if _, err := os.Stat(archive); err != nil {
		b.Skipf("missing std archive %s: %v", archive, err)
	}

	clearGOROOTArchiveTemplateCacheForTest()
	ctxt := newLoadcacheBenchmarkContext(b, stdlibDir)
	if !maybeLoadArchiveTemplate(ctxt, &sym.Library{File: archive, Pkg: "fmt"}) {
		b.Fatalf("expected cache hit for %s", archive)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctxt := newLoadcacheBenchmarkContext(b, stdlibDir)
		if !maybeLoadArchiveTemplate(ctxt, &sym.Library{File: archive, Pkg: "fmt"}) {
			b.Fatalf("expected cache hit for %s", archive)
		}
	}
}

func buildArchiveTemplateTestPackage(t testing.TB) (archive, pkg string) {
	t.Helper()

	dir := t.TempDir()
	pkg = "example.com/loadcache"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+pkg+"\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const src = "package loadcache\n\nfunc F() int { return 42 }\n"
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	archive = filepath.Join(dir, "loadcache.a")
	cmd := testenv.Command(t, testenv.GoToolPath(t), "build", "-buildmode=archive", "-o", archive, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOENV=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building archive: %v\n%s", err, out)
	}
	return archive, pkg
}

func newLoadcacheTestContext(t testing.TB) *Link {
	t.Helper()
	ctxt := linknew(currentLoadcacheTestArch(t))
	ctxt.loader = loader.NewLoader(0, &ctxt.ErrorReporter.ErrorReporter)
	ctxt.ErrorReporter.SymName = func(s loader.Sym) string { return ctxt.loader.SymName(s) }
	return ctxt
}

func newLoadcacheBenchmarkContext(tb testing.TB, libdir string) *Link {
	tb.Helper()
	ctxt := newLoadcacheTestContext(tb)
	ctxt.Libdir = []string{libdir}
	return ctxt
}

func currentLoadcacheTestArch(t testing.TB) *sys.Arch {
	t.Helper()
	for _, arch := range sys.Archs {
		if arch.Name == buildcfg.GOARCH {
			return arch
		}
	}
	t.Fatalf("unsupported GOARCH %q", buildcfg.GOARCH)
	return nil
}

func snapshotLoadedLibrary(t testing.TB, ctxt *Link, lib *sym.Library) loadobjSnapshot {
	t.Helper()
	ctxt.loader.LoadSyms(ctxt.Arch)
	var snap loadobjSnapshot
	for _, unit := range lib.Units {
		snap.unitFiles = append(snap.unitFiles, append([]string(nil), unit.FileTable...))
	}
	for s := loader.Sym(1); s < loader.Sym(ctxt.loader.NSym()); s++ {
		snap.syms = append(snap.syms, loadedSym{
			name:    ctxt.loader.SymName(s),
			version: ctxt.loader.SymVersion(s),
			kind:    ctxt.loader.SymType(s),
		})
	}
	return snap
}
