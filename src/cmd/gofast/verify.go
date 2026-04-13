// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type layoutVerification struct {
	EnvKey      string `json:"env_key"`
	Verified    bool   `json:"verified"`
	Details     string `json:"details"`
	VerifiedAt  int64  `json:"verified_at_unix"`
	ToolVersion int    `json:"tool_version"`
}

func layoutVerificationPath() string {
	return filepath.Join(cacheDir(), "layout_verification.json")
}

func loadOrRunLayoutVerification(ctx context.Context, goversion, goos, goarch string) (bool, string, error) {
	key := hashStrings("layout-v1", goversion, goos, goarch)
	if data, err := os.ReadFile(layoutVerificationPath()); err == nil {
		var v layoutVerification
		if json.Unmarshal(data, &v) == nil && v.EnvKey == key {
			return v.Verified, v.Details, nil
		}
	}
	ok, details, err := verifyStdlibLayout(ctx)
	if err != nil {
		return false, "", err
	}
	v := layoutVerification{
		EnvKey:      key,
		Verified:    ok,
		Details:     details,
		VerifiedAt:  time.Now().Unix(),
		ToolVersion: 1,
	}
	if err := os.MkdirAll(cacheDir(), 0o755); err == nil {
		if data, err := json.MarshalIndent(v, "", "  "); err == nil {
			_ = os.WriteFile(layoutVerificationPath(), data, 0o644)
		}
	}
	return ok, details, nil
}

func verifyStdlibLayout(ctx context.Context) (bool, string, error) {
	tmp, err := os.MkdirTemp("", "gofast-layout-*")
	if err != nil {
		return false, "", err
	}
	defer os.RemoveAll(tmp)

	src := `package main
import (
	"fmt"
	"net/http"
)
func main() {
	_, _ = fmt.Println(http.MethodGet)
}
`
	mainFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(mainFile, []byte(src), 0o644); err != nil {
		return false, "", err
	}
	bin := filepath.Join(tmp, "probe.bin")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", bin, mainFile)
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return false, "", fmt.Errorf("probe build failed: %w", err)
	}

	nmCmd := exec.CommandContext(ctx, "go", "tool", "nm", "-n", bin)
	out, err := nmCmd.Output()
	if err != nil {
		return false, "", fmt.Errorf("go tool nm failed: %w", err)
	}

	var mainAddr uint64
	var runtimeAddr uint64
	var fmtAddr uint64
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		addr, err := strconv.ParseUint(f[0], 16, 64)
		if err != nil {
			continue
		}
		sym := f[2]
		switch sym {
		case "main.main":
			mainAddr = addr
		case "runtime.main":
			runtimeAddr = addr
		case "fmt.Println":
			fmtAddr = addr
		}
	}
	if mainAddr == 0 || runtimeAddr == 0 {
		return false, "layout probe failed: missing required symbols", nil
	}
	if !(runtimeAddr < mainAddr) {
		return false, "layout probe failed: runtime symbols are not before main symbols", nil
	}
	if fmtAddr != 0 && !(fmtAddr < mainAddr) {
		return false, "layout probe failed: stdlib symbols are not before main symbols", nil
	}
	return true, "layout probe passed for current go toolchain", nil
}
