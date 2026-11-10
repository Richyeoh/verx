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
	WingetPackages []string `yaml:"wingetPackages"`
	GitHubTop      []githubTemplate `yaml:"githubTop"`
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

// runAll 按 config 跑全量：Scoop 批量 + Winget 批量 + GitHub Top。
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
	for _, pkg := range c.WingetPackages {
		m, err := importWinget(pkg)
		if err != nil {
			report("winget/"+pkg, err)
			continue
		}
		report("winget/"+m.Name, writeManifest(outDir, m))
	}
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
