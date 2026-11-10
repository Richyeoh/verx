package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
)

type fullConfig struct {
	ScoopBuckets []struct {
		Repo string   `yaml:"repo"`
		Apps []string `yaml:"apps"`
	} `yaml:"scoopBuckets"`
	// 全桶：不再手写 apps，枚举 bucket/*.json 全收；limit<=0 全量
	ScoopFull []struct {
		Repo  string `yaml:"repo"`
		Limit int    `yaml:"limit"`
	} `yaml:"scoopFull"`
	WingetPackages []string `yaml:"wingetPackages"`
	GitHubTop      []githubTemplate `yaml:"githubTop"`
	// Homebrew 全量：formula/cask 只复用上游 hash；limit<=0 全量
	Brew struct {
		MaxFormula int `yaml:"maxFormula"`
		MaxCask    int `yaml:"maxCask"`
	} `yaml:"brew"`
	// Arch 官方仓批量（pacman/paru 源）：只产 linux/amd64
	ArchPackages []string `yaml:"archPackages"`
	// VCPKG 全量（多为 lib 源码包）：limit<=0 全量，默认配小批量增量跑
	Vcpkg struct {
		Limit int `yaml:"limit"`
	} `yaml:"vcpkg"`
}

func loadConfig(path string) (*fullConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c fullConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config 解析失败: %w", err)
	}
	return &c, nil
}

// runAll 按 config 跑全量：Scoop 全桶 + Winget + Arch + VCPKG + Brew + GitHub Top。
// 多源合并：同名包在 merger 里按 os/arch 合并平台行后一次落盘，
// 如 ripgrep = scoop(windows) + brew(darwin/linux) + github(5 平台) 合成一条 verx manifest。
// 单个失败只记错继续跑，最后汇总；全部写进 out/manifests/<name>/<name>.yaml。
func runAll(configPath, outDir string) error {
	c, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	ok, fail := 0, 0
	var failed []string
	report := func(name string, err error) {
		if err != nil {
			fail++
			failed = append(failed, name+": "+err.Error())
			fmt.Printf("FAIL %s: %v\n", name, err)
		} else {
			ok++
			fmt.Printf("OK %s\n", name)
		}
	}

	mg := newMerger()
	collect := func(m *manifest.Manifest) { mg.add(m) }

	for _, b := range c.ScoopBuckets {
		for _, app := range b.Apps {
			m, err := importScoop(b.Repo, app)
			if err != nil {
				report("scoop/"+app, err)
				continue
			}
			collect(m)
			fmt.Printf("COLLECT scoop/%s\n", m.Name)
		}
	}
	// Scoop 全桶：枚举 bucket/*.json 全收，不再手写
	for _, b := range c.ScoopFull {
		importScoopAll(b.Repo, b.Limit, collect, report)
	}
	for _, pkg := range c.WingetPackages {
		m, err := importWinget(pkg)
		if err != nil {
			report("winget/"+pkg, err)
			continue
		}
		collect(m)
		fmt.Printf("COLLECT winget/%s\n", m.Name)
	}
	// Arch 官方仓批量（pacman/paru 源）：只产 linux/amd64，进合并器补行
	if len(c.ArchPackages) > 0 {
		importArchAll(c.ArchPackages, collect, report)
	}
	// VCPKG（多为 lib 源码包）：进合并器
	if c.Vcpkg.Limit != 0 {
		importVcpkgAll(c.Vcpkg.Limit, collect, report)
	}
	// Homebrew 全量：formula bottle + cask，只复用上游 hash，进合并器
	if c.Brew.MaxFormula > 0 || c.Brew.MaxCask > 0 {
		importBrewAll(c.Brew.MaxFormula, c.Brew.MaxCask, collect, report)
	}
	// GitHub Top：5 平台最全，进合并器覆盖/补齐同名 Windows-only
	for _, t := range c.GitHubTop {
		t := t
		m, err := importGitHub(t.Repo, t.Name, &t)
		if err != nil {
			report("github/"+t.Repo, err)
			continue
		}
		collect(m)
		fmt.Printf("COLLECT github/%s\n", m.Name)
	}

	// 统一落盘：同名多源已合成一条 manifest；只有这里才计成功数
	mok, mfail := mg.flush(outDir, report)
	ok += mok
	fail += mfail

	fmt.Printf("\n汇总: 成功 %d，失败 %d\n", ok, fail)
	for _, f := range failed {
		fmt.Printf("  - %s\n", f)
	}
	if ok == 0 {
		return fmt.Errorf("全量转制 0 成功")
	}
	return nil
}
