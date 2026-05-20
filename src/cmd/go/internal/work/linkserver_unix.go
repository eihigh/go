// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package work

import (
	"bytes"
	"cmd/go/internal/cfg"
	"cmd/go/internal/str"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type debugLinkServerRequest struct {
	Dir  string
	Env  []string
	Args []string
}

type debugLinkServerResponse struct {
	Code   int
	Output string
}

func runLinkWithServer(sh *Shell, dir string, desc string, env []string, cmdargs ...any) error {
	cmdline := str.StringList(cmdargs...)
	for _, arg := range cmdline {
		if strings.HasPrefix(arg, "@") {
			return fmt.Errorf("invalid command-line argument %s in command: %s", arg, joinUnambiguously(cmdline))
		}
	}
	if cfg.BuildN || cfg.BuildX {
		var envcmdline string
		for _, e := range env {
			if j := strings.IndexByte(e, '='); j != -1 {
				if strings.ContainsRune(e[j+1:], '\'') {
					envcmdline += fmt.Sprintf("%s=%q", e[:j], e[j+1:])
				} else {
					envcmdline += fmt.Sprintf("%s='%s'", e[:j], e[j+1:])
				}
				envcmdline += " "
			}
		}
		envcmdline += joinUnambiguously(cmdline)
		sh.ShowCmd(dir, "%s", envcmdline)
		if cfg.BuildN {
			return nil
		}
	}

	if desc == "" {
		desc = sh.fmtCmd(dir, "%s", strings.Join(cmdline, " "))
	}
	out, err := runLinkServerOut(dir, env, cmdline)
	return sh.reportCmd(desc, dir, out, err)
}

func runLinkServerOut(dir string, env []string, cmdline []string) ([]byte, error) {
	addr, err := debugLinkServerAddr(cmdline[0])
	if err != nil {
		return nil, err
	}
	if err := ensureDebugLinkServer(cmdline[0], addr); err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	reqEnv := os.Environ()
	reqEnv = append(reqEnv, env...)
	if err := json.NewEncoder(conn).Encode(debugLinkServerRequest{Dir: dir, Env: reqEnv, Args: cmdline}); err != nil {
		return nil, err
	}
	var resp debugLinkServerResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return []byte(resp.Output), errors.New(cmdline[0] + ": exit status " + fmt.Sprint(resp.Code))
	}
	return []byte(resp.Output), nil
}

func debugLinkServerAddr(linkTool string) (string, error) {
	info, err := os.Stat(linkTool)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%d\n%d\n%s\n%s\n", linkTool, cfg.GOROOT, info.Size(), info.ModTime().UnixNano(), cfg.Goos, cfg.Goarch)
	sum := hex.EncodeToString(h.Sum(nil))[:16]
	return filepath.Join(os.TempDir(), "go-linkserver-"+fmt.Sprint(os.Getuid())+"-"+sum+".sock"), nil
}

func ensureDebugLinkServer(linkTool, addr string) error {
	if conn, err := net.DialTimeout("unix", addr, 100*time.Millisecond); err == nil {
		conn.Close()
		return nil
	}
	os.Remove(addr)
	cmd := exec.Command(linkTool, "-debug-linkserver", addr)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	var lastErr error
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("unix", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "link server did not start")
	if lastErr != nil {
		fmt.Fprintf(&b, ": %v", lastErr)
	}
	return errors.New(b.String())
}
