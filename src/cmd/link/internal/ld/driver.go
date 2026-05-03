// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"cmd/internal/dwarf"
	"cmd/internal/sys"
	"fmt"
	"os"
	"strings"
	"sync"

	"cmd/link/internal/sym"
)

type Driver struct {
	arch    *sys.Arch
	theArch Arch
	cache   *loadlibCache
	mu      sync.Mutex
}

func NewDriver(arch *sys.Arch, theArch Arch) *Driver {
	return &Driver{
		arch:    arch,
		theArch: theArch,
		cache:   newLoadlibCache(),
	}
}

func (d *Driver) Run(args []string, dir string, env []string, stdout, stderr *os.File) (err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	oldwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return err
		}
		defer os.Chdir(oldwd)
	}

	restoreEnv := applyEnv(env)
	defer restoreEnv()

	oldStdout, oldStderr, oldArgs := os.Stdout, os.Stderr, os.Args
	oldCache := currentLoadlibCache
	oldExitHook := exitHook
	os.Stdout = stdout
	os.Stderr = stderr
	os.Args = append([]string{oldArgs[0]}, args...)
	currentLoadlibCache = d.cache
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		os.Args = oldArgs
		currentLoadlibCache = oldCache
		exitHook = oldExitHook
	}()

	resetLinkerState()
	exitHook = func(code int) {
		panic(linkerExit{code: code})
	}
	defer func() {
		if r := recover(); r != nil {
			if ex, ok := r.(linkerExit); ok && ex.code == 0 {
				err = nil
				return
			}
			if ex, ok := r.(linkerExit); ok {
				err = fmt.Errorf("link exited with status %d", ex.code)
				return
			}
			panic(r)
		}
	}()
	Main(d.arch, d.theArch)
	return nil
}

type linkerExit struct {
	code int
}

func applyEnv(env []string) func() {
	old := append([]string(nil), os.Environ()...)
	os.Clearenv()
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		os.Setenv(key, value)
	}
	return func() {
		os.Clearenv()
		for _, kv := range old {
			key, value, ok := strings.Cut(kv, "=")
			if ok {
				os.Setenv(key, value)
			}
		}
	}
}

func resetLinkerState() {
	pkglistfornote = nil
	windowsgui = false
	ownTmpDir = false
	atExitFuncs = nil

	lcSize = 0
	rpath = Rpath{}
	spSize = 0
	symSize = 0
	abiInternalVer = sym.SymVerABIInternal

	dynlib = nil
	ldflag = nil
	havedynamic = 0
	Funcalign = 0
	iscgo = false
	elfglobalsymndx = 0
	interpreter = ""
	debug_s = false
	HEADR = 0
	nerrors = 0
	liveness = 0
	checkStrictDups = 0
	strictDupMsgCount = 0

	Segtext = sym.Segment{}
	Segrodata = sym.Segment{}
	Segrelrodata = sym.Segment{}
	Segdata = sym.Segment{}
	Segdwarf = sym.Segment{}
	Segpdata = sym.Segment{}
	Segxdata = sym.Segment{}

	externalobj = false
	dynimportfail = nil
	preferlinkext = nil
	unknownObjFormat = false
	theline = ""
	hostobj = nil
	hostobjcounter = 0
	covCounterDataStartOff = 0
	covCounterDataLen = 0
	strdata = make(map[string]string)
	strnames = nil
	seenlib = make(map[string]bool)

	elfstrdat = nil
	elfshstrdat = nil
	Nelfsym = 1
	elf64 = false
	elfRelType = ""
	ehdr = ElfEhdr{}
	phdr = [NSECT]*ElfPhdr{}
	shdr = [NSECT]*ElfShdr{}
	interp = ""
	elfstr = [100]Elfstring{}
	nelfstr = 0
	buildinfo = nil
	elfverneed = 0

	gdbscript = ""
	dwarfp = nil
	dwtypes = dwarf.DWDie{}
	prototypedies = nil
	dwsectCUSize = nil
}
