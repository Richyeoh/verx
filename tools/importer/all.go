package main

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
)

type fullConfig struct {
	// Scoop 全桶：枚举 bucket/*.json 全收；limit<=0 全量
	ScoopFull []struct {
		Repo  string `yaml:"repo"`
		Limit int    `yaml:"limit"`
	} `yaml:"scoopFull"`
	// Winget 全量：full=true 全枚举；packages 非空只跑这几个（调试用）
	Winget struct {
		Full     bool     `yaml:"full"`
		Packages []string `yaml:"packages"`
	} `yaml:"winget"`
	GitHubTop []githubTemplate `yaml:"githubTop"`
	// Homebrew 全量：只复用上游 hash；max*<=0 全量
	Brew struct {
		MaxFormula int `yaml:"maxFormula"`
		MaxCask    int `yaml:"maxCask"`
	} `yaml:"brew"`
	// Arch 全量：full=true 枚举 Core+Extra 全仓；packages 非空只跑这几个
	Arch struct {
		Full     bool     `yaml:"full"`
		Packages []string `yaml:"packages"`
	} `yaml:"arch"`
	// VCPKG 全量：limit<=0 全量
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

// runAll 真全量：Scoop 全桶 + Winget 全枚举 + Arch 全仓 + VCPKG 全枚举 + Brew 全量 + GitHub 精选。
// 多源合并：同名包在 merger 里按 os/arch 合并平台行后一次落盘，
// 如 ripgrep = scoop(windows) + brew(darwin/linux) + github(5 平台) 合成一条 verx manifest。
// 全程并发（worker 池），单个失败只记错继续跑；全部写进 out/manifests/<name>/<name>.yaml。
func runAll(configPath, outDir string) error {
	c, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	ok, fail := 0, 0
	var failed []string
	report := func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
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

	// A. Scoop 全桶（内部并发）
	for _, b := range c.ScoopFull {
		importScoopAll(b.Repo, b.Limit, collect, report)
	}
	// B. Winget 全量（内部并发）：只产 windows/amd64，进合并器补行
	if len(c.Winget.Packages) > 0 {
		importWingetAll(c.Winget.Packages, collect, report)
	} else if c.Winget.Full {
		pkgs, err := listWingetPackages()
		if err != nil {
			report("winget/list", err)
		} else {
			fmt.Printf("winget 全枚举: %d 个\n", len(pkgs))
			importWingetAll(pkgs, collect, report)
		}
	}
	// E. Arch 全量（内部并发）：只产 linux/amd64，进合并器补行
	if len(c.Arch.Packages) > 0 {
		importArchAll(c.Arch.Packages, collect, report)
	} else if c.Arch.Full {
		pkgs, err := listArchFull()
		if err != nil {
			report("arch/list", err)
		} else {
			fmt.Printf("arch 全枚举: %d 个\n", len(pkgs))
			importArchAll(pkgs, collect, report)
		}
	}
	// F. VCPKG 全枚举（内部并发，多为 lib 源码包）
	importVcpkgAll(c.Vcpkg.Limit, collect, report)
	// D. Homebrew 全量：formula bottle + cask，只复用上游 hash（本地解析，最快）
	importBrewAll(c.Brew.MaxFormula, c.Brew.MaxCask, collect, report)
	// C. GitHub 精选（并发）：5 平台最全，进合并器覆盖/补齐同名 Windows-only
	runJobs(c.GitHubTop, 16, func(t githubTemplate) {
		m, err := importGitHub(t.Repo, t.Name, &t)
		if err != nil {
			report("github/"+t.Repo, err)
			return
		}
		collect(m)
		fmt.Printf("COLLECT github/%s\n", m.Name)
	})

	// 统一落盘：同名多源已合成一条 manifest；只有这里才计成功数
	mok, mfail := mg.flush(outDir, report)
	mu.Lock()
	ok += mok
	fail += mfail
	mu.Unlock()

	fmt.Printf("\n汇总: 成功 %d，失败 %d（去重后包数 %d）\n", ok, fail, mg.size())
	mu.Lock()
	defer mu.Unlock()
	for _, f := range failed {
		fmt.Printf("  - %s\n", f)
	}
	if ok == 0 {
		return fmt.Errorf("全量转制 0 成功")
	}
	return nil
}
