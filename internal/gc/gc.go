package gc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func verxHome() string {
	if v := os.Getenv("VERX_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".verx"
	}
	return filepath.Join(h, ".verx")
}

// Run 对应 06 §4.4：清孤儿 stage、旧版、下载缓存。
// olderThan 如 "720h" / "30d" / ""（空=全清可清项，旧版只留 current）。
func Run(olderThan string) error {
	home := verxHome()
	cutoff := time.Now()
	hasCutoff := false
	if strings.TrimSpace(olderThan) != "" {
		d, err := parseDuration(olderThan)
		if err != nil {
			return fmt.Errorf("--older-than 非法: %w", err)
		}
		cutoff = time.Now().Add(-d)
		hasCutoff = true
	}
	// 1. 孤儿 stage：install 下 verx-*-* 临时目录
	installBase := filepath.Join(home, "install")
	entries, _ := os.ReadDir(installBase)
	removedStage := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "verx-") {
			p := filepath.Join(installBase, e.Name())
			if !hasCutoff || olderThanMod(p, cutoff) {
				_ = os.RemoveAll(p)
				removedStage++
			}
		}
	}
	// 2. journal 残留：超过 7 天的 tx 日志提示清理（默认保留）
	// 3. 旧版：每个包只留 current，其余版本目录删除
	removedOld := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "verx-") {
			continue
		}
		name := e.Name()
		curVer := currentVersion(name)
		if curVer == "" {
			continue
		}
		vers, _ := os.ReadDir(filepath.Join(installBase, name))
		for _, v := range vers {
			if !v.IsDir() {
				continue
			}
			if v.Name() == curVer {
				continue
			}
			p := filepath.Join(installBase, name, v.Name())
			if !hasCutoff || olderThanMod(p, cutoff) {
				_ = os.RemoveAll(p)
				removedOld++
			}
		}
		// 同步 current.json 的 versions 列表
		syncVersions(name, curVer)
	}
	// 4. 下载缓存
	dl := filepath.Join(home, "cache", "downloads")
	dlEntries, _ := os.ReadDir(dl)
	removedDl := 0
	for _, e := range dlEntries {
		p := filepath.Join(dl, e.Name())
		if !hasCutoff || olderThanMod(p, cutoff) {
			_ = os.Remove(p)
			removedDl++
		}
	}
	fmt.Printf("gc: 孤儿 stage %d 个，旧版 %d 个，下载缓存 %d 个已清\n", removedStage, removedOld, removedDl)
	return nil
}

// CacheClean 对应 verx cache clean。
func CacheClean() error {
	dl := filepath.Join(verxHome(), "cache", "downloads")
	entries, _ := os.ReadDir(dl)
	n := 0
	for _, e := range entries {
		if err := os.Remove(filepath.Join(dl, e.Name())); err == nil {
			n++
		}
	}
	fmt.Printf("cache 已清理 %d 个文件\n", n)
	return nil
}

func currentVersion(name string) string {
	data, err := os.ReadFile(filepath.Join(verxHome(), "install", name, "current.json"))
	if err != nil {
		return ""
	}
	var c struct {
		Current string `json:"current"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return ""
	}
	return c.Current
}

func syncVersions(name, cur string) {
	p := filepath.Join(verxHome(), "install", name, "current.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	vers, _ := os.ReadDir(filepath.Join(verxHome(), "install", name))
	var list []string
	for _, v := range vers {
		if v.IsDir() {
			list = append(list, v.Name())
		}
	}
	m["versions"] = list
	m["current"] = cur
	out, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(p, out, 0o644)
}

func olderThanMod(p string, cutoff time.Time) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return true
	}
	return fi.ModTime().Before(cutoff)
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
