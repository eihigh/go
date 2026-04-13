// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func cacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "gofast-cache")
	}
	return filepath.Join(dir, "gofast")
}

func clearCache() error {
	return os.RemoveAll(cacheDir())
}

func entryDir(entryID string) string {
	return filepath.Join(cacheDir(), "entries", entryID)
}

func entryBinaryPath(entryID string) string {
	return filepath.Join(entryDir(entryID), "binary")
}

func entryMetaPath(entryID string) string {
	return filepath.Join(entryDir(entryID), "meta.json")
}

func statePath(stateID string) string {
	return filepath.Join(cacheDir(), "state", stateID+".json")
}

func storeEntry(entryID, outputPath string, meta cacheEntryMeta) error {
	dir := entryDir(entryID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := copyFile(outputPath, entryBinaryPath(entryID)); err != nil {
		return err
	}

	f, err := os.Create(entryMetaPath(entryID))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(meta)
}

func restoreEntry(ctx context.Context, entryID, outputPath string, explain bool) (bool, error) {
	meta, err := loadEntryMeta(entryID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	bin := entryBinaryPath(entryID)
	info, err := os.Stat(bin)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Size() != meta.BinarySize {
		if explain {
			fmt.Fprintf(os.Stderr, "gofast: cache mismatch (size) expected=%d got=%d\n", meta.BinarySize, info.Size())
		}
		return false, nil
	}
	buildID, err := readBuildID(ctx, bin)
	if err == nil && meta.BuildID != "" && buildID != meta.BuildID {
		if explain {
			fmt.Fprintf(os.Stderr, "gofast: cache mismatch (buildid)\n")
		}
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return false, err
	}
	if err := copyFile(bin, outputPath); err != nil {
		return false, err
	}
	if explain {
		fmt.Fprintf(os.Stderr, "gofast: cache hit (%s)\n", entryID[:12])
	}
	return true, nil
}

func loadEntryMeta(entryID string) (cacheEntryMeta, error) {
	var meta cacheEntryMeta
	f, err := os.Open(entryMetaPath(entryID))
	if err != nil {
		return meta, err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func loadTargetState(stateID string) (*targetState, error) {
	f, err := os.Open(statePath(stateID))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var st targetState
	if err := json.NewDecoder(f).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveTargetState(stateID string, st *targetState) error {
	if err := os.MkdirAll(filepath.Dir(statePath(stateID)), 0o755); err != nil {
		return err
	}
	f, err := os.Create(statePath(stateID))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func readBuildID(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "tool", "buildid", path)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type cacheMetrics struct {
	Hits        int64            `json:"hits"`
	Misses      int64            `json:"misses"`
	HitByStage  map[string]int64 `json:"hit_by_stage"`
	MissReasons map[string]int64 `json:"miss_reasons"`
	UpdatedUnix int64            `json:"updated_unix"`
}

func metricsPath() string {
	return filepath.Join(cacheDir(), "metrics.json")
}

func recordHit(stage string) {
	updateMetrics(func(m *cacheMetrics) {
		m.Hits++
		if m.HitByStage == nil {
			m.HitByStage = make(map[string]int64)
		}
		m.HitByStage[stage]++
	})
}

func recordMiss(reason string) {
	updateMetrics(func(m *cacheMetrics) {
		m.Misses++
		if m.MissReasons == nil {
			m.MissReasons = make(map[string]int64)
		}
		m.MissReasons[reason]++
	})
}

func updateMetrics(f func(*cacheMetrics)) {
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return
	}
	var m cacheMetrics
	if data, err := os.ReadFile(metricsPath()); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	f(&m)
	m.UpdatedUnix = time.Now().Unix()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(metricsPath(), data, 0o644)
}
