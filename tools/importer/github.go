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

// githubTemplate 对应 config.yaml 里 githubTop 一项。
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
		assetURL := pickAsset(rel.Assets, keywordsHit)
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
		test = defaultBin(name, "windows") + " --version"
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
}, keywords []string) string {
	lower := func(s string) string { return strings.ToLower(s) }
	for _, kw := range keywords {
		k := lower(kw)
		for _, a := range assets {
			n := lower(a.Name)
			if strings.Contains(n, k) {
				// 排除 .sha256/.sig/.sbom 等校验文件本身
				if strings.HasSuffix(n, ".sha256") || strings.HasSuffix(n, ".sha256sum") || strings.HasSuffix(n, ".sig") || strings.HasSuffix(n, ".sbom") {
					continue
				}
				return a.BrowserDownloadURL
			}
		}
	}
	return ""
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
