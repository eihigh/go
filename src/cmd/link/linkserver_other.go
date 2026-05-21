// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package main

import (
	"cmd/internal/sys"
	"cmd/link/internal/ld"
	"fmt"
)

func runLinkServer(addr string, arch *sys.Arch, theArch ld.Arch) error {
	return fmt.Errorf("unsupported on this platform")
}
