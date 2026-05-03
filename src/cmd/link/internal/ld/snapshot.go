// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"bytes"
	"cmd/internal/bio"
	"cmd/internal/objabi"
	"cmd/link/internal/sym"
	"errors"
	"fmt"
	"internal/buildcfg"
	"io"
	"os"
	"strings"
)

var openLibraryFile = bio.Open

var errSnapshotUnsupported = errors.New("unsupported baseline snapshot object")

type BaselineSnapshotKey struct {
	GOROOT        string
	GOOS          string
	GOARCH        string
	GOEXPERIMENT  string
	BuildMode     BuildMode
	LinkMode      LinkMode
	LinkShared    bool
	HeadType      objabi.HeadType
	InstallSuffix string
	Race          bool
	Msan          bool
	Asan          bool
	Trimpath      bool
	CheckLinkname bool
	StrictDups    int
	PackageData   bool
}

type BaselineSnapshot struct {
	key       BaselineSnapshotKey
	libraries map[string]*baselineLibrarySnapshot
}

type baselineLibrarySnapshot struct {
	file        string
	size        int64
	modUnixNano int64
	objects     []baselineObjectSnapshot
}

type baselineObjectSnapshot struct {
	displayName string
	sourceFile  string
	data        []byte
	readonly    bool
}

func (ctxt *Link) BaselineSnapshotKey() BaselineSnapshotKey {
	return BaselineSnapshotKey{
		GOROOT:        buildcfg.GOROOT,
		GOOS:          buildcfg.GOOS,
		GOARCH:        buildcfg.GOARCH,
		GOEXPERIMENT:  buildcfg.Experiment.String(),
		BuildMode:     ctxt.BuildMode,
		LinkMode:      ctxt.LinkMode,
		LinkShared:    ctxt.linkShared,
		HeadType:      ctxt.HeadType,
		InstallSuffix: *flagInstallSuffix,
		Race:          *flagRace,
		Msan:          *flagMsan,
		Asan:          *flagAsan,
		Trimpath:      buildcfg.GOROOT == "",
		CheckLinkname: *flagCheckLinkname,
		StrictDups:    *FlagStrictDups,
		PackageData:   !*flagG,
	}
}

func (ctxt *Link) SetBaselineSnapshot(snapshot *BaselineSnapshot) {
	ctxt.baseline = snapshot
}

func (ctxt *Link) CaptureBaselineSnapshot() (*BaselineSnapshot, error) {
	snapshot := &BaselineSnapshot{
		key:       ctxt.BaselineSnapshotKey(),
		libraries: make(map[string]*baselineLibrarySnapshot),
	}
	for _, lib := range ctxt.Library {
		if ctxt.packageReuseModeForLibrary(lib) != packageReuseBaseline || lib == nil || lib.Shlib != "" {
			continue
		}
		ls, err := captureBaselineLibrary(lib)
		if err != nil {
			return nil, err
		}
		snapshot.libraries[lib.Pkg] = ls
	}
	return snapshot, nil
}

func (ctxt *Link) tryLoadBaseline(lib *sym.Library) bool {
	if lib == nil || ctxt.packageReuseModeForLibrary(lib) != packageReuseBaseline || ctxt.baseline == nil {
		return false
	}
	if ctxt.baseline.key != ctxt.BaselineSnapshotKey() {
		return false
	}
	ls := ctxt.baseline.libraries[lib.Pkg]
	if ls == nil || !ls.matches(lib.File) {
		return false
	}
	if err := ls.load(ctxt, lib); err != nil {
		if ctxt.Debugvlog > 1 {
			ctxt.Logf("loadlib[baseline]: snapshot miss for %s: %v\n", lib.Pkg, err)
		}
		return false
	}
	if ctxt.Debugvlog > 1 {
		ctxt.Logf("loadlib[baseline]: reused %s from snapshot\n", lib.Pkg)
	}
	return true
}

func captureBaselineLibrary(lib *sym.Library) (*baselineLibrarySnapshot, error) {
	if lib == nil || lib.File == "" {
		return nil, fmt.Errorf("cannot capture empty library")
	}
	f, err := openLibraryFile(lib.File)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.File().Stat()
	if err != nil {
		return nil, err
	}

	snapshot := &baselineLibrarySnapshot{
		file:        lib.File,
		size:        fi.Size(),
		modUnixNano: fi.ModTime().UnixNano(),
	}

	var mag [len(ARMAG)]byte
	if _, err := io.ReadFull(f, mag[:]); err != nil {
		return nil, err
	}
	if string(mag[:]) != ARMAG {
		f.MustSeek(0, 0)
		obj, err := captureBaselineObject(f, lib.File, lib.File, fi.Size())
		if err != nil {
			return nil, err
		}
		snapshot.objects = append(snapshot.objects, obj)
		return snapshot, nil
	}

	var arhdr ArHdr
	off := f.Offset()
	for {
		l := nextar(f, off, &arhdr)
		if l == 0 {
			break
		}
		if l < 0 {
			return nil, fmt.Errorf("%s: malformed archive", lib.File)
		}
		off += l

		if arhdr.name == pkgdef {
			continue
		}
		if len(arhdr.name) < 16 {
			if ext := filepathExt(arhdr.name); ext != ".o" && ext != ".syso" {
				continue
			}
		}

		size := atolwhex(arhdr.size)
		name := fmt.Sprintf("%s(%s)", lib.File, arhdr.name)
		obj, err := captureBaselineObject(f, name, lib.File, size)
		if err != nil {
			return nil, err
		}
		snapshot.objects = append(snapshot.objects, obj)
	}
	return snapshot, nil
}

func captureBaselineObject(f *bio.Reader, displayName, sourceFile string, size int64) (baselineObjectSnapshot, error) {
	data, readonly, err := f.Slice(uint64(size))
	if err != nil {
		return baselineObjectSnapshot{}, err
	}
	return baselineObjectSnapshot{
		displayName: displayName,
		sourceFile:  sourceFile,
		data:        data,
		readonly:    readonly,
	}, nil
}

func (s *baselineLibrarySnapshot) matches(file string) bool {
	if s == nil || file == "" || s.file != file {
		return false
	}
	fi, err := os.Stat(file)
	if err != nil {
		return false
	}
	return fi.Size() == s.size && fi.ModTime().UnixNano() == s.modUnixNano
}

func (s *baselineLibrarySnapshot) load(ctxt *Link, lib *sym.Library) error {
	if s == nil {
		return errors.New("missing snapshot")
	}
	defer func() {
		if objabi.PathToPrefix(lib.Pkg) == "main" && !lib.Main {
			Exitf("%s: not package main", lib.File)
		}
	}()
	for _, obj := range s.objects {
		if err := loadObjectFromSnapshot(ctxt, lib, obj); err != nil {
			return err
		}
	}
	return nil
}

func loadObjectFromSnapshot(ctxt *Link, lib *sym.Library, obj baselineObjectSnapshot) error {
	if len(obj.data) < 4 {
		return errSnapshotUnsupported
	}
	if obj.data[0] != 'g' || obj.data[1] != 'o' || obj.data[2] != ' ' || obj.data[3] != 'o' {
		return errSnapshotUnsupported
	}

	unit := &sym.CompilationUnit{Lib: lib}
	lib.Units = append(lib.Units, unit)

	line, import1, err := parseGoObjectHeader(obj.data)
	if err != nil {
		return err
	}
	if line != wantHdr {
		Errorf("%s: linked object header mismatch:\nhave %q\nwant %q\n", obj.displayName, line, wantHdr)
	}

	import0 := findImportStart(obj.data)
	ldpkgData(ctxt, obj.data[import0:import1-2], lib, obj.displayName)

	fingerprint := ctxt.loader.PreloadFromBytes(ctxt.IncVersion(), obj.data[import1:], obj.readonly, obj.displayName, lib, unit)
	if !fingerprint.IsZero() {
		if lib.Fingerprint.IsZero() {
			lib.Fingerprint = fingerprint
		}
		checkFingerprint(lib, fingerprint, lib.Srcref, lib.Fingerprint)
	}
	addImports(ctxt, lib, obj.displayName)
	return nil
}

func parseGoObjectHeader(data []byte) (line string, import1 int, err error) {
	i := bytes.IndexByte(data, '\n')
	if i < 0 {
		return "", 0, fmt.Errorf("truncated object file")
	}
	line = string(data[:i+1])
	if !strings.HasPrefix(line, "go object ") {
		return "", 0, errSnapshotUnsupported
	}

	markers := 0
	c1, c2, c3 := byte('\n'), data[i+1], byte(0)
	if i+2 < len(data) {
		c3 = data[i+2]
	}
	for j := i + 3; j < len(data); j++ {
		if c1 == '\n' {
			if markers%2 == 0 && c2 == '!' && c3 == '\n' {
				return line, j, nil
			}
			if c2 == '$' && c3 == '$' {
				markers++
			}
		}
		c1, c2 = c2, c3
		c3 = data[j]
	}
	return "", 0, fmt.Errorf("truncated object file")
}

func findImportStart(data []byte) int {
	i := bytes.IndexByte(data, '\n')
	if i < 0 {
		return 0
	}
	return i + 1
}

func ldpkgData(ctxt *Link, data []byte, lib *sym.Library, filename string) {
	if *flagG {
		return
	}
	text := string(data)
	for text != "" {
		var line string
		line, text, _ = strings.Cut(text, "\n")
		if line == "main" {
			lib.Main = true
		}
		if line == "" {
			break
		}
	}
	p0 := strings.Index(text, "\n$$  // cgo")
	var p1 int
	if p0 >= 0 {
		p0 += p1
		i := strings.IndexByte(text[p0+1:], '\n')
		if i < 0 {
			fmt.Fprintf(os.Stderr, "%s: found $$ // cgo but no newline in %s\n", os.Args[0], filename)
			return
		}
		p0 += 1 + i
		p1 = strings.Index(text[p0:], "\n$$")
		if p1 < 0 {
			p1 = strings.Index(text[p0:], "\n!\n")
		}
		if p1 < 0 {
			fmt.Fprintf(os.Stderr, "%s: cannot find end of // cgo section in %s\n", os.Args[0], filename)
			return
		}
		p1 += p0
		loadcgo(ctxt, filename, objabi.PathToPrefix(lib.Pkg), text[p0:p1])
	}
}

func filepathExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		switch name[i] {
		case '/':
			return ""
		case '.':
			return name[i:]
		}
	}
	return ""
}
