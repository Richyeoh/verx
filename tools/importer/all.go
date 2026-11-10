package main

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
)

type fullConfig struct {
	All struct {
		Enabled bool `yaml:"enabled"`
		Workers int  `yaml:"workers"`
	} `yaml:"all"`
	Scoop struct {
		Enabled bool `yaml:"enabled"`
		Buckets []struct {
			Repo  string `yaml:"repo"`
			Limit int    `yaml:"limit"`
		} `yaml:"buckets"`
	} `yaml:"scoop"`
	Winget struct {
		Enabled  bool     `yaml:"enabled"`
		Full     bool     `yaml:"full"`
		Packages []string `yaml:"packages"`
	} `yaml:"winget"`
	Brew struct {
		Enabled    bool `yaml:"enabled"`
		MaxFormula int  `yaml:"maxFormula"`
		MaxCask    int  `yaml:"maxCask"`
	} `yaml:"brew"`
	Arch struct {
		Enabled  bool     `yaml:"enabled"`
		Full     bool     `yaml:"full"`
		Packages []string `yaml:"packages"`
	} `yaml:"arch"`
	Vcpkg struct {
		Enabled bool `yaml:"enabled"`
		Limit   int  `yaml:"limit"`
	} `yaml:"vcpkg"`
	GitHub struct {
		Enabled  bool `yaml:"enabled"`
		MinStars int  `yaml:"minStars"`
		Limit    int  `yaml:"limit"`
	} `yaml:"github"`
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

// runAll 按源开关跑全量：scoop + winget + arch + vcpkg + brew + github。
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

	if c.Scoop.Enabled {
		for _, b := range c.Scoop.Buckets {
			importScoopAll(b.Repo, b.Limit, collect, report)
		}
	} else {
		fmt.Println("skip scoop (enabled=false)")
	}
	if c.Winget.Enabled {
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
	} else {
		fmt.Println("skip winget (enabled=false)")
	}
	if c.Arch.Enabled {
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
	} else {
		fmt.Println("skip arch (enabled=false)")
	}
	if c.Vcpkg.Enabled {
		importVcpkgAll(c.Vcpkg.Limit, collect, report)
	} else {
		fmt.Println("skip vcpkg (enabled=false)")
	}
	if c.Brew.Enabled {
		importBrewAll(c.Brew.MaxFormula, c.Brew.MaxCask, collect, report)
	} else {
		fmt.Println("skip brew (enabled=false)")
	}
	if c.GitHub.Enabled {
		importGithubAll(c.GitHub.MinStars, c.GitHub.Limit, collect, report)
	} else {
		fmt.Println("skip github (enabled=false)")
	}

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
