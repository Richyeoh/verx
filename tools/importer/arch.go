package main

import (
	"fmt"
	"strings"

	"github.com/verx/verx/internal/manifest"
)

// Arch 官方仓批量：https://archlinux.org/packages/search/json/?name=<pkg>
// 只产 linux/amd64（缺行即不支持），URL 用官方 download 跳转解析后的直链，sha 下载计算。

type archSearchResp struct {
	Results []struct {
		Pkgname string `json:"pkgname"`
		Pkgver  string `json:"pkgver"`
		Repo    string `json:"repo"`
		Arch    string `json:"arch"`
		Desc    string `json:"desc"`
		URL     string `json:"url"`
	} `json:"results"`
}

func importArch(pkg string) (*manifest.Manifest, error) {
	var sr archSearchResp
	if err := getJSON("https://archlinux.org/packages/search/json/?name="+pkg+"&arch=x86_64", &sr); err != nil {
		return nil, fmt.Errorf("arch 搜索失败 %s: %w", pkg, err)
	}
	var hit *struct {
		Pkgname string `json:"pkgname"`
		Pkgver  string `json:"pkgver"`
		Repo    string `json:"repo"`
		Arch    string `json:"arch"`
		Desc    string `json:"desc"`
		URL     string `json:"url"`
	}
	for i := range sr.Results {
		if strings.EqualFold(sr.Results[i].Pkgname, pkg) && sr.Results[i].Arch == "x86_64" {
			hit = &sr.Results[i]
			break
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("arch 未找到包: %s", pkg)
	}
	ver := normalizeVersion(strings.Split(hit.Pkgver, "-")[0])
	if ver == "" {
		ver = normalizeVersion(hit.Pkgver)
	}
	name := sanitizeName(hit.Pkgname)
	desc := strings.TrimSpace(hit.Desc)
	if desc == "" {
		desc = hit.Pkgname
	}
	home := hit.URL
	if !strings.HasPrefix(home, "http") {
		home = "https://archlinux.org/packages/" + hit.Repo + "/x86_64/" + hit.Pkgname + "/"
	}
	// 官方 download 直链（302 到镜像 tar.zst），fetcher 跟随跳转即可；sha 下载计算
	dlURL := "https://archlinux.org/packages/" + hit.Repo + "/x86_64/" + hit.Pkgname + "/download/"
	// 先拿最终文件名以便 guessStrip：发 HEAD 拿不到就用通用 .pkg.tar.zst
	sha := tryFetchUpstreamSHA256(dlURL)
	if sha == "" {
		h, err := downloadSHA256(dlURL)
		if err != nil {
			return nil, fmt.Errorf("arch 包 sha256 失败 %s: %w", pkg, err)
		}
		sha = h
	}
	return &manifest.Manifest{
		API: "v1", Name: name, Kind: "app", Display: hit.Pkgname,
		Description: desc, Homepage: home, License: "Unknown",
		Keywords:    []string{"arch", "pacman", strings.ToLower(hit.Repo)},
		Version:     ver, Channels: map[string]string{"stable": ver},
		Provides: []string{name},
		Platforms: []manifest.Platform{
			{OS: "linux", Arch: "amd64", URL: dlURL, SHA256: sha, Bin: []string{name}, Strip: 0},
		},
		Test:   name + " --version",
		Caveats: "由 Arch 官方仓自动转制（pacman/paru 源）",
	}, nil
}

// importArchAll 批量：config 里 archPackages 列表；单个失败跳过。
func importArchAll(pkgs []string, outDir string, report func(string, error)) (int, int) {
	ok, fail := 0, 0
	for _, p := range pkgs {
		m, err := importArch(p)
		if err != nil {
			report("arch/"+p, err)
			fail++
			continue
		}
		if err := writeManifest(outDir, m); err != nil {
			report("arch/"+m.Name, err)
			fail++
			continue
		}
		report("arch/"+m.Name, nil)
		ok++
	}
	return ok, fail
}
