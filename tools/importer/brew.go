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

// importBrewAll 全量：limit<=0 不限；casks 默认只收前 N（dmg 体积大但只复用 hash，不下载）。
func importBrewAll(maxFormula, maxCask int, outDir string, report func(string, error)) (int, int) {
	ok, fail := 0, 0
	var formulae []brewFormula
	if err := getJSON("https://formulae.brew.sh/api/formula.json", &formulae); err != nil {
		report("brew/formulae-index", err)
		return 0, 1
	}
	n := 0
	for i := range formulae {
		if maxFormula > 0 && n >= maxFormula {
			break
		}
		n++
		m, err := importBrewFormula(formulae[i])
		if err != nil {
			report("brew/"+formulae[i].Name, err)
			fail++
			continue
		}
		if err := writeManifest(outDir, m); err != nil {
			report("brew/"+m.Name, err)
			fail++
			continue
		}
		report("brew/"+m.Name, nil)
		ok++
	}
	var casks []brewCask
	if err := getJSON("https://formulae.brew.sh/api/cask.json", &casks); err != nil {
		report("brew/casks-index", err)
		return ok, fail + 1
	}
	n = 0
	for i := range casks {
		if maxCask > 0 && n >= maxCask {
			break
		}
		n++
		m, err := importBrewCask(casks[i])
		if err != nil {
			report("brew-cask/"+casks[i].Token, err)
			fail++
			continue
		}
		if err := writeManifest(outDir, m); err != nil {
			report("brew-cask/"+m.Name, err)
			fail++
			continue
		}
		report("brew-cask/"+m.Name, nil)
		ok++
	}
	return ok, fail
}
