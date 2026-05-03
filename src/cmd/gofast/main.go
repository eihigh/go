// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"cmd/link/shared"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type linkServiceState struct {
	Addr string `json:"addr"`
	PID  int    `json:"pid"`
}

type linkServiceRequest struct {
	Args []string `json:"args"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
}

type linkServiceResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

type service struct {
	driver *shared.Driver
	mu     sync.Mutex
}

const serverStartTimeout = 5 * time.Second

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serveMain(os.Args[2:])
	case "link":
		linkMain(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gofast {serve|link} ...")
	os.Exit(2)
}

func serveMain(args []string) {
	fs := flag.NewFlagSet("gofast serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "listen address")
	stateFile := fs.String("statefile", "", "state file")
	fs.Parse(args)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *stateFile != "" {
		if err := writeState(*stateFile, linkServiceState{Addr: "http://" + ln.Addr().String(), PID: os.Getpid()}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	svc := &service{driver: shared.NewDriver()}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/link", svc.handleLink)
	if err := (&http.Server{Handler: mux}).Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func linkMain(args []string) {
	fs := flag.NewFlagSet("gofast link", flag.ExitOnError)
	stateFile := fs.String("statefile", "", "state file")
	fs.Parse(args)
	if *stateFile == "" {
		fmt.Fprintln(os.Stderr, "gofast link: -statefile is required")
		os.Exit(2)
	}

	state, err := ensureServer(*stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	resp, err := callLinkServer(state.Addr, linkServiceRequest{
		Args: fs.Args(),
		Dir:  mustGetwd(),
		Env:  os.Environ(),
	})
	if err != nil {
		_ = os.Remove(*stateFile)
		state, err = ensureServer(*stateFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		resp, err = callLinkServer(state.Addr, linkServiceRequest{
			Args: fs.Args(),
			Dir:  mustGetwd(),
			Env:  os.Environ(),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if resp.Stdout != "" {
		_, _ = os.Stdout.WriteString(resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = os.Stderr.WriteString(resp.Stderr)
	}
}

func ensureServer(stateFile string) (linkServiceState, error) {
	if state, err := readState(stateFile); err == nil && linkServerHealthy(state.Addr) {
		return state, nil
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0777); err != nil {
		return linkServiceState{}, err
	}
	_ = os.Remove(stateFile)
	self, err := os.Executable()
	if err != nil {
		return linkServiceState{}, err
	}
	cmd := exec.Command(self, "serve", "-listen=127.0.0.1:0", "-statefile="+stateFile)
	cmd.Env = os.Environ()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return linkServiceState{}, err
	}
	defer null.Close()
	cmd.Stdout = null
	cmd.Stderr = null
	if err := cmd.Start(); err != nil {
		return linkServiceState{}, err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(serverStartTimeout)
	for time.Now().Before(deadline) {
		state, err := readState(stateFile)
		if err == nil && linkServerHealthy(state.Addr) {
			return state, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return linkServiceState{}, fmt.Errorf("timed out waiting for gofast server")
}

func readState(file string) (linkServiceState, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return linkServiceState{}, err
	}
	var state linkServiceState
	if err := json.Unmarshal(data, &state); err != nil {
		return linkServiceState{}, err
	}
	return state, nil
}

func writeState(file string, state linkServiceState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0666); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func linkServerHealthy(addr string) bool {
	if addr == "" {
		return false
	}
	resp, err := http.Get(addr + "/healthz")
	if err != nil {
		return false
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		resp.Body.Close()
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func callLinkServer(addr string, req linkServiceRequest) (linkServiceResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return linkServiceResponse{}, err
	}
	resp, err := http.Post(addr+"/link", "application/json", bytes.NewReader(body))
	if err != nil {
		return linkServiceResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return linkServiceResponse{}, fmt.Errorf("gofast server: %s", bytes.TrimSpace(msg))
	}
	var result linkServiceResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return linkServiceResponse{}, err
	}
	return result, nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return wd
}

func (s *service) handleLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req linkServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stdoutFile, err := os.CreateTemp("", "gofast-stdout-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(stdoutFile.Name())
	defer stdoutFile.Close()
	stderrFile, err := os.CreateTemp("", "gofast-stderr-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(stderrFile.Name())
	defer stderrFile.Close()
	if err := s.driver.Run(req.Args, req.Dir, req.Env, stdoutFile, stderrFile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := stderrFile.Seek(0, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stdout, err := io.ReadAll(stdoutFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stderr, err := io.ReadAll(stderrFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(linkServiceResponse{Stdout: string(stdout), Stderr: string(stderr)}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
