// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package work

import "fmt"

func runLinkWithServer(sh *Shell, dir string, desc string, env []string, cmdargs ...any) error {
	return fmt.Errorf("-debug-linkserver is unsupported on this platform")
}
