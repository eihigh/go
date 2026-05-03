// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import "testing"

func TestBuildCachesLinkedExecutableAcrossOutputPaths(t *testing.T) {
	tooSlow(t, "checks that a rebuild reuses a cached linked executable")
	if gocacheverify.Value() == "1" {
		t.Skip("GODEBUG gocacheverify")
	}

	tg := testgo(t)
	defer tg.cleanup()
	tg.makeTempdir()
	tg.execDir = tg.path(".")
	tg.setenv("GO111MODULE", "on")
	tg.setenv("GOENV", "off")
	tg.setenv("GOCACHE", tg.path("cache"))
	tg.tempFile("go.mod", "module example.com/rebuild\n\ngo 1.25\n")
	tg.tempFile("main.go", `package main

import (
	_ "archive/tar"
	_ "compress/gzip"
	_ "crypto/tls"
	_ "encoding/json"
	_ "net/http"
	_ "regexp"
)

func main() {}
`)

	tg.run("build", "-o", tg.path("first"+exeSuffix), "-x", "-ldflags=-benchmark=cpu", ".")
	tg.grepStderr(`/pkg/tool/.*/link `, "initial build should invoke the linker")
	tg.grepStderr(`BenchmarkLoadlib `, "initial build should report linker benchmarks")

	tg.run("build", "-o", tg.path("second"+exeSuffix), "-x", "-ldflags=-benchmark=cpu", ".")
	tg.grepStderrNot(`/pkg/tool/.*/link `, "cached rebuild should skip invoking the linker")
	tg.grepStderr(`/cache/.+/rebuild `, "cached rebuild should copy the executable from the build cache")
	tg.mustExist(tg.path("second" + exeSuffix))
}
