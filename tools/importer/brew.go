package main

import (
	"fmt"
	"strings"

	"github.com/verx/verx/internal/manifest"
)

// Homebrew 全量：formulae.brew.sh/api/formula.json（~8000）+ cask.json（~6000）。
// formula 用 bottle 文件（已有 sha256，无需下载）；cask 用 url+sha256（mac 专属）。
// 全部只复用上游 hash，速度快，适合全量跑。

type brewBottleFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type brewFormula struct {
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	Homepage string `json:"homepage"`
	License  string `json:"license"`
	Versions struct {
		Stable string `json:"stable"`
	} `json:"versions"`
	Bottle struct {
		Stable struct {
			Files map[string]brewBottleFile `json:"files"`
		} `json:"stable"`
	} `json:"bottle"`
}

type brewCask struct {
	Token    string   `json:"token"`
	Name     []string `json:"name"`
	Desc     string   `json:"desc"`
	Homepage string   `json:"homepage"`
	URL      string   `json:"url"`
	Version  string   `json:"version"`
	SHA256   string   `json:"sha256"`
}

// bottleTag -> GOOS/GOARCH：arm64_*=darwin/arm64，x86_64/mac=darwin/amd64，linux=linux对应架构。
func brewTagToPlatform(tag string) (string, string, bool) {
	t := strings.ToLower(tag)
	if strings.Contains(t, "linux") {
		if strings.Contains(t, "arm64") || strings.Contains(t, "aarch64") {
			return "linux", "arm64", true
		}
		return "linux", "amd64", true
	}
	// 默认 macOS：tag 含 arm 即 arm64，否则 amd64
	if strings.Contains(t, "arm") || strings.Contains(t, "aarch64") {
		return "darwin", "arm64", true
	}
	return "darwin", "amd64", true
}

func importBrewFormula(f brewFormula) (*manifest.Manifest, error) {
	ver := normalizeVersion(f.Versions.Stable)
	if ver == "" {
		return nil, fmt.Errorf("brew 无版本: %s", f.Name)
	}
	name := sanitizeName(f.Name)
	desc := strings.TrimSpace(f.Desc)
	if desc == "" {
		desc = f.Name
	}
	home := f.Homepage
	if !strings.HasPrefix(home, "http") {
		home = "https://formulae.brew.sh/formula/" + f.Name
	}
	lic := strings.TrimSpace(f.License)
	if lic == "" {
		lic = "Unknown"
	}
	seen := map[string]bool{}
	var plats []manifest.Platform
	for tag, bf := range f.Bottle.Stable.Files {
		goos, goarch, ok := brewTagToPlatform(tag)
		if !ok {
			continue
		}
		key := goos + "/" + goarch
		if seen[key] {
			continue
		}
		if !isHex64Full(bf.SHA256) || !strings.HasPrefix(bf.URL, "http") {
			continue
		}
		seen[key] = true
		bin := name
		if goos == "windows" {
			bin += ".exe"
		}
		plats = append(plats, manifest.Platform{
			OS: goos, Arch: goarch, URL: bf.URL, SHA256: strings.ToLower(bf.SHA256),
			Bin: []string{bin}, Strip: guessStrip(bf.URL),
		})
	}
	if len(plats) == 0 {
		return nil, fmt.Errorf("brew 无可用 bottle: %s", f.Name)
	}
	display := f.Name
	if len(display) == 0 {
		display = name
	}
	return &manifest.Manifest{
		API: "v1", Name: name, Kind: "app", Display: display,
		Description: desc, Homepage: home, License: lic,
		Keywords:    []string{"brew", "homebrew"},
		Version:     ver, Channels: map[string]string{"stable": ver},
		Provides: []string{name}, Platforms: plats,
		Test:   name + " --version",
		Caveats: "由 Homebrew formula 自动转制（bottle 复用上游 hash）",
	}, nil
}

func importBrewCask(c brewCask) (*manifest.Manifest, error) {
	if c.URL == "" || !strings.HasPrefix(c.URL, "http") {
		return nil, fmt.Errorf("cask 无 URL: %s", c.Token)
	}
	sha := strings.ToLower(strings.TrimSpace(c.SHA256))
	if sha == "no_check" || !isHex64Full(sha) {
		return nil, fmt.Errorf("cask 无可用 sha256（已拒）: %s", c.Token)
	}
	name := sanitizeName(c.Token)
	display := c.Token
	if len(c.Name) > 0 && strings.TrimSpace(c.Name[0]) != "" {
		display = c.Name[0]
	}
	desc := strings.TrimSpace(c.Desc)
	if desc == "" {
		desc = display
	}
	home := c.Homepage
	if !strings.HasPrefix(home, "http") {
		home = "https://formulae.brew.sh/cask/" + c.Token
	}
	// cask 多为 dmg/pkg 通用包：darwin 双架构同 URL（缺行即不支持由安装时判定）
	bin := name
	ver := normalizeVersion(c.Version)
	if ver == "" {
		ver = "0.0.0"
	}
	plats := []manifest.Platform{
		{OS: "darwin", Arch: "amd64", URL: c.URL, SHA256: sha, Bin: []string{bin}, Strip: 1},
		{OS: "darwin", Arch: "arm64", URL: c.URL, SHA256: sha, Bin: []string{bin}, Strip: 1},
	}
	return &manifest.Manifest{
		API: "v1", Name: name, Kind: "app", Display: display,
		Description: desc, Homepage: home, License: "Unknown",
		Keywords:    []string{"brew", "cask"},
		Version:     ver,
		Channels:    map[string]string{"stable": ver},
		Provides: []string{name}, Platforms: plats,
		Test:   bin + " --version",
		Caveats: "由 Homebrew cask 自动转制（mac 专属，版本由 cask 更新驱动）",
	}, nil
}

// importBrewPackages 清单驱动：拉一次 formula/cask 全索引，内存过滤命中清单的包。
// formulaOnly=true 时跳过 cask（除非在 caskAllowlist 里）；cask 无可用 sha 直接拒。
// 只进合并器，Name 统一回清单名。
func importBrewPackages(pkgs []string, allow map[string]bool, formulaOnly bool, caskAllowlist []string, workers int, collect func(*manifest.Manifest), report func(string, error)) {
	allowNorm := map[string]string{}
	for _, p := range pkgs {
		w := sanitizeName(strings.ToLower(strings.TrimSpace(p)))
		if w == "" {
			continue
		}
		allowNorm[w] = w
	}
	white := map[string]bool{}
	for _, c := range caskAllowlist {
		white[sanitizeName(strings.ToLower(strings.TrimSpace(c)))] = true
	}
	var formulae []brewFormula
	if err := getJSON("https://formulae.brew.sh/api/formula.json", &formulae); err != nil {
		report("brew/formulae-index", err)
		return
	}
	type fjob struct {
		want string
		f    brewFormula
	}
	var fjobs []fjob
	seenF := map[string]bool{}
	for _, f := range formulae {
		norm := sanitizeName(strings.ToLower(strings.TrimSpace(f.Name)))
		want, ok := allowNorm[norm]
		if !ok {
			if !allow[strings.ToLower(strings.TrimSpace(f.Name))] {
				continue
			}
			want = sanitizeName(strings.ToLower(strings.TrimSpace(f.Name)))
		}
		if seenF[norm] {
			continue
		}
		seenF[norm] = true
		fjobs = append(fjobs, fjob{want: want, f: f})
	}
	fmt.Printf("brew formula 清单: %d 个，命中上游 %d 个，并发转制中…\n", len(pkgs), len(fjobs))
	runJobs(fjobs, workers, func(j fjob) {
		m, err := importBrewFormula(j.f)
		if err != nil {
			return // 静默跳过：无 bottle 等
		}
		m.Name = sanitizeName(j.want)
		m.Provides = []string{m.Name}
		collect(m)
	})
	if formulaOnly && len(white) == 0 {
		return
	}
	var casks []brewCask
	if err := getJSON("https://formulae.brew.sh/api/cask.json", &casks); err != nil {
		report("brew/casks-index", err)
		return
	}
	type cjob struct {
		want string
		c    brewCask
	}
	var cjobs []cjob
	for _, ck := range casks {
		norm := sanitizeName(strings.ToLower(strings.TrimSpace(ck.Token)))
		if _, ok := allowNorm[norm]; !ok {
			continue
		}
		if formulaOnly && !white[norm] {
			continue
		}
		cjobs = append(cjobs, cjob{want: norm, c: ck})
	}
	if len(cjobs) == 0 {
		return
	}
	fmt.Printf("brew cask 清单: 命中 %d 个，并发转制中…\n", len(cjobs))
	runJobs(cjobs, workers, func(j cjob) {
		m, err := importBrewCask(j.c)
		if err != nil {
			return
		}
		m.Name = sanitizeName(j.want)
		m.Provides = []string{m.Name}
		collect(m)
	})
}
