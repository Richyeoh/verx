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
// 流程：contents API 列版本目录 -> 取最新版 -> 解析 x64 安装包 -> 补 locale 元数据。
// 不用 search/code（需特殊权限且易 400），contents API 更稳。
func importWinget(pkg string) (*manifest.Manifest, error) {
	// 1. 按 winget 目录规则定位：manifests/<首字母小写>/<Publisher>/<PackageIdentifier>/
	dot := strings.Index(pkg, ".")
	if dot <= 0 {
		return nil, fmt.Errorf("winget 包名非法: %s", pkg)
	}
	publisherDir := pkg[:dot]
	letter := strings.ToLower(string(pkg[0]))
	base := "https://api.github.com/repos/microsoft/winget-pkgs/contents/manifests/" + letter + "/" + publisherDir + "/" + pkg
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := getJSON(base, &entries); err != nil {
		return nil, fmt.Errorf("winget 列版本失败 %s: %w", pkg, err)
	}
	// 2. 从目录名里抽版本，选最新
	bestVersion := ""
	for _, e := range entries {
		if e.Type != "dir" {
			continue
		}
		v := strings.TrimSpace(e.Name)
		if v == "" {
			continue
		}
		if bestVersion == "" || ver.Compare(v, bestVersion) > 0 {
			bestVersion = v
		}
	}
	if bestVersion == "" {
		return nil, fmt.Errorf("winget 未解析出版本: %s", pkg)
	}
	dir := "manifests/" + letter + "/" + publisherDir + "/" + pkg + "/" + bestVersion
	// 3. 取 installer.yaml
	rawURL := "https://raw.githubusercontent.com/microsoft/winget-pkgs/master/" + dir + "/" + pkg + ".installer.yaml"
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
		return nil, fmt.Errorf("winget 包无可用 x64 安装包: %s@%s", pkg, bestVersion)
	}
	if !isHex64Full(pick.InstallerSha256) {
		return nil, fmt.Errorf("winget sha256 非法（已拒）: %s", pkg)
	}
	// 5. 补元数据：同目录 locale.en-US.yaml
	localeURL := "https://raw.githubusercontent.com/microsoft/winget-pkgs/master/" + dir + "/" + pkg + ".locale.en-US.yaml"
	display, localePublisher, license, desc, homepage := pkg, "", "Unknown", pkg, "https://winget.run/pkg/"+pkg
	if lt, err := getText(localeURL); err == nil {
		var lf wingetLocaleFile
		if yaml.Unmarshal([]byte(lt), &lf) == nil {
			if lf.PackageName != "" {
				display = lf.PackageName
			}
			localePublisher = lf.Publisher
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
	name := sanitizeName(lastSegment(pkg))
	verStr := normalizeVersion(bestVersion)
	bin := name + ".exe"
	m := &manifest.Manifest{
		API:         "v1",
		Name:        name,
		Kind:        "app",
		Display:     display,
		Description: desc,
		Homepage:    homepage,
		License:     license,
		Keywords:    []string{"winget", strings.ToLower(localePublisher)},
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
