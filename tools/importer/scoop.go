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
		URL       any `json:"url"`
		Hash      any `json:"hash"`
		Bin       any `json:"bin"`
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
func importScoop(bucket, app string) (*manifest.Manifest, error) {
	base, local := resolveBucketURL(bucket)
	var raw []byte
	var err error
	if local == "local" {
		raw, err = os.ReadFile(filepath.Join(base, app+".json"))
		if err != nil {
			return nil, fmt.Errorf("读本地 bucket 失败: %w", err)
		}
	} else {
	// Scoop 官方桶 master/main 双分支兼容：404 则换分支重试；
		// Main/Extras 互为兜底：配错桶也自动找到。
		var txt string
		var lastErr error
		cands := []string{base + "/" + app + ".json"}
		if strings.Contains(base, "raw.githubusercontent.com") {
			if strings.Contains(base, "/master/") {
				cands = append(cands, strings.Replace(base, "/master/", "/main/", 1)+"/"+app+".json")
			} else if strings.Contains(base, "/main/") {
				cands = append(cands, strings.Replace(base, "/main/", "/master/", 1)+"/"+app+".json")
			}
			// Main <-> Extras 兜底
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
	// msi/exe 必须带 installer.silent+uninstall（manifest.Validate 强制），否则 CI 直接拒。
	// Scoop 本身是解压式安装，这里委托给 scoop，保证可卸载、有据可查。
	lowerURL := strings.ToLower(url)
	if strings.HasSuffix(lowerURL, ".msi") || strings.HasSuffix(lowerURL, ".exe") {
		m.Installer = &manifest.Installer{
			Silent:    "/S",
			Uninstall: "scoop uninstall " + app,
		}
	}
	return m, nil
}
