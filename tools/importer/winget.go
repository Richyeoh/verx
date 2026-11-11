package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
	ver "github.com/verx/verx/internal/version"
)

type wingetInstaller struct {
	Architecture    string `yaml:"Architecture"`
	InstallerURL    string `yaml:"InstallerUrl"`
	InstallerSha256 string `yaml:"InstallerSha256"`
	InstallerType   string `yaml:"InstallerType"`
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

// wingetCloneDir 本地 no-checkout 克隆根目录，零 GitHub API。
// 只拉 commit + tree（无 blob），枚举用 git ls-tree，文件内容走 CDN 按需拉。
func wingetCloneDir() string { return filepath.Join(cacheDir(), "winget-pkgs") }

func ensureWingetClone() error {
	return cloneNoCheckout("microsoft/winget-pkgs", wingetCloneDir())
}

// wingetVersionCache 全量时一次建好的 包 -> 最新版 映射，避免每个包再起 git 进程。
var (
	wingetVersionCache   = map[string]string{}
	wingetVersionCacheMu sync.RWMutex
)

func wingetCachedVersion(pkg string) string {
	wingetVersionCacheMu.RLock()
	defer wingetVersionCacheMu.RUnlock()
	return wingetVersionCache[pkg]
}

func wingetCacheVersions(all map[string]string) {
	wingetVersionCacheMu.Lock()
	defer wingetVersionCacheMu.Unlock()
	for k, v := range all {
		wingetVersionCache[k] = v
	}
}

// wingetPkgDir 把 PackageIdentifier 映射到 manifests/<首字母>/<Publisher>/<ID> 相对路径。
func wingetPkgRelDir(pkg string) string {
	dot := strings.Index(pkg, ".")
	if dot <= 0 {
		return ""
	}
	publisherDir := pkg[:dot]
	letter := strings.ToLower(string(pkg[0]))
	return "manifests/" + letter + "/" + publisherDir + "/" + pkg
}

// wingetBestVersion 取包的最新版：先查全量缓存，缺失则单包 git ls-tree（单包调试用）。
func wingetBestVersion(pkg string) (string, string, error) {
	if v := wingetCachedVersion(pkg); v != "" {
		return v, wingetPkgRelDir(pkg), nil
	}
	rel := wingetPkgRelDir(pkg)
	if rel == "" {
		return "", "", fmt.Errorf("winget 包名非法: %s", pkg)
	}
	if err := ensureWingetClone(); err != nil {
		return "", "", err
	}
	vers, err := gitLsTreeDirs(wingetCloneDir(), rel)
	if err != nil {
		return "", "", fmt.Errorf("winget 列版本失败 %s: %w", pkg, err)
	}
	best := ""
	for _, v := range vers {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if best == "" || ver.Compare(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("winget 未解析出版本: %s", pkg)
	}
	return best, rel, nil
}

// wingetRawFile 经 CDN 拉单个 yaml（raw.githubusercontent.com，不耗 API 配额，不带 token）。
func wingetRawFile(relDir, version, file string) (string, error) {
	// winget-pkgs 默认分支 master
	u := "https://raw.githubusercontent.com/microsoft/winget-pkgs/master/" + relDir + "/" + version + "/" + file
	return getText(u)
}

// importWinget 从本地枚举 + CDN 文件转制单个包，只产 windows/amd64 一行，全程零 GitHub API。
func importWinget(pkg string) (*manifest.Manifest, error) {
	bestVersion, relDir, err := wingetBestVersion(pkg)
	if err != nil {
		return nil, err
	}
	txt, err := wingetRawFile(relDir, bestVersion, pkg+".installer.yaml")
	if err != nil {
		return nil, fmt.Errorf("winget 取 installer 失败 %s: %w", pkg, err)
	}
	var wf wingetInstallerFile
	if err := yaml.Unmarshal([]byte(txt), &wf); err != nil {
		return nil, fmt.Errorf("winget installer.yaml 解析失败: %w", err)
	}
	// 选 x64 安装包：优先 portable/zip（免安装），其次 exe/msi
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
	// 补元数据：同目录 locale.en-US.yaml（CDN 拉，缺失不致命）
	display, localePublisher, license, desc, homepage := pkg, "", "Unknown", pkg, "https://winget.run/pkg/"+pkg
	if lt, err := wingetRawFile(relDir, bestVersion, pkg+".locale.en-US.yaml"); err == nil {
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
		Test:    bin + " --version",
		Caveats: "由 Winget 转制；原始 PackageIdentifier=" + pkg,
	}
	// msi/exe 必须带 installer.silent+uninstall，否则 Validate 会拒
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

// listWingetPackages 全枚举本地 no-checkout 克隆下全部 PackageIdentifier。
// 单次 git ls-tree -r 拿全量目录树，在内存里按 manifests/<首字母>/<Publisher>/<包>/<版本>
// 分组，包级去重 + 每个包算出最新版写入缓存。零 GitHub API，零 blob 下载。
func listWingetPackages() ([]string, error) {
	if err := ensureWingetClone(); err != nil {
		return nil, err
	}
	dirs, err := gitLsTreeAllDirs(wingetCloneDir(), "manifests")
	if err != nil {
		return nil, fmt.Errorf("winget 本地枚举失败: %w", err)
	}
	// pkgID -> bestVersion
	best := map[string]string{}
	for _, d := range dirs {
		parts := strings.Split(d, "/")
		// 期望 manifests/<letter>/<publisher>/<pkg>[/<version>[/...]]
		if len(parts) < 4 || parts[0] != "manifests" {
			continue
		}
		pkg := parts[2] + "." + parts[3]
		if len(parts) == 4 {
			// 包目录本身：确保存在（版本在子目录里算）
			if _, ok := best[pkg]; !ok {
				best[pkg] = ""
			}
			continue
		}
		v := strings.TrimSpace(parts[4])
		if v == "" {
			continue
		}
		if cur := best[pkg]; cur == "" || ver.Compare(v, cur) > 0 {
			best[pkg] = v
		}
	}
	var out []string
	resolved := map[string]string{}
	for pkg, v := range best {
		if v == "" {
			continue
		}
		out = append(out, pkg)
		resolved[pkg] = v
	}
	if len(out) == 0 {
		// 兜底：tree 可能超大被截断？直接报错，别静默产出空 registry
		return nil, fmt.Errorf("winget 本地枚举为空")
	}
	wingetCacheVersions(resolved)
	sort.Strings(out)
	return out, nil
}

// importWingetPackages 清单驱动：本地枚举一次建索引，只拉命中清单的包。
// 匹配：verx 名 vs PackageIdentifier 尾段/全名归一化；未命中静默跳过。
// 只进合并器，Name 统一回清单名，避免 MSVC/尾缀产出不同 name。
func importWingetPackages(pkgs []string, allow map[string]bool, workers int, collect func(*manifest.Manifest), report func(string, error)) {
	all, err := listWingetPackages()
	if err != nil {
		report("winget/list", err)
		return
	}
	// 归一化索引：norm -> pkgIDs
	idx := map[string][]string{}
	addIdx := func(key, pkg string) {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return
		}
		idx[key] = append(idx[key], pkg)
	}
	for _, pkg := range all {
		addIdx(pkg, pkg)
		seg := lastSegment(pkg)
		addIdx(seg, pkg)
		addIdx(sanitizeName(seg), pkg)
		addIdx(sanitizeName(strings.ReplaceAll(pkg, ".", "-")), pkg)
	}
	type job struct {
		want string
		pkg  string
	}
	var jobs []job
	seenPkg := map[string]bool{}
	for _, want := range pkgs {
		w := strings.ToLower(strings.TrimSpace(want))
		if w == "" {
			continue
		}
		cands := idx[w]
		if len(cands) == 0 {
			continue
		}
		// 同一上游包只拉一次（多个 verx 名命中同一 pkgID 时首个赢）
		pick := cands[0]
		for _, c := range cands[1:] {
			if c < pick {
				pick = c
			}
		}
		if seenPkg[pick] {
			continue
		}
		seenPkg[pick] = true
		jobs = append(jobs, job{want: w, pkg: pick})
	}
	_ = allow
	fmt.Printf("winget 清单: %d 个，命中上游 %d 个，并发转制中…\n", len(pkgs), len(jobs))
	runJobs(jobs, workers, func(j job) {
		m, err := importWinget(j.pkg)
		if err != nil {
			return // 静默跳过：winget 只补 windows 一行
		}
		m.Name = sanitizeName(j.want)
		m.Provides = []string{m.Name}
		collect(m)
	})
}
