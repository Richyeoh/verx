package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
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
// 优先级：越靠后越全（GitHub 5 平台最后写，覆盖同名 Windows-only）。
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

	for _, b := range c.ScoopBuckets {
		for _, app := range b.Apps {
			m, err := importScoop(b.Repo, app)
			if err != nil {
				report("scoop/"+app, err)
				continue
			}
			report("scoop/"+m.Name, writeManifest(outDir, m))
		}
	}
	// Scoop 全桶：枚举 bucket/*.json 全收，不再手写（report 已计数，不重复加）
	for _, b := range c.ScoopFull {
		importScoopAll(b.Repo, b.Limit, outDir, report)
	}
	for _, pkg := range c.WingetPackages {
		m, err := importWinget(pkg)
		if err != nil {
			report("winget/"+pkg, err)
			continue
		}
		report("winget/"+m.Name, writeManifest(outDir, m))
	}
	// Arch 官方仓批量（pacman/paru 源）：只产 linux/amd64，先写
	if len(c.ArchPackages) > 0 {
		importArchAll(c.ArchPackages, outDir, report)
	}
	// VCPKG（多为 lib 源码包）：先写
	if c.Vcpkg.Limit != 0 {
		importVcpkgAll(c.Vcpkg.Limit, outDir, report)
	}
	// Homebrew 全量：formula bottle + cask，只复用上游 hash，先写
	if c.Brew.MaxFormula > 0 || c.Brew.MaxCask > 0 {
		importBrewAll(c.Brew.MaxFormula, c.Brew.MaxCask, outDir, report)
	}
	// GitHub Top 最后写：5 平台覆盖同名 Windows-only（ripgrep/fd/fzf 等以此为准）
	for _, t := range c.GitHubTop {
		t := t
		m, err := importGitHub(t.Repo, t.Name, &t)
		if err != nil {
			report("github/"+t.Repo, err)
			continue
		}
		report("github/"+m.Name, writeManifest(outDir, m))
	}

	fmt.Printf("\n汇总: 成功 %d，失败 %d\n", ok, fail)
	for _, f := range failed {
		fmt.Printf("  - %s\n", f)
	}
	if ok == 0 {
		return fmt.Errorf("全量转制 0 成功")
	}
	return nil
}
