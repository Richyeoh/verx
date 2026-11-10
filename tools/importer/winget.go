package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
	ver "github.com/verx/verx/internal/version"
)

type wingetInstaller struct {
	Architecture   string `yaml:"Architecture"`
	InstallerURL   string `yaml:"InstallerUrl"`
	InstallerSha256 string `yaml:"InstallerSha256"`
	InstallerType  string `yaml:"InstallerType"`
	Scope          string `yaml:"Scope"`
}

type wingetInstallerFile struct {
	PackageIdentifier string            `yaml:"PackageIdentifier"`
	PackageVersion    string            `yaml:"PackageVersion"`
	Installers        []wingetInstaller `yaml:"Installers"`
}

type wingetLocaleFile struct {
	PackageIdentifier string `yaml:"PackageIdentifier"`
	PackageVersion    string `yaml:"PackageVersion"`
	PackageName       string `yaml:"PackageName"`
	Publisher         string `yaml:"Publisher"`
	License           string `yaml:"License"`
	ShortDescription  string `yaml:"ShortDescription"`
	Description       string `yaml:"Description"`
	Homepage          string `yaml:"Homepage"`
}

// githubCodeSearch 是 search/code 返回的最小子集。
type githubCodeSearch struct {
	Items []struct {
		Path string `json:"path"`
	} `json:"items"`
}

// importWinget 从 microsoft/winget-pkgs 转制单个包，只产 windows/amd64 一行。
// 流程：search/code 定位 installer.yaml -> 取最新版 -> 解析 x64 安装包 -> 补 locale 元数据。
func importWinget(pkg string) (*manifest.Manifest, error) {
	// 1. 找该 PackageIdentifier 的所有 installer.yaml
	q := "https://api.github.com/search/code?q=PackageIdentifier:+\"" + pkg + "\"+repo:microsoft/winget-pkgs+filename:installer"
	var sr githubCodeSearch
	if err := getJSON(q, &sr); err != nil {
		return nil, fmt.Errorf("winget 搜索失败 %s: %w", pkg, err)
	}
	if len(sr.Items) == 0 {
		// search/code 可能限流，退化为直接猜 master 路径（publisher 首字母目录）
		return nil, fmt.Errorf("winget 未找到包（可能 search API 限流，请配 GITHUB_TOKEN 重试）: %s", pkg)
	}
	// 2. 从 path 里抽版本，选最新
	type cand struct {
		version string
		path    string
		dir     string
	}
	var cands []cand
	for _, it := range sr.Items {
		// manifests/b/<pub>/<pkg>/<ver>/<pkg>.installer.yaml
		parts := strings.Split(it.Path, "/")
		if len(parts) < 6 {
			continue
		}
		if !strings.HasSuffix(parts[len(parts)-1], ".installer.yaml") {
			continue
		}
		ver := parts[len(parts)-2]
		dir := strings.Join(parts[:len(parts)-1], "/")
		cands = append(cands, cand{version: ver, path: it.Path, dir: dir})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("winget 未解析出版本: %s", pkg)
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if ver.Compare(c.version, best.version) > 0 {
			best = c
		}
	}
	// 3. 取 installer.yaml
	rawURL := "https://raw.githubusercontent.com/microsoft/winget-pkgs/master/" + best.path
	txt, err := getText(rawURL)
	if err != nil {
		return nil, err
	}
	var wf wingetInstallerFile
	if err := yaml.Unmarshal([]byte(txt), &wf); err != nil {
		return nil, fmt.Errorf("winget installer.yaml 解析失败: %w", err)
	}
	// 4. 选 x64 安装包：优先 portable/zip（免安装），其次 exe/msi
	var pick *wingetInstaller
	pri := func(t string) int {
		switch strings.ToLower(t) {
		case "portable", "zip":
			return 0
		case "exe", "msix", "appx":
			return 1
		case "msi", "wix":
			return 2
		default:
			return 3
		}
	}
	for i := range wf.Installers {
		ins := &wf.Installers[i]
		arch := strings.ToLower(ins.Architecture)
		if arch != "x64" && arch != "x86" && arch != "neutral" {
			continue
		}
		if arch == "x86" && pick != nil {
			continue
		}
		if pick == nil || pri(ins.InstallerType) < pri(pick.InstallerType) {
			pick = ins
		}
	}
	if pick == nil {
		return nil, fmt.Errorf("winget 包无可用 x64 安装包: %s@%s", pkg, best.version)
	}
	if !isHex64Full(pick.InstallerSha256) {
		return nil, fmt.Errorf("winget sha256 非法（已拒）: %s", pkg)
	}
	// 5. 补元数据：同目录 locale.en-US.yaml
	localeURL := "https://raw.githubusercontent.com/microsoft/winget-pkgs/master/" + best.dir + "/" + pkg + ".locale.en-US.yaml"
	display, publisher, license, desc, homepage := pkg, "", "Unknown", pkg, "https://winget.run/pkg/"+pkg
	if lt, err := getText(localeURL); err == nil {
		var lf wingetLocaleFile
		if yaml.Unmarshal([]byte(lt), &lf) == nil {
			if lf.PackageName != "" {
				display = lf.PackageName
			}
			publisher = lf.Publisher
			if lf.License != "" {
				license = lf.License
			}
			if lf.ShortDescription != "" {
				desc = lf.ShortDescription
			} else if lf.Description != "" {
				desc = firstLine(lf.Description)
			}
			if strings.HasPrefix(lf.Homepage, "http") {
				homepage = lf.Homepage
			}
		}
	}
	_ = publisher
	name := sanitizeName(lastSegment(pkg))
	verStr := normalizeVersion(best.version)
	bin := name + ".exe"
	m := &manifest.Manifest{
		API:         "v1",
		Name:        name,
		Kind:        "app",
		Display:     display,
		Description: desc,
		Homepage:    homepage,
		License:     license,
		Keywords:    []string{"winget", strings.ToLower(publisher)},
		Version:     verStr,
		Channels:    map[string]string{"stable": verStr},
		Provides:    []string{name},
		Platforms: []manifest.Platform{
			{OS: "windows", Arch: "amd64", URL: pick.InstallerURL, SHA256: strings.ToLower(pick.InstallerSha256), Bin: []string{bin}, Strip: guessStrip(pick.InstallerURL)},
		},
		Test:   bin + " --version",
		Caveats: "由 Winget 转制；原始 PackageIdentifier=" + pkg,
	}
	// msi/exe 必须带 installer.silent+uninstall（06 §3.1），否则 Validate 会拒
	lower := strings.ToLower(pick.InstallerURL)
	if strings.HasSuffix(lower, ".msi") || strings.HasSuffix(lower, ".exe") {
		m.Installer = &manifest.Installer{
			Silent:    "/S",
			Uninstall: "winget uninstall --id " + pkg + " --silent",
		}
	}
	return m, nil
}

func isHex64Full(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 64 {
		return false
	}
	return extractHex64(s) != ""
}

func lastSegment(pkg string) string {
	parts := strings.Split(pkg, ".")
	return parts[len(parts)-1]
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
