package main

import (
	"fmt"
	"strings"

	"github.com/verx/verx/internal/manifest"
)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

type githubRepoInfo struct {
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	License     struct {
		SpdxID string `json:"spdx_id"`
	} `json:"license"`
}

// githubTemplate 保留给单包调试用（importer github --repo），全量不再用手写列表。
type githubTemplate struct {
	Repo        string              `yaml:"repo"`
	Name        string              `yaml:"name"`
	Display     string              `yaml:"display"`
	Description string              `yaml:"description"`
	Homepage    string              `yaml:"homepage"`
	License     string              `yaml:"license"`
	AssetMap    map[string][]string `yaml:"assetMap"`
	BinMap      map[string][]string `yaml:"binMap"`
	Test        string              `yaml:"test"`
	Keywords    []string            `yaml:"keywords"`
}

// listGithubRepos 自动发现：按 stars 排序拉取 GitHub 全站仓库（search API 上限 1000）。
// minStars<=0 默认 2000；limit<=0 全量（最多 1000，search API 上限）。
// 全量不再手写 top 列表，和 scoop/winget/brew/arch/vcpkg 一样只配开关+阈值。
func listGithubRepos(minStars, limit int) ([]string, error) {
	if minStars <= 0 {
		minStars = 2000
	}
	const perPage = 100
	var out []string
	seen := map[string]bool{}
	page := 1
	for {
		u := fmt.Sprintf("https://api.github.com/search/repositories?q=stars:%%3E%d&sort=stars&order=desc&per_page=%d&page=%d", minStars, perPage, page)
		var sr struct {
			TotalCount int `json:"total_count"`
			Items      []struct {
				FullName string `json:"full_name"`
			} `json:"items"`
		}
		if err := getJSON(u, &sr); err != nil {
			if len(out) == 0 {
				return nil, fmt.Errorf("github 自动发现失败: %w", err)
			}
			break
		}
		if len(sr.Items) == 0 {
			break
		}
		for _, it := range sr.Items {
			if it.FullName == "" || seen[it.FullName] {
				continue
			}
			seen[it.FullName] = true
			out = append(out, it.FullName)
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
		if len(sr.Items) < perPage {
			break
		}
		page++
		if page > 10 {
			break // search API 最多返回前 1000
		}
	}
	return out, nil
}

// importGithubAll 自动发现批量：先按 stars 拉全站仓库，再逐个 latest release 转制。
// 无 release / 无匹配资产的仓库直接跳过（缺行即不支持），只收有可用二进制资产的。
// 只进合并器，不直接落盘。内部 16 并发。
func importGithubAll(minStars, limit int, collect func(*manifest.Manifest), report func(string, error)) {
	repos, err := listGithubRepos(minStars, limit)
	if err != nil {
		report("github/list", err)
		return
	}
	fmt.Printf("github 自动发现: %d 个仓库，并发转制中…\n", len(repos))
	runJobs(repos, 16, func(repo string) {
		// 全自动：name 为空取 repo 名，tmpl=nil 走默认资产关键字+自动元数据
		m, err := importGitHub(repo, "", nil)
		if err != nil {
			report("github/"+repo, err)
			return
		}
		collect(m)
		fmt.Printf("COLLECT github/%s\n", m.Name)
	})
}

// importGitHub 从 GitHub latest release 转制。assetMap 为空时自动猜测。
func importGitHub(repo, name string, tmpl *githubTemplate) (*manifest.Manifest, error) {
	var rel githubRelease
	if err := getJSON("https://api.github.com/repos/"+repo+"/releases/latest", &rel); err != nil {
		return nil, fmt.Errorf("github latest 失败 %s: %w", repo, err)
	}
	verStr := normalizeVersion(rel.TagName)
	if verStr == "" {
		return nil, fmt.Errorf("github 取不到版本: %s", repo)
	}
	if name == "" {
		if tmpl != nil && tmpl.Name != "" {
			name = tmpl.Name
		} else {
			parts := strings.Split(repo, "/")
			name = sanitizeName(parts[len(parts)-1])
		}
	} else {
		name = sanitizeName(name)
	}
	// 元数据：模板优先，否则调 repo API
	display, desc, homepage, license := name, name, "https://github.com/"+repo, "Unknown"
	var keywords []string
	var assetMap map[string][]string
	var binMap map[string][]string
	var test string
	if tmpl != nil {
		if tmpl.Display != "" {
			display = tmpl.Display
		}
		if tmpl.Description != "" {
			desc = tmpl.Description
		}
		if tmpl.Homepage != "" {
			homepage = tmpl.Homepage
		}
		if tmpl.License != "" {
			license = tmpl.License
		}
		assetMap = tmpl.AssetMap
		binMap = tmpl.BinMap
		test = tmpl.Test
		keywords = tmpl.Keywords
	}
	if desc == name || homepage == "" {
		var info githubRepoInfo
		if err := getJSON("https://api.github.com/repos/"+repo, &info); err == nil {
			if desc == name && strings.TrimSpace(info.Description) != "" {
				desc = strings.TrimSpace(info.Description)
			}
			if strings.HasPrefix(info.Homepage, "http") {
				homepage = info.Homepage
			} else if homepage == "" {
				homepage = "https://github.com/" + repo
			}
			if license == "Unknown" && info.License.SpdxID != "" && info.License.SpdxID != "NOASSERTION" {
				license = info.License.SpdxID
			}
		}
	}
	// 平台匹配
	platforms := []struct{ os, arch string }{
		{"windows", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"},
	}
	var out []manifest.Platform
	for _, p := range platforms {
		key := p.os + "/" + p.arch
		var keywordsHit []string
		if assetMap != nil {
			keywordsHit = assetMap[key]
		}
		if len(keywordsHit) == 0 {
			keywordsHit = defaultAssetKeywords(p.os, p.arch)
		}
		assetURL := pickAsset(rel.Assets, keywordsHit, p.os, p.arch)
		if assetURL == "" {
			continue // 缺行即不支持
		}
		bins := binMap[key]
		if len(bins) == 0 {
			bins = []string{defaultBin(name, p.os)}
		}
		sha := tryFetchUpstreamSHA256(assetURL)
		if sha == "" {
			h, err := downloadSHA256(assetURL)
			if err != nil {
				return nil, fmt.Errorf("资产 sha256 失败 %s: %w", assetURL, err)
			}
			sha = h
		}
		out = append(out, manifest.Platform{
			OS: p.os, Arch: p.arch, URL: assetURL, SHA256: sha, Bin: bins, Strip: guessStrip(assetURL),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("github 包无可用资产: %s@%s", repo, verStr)
	}
	if test == "" {
		test = name + " --version"
	}
	m := &manifest.Manifest{
		API:         "v1",
		Name:        name,
		Kind:        "app",
		Display:     display,
		Description: desc,
		Homepage:    homepage,
		License:     license,
		Keywords:    append([]string{"github", strings.ToLower(strings.Split(repo, "/")[0])}, keywords...),
		Version:     verStr,
		Channels:    map[string]string{"stable": verStr},
		Provides:    []string{name},
		Platforms:   out,
		Test:        test,
		Caveats:     "由 GitHub " + repo + " latest 自动转制",
	}
	// 单文件 exe/msi 资产（如 jq）必须带 installer，否则 Validate 直接拒
	for _, p := range out {
		lower := strings.ToLower(p.URL)
		if strings.HasSuffix(lower, ".msi") || strings.HasSuffix(lower, ".exe") {
			m.Installer = &manifest.Installer{
				Silent:    "/S",
				Uninstall: "verx remove " + name + "@" + verStr,
			}
			break
		}
	}
	return m, nil
}

type simpleAsset struct {
	Name               string
	BrowserDownloadURL string
}

func pickAsset(assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}, keywords []string, goos, goarch string) string {
	lower := func(s string) string { return strings.ToLower(s) }
	for _, kw := range keywords {
		k := lower(kw)
		for _, a := range assets {
			n := lower(a.Name)
			if !strings.Contains(n, k) {
				continue
			}
			// 排除 .sha256/.sig/.sbom 等校验文件本身
			if strings.HasSuffix(n, ".sha256") || strings.HasSuffix(n, ".sha256sum") || strings.HasSuffix(n, ".sig") || strings.HasSuffix(n, ".sbom") {
				continue
			}
			if !archOk(n, goarch) {
				continue
			}
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// archOk 防止 amd64 命中 aarch64/arm64 包（ripgrep/fd 之前就错配过）。
func archOk(lowerName, goarch string) bool {
	n := lowerName
	if goarch == "amd64" {
		// amd64 行绝不允许 arm 资产
		for _, bad := range []string{"aarch64", "arm64", "armv7", "armv6", "armhf", "i686", "i386", "32-bit", "win32", "-32.", "_32."} {
			if strings.Contains(n, bad) {
				return false
			}
		}
		return true
	}
	if goarch == "arm64" {
		// arm64 行绝不允许 x86_64/amd64 资产
		for _, bad := range []string{"x86_64", "x86-64", "amd64", "x64-windows", "x64_macos", "win64", "x64-linux", "i686", "i386", "intel"} {
			// 例外：文件名本身就是 aarch64 时，里面不会出现这些 bad（aarch64 不含 x86_64），可直接过
			if strings.Contains(n, bad) {
				// aarch64-apple-darwin.tar.gz 不含上面任何 bad，直接放行；
				// x86_64-apple-darwin 则命中 x86_64 被拒，符合预期
				return false
			}
		}
		return true
	}
	return true
}

func defaultAssetKeywords(os, arch string) []string {
	switch os + "/" + arch {
	case "windows/amd64":
		return []string{"pc-windows-msvc.zip", "windows-x86_64.zip", "win64.zip", "windows_amd64.zip"}
	case "darwin/amd64":
		return []string{"apple-darwin.tar.gz", "darwin_amd64.tar.gz", "macos-x86_64.tar.gz"}
	case "darwin/arm64":
		return []string{"aarch64-apple-darwin.tar.gz", "darwin_arm64.tar.gz", "macos-aarch64.tar.gz"}
	case "linux/amd64":
		return []string{"unknown-linux-musl.tar.gz", "linux_amd64.tar.gz", "linux-x86_64.tar.gz"}
	case "linux/arm64":
		return []string{"aarch64-unknown-linux-gnu.tar.gz", "linux_arm64.tar.gz", "linux-aarch64.tar.gz"}
	}
	return nil
}

func defaultBin(name, os string) string {
	if os == "windows" {
		return name + ".exe"
	}
	return name
}
