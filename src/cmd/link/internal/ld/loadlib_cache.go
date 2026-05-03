// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"bytes"
	"cmd/internal/goobj"
	"cmd/link/internal/sym"
	"internal/buildcfg"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type loadlibCache struct {
	mu       sync.Mutex
	archives map[string]cachedArchiveEntry
}

type cachedArchiveEntry struct {
	size     int64
	modTime  int64
	archive  *cachedArchive
}

type cachedArchive struct {
	dynimportfail bool
	preferlinkext bool
	members       []cachedArchiveMember
}

type cachedArchiveMember struct {
	filename   string
	importData []byte
	objectData []byte
}

func newLoadlibCache() *loadlibCache {
	return &loadlibCache{archives: make(map[string]cachedArchiveEntry)}
}

func (c *loadlibCache) load(ctxt *Link, lib *sym.Library) bool {
	if c == nil || !cacheableGOROOTArchive(lib.File) {
		return false
	}
	st, err := os.Stat(lib.File)
	if err != nil {
		return false
	}

	c.mu.Lock()
	entry, ok := c.archives[lib.File]
	if ok && entry.size == st.Size() && entry.modTime == st.ModTime().UnixNano() {
		c.mu.Unlock()
		if entry.archive == nil {
			return false
		}
		entry.archive.load(ctxt, lib)
		return true
	}
	c.mu.Unlock()

	archive, ok := parseCachedArchive(lib.File)

	c.mu.Lock()
	c.archives[lib.File] = cachedArchiveEntry{
		size:    st.Size(),
		modTime: st.ModTime().UnixNano(),
		archive: archive,
	}
	c.mu.Unlock()

	if !ok {
		return false
	}
	archive.load(ctxt, lib)
	return true
}

func cacheableGOROOTArchive(file string) bool {
	if buildcfg.GOROOT == "" || filepath.Ext(file) != ".a" {
		return false
	}
	root := filepath.Clean(buildcfg.GOROOT) + string(os.PathSeparator)
	file = filepath.Clean(file)
	return strings.HasPrefix(file, root)
}

func parseCachedArchive(file string) (*cachedArchive, bool) {
	data, err := os.ReadFile(file)
	if err != nil || len(data) < len(ARMAG) || !bytes.Equal(data[:len(ARMAG)], []byte(ARMAG)) {
		return nil, false
	}
	ar := &cachedArchive{}
	off := len(ARMAG)
	for off < len(data) {
		if off&1 != 0 {
			off++
		}
		if off == len(data) {
			break
		}
		if off+SAR_HDR > len(data) {
			return nil, false
		}
		hdr := data[off : off+SAR_HDR]
		name := artrim(hdr[0:16])
		size := int(atolwhex(artrim(hdr[48:58])))
		off += SAR_HDR
		if size < 0 || off+size > len(data) {
			return nil, false
		}
		member := data[off : off+size]
		off += size

		switch name {
		case pkgdef:
			continue
		case "dynimportfail":
			ar.dynimportfail = true
			continue
		case "preferlinkext":
			ar.preferlinkext = true
			continue
		}
		if len(name) < 16 {
			switch filepath.Ext(name) {
			case ".o":
			default:
				return nil, false
			}
		}
		cm, ok := parseCachedArchiveMember(file, name, member)
		if !ok {
			return nil, false
		}
		ar.members = append(ar.members, cm)
	}
	return ar, true
}

func parseCachedArchiveMember(file, name string, data []byte) (cachedArchiveMember, bool) {
	if len(data) < 4 || !bytes.Equal(data[:4], []byte("go o")) {
		return cachedArchiveMember{}, false
	}
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		return cachedArchiveMember{}, false
	}
	line := string(data[:lineEnd+1])
	if !strings.HasPrefix(line, "go object ") || line != wantHdr {
		return cachedArchiveMember{}, false
	}

	import0 := lineEnd + 1
	c1, c2, c3 := byte('\n'), byte(0), byte(0)
	if import0 < len(data) {
		c2 = data[import0]
	}
	if import0+1 < len(data) {
		c3 = data[import0+1]
	}
	markers := 0
	import1 := -1
	for i := import0 + 2; i < len(data); i++ {
		if c1 == '\n' {
			if markers%2 == 0 && c2 == '!' && c3 == '\n' {
				import1 = i
				break
			}
			if c2 == '$' && c3 == '$' {
				markers++
			}
		}
		c1, c2, c3 = c2, c3, data[i]
	}
	if import1 < 0 || import1 > len(data) {
		return cachedArchiveMember{}, false
	}
	objectData := data[import1:]
	if goobj.NewReaderFromBytes(objectData, false) == nil {
		return cachedArchiveMember{}, false
	}
	return cachedArchiveMember{
		filename:   file + "(" + name + ")",
		importData: append([]byte(nil), data[import0:import1-2]...),
		objectData: append([]byte(nil), objectData...),
	}, true
}

func (ar *cachedArchive) load(ctxt *Link, lib *sym.Library) {
	if ar.dynimportfail {
		dynimportfail = append(dynimportfail, lib.Pkg)
	}
	if ar.preferlinkext && ctxt.LinkMode == LinkAuto {
		preferlinkext = append(preferlinkext, lib.Pkg)
	}
	for _, m := range ar.members {
		unit := &sym.CompilationUnit{Lib: lib}
		lib.Units = append(lib.Units, unit)
		ldpkgData(ctxt, lib, m.importData, m.filename)
		fingerprint := ctxt.loader.PreloadFromBytes(ctxt.IncVersion(), m.objectData, false, lib, unit, m.filename)
		if !fingerprint.IsZero() {
			if lib.Fingerprint.IsZero() {
				lib.Fingerprint = fingerprint
			}
			checkFingerprint(lib, fingerprint, lib.Srcref, lib.Fingerprint)
		}
	}
}
