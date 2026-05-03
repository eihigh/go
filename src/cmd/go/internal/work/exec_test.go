// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package work

import (
	"bytes"
	"cmd/go/internal/cfg"
	"cmd/go/internal/load"
	"cmd/internal/objabi"
	"cmd/internal/sys"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEncodeArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arg, want string
	}{
		{"", ""},
		{"hello", "hello"},
		{"hello\n", "hello\\n"},
		{"hello\\", "hello\\\\"},
		{"hello\nthere", "hello\\nthere"},
		{"\\\n", "\\\\\\n"},
	}
	for _, test := range tests {
		if got := encodeArg(test.arg); got != test.want {
			t.Errorf("encodeArg(%q) = %q, want %q", test.arg, got, test.want)
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	t.Parallel()
	tests := []string{
		"",
		"hello",
		"hello\\there",
		"hello\nthere",
		"hello 中国",
		"hello \n中\\国",
	}
	for _, arg := range tests {
		if got := objabi.DecodeArg(encodeArg(arg)); got != arg {
			t.Errorf("objabi.DecodeArg(encodeArg(%q)) = %q", arg, got)
		}
	}
}

func TestEncodeDecodeFuzz(t *testing.T) {
	if testing.Short() {
		t.Skip("fuzz test is slow")
	}
	t.Parallel()

	nRunes := sys.ExecArgLengthLimit + 100
	rBuffer := make([]rune, nRunes)
	buf := bytes.NewBuffer([]byte(string(rBuffer)))

	seed := time.Now().UnixNano()
	t.Logf("rand seed: %v", seed)
	rng := rand.New(rand.NewSource(seed))

	for i := 0; i < 50; i++ {
		// Generate a random string of runes.
		buf.Reset()
		for buf.Len() < sys.ExecArgLengthLimit+1 {
			var r rune
			for {
				r = rune(rng.Intn(utf8.MaxRune + 1))
				if utf8.ValidRune(r) {
					break
				}
			}
			fmt.Fprintf(buf, "%c", r)
		}
		arg := buf.String()

		if got := objabi.DecodeArg(encodeArg(arg)); got != arg {
			t.Errorf("[%d] objabi.DecodeArg(encodeArg(%q)) = %q [seed: %v]", i, arg, got, seed)
		}
	}
}

func TestCollectLinkInputs(t *testing.T) {
	t.Parallel()

	std := &Action{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "fmt", Name: "fmt", Goroot: true, Standard: true}}}
	main := &Action{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "cmd/go", Name: "main", Goroot: true, Standard: true}}}
	toolDep := &Action{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "cmd/internal/objabi", Name: "objabi", Goroot: true, Standard: true}}}
	mod := &Action{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "example.com/mod/pkg", Name: "pkg"}}}

	inputs := collectLinkInputs([]*Action{{Mode: "nop"}, std, main, toolDep, mod})

	if got, want := inputs.all, []*Action{std, main, toolDep, mod}; !slices.Equal(got, want) {
		t.Fatalf("all inputs = %#v, want %#v", got, want)
	}
	if got, want := inputs.baseline, []*Action{std}; !slices.Equal(got, want) {
		t.Fatalf("baseline inputs = %#v, want %#v", got, want)
	}
	if got, want := inputs.overlay, []*Action{main, toolDep, mod}; !slices.Equal(got, want) {
		t.Fatalf("overlay inputs = %#v, want %#v", got, want)
	}
}

func TestLinkBaselineSnapshotIDIgnoresImportPath(t *testing.T) {
	t.Parallel()

	oldBuildmode := ldBuildmode
	oldForced := forcedLdflags
	ldBuildmode = "exe"
	forcedLdflags = nil
	t.Cleanup(func() {
		ldBuildmode = oldBuildmode
		forcedLdflags = oldForced
	})

	b := &Builder{}
	root1 := &Action{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "example.com/cmd/one", Name: "main"}}}
	root2 := &Action{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "example.com/cmd/two", Name: "main"}}}

	id1 := b.linkBaselineSnapshotID(root1)
	id2 := b.linkBaselineSnapshotID(root2)

	if id1 != id2 {
		t.Fatalf("baseline snapshot ids differ for import path only: %x != %x", id1, id2)
	}
}

func TestLinkBaselineSnapshotPathUsesObjdir(t *testing.T) {
	t.Parallel()

	a := &Action{Objdir: "/tmp/work/"}
	got := linkBaselineSnapshotPath(a)
	want := "/tmp/work/linker-baseline-snapshot.gob"
	if got != want {
		t.Fatalf("linkBaselineSnapshotPath = %q, want %q", got, want)
	}
}

func TestCanCacheLinkBaselineSnapshot(t *testing.T) {
	t.Parallel()

	oldBuildmode := cfg.BuildBuildmode
	oldLinkshared := cfg.BuildLinkshared
	cfg.BuildBuildmode = "default"
	cfg.BuildLinkshared = false
	t.Cleanup(func() {
		cfg.BuildBuildmode = oldBuildmode
		cfg.BuildLinkshared = oldLinkshared
	})

	pure := &Action{
		Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "example.com/cmd", Name: "main"}},
		Deps: []*Action{
			{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "fmt", Name: "fmt", Goroot: true, Standard: true}}},
		},
	}
	if !canCacheLinkBaselineSnapshot(pure, []string{"-buildmode=exe"}) {
		t.Fatal("pure internal link unexpectedly disabled")
	}

	withCgo := &Action{
		Package: pure.Package,
		Deps: []*Action{
			{Package: &load.Package{PackagePublic: load.PackagePublic{ImportPath: "runtime/cgo", Name: "cgo", CgoFiles: []string{"cgo.go"}}}},
		},
	}
	if canCacheLinkBaselineSnapshot(withCgo, []string{"-buildmode=exe"}) {
		t.Fatal("cgo link unexpectedly enabled")
	}

	if canCacheLinkBaselineSnapshot(pure, []string{"-linkmode=external"}) {
		t.Fatal("external linkmode unexpectedly enabled")
	}
}
