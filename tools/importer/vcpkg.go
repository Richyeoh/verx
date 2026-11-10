package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/verx/verx/internal/manifest"
)

// VCPKG 全量：microsoft/vcpkg ports/<port>/vcpkg.json（~2000+，多为 lib）。
// 转制为 kind=lib 源码包：三平台同源 tarball，exports 通用 include/lib。
// portfile.cmake 里只有 SHA512（manifest 要 sha256），所以下载算 hash；
// 全量太重，默认只收 limit 个（config 可配），剩下的每天增量跑。

type vcpkgMeta struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description any    `json:"description"`
	Homepage    string `json:"homepage"`
	License     any    `json:"license"`
}

var portURLRe = regexp.MustCompile(`(?i)\bURLS?\s+"([^"]+)"`)

func vcpkgDesc(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func vcpkgLicense(v any) string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) != "" {
			return t
		}
	case []any:
		var parts []string
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ", ")
		}
	}
	return "Unknown"
}

func listVcpkgPorts(limit int) ([]string, error) {
	var tr gitTreeResp
	if err := getJSON("https://api.github.com/repos/microsoft/vcpkg/git/trees/master?recursive=1", &tr); err != nil {
		return nil, fmt.Errorf("列 vcpkg ports 失败: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range tr.Tree {
		if e.Type != "blob" {
			continue
		}
		// ports/<port>/vcpkg.json
		if !strings.HasPrefix(e.Path, "ports/") || !strings.HasSuffix(e.Path, "/vcpkg.json") {
			continue
		}
		port := strings.TrimSuffix(strings.TrimPrefix(e.Path, "ports/"), "/vcpkg.json")
		if port == "" || strings.Contains(port, "/") || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func importVcpkg(port string) (*manifest.Manifest, error) {
	base := "https://raw.githubusercontent.com/microsoft/vcpkg/master/ports/" + port + "/"
	var meta vcpkgMeta
	if err := getJSON(base+"vcpkg.json", &meta); err != nil {
		return nil, fmt.Errorf("vcpkg meta 失败 %s: %w", port, err)
	}
	// portfile 找源 tarball
	pf, err := getText(base + "portfile.cmake")
	if err != nil {
		return nil, fmt.Errorf("vcpkg portfile 失败 %s: %w", port, err)
	}
	url := ""
	for _, m := range portURLRe.FindAllStringSubmatch(pf, -1) {
		u := strings.TrimSpace(m[1])
		if strings.HasPrefix(u, "http") {
			// 去掉 ${VERSION} 模板变量的条目（算不出固定 URL）
			if strings.Contains(u, "${") {
				continue
			}
			url = u
			break
		}
	}
	if url == "" {
		return nil, fmt.Errorf("vcpkg 无固定 URL（模板变量，已跳过）: %s", port)
	}
	ver := normalizeVersion(meta.Version)
	if ver == "" {
		ver = "0.0.0"
	}
	name := sanitizeName(port)
	if name == "" {
		name = sanitizeName(meta.Name)
	}
	desc := vcpkgDesc(meta.Description)
	if strings.TrimSpace(desc) == "" {
		desc = port
	}
	home := meta.Homepage
	if !strings.HasPrefix(home, "http") {
		home = "https://vcpkg.io/en/packages.html"
	}
	lic := vcpkgLicense(meta.License)
	h, err := downloadSHA256(url)
	if err != nil {
		return nil, fmt.Errorf("vcpkg 下载算 hash 失败 %s: %w", port, err)
	}
	plats := []manifest.Platform{
		{OS: "windows", Arch: "amd64", URL: url, SHA256: h, Strip: 1},
		{OS: "linux", Arch: "amd64", URL: url, SHA256: h, Strip: 1},
		{OS: "darwin", Arch: "arm64", URL: url, SHA256: h, Strip: 1},
	}
	return &manifest.Manifest{
		API: "v1", Name: name, Kind: "lib", Display: port,
		Description: desc, Homepage: home, License: lic,
		Keywords:    []string{"vcpkg"},
		Version:     ver, Channels: map[string]string{"stable": ver},
		Provides: []string{name}, Platforms: plats,
		Exports: &manifest.Exports{Include: []string{"include"}, Lib: []string{name}},
		Caveats: "由 VCPKG ports/" + port + " 自动转制（源码包，需构建）",
	}, nil
}

func importVcpkgAll(limit int, collect func(*manifest.Manifest), report func(string, error)) {
	ports, err := listVcpkgPorts(limit)
	if err != nil {
		report("vcpkg/list", err)
		return
	}
	fmt.Printf("vcpkg: %d 个，并发转制中…\n", len(ports))
	runJobs(ports, 16, func(p string) {
		m, err := importVcpkg(p)
		if err != nil {
			report("vcpkg/"+p, err)
			return
		}
		collect(m)
		fmt.Printf("COLLECT vcpkg/%s\n", m.Name)
	})
}
