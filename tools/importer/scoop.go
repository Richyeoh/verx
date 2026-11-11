package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verx/verx/internal/manifest"
)

// Scoop bucket JSON（只取转制需要的子集，兼容 url 单串/数组、hash 多形态）。
type scoopManifest struct {
	Version      string `json:"version"`
	Description  string `json:"description"`
	Homepage     string `json:"homepage"`
	License      any    `json:"license"`
	Bin          any    `json:"bin"`
	Shortcuts    any    `json:"shortcuts"`
	Architecture map[string]struct {
		URL        any `json:"url"`
		Hash       any `json:"hash"`
		Bin        any `json:"bin"`
		ExtractDir any `json:"extract_dir"`
	} `json:"architecture"`
	URL  any `json:"url"`
	Hash any `json:"hash"`
}

func scoopStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			if s, ok := t[0].(string); ok {
				return s
			}
		}
	case []string:
		if len(t) > 0 {
			return t[0]
		}
	}
	return ""
}

func scoopHash(v any) string {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		// scoop 常见 "sha256:abcd..." 或 "abcd..."
		if i := strings.Index(s, ":"); i >= 0 {
			s = s[i+1:]
		}
		return strings.ToLower(strings.TrimSpace(s))
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				if h := scoopHash(s); h != "" {
					return h
				}
			}
		}
	case []string:
		for _, s := range t {
			if h := scoopHash(s); h != "" {
				return h
			}
		}
	}
	return ""
}

func scoopBins(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = append(out, filepath.Base(t))
	case []any:
		for _, e := range t {
			switch x := e.(type) {
			case string:
				out = append(out, filepath.Base(x))
			case []any:
				if len(x) > 0 {
					if s, ok := x[0].(string); ok {
						out = append(out, filepath.Base(s))
					}
				}
			}
		}
	case []string:
		for _, s := range t {
			out = append(out, filepath.Base(s))
		}
	}
	return out
}

// scoopCloneDir 每个 bucket 一个浅克隆，零 GitHub API。
func scoopCloneDir(repo string) string {
	safe := strings.ReplaceAll(strings.ToLower(strings.TrimSuffix(repo, ".git")), "/", "__")
	return filepath.Join(cacheDir(), "scoop-"+safe)
}

func ensureScoopClone(repo string) (string, error) {
	dir := scoopCloneDir(repo)
	if err := sparseClone(repo, dir, []string{"bucket"}); err != nil {
		return "", err
	}
	return filepath.Join(dir, "bucket"), nil
}

func resolveBucketURL(bucket string) (string, string) {
	// 返回 (rawBase, isLocalDir)
	if strings.HasPrefix(bucket, "http://") || strings.HasPrefix(bucket, "https://") {
		return strings.TrimSuffix(bucket, "/"), ""
	}
	if st, err := os.Stat(bucket); err == nil && st.IsDir() {
		return bucket, "local"
	}
	// owner/repo 简写
	bucket = strings.TrimSuffix(bucket, ".git")
	parts := strings.Split(bucket, "/")
	if len(parts) == 1 {
		// ScoopInstaller/Main 简写
		return "https://raw.githubusercontent.com/ScoopInstaller/" + parts[0] + "/master/bucket", ""
	}
	if len(parts) == 2 {
		return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/master/bucket", ""
	}
	return bucket, ""
}

// importScoop 从 Scoop bucket 转制单个 app，只产 windows/amd64 一行（缺行即不支持）。
// 优先本地浅克隆（零 API），本地缺失才回退 raw.githubusercontent.com 单文件拉取。
func importScoop(bucket, app string) (*manifest.Manifest, error) {
	var raw []byte
	// 1. 本地克隆优先
	if !strings.HasPrefix(bucket, "http") {
		if st, err := os.Stat(bucket); err == nil && st.IsDir() {
			raw, err = os.ReadFile(filepath.Join(bucket, app+".json"))
			if err != nil {
				// 本地目录显式指定：直接失败，不兜底
				return nil, fmt.Errorf("读本地 bucket 失败: %w", err)
			}
		} else if bdir, err := ensureScoopClone(strings.TrimSuffix(bucket, ".git")); err == nil {
			if data, err2 := os.ReadFile(filepath.Join(bdir, app+".json")); err2 == nil {
				raw = data
			}
		}
	}
	// 2. 回退：单文件 CDN 拉取（不走 api.github.com，不耗配额）
	if len(raw) == 0 {
		base, local := resolveBucketURL(bucket)
		if local == "local" {
			var err error
			raw, err = os.ReadFile(filepath.Join(base, app+".json"))
			if err != nil {
				return nil, fmt.Errorf("读本地 bucket 失败: %w", err)
			}
		} else {
			var txt string
			var lastErr error
			cands := []string{base + "/" + app + ".json"}
			if strings.Contains(base, "raw.githubusercontent.com") {
				if strings.Contains(base, "/master/") {
					cands = append(cands, strings.Replace(base, "/master/", "/main/", 1)+"/"+app+".json")
				} else if strings.Contains(base, "/main/") {
					cands = append(cands, strings.Replace(base, "/main/", "/master/", 1)+"/"+app+".json")
				}
				if strings.Contains(base, "ScoopInstaller/Main") {
					alt := strings.Replace(base, "ScoopInstaller/Main", "ScoopInstaller/Extras", 1)
					cands = append(cands, alt+"/"+app+".json", strings.Replace(alt, "/master/", "/main/", 1)+"/"+app+".json")
				} else if strings.Contains(base, "ScoopInstaller/Extras") {
					alt := strings.Replace(base, "ScoopInstaller/Extras", "ScoopInstaller/Main", 1)
					cands = append(cands, alt+"/"+app+".json", strings.Replace(alt, "/master/", "/main/", 1)+"/"+app+".json")
				}
			}
			for _, u := range cands {
				t, err2 := getText(u)
				if err2 == nil {
					txt = t
					lastErr = nil
					break
				}
				lastErr = err2
			}
			if lastErr != nil {
				return nil, lastErr
			}
			raw = []byte(txt)
		}
	}
	var sm scoopManifest
	if err := decodeJSON(bytes.NewReader(raw), &sm); err != nil {
		return nil, fmt.Errorf("scoop json 解析失败: %w", err)
	}
	// 取 64bit 架构，兼容无 architecture 字段的老包
	url, hash := "", ""
	var bins []string
	if a64, ok := sm.Architecture["64bit"]; ok {
		url = scoopStr(a64.URL)
		hash = scoopHash(a64.Hash)
		bins = scoopBins(a64.Bin)
	}
	if url == "" {
		url = scoopStr(sm.URL)
		hash = scoopHash(sm.Hash)
	}
	if len(bins) == 0 {
		bins = scoopBins(sm.Bin)
		if len(bins) == 0 {
			bins = scoopBins(sm.Architecture["64bit"].Bin)
		}
	}
	if sm.Version == "" || url == "" {
		return nil, fmt.Errorf("scoop 包字段不全: version=%q url=%q", sm.Version, url)
	}
	if hash == "" {
		return nil, fmt.Errorf("scoop 包无可用 sha256（已拒）：%s", app)
	}
	if len(hash) != 64 || extractHex64(hash) == "" {
		return nil, fmt.Errorf("scoop hash 非 64hex（已拒）: %q", hash)
	}
	name := sanitizeName(app)
	display := app
	desc := sm.Description
	if strings.TrimSpace(desc) == "" {
		desc = app
	}
	home := sm.Homepage
	if !strings.HasPrefix(home, "http") {
		home = "https://scoop.sh"
	}
	lic := "Unknown"
	switch t := sm.License.(type) {
	case string:
		if strings.TrimSpace(t) != "" {
			lic = t
		}
	}
	if len(bins) == 0 {
		bins = []string{name + ".exe"}
	}
	// bin 必须带 .exe（windows）
	for i, b := range bins {
		if !strings.HasSuffix(strings.ToLower(b), ".exe") {
			bins[i] = b + ".exe"
		}
	}
	ver := normalizeVersion(sm.Version)
	m := &manifest.Manifest{
		API:         "v1",
		Name:        name,
		Kind:        "app",
		Display:     display,
		Description: desc,
		Homepage:    home,
		License:     lic,
		Version:     ver,
		Channels:    map[string]string{"stable": ver},
		Provides:    []string{name},
		Platforms: []manifest.Platform{
			{OS: "windows", Arch: "amd64", URL: url, SHA256: strings.ToLower(hash), Bin: bins, Strip: guessStrip(url)},
		},
		Test: bins[0] + " --version",
	}
	lowerURL := strings.ToLower(url)
	if strings.HasSuffix(lowerURL, ".msi") || strings.HasSuffix(lowerURL, ".exe") {
		m.Installer = &manifest.Installer{
			Silent:    "/S",
			Uninstall: "scoop uninstall " + app,
		}
	}
	return m, nil
}

// listScoopApps 返回 bucket 目录下全部 app 名（不带 .json）。
// 优先本地浅克隆目录枚举（零 API），失败才回退 git/trees API。
func listScoopApps(repo string) ([]string, error) {
	repo = strings.TrimSuffix(repo, ".git")
	if bdir, err := ensureScoopClone(repo); err == nil {
		entries, err2 := os.ReadDir(bdir)
		if err2 == nil {
			var out []string
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
					continue
				}
				name := strings.TrimSuffix(e.Name(), ".json")
				if name != "" {
					out = append(out, name)
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}
	// 回退：git/trees API（只剩 2 次调用）
	cands := []string{
		"https://api.github.com/repos/" + repo + "/git/trees/master?recursive=1",
		"https://api.github.com/repos/" + repo + "/git/trees/main?recursive=1",
	}
	var lastErr error
	for _, u := range cands {
		var tr gitTreeResp
		if err := getJSON(u, &tr); err != nil {
			lastErr = err
			continue
		}
		var out []string
		for _, e := range tr.Tree {
			if e.Type != "blob" {
				continue
			}
			if !strings.HasPrefix(e.Path, "bucket/") || !strings.HasSuffix(e.Path, ".json") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(e.Path, "bucket/"), ".json")
			if name != "" {
				out = append(out, name)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
		lastErr = fmt.Errorf("bucket 空: %s", repo)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("列桶失败: %s", repo)
	}
	return nil, lastErr
}

// gitTreeResp 仅回退路径使用。
type gitTreeResp struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

// importScoopPackages 清单驱动：只拉 pkgs 中命中 bucket 的包，不再全桶枚举。
// 每个包按 buckets 顺序试（Main > Extras > Versions），首个成功即收，
// 全部缺失则静默跳过（不算失败，避免 5000 清单刷出几千 FAIL）。
// 只进合并器，不直接落盘。
func importScoopPackages(pkgs []string, buckets []string, workers int, collect func(*manifest.Manifest), report func(string, error)) {
	// 预热克隆：失败的桶直接跳过
	var live []string
	for _, b := range buckets {
		if _, err := ensureScoopClone(strings.TrimSuffix(b, ".git")); err != nil {
			report("scoop/clone-"+b, err)
			continue
		}
		live = append(live, b)
	}
	if len(live) == 0 {
		report("scoop/clone", fmt.Errorf("无可用 bucket 克隆"))
		return
	}
	// 各桶应用集合（本地目录枚举，零 API）
	sets := map[string]map[string]bool{}
	for _, b := range live {
		apps, err := listScoopApps(b)
		if err != nil {
			continue
		}
		s := map[string]bool{}
		for _, a := range apps {
			s[strings.ToLower(a)] = true
			s[sanitizeName(a)] = true
		}
		sets[b] = s
	}
	fmt.Printf("scoop 清单: %d 个，桶 %d 个，并发转制中…\n", len(pkgs), len(live))
	runJobs(pkgs, workers, func(pkg string) {
		want := strings.ToLower(strings.TrimSpace(pkg))
		for _, b := range live {
			// 候选名：verx 名本身 + 原始名（大小写不敏感由 list 归一）
			cands := []string{pkg, want}
			hit := ""
			for _, c := range cands {
				if sets[b][strings.ToLower(c)] {
					hit = c
					break
				}
			}
			if hit == "" {
				continue
			}
			m, err := importScoop(b, hit)
			if err != nil {
				// 同名但转制失败（如无 hash）：换下一个桶试试
				continue
			}
			// 统一回清单名，避免 Main/Extras 同软件产出不同 name
			m.Name = sanitizeName(pkg)
			collect(m)
			return
		}
		// 全桶缺失：静默跳过，不污染 FAIL（scoop 只覆盖 windows 一行）
	})
}
