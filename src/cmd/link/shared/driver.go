// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package shared

import (
	"cmd/link/internal/amd64"
	"cmd/link/internal/arm"
	"cmd/link/internal/arm64"
	"cmd/link/internal/ld"
	"cmd/link/internal/loong64"
	"cmd/link/internal/mips"
	"cmd/link/internal/mips64"
	"cmd/link/internal/ppc64"
	"cmd/link/internal/riscv64"
	"cmd/link/internal/s390x"
	"cmd/link/internal/wasm"
	"cmd/link/internal/x86"
	"internal/buildcfg"
	"os"

	"cmd/internal/sys"
)

type Driver struct {
	driver *ld.Driver
}

func NewDriver() *Driver {
	arch, theArch := currentArch()
	return &Driver{driver: ld.NewDriver(arch, theArch)}
}

func (d *Driver) Run(args []string, dir string, env []string, stdout, stderr *os.File) error {
	return d.driver.Run(args, dir, env, stdout, stderr)
}

func currentArch() (*sys.Arch, ld.Arch) {
	buildcfg.Check()
	switch buildcfg.GOARCH {
	default:
		panic("unknown architecture: " + buildcfg.GOARCH)
	case "386":
		return x86.Init()
	case "amd64":
		return amd64.Init()
	case "arm":
		return arm.Init()
	case "arm64":
		return arm64.Init()
	case "loong64":
		return loong64.Init()
	case "mips", "mipsle":
		return mips.Init()
	case "mips64", "mips64le":
		return mips64.Init()
	case "ppc64", "ppc64le":
		return ppc64.Init()
	case "riscv64":
		return riscv64.Init()
	case "s390x":
		return s390x.Init()
	case "wasm":
		return wasm.Init()
	}
}
