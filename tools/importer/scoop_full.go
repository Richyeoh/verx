package main

import (
	"fmt"
	"strings"
)

// Scoop 全桶枚举：用 GitHub git/trees 一次拿全 bucket/*.json，避免手写 apps 列表。
// Main 约 1300+、Extras 约 1000+，全部转制约 2-3 分钟（只复用上游 hash，不下载）。

type gitTreeResp struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

// listScoopApps 返回 bucket 目录下全部 app 名（不带 .json）。
func listScoopApps(repo string) ([]string, error) {
	repo = strings.TrimSuffix(repo, ".git")
	// master/main 双分支兼容
	cands := []string{
		"https://api.github.com/repos/" + repo + "/git/trees/master?recursive=1",
		"https://api.github.com/repos/" + repo + "/git/trees/main?recursive=1",
	}
	var lastErr error
	for _, u := range cands {
		var tr gitTreeResp
		if err := getJSON(u, &tr); err != nil {
			lastErr = err
			continue
		}
		var out []string
		for _, e := range tr.Tree {
			if e.Type != "blob" {
				continue
			}
			// 只收 bucket/*.json
			if !strings.HasPrefix(e.Path, "bucket/") || !strings.HasSuffix(e.Path, ".json") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(e.Path, "bucket/"), ".json")
			if name != "" {
				out = append(out, name)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
		lastErr = fmt.Errorf("bucket 空: %s", repo)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("列桶失败: %s", repo)
	}
	return nil, lastErr
}

// importScoopAll 全桶转制：limit<=0 全量；单个失败跳过继续。
func importScoopAll(repo string, limit int, outDir string, report func(string, error)) (int, int) {
	apps, err := listScoopApps(repo)
	if err != nil {
		report("scoop-full/"+repo, err)
		return 0, 1
	}
	ok, fail := 0, 0
	n := 0
	for _, app := range apps {
		if limit > 0 && n >= limit {
			break
		}
		n++
		m, err := importScoop(repo, app)
		if err != nil {
			report("scoop/"+app, err)
			fail++
			continue
		}
		if err := writeManifest(outDir, m); err != nil {
			report("scoop/"+m.Name, err)
			fail++
			continue
		}
		report("scoop/"+m.Name, nil)
		ok++
	}
	return ok, fail
}
