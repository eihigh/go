// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package main

import (
	"bufio"
	"bytes"
	"cmd/internal/sys"
	"cmd/link/internal/ld"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

type linkServerRequest struct {
	Dir  string
	Env  []string
	Args []string
}

type linkServerResponse struct {
	Code   int
	Output string
}

func runLinkServer(addr string, arch *sys.Arch, theArch ld.Arch) error {
	if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(addr)

	var mu sync.Mutex
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			var req linkServerRequest
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				json.NewEncoder(conn).Encode(linkServerResponse{Code: 2, Output: err.Error() + "\n"})
				return
			}
			mu.Lock()
			resp := runLinkServerRequest(req, arch, theArch)
			mu.Unlock()
			json.NewEncoder(conn).Encode(resp)
		}()
	}
}

func runLinkServerRequest(req linkServerRequest, arch *sys.Arch, theArch ld.Arch) linkServerResponse {
	if len(req.Args) == 0 {
		return linkServerResponse{Code: 2, Output: "missing linker arguments\n"}
	}

	oldDir, _ := os.Getwd()
	oldEnv := os.Environ()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	defer func() {
		os.Chdir(oldDir)
		os.Clearenv()
		for _, e := range oldEnv {
			k, v, ok := cutEnv(e)
			if ok {
				os.Setenv(k, v)
			}
		}
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	os.Clearenv()
	for _, e := range req.Env {
		k, v, ok := cutEnv(e)
		if ok {
			os.Setenv(k, v)
		}
	}
	if req.Dir != "" && req.Dir != "." {
		if err := os.Chdir(req.Dir); err != nil {
			return linkServerResponse{Code: 2, Output: err.Error() + "\n"}
		}
	}

	r, w, err := os.Pipe()
	if err != nil {
		return linkServerResponse{Code: 2, Output: err.Error() + "\n"}
	}
	os.Stdout = w
	os.Stderr = w

	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&out, r)
		close(done)
	}()

	code := 2
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(w, "panic: %v\n", r)
			}
		}()
		code = ld.Run(arch, theArch, req.Args)
	}()
	w.Close()
	<-done
	r.Close()

	output := out.String()
	if code != 0 && output == "" {
		var b bytes.Buffer
		bw := bufio.NewWriter(&b)
		fmt.Fprintf(bw, "%s: exit status %d\n", req.Args[0], code)
		bw.Flush()
		output = b.String()
	}
	return linkServerResponse{Code: code, Output: output}
}

func cutEnv(e string) (key, value string, ok bool) {
	for i := 0; i < len(e); i++ {
		if e[i] == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return "", "", false
}
