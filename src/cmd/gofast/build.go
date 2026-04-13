// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type buildOptions struct {
	GoArgs  []string
	Explain bool
}

type buildDecision struct {
	stage    string
	reason   string
	entryID  string
	disable  bool
	fallback bool
}

type targetState struct {
	EnvKey      string   `json:"env_key"`
	InputKey    string   `json:"input_key"`
	StdlibKey   string   `json:"stdlib_key"`
	WatchKey    string   `json:"watch_key"`
	EntryID     string   `json:"entry_id"`
	BuildArgs   []string `json:"build_args"`
	UpdatedUnix int64    `json:"updated_unix"`
}

type cacheEntryMeta struct {
	Version        int      `json:"version"`
	EntryID        string   `json:"entry_id"`
	EnvKey         string   `json:"env_key"`
	InputKey       string   `json:"input_key"`
	StdlibKey      string   `json:"stdlib_key"`
	WatchKey       string   `json:"watch_key"`
	BuildArgs      []string `json:"build_args"`
	OutputPath     string   `json:"output_path"`
	BinarySize     int64    `json:"binary_size"`
	BuildID        string   `json:"build_id"`
	GoVersion      string   `json:"go_version"`
	GOOS           string   `json:"goos"`
	GOARCH         string   `json:"goarch"`
	CGOEnabled     string   `json:"cgo_enabled"`
	BuildDuration  int64    `json:"build_duration_nanos"`
	CreatedAtUnix  int64    `json:"created_at_unix"`
	StdlibPackages []string `json:"stdlib_packages"`
}

type buildConfig struct {
	OutputPath string
	PkgArgs    []string
	NoCache    bool
	Reasons    []string
}

type buildInputs struct {
	ProjectRoot   string
	WatchKey      string
	InputKey      string
	StdlibKey     string
	StdlibPkgs    []string
	EnvKey        string
	EntryID       string
	TargetStateID string
	GoVersion     string
	GOOS          string
	GOARCH        string
	CGOEnabled    string
}

type goListPackage struct {
	ImportPath string   `json:"ImportPath"`
	Dir        string   `json:"Dir"`
	Standard   bool     `json:"Standard"`
	GoFiles    []string `json:"GoFiles"`
	CgoFiles   []string `json:"CgoFiles"`
	CFiles     []string `json:"CFiles"`
	CXXFiles   []string `json:"CXXFiles"`
	MFiles     []string `json:"MFiles"`
	HFiles     []string `json:"HFiles"`
	SFiles     []string `json:"SFiles"`
	SysoFiles  []string `json:"SysoFiles"`
	SwigFiles  []string `json:"SwigFiles"`
	SwigCXX    []string `json:"SwigCXXFiles"`
	EmbedFiles []string `json:"EmbedFiles"`
}

func runBuild(ctx context.Context, opts buildOptions) error {
	cfg := parseBuildConfig(opts.GoArgs)
	if opts.Explain {
		for _, r := range cfg.Reasons {
			fmt.Fprintf(os.Stderr, "gofast: %s\n", r)
		}
	}

	if len(cfg.PkgArgs) == 0 {
		cfg.PkgArgs = []string{"."}
	}
	if cfg.OutputPath == "" {
		cfg.NoCache = true
		cfg.Reasons = append(cfg.Reasons, "cache disabled: -o is required for safe binary reuse")
	}

	projectRoot, err := detectProjectRoot()
	if err != nil {
		return err
	}

	if cfg.NoCache {
		return runGoBuild(ctx, opts.GoArgs)
	}

	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}

	inputs, disableReasons, err := inspectInputs(ctx, projectRoot, opts.GoArgs, cfg)
	if err != nil {
		return err
	}
	if len(disableReasons) > 0 {
		if opts.Explain {
			for _, r := range disableReasons {
				fmt.Fprintf(os.Stderr, "gofast: %s\n", r)
			}
		}
		recordMiss("disabled")
		return runGoBuild(ctx, opts.GoArgs)
	}

	state, _ := loadTargetState(inputs.TargetStateID)
	decision := chooseDecision(state, inputs)
	if opts.Explain {
		fmt.Fprintf(os.Stderr, "gofast: decision stage=%s reason=%s\n", decision.stage, decision.reason)
	}

	if !decision.disable && decision.entryID != "" {
		if ok, err := restoreEntry(ctx, decision.entryID, cfg.OutputPath, opts.Explain); err != nil {
			return err
		} else if ok {
			recordHit(decision.stage)
			state = &targetState{
				EnvKey:      inputs.EnvKey,
				InputKey:    inputs.InputKey,
				StdlibKey:   inputs.StdlibKey,
				WatchKey:    inputs.WatchKey,
				EntryID:     decision.entryID,
				BuildArgs:   normalizeArgs(opts.GoArgs),
				UpdatedUnix: time.Now().Unix(),
			}
			if err := saveTargetState(inputs.TargetStateID, state); err != nil {
				return err
			}
			return nil
		}
		if opts.Explain {
			fmt.Fprintf(os.Stderr, "gofast: cache candidate invalid, falling back to go build\n")
		}
	}

	start := time.Now()
	if err := runGoBuild(ctx, opts.GoArgs); err != nil {
		recordMiss("build_failed")
		return err
	}
	buildDuration := time.Since(start)

	buildID, _ := readBuildID(ctx, cfg.OutputPath)
	info, err := os.Stat(cfg.OutputPath)
	if err != nil {
		return err
	}

	meta := cacheEntryMeta{
		Version:        1,
		EntryID:        inputs.EntryID,
		EnvKey:         inputs.EnvKey,
		InputKey:       inputs.InputKey,
		StdlibKey:      inputs.StdlibKey,
		WatchKey:       inputs.WatchKey,
		BuildArgs:      normalizeArgs(opts.GoArgs),
		OutputPath:     cfg.OutputPath,
		BinarySize:     info.Size(),
		BuildID:        buildID,
		GoVersion:      inputs.GoVersion,
		GOOS:           inputs.GOOS,
		GOARCH:         inputs.GOARCH,
		CGOEnabled:     inputs.CGOEnabled,
		BuildDuration:  buildDuration.Nanoseconds(),
		CreatedAtUnix:  time.Now().Unix(),
		StdlibPackages: inputs.StdlibPkgs,
	}
	if err := storeEntry(inputs.EntryID, cfg.OutputPath, meta); err != nil {
		return err
	}
	if err := saveTargetState(inputs.TargetStateID, &targetState{
		EnvKey:      inputs.EnvKey,
		InputKey:    inputs.InputKey,
		StdlibKey:   inputs.StdlibKey,
		WatchKey:    inputs.WatchKey,
		EntryID:     inputs.EntryID,
		BuildArgs:   normalizeArgs(opts.GoArgs),
		UpdatedUnix: time.Now().Unix(),
	}); err != nil {
		return err
	}
	recordMiss("cache_miss")
	return nil
}

func chooseDecision(state *targetState, in buildInputs) buildDecision {
	if state == nil {
		return buildDecision{stage: "none", reason: "no previous state"}
	}
	if state.EnvKey != in.EnvKey {
		return buildDecision{stage: "none", reason: "environment changed"}
	}
	if state.WatchKey == in.WatchKey {
		return buildDecision{
			stage:   "watch",
			reason:  "no relevant file changes",
			entryID: state.EntryID,
		}
	}
	if state.StdlibKey == in.StdlibKey {
		return buildDecision{
			stage:   "stdlib",
			reason:  "stdlib fingerprint unchanged",
			entryID: in.EntryID,
		}
	}
	return buildDecision{stage: "none", reason: "stdlib fingerprint changed"}
}

func inspectInputs(ctx context.Context, projectRoot string, goArgs []string, cfg buildConfig) (buildInputs, []string, error) {
	goVersion, err := goEnv(ctx, "GOVERSION")
	if err != nil {
		return buildInputs{}, nil, err
	}
	goos, err := goEnv(ctx, "GOOS")
	if err != nil {
		return buildInputs{}, nil, err
	}
	goarch, err := goEnv(ctx, "GOARCH")
	if err != nil {
		return buildInputs{}, nil, err
	}
	cgoEnabled, err := goEnv(ctx, "CGO_ENABLED")
	if err != nil {
		return buildInputs{}, nil, err
	}
	goexperiment, _ := goEnv(ctx, "GOEXPERIMENT")

	reasons := disableReasons(goArgs, cgoEnabled)
	if cgoEnabled != "0" {
		reasons = append(reasons, "cache disabled: CGO_ENABLED=1 is not supported in initial release")
	}
	verified, verifyDetails, err := loadOrRunLayoutVerification(ctx, goVersion, goos, goarch)
	if err != nil {
		reasons = append(reasons, "cache disabled: stdlib layout verification failed ("+err.Error()+")")
	} else if !verified {
		reasons = append(reasons, "cache disabled: "+verifyDetails)
	}

	watchKey, err := computeWatchKey(projectRoot)
	if err != nil {
		return buildInputs{}, nil, err
	}

	pkgs, err := goListDeps(ctx, goArgs, cfg.PkgArgs)
	if err != nil {
		return buildInputs{}, nil, err
	}
	inputKey, stdlibKey, stdlibPkgs, err := computeDependencyKeys(projectRoot, goArgs, pkgs, goexperiment)
	if err != nil {
		return buildInputs{}, nil, err
	}

	envKey := hashStrings(
		"env-v1",
		goVersion,
		goos,
		goarch,
		cgoEnabled,
		goexperiment,
		hashStrings(normalizeArgs(goArgs)...),
	)
	targetStateID := hashStrings("state-v1", projectRoot, cfg.OutputPath, hashStrings(normalizeArgs(goArgs)...))
	entryID := hashStrings("entry-v1", envKey, inputKey, stdlibKey)

	return buildInputs{
		ProjectRoot:   projectRoot,
		WatchKey:      watchKey,
		InputKey:      inputKey,
		StdlibKey:     stdlibKey,
		StdlibPkgs:    stdlibPkgs,
		EnvKey:        envKey,
		EntryID:       entryID,
		TargetStateID: targetStateID,
		GoVersion:     goVersion,
		GOOS:          goos,
		GOARCH:        goarch,
		CGOEnabled:    cgoEnabled,
	}, reasons, nil
}

func parseBuildConfig(args []string) buildConfig {
	cfg := buildConfig{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" && i+1 < len(args):
			cfg.OutputPath = args[i+1]
			i++
		case strings.HasPrefix(a, "-o="):
			cfg.OutputPath = strings.TrimPrefix(a, "-o=")
		case strings.HasPrefix(a, "-"):
			// recognized as flag, keep walking
		default:
			cfg.PkgArgs = append(cfg.PkgArgs, a)
		}
	}
	if cfg.OutputPath != "" {
		cfg.OutputPath = absPath(cfg.OutputPath)
	}
	return cfg
}

func normalizeArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

func disableReasons(args []string, cgoEnabled string) []string {
	var reasons []string
	buildmode := "default"
	var ldflags string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-buildmode" && i+1 < len(args):
			buildmode = args[i+1]
			i++
		case strings.HasPrefix(a, "-buildmode="):
			buildmode = strings.TrimPrefix(a, "-buildmode=")
		case a == "-ldflags" && i+1 < len(args):
			ldflags = args[i+1]
			i++
		case strings.HasPrefix(a, "-ldflags="):
			ldflags = strings.TrimPrefix(a, "-ldflags=")
		}
	}
	if buildmode != "" && buildmode != "default" && buildmode != "exe" {
		reasons = append(reasons, "cache disabled: unsupported buildmode="+buildmode)
	}
	if strings.Contains(ldflags, "-linkmode external") || strings.Contains(ldflags, "-linkmode=external") {
		reasons = append(reasons, "cache disabled: external link mode is not supported")
	}
	if cgoEnabled != "0" && (buildmode == "c-archive" || buildmode == "c-shared" || buildmode == "plugin") {
		reasons = append(reasons, "cache disabled: cgo buildmode is not supported")
	}
	return reasons
}

func detectProjectRoot() (string, error) {
	mod, err := goEnv(context.Background(), "GOMOD")
	if err != nil {
		return "", err
	}
	if mod != "" && mod != os.DevNull {
		return filepath.Dir(mod), nil
	}
	return os.Getwd()
}

func runGoBuild(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "go", append([]string{"build"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func goEnv(ctx context.Context, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", key)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func computeWatchKey(root string) (string, error) {
	hasher := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isRelevantFile(path) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hasher, "%s:%d:%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func skipDir(path string) bool {
	base := filepath.Base(path)
	switch base {
	case ".git", "vendor", ".idea", ".vscode", "tmp", "dist", "bin":
		return true
	}
	return strings.HasPrefix(base, ".")
}

func isRelevantFile(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "go.mod", "go.sum", "go.work", "go.work.sum", ".envrc", ".tool-versions":
		return true
	}
	ext := filepath.Ext(path)
	switch ext {
	case ".go", ".s", ".S", ".c", ".h", ".cc", ".cpp", ".cxx", ".m", ".mm":
		return true
	}
	return false
}

func goListDeps(ctx context.Context, args []string, pkgs []string) ([]goListPackage, error) {
	listArgs := []string{"list", "-deps", "-json"}
	listArgs = append(listArgs, argsForGoList(args)...)
	listArgs = append(listArgs, pkgs...)
	cmd := exec.CommandContext(ctx, "go", listArgs...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("go list failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgsOut []goListPackage
	for {
		var p goListPackage
		if err := dec.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		pkgsOut = append(pkgsOut, p)
	}
	return pkgsOut, nil
}

func argsForGoList(args []string) []string {
	filtered := make([]string, 0, len(args))
	skipNext := false
	for i, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case a == "-o":
			skipNext = true
		case strings.HasPrefix(a, "-o="):
			continue
		case strings.HasPrefix(a, "-"):
			filtered = append(filtered, a)
			if i+1 < len(args) && argNeedsValue(a) {
				skipNext = true
			}
		default:
			// positional package arguments are supplied explicitly by caller
			continue
		}
	}
	return filtered
}

func argNeedsValue(flag string) bool {
	switch flag {
	case "-asmflags", "-buildmode", "-compiler", "-gcflags", "-installsuffix", "-ldflags", "-mod", "-modfile", "-overlay", "-p", "-pkgdir", "-tags", "-toolexec":
		return true
	}
	return false
}

func computeDependencyKeys(projectRoot string, goArgs []string, pkgs []goListPackage, goexperiment string) (string, string, []string, error) {
	var stdlib []string
	var lines []string
	for _, p := range pkgs {
		if p.ImportPath == "" {
			continue
		}
		if p.Standard {
			stdlib = append(stdlib, p.ImportPath)
			continue
		}
		files := packageFiles(p)
		sort.Strings(files)
		for _, f := range files {
			abs := filepath.Join(p.Dir, f)
			sum, err := hashFile(abs)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return "", "", nil, err
			}
			rel, _ := filepath.Rel(projectRoot, abs)
			lines = append(lines, p.ImportPath+":"+filepath.ToSlash(rel)+":"+sum)
		}
	}
	sort.Strings(stdlib)
	stdlib = dedupe(stdlib)
	sort.Strings(lines)
	inputKey := hashStrings(append([]string{"input-v1", hashStrings(normalizeArgs(goArgs)...)}, lines...)...)
	stdlibKey := hashStrings(append([]string{"stdlib-v1", goexperiment}, stdlib...)...)
	return inputKey, stdlibKey, stdlib, nil
}

func packageFiles(p goListPackage) []string {
	out := make([]string, 0, len(p.GoFiles)+len(p.CgoFiles)+len(p.CFiles)+len(p.CXXFiles)+len(p.MFiles)+len(p.HFiles)+len(p.SFiles)+len(p.SysoFiles)+len(p.SwigFiles)+len(p.SwigCXX)+len(p.EmbedFiles))
	out = append(out, p.GoFiles...)
	out = append(out, p.CgoFiles...)
	out = append(out, p.CFiles...)
	out = append(out, p.CXXFiles...)
	out = append(out, p.MFiles...)
	out = append(out, p.HFiles...)
	out = append(out, p.SFiles...)
	out = append(out, p.SysoFiles...)
	out = append(out, p.SwigFiles...)
	out = append(out, p.SwigCXX...)
	out = append(out, p.EmbedFiles...)
	return out
}

func dedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashStrings(v ...string) string {
	h := sha256.New()
	for _, s := range v {
		io.WriteString(h, s)
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func absPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	return filepath.Join(wd, path)
}
