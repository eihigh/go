// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"bytes"
	"cmd/internal/bio"
	"cmd/internal/goobj"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cmd/link/internal/sym"
)

type archiveTemplate struct {
	objects       []objectTemplate
	dynimportfail bool
	preferlinkext bool
}

type objectTemplate struct {
	pn     string
	reader *goobj.Reader
}

type archiveTemplateCacheKey struct {
	path    string
	size    int64
	modTime int64
}

type archiveTemplateCacheEntry struct {
	once      sync.Once
	template  *archiveTemplate
	cacheable bool
}

var archiveTemplateCache sync.Map

func clearGOROOTArchiveTemplateCacheForTest() {
	archiveTemplateCache = sync.Map{}
}

func maybeLoadArchiveTemplate(ctxt *Link, lib *sym.Library) bool {
	tmpl, ok := cachedArchiveTemplate(lib)
	if !ok {
		return false
	}
	if ctxt.Debugvlog > 1 {
		ctxt.Logf("loadobjfile cache hit: %s\n", lib.File)
	}
	loadobjfileTemplate(ctxt, lib, tmpl)
	return true
}

func loadobjfileTemplate(ctxt *Link, lib *sym.Library, tmpl *archiveTemplate) {
	if tmpl.dynimportfail {
		dynimportfail = append(dynimportfail, lib.Pkg)
	}
	if tmpl.preferlinkext && ctxt.LinkMode == LinkAuto {
		preferlinkext = append(preferlinkext, lib.Pkg)
	}
	for _, obj := range tmpl.objects {
		unit := &sym.CompilationUnit{Lib: lib}
		lib.Units = append(lib.Units, unit)
		fingerprint := ctxt.loader.PreloadFromObjectReader(ctxt.IncVersion(), obj.reader, lib, unit)
		if !fingerprint.IsZero() {
			if lib.Fingerprint.IsZero() {
				lib.Fingerprint = fingerprint
			}
			checkFingerprint(lib, fingerprint, lib.Srcref, lib.Fingerprint)
		}
		addImports(ctxt, lib, obj.pn)
	}
}

func cachedArchiveTemplate(lib *sym.Library) (*archiveTemplate, bool) {
	if !canCacheArchive(lib) {
		return nil, false
	}
	info, err := os.Stat(lib.File)
	if err != nil {
		return nil, false
	}
	key := archiveTemplateCacheKey{
		path:    lib.File,
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
	}
	entryValue, _ := archiveTemplateCache.LoadOrStore(key, &archiveTemplateCacheEntry{})
	entry := entryValue.(*archiveTemplateCacheEntry)
	entry.once.Do(func() {
		entry.template, entry.cacheable = readArchiveTemplate(lib.File)
	})
	return entry.template, entry.cacheable
}

func canCacheArchive(lib *sym.Library) bool {
	if lib == nil || lib.Shlib != "" || lib.File == "" || lib.Pkg == "runtime/cgo" || lib.Pkg == "main" {
		return false
	}
	if filepath.Ext(lib.File) != ".a" && !strings.HasSuffix(filepath.Base(lib.File), "-d") {
		return false
	}
	return true
}

func readArchiveTemplate(file string) (*archiveTemplate, bool) {
	f, err := bio.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	for i := 0; i < len(ARMAG); i++ {
		if c, err := f.ReadByte(); err == nil && c == ARMAG[i] {
			continue
		}
		return nil, false
	}

	tmpl := &archiveTemplate{}
	var arhdr ArHdr
	off := f.Offset()
	for {
		l := nextar(f, off, &arhdr)
		if l == 0 {
			break
		}
		if l < 0 {
			return nil, false
		}
		off += l

		switch arhdr.name {
		case pkgdef:
			continue
		case "dynimportfail":
			tmpl.dynimportfail = true
			continue
		case "preferlinkext":
			tmpl.preferlinkext = true
			continue
		}

		if len(arhdr.name) < 16 {
			if ext := filepath.Ext(arhdr.name); ext != ".o" && ext != ".syso" {
				continue
			}
		}

		memberSize := atolwhex(arhdr.size)
		reader, ok := readObjectTemplate(f, memberSize)
		if !ok {
			return nil, false
		}
		tmpl.objects = append(tmpl.objects, objectTemplate{
			pn:     file + "(" + arhdr.name + ")",
			reader: reader,
		})
	}
	if len(tmpl.objects) == 0 {
		return nil, false
	}
	return tmpl, true
}

func readObjectTemplate(f *bio.Reader, length int64) (*goobj.Reader, bool) {
	eof := f.Offset() + length
	start := f.Offset()
	c1 := bgetc(f)
	c2 := bgetc(f)
	c3 := bgetc(f)
	c4 := bgetc(f)
	f.MustSeek(start, 0)

	if c1 != 'g' || c2 != 'o' || c3 != ' ' || c4 != 'o' {
		return nil, false
	}

	line, err := f.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "go object ") || line != wantHdr {
		return nil, false
	}

	import0 := f.Offset()
	c1 = '\n'
	c2 = bgetc(f)
	c3 = bgetc(f)
	markers := 0
	for {
		if c1 == '\n' {
			if markers%2 == 0 && c2 == '!' && c3 == '\n' {
				break
			}
			if c2 == '$' && c3 == '$' {
				markers++
			}
		}
		c1 = c2
		c2 = c3
		c3 = bgetc(f)
		if c3 == -1 {
			return nil, false
		}
	}
	import1 := f.Offset()

	if !isPureGoObject(f, import0, import1-import0-2) {
		return nil, false
	}
	f.MustSeek(import1, 0)

	roObject, readonly, err := f.Slice(uint64(eof - f.Offset()))
	if err != nil {
		return nil, false
	}
	reader := goobj.NewReaderFromBytes(roObject, readonly)
	f.MustSeek(eof, 0)
	return reader, reader != nil
}

func isPureGoObject(f *bio.Reader, off, length int64) bool {
	if length <= 0 {
		return true
	}
	buf := make([]byte, length)
	f.MustSeek(off, 0)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	return !bytes.Contains(buf, []byte("\n$$  // cgo"))
}
