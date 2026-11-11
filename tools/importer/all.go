package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
)

type fullConfig struct {
	Version    int              `yaml:"version"`
	All        struct {
		Enabled bool `yaml:"enabled"`
		Workers int  `yaml:"workers"`
	} `yaml:"all"`
	Platforms map[string][]string `yaml:"platforms"`
	Kinds     map[string][]string `yaml:"kinds"`
	Scoop     struct {
		Enabled bool `yaml:"enabled"`
		Workers int  `yaml:"workers"`
		Buckets []struct {
			Repo string `yaml:"repo"`
		} `yaml:"buckets"`
	} `yaml:"scoop"`
	Winget struct {
		Enabled bool `yaml:"enabled"`
		Workers int  `yaml:"workers"`
	} `yaml:"winget"`
	Brew struct {
		Enabled       bool     `yaml:"enabled"`
		Workers       int      `yaml:"workers"`
		FormulaOnly   bool     `yaml:"formulaOnly"`
		CaskAllowlist []string `yaml:"caskAllowlist"`
	} `yaml:"brew"`
	Vcpkg struct {
		Enabled     bool     `yaml:"enabled"`
		Workers     int      `yaml:"workers"`
		LibPackages []string `yaml:"libPackages"`
	} `yaml:"vcpkg"`
	GitHub struct {
		Enabled bool     `yaml:"enabled"`
		Workers int      `yaml:"workers"`
		Limit   int      `yaml:"limit"`
		Repos   []string `yaml:"repos"`
	} `yaml:"github"`
	Packages []string `yaml:"packages"`
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

func workersOr(v, def int) int {
	if v > 0 {
		return v
	}
	if def > 0 {
		return def
	}
	return 16
}

// runAll 清单驱动：只拉 config.packages（5000+），不再全枚举。
// 平台权威由 config.platforms 决定，同 os/arch 高优先级赢；kind 隔离 app/lib。
// API 预算：scoop/winget/brew/vcpkg 走本地克隆+索引过滤（零 GitHub API），
// github 只拉 config.github.repos 显式列表（默认跳过自动发现，避免 5000 包打爆配额）。
func runAll(configPath, outDir string) error {
	c, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	defWorkers := workersOr(c.All.Workers, 16)
	var mu sync.Mutex
	ok, fail := 0, 0
	var failed []string
	report := func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			fail++
			if len(failed) < 500 {
				failed = append(failed, name+": "+err.Error())
			}
			fmt.Printf("FAIL %s: %v\n", name, err)
		} else {
			ok++
			fmt.Printf("OK %s\n", name)
		}
	}

	// 去重 + 规范化清单
	seen := map[string]bool{}
	var pkgs []string
	for _, p := range c.Packages {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		pkgs = append(pkgs, p)
	}
	fmt.Printf("清单: %d 个（去重后）\n", len(pkgs))

	mg := newMerger()
	mg.setPlatformPriority(c.Platforms)

	allow := map[string]bool{}
	for _, p := range pkgs {
		allow[p] = true
	}

	if c.Scoop.Enabled {
		var buckets []string
		for _, b := range c.Scoop.Buckets {
			buckets = append(buckets, b.Repo)
		}
		importScoopPackages(pkgs, buckets, workersOr(c.Scoop.Workers, defWorkers),
			func(m *manifest.Manifest) { mg.add(m, "scoop") }, report)
	} else {
		fmt.Println("skip scoop (enabled=false)")
	}
	if c.Winget.Enabled {
		importWingetPackages(pkgs, allow, workersOr(c.Winget.Workers, defWorkers),
			func(m *manifest.Manifest) { mg.add(m, "winget") }, report)
	} else {
		fmt.Println("skip winget (enabled=false)")
	}
	if c.Brew.Enabled {
		importBrewPackages(pkgs, allow, c.Brew.FormulaOnly, c.Brew.CaskAllowlist,
			workersOr(c.Brew.Workers, defWorkers),
			func(m *manifest.Manifest) { mg.add(m, "brew") }, report)
	} else {
		fmt.Println("skip brew (enabled=false)")
	}
	if c.Vcpkg.Enabled {
		if len(c.Vcpkg.LibPackages) > 0 {
			importVcpkgPackages(c.Vcpkg.LibPackages, workersOr(c.Vcpkg.Workers, defWorkers),
				func(m *manifest.Manifest) { mg.add(m, "vcpkg") }, report)
		} else {
			fmt.Println("skip vcpkg (libPackages 为空：lib 与 app 隔离，不跟清单抢名)")
		}
	} else {
		fmt.Println("skip vcpkg (enabled=false)")
	}
	if c.GitHub.Enabled {
		if len(c.GitHub.Repos) > 0 {
			repos := c.GitHub.Repos
			if c.GitHub.Limit > 0 && len(repos) > c.GitHub.Limit {
				repos = repos[:c.GitHub.Limit]
			}
			importGithubRepos(repos, workersOr(c.GitHub.Workers, 8),
				func(m *manifest.Manifest) { mg.add(m, "github") }, report)
		} else {
			fmt.Println("skip github (repos 为空：清单模式不做全站自动发现，避免打爆 API 配额；单包调试用 importer github --repo)")
		}
	} else {
		fmt.Println("skip github (enabled=false)")
	}

	// 统一落盘：同名多源已按权威合成一条 manifest；只有这里才计成功数
	mok, mfail := mg.flush(outDir, report)
	mu.Lock()
	ok += mok
	fail += mfail
	mu.Unlock()

	apiN, hitN := APIStats()
	fmt.Printf("\n汇总: 成功 %d，失败 %d（去重后包数 %d）GitHub API 调用 %d 次，限流命中 %d 次\n", ok, fail, mg.size(), apiN, hitN)
	mu.Lock()
	defer mu.Unlock()
	shown := failed
	if len(shown) > 50 {
		shown = shown[:50]
	}
	for _, f := range shown {
		fmt.Printf("  - %s\n", f)
	}
	if len(failed) >= 500 {
		fmt.Printf("  …失败已达上限 500 条，后续省略（总数见上）\n")
	} else if len(failed) > 50 {
		fmt.Printf("  …还有 %d 条失败已省略\n", len(failed)-50)
	}
	if ok == 0 {
		return fmt.Errorf("全量转制 0 成功")
	}
	return nil
}
