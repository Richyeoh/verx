package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func verxHome() string {
	if v := os.Getenv("VERX_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".verx"
	}
	return filepath.Join(h, ".verx")
}

func readCurrentRaw(name string) (map[string]any, error) {
	var m map[string]any
	data, err := os.ReadFile(filepath.Join(verxHome(), "install", name, "current.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Check 对应 06 §4.4：查 bin/shims/PATH、install 实体状态一致、Registry 可达。
// fix 修 shims 缺失；repair 从 receipt 重建 current.json。
func Check(fix bool) error {
	home := verxHome()
	bad := 0
	bin := filepath.Join(home, "bin")
	shims := filepath.Join(home, "shims")
	path := os.Getenv("PATH")
	for _, d := range []string{bin, shims} {
		inPath := pathContains(path, d)
		status := "OK"
		if !inPath {
			status = "缺失"
			bad++
		}
		fmt.Printf("%s %s (PATH %s)\n", status, d, map[bool]string{true: "已进", false: "未进"}[inPath])
		if fix && !inPath {
			fmt.Printf("  请手动将 %s 加入 PATH\n", d)
		}
	}
	// install 实体与 receipt 一致性
	installBase := filepath.Join(home, "install")
	entries, _ := os.ReadDir(installBase)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		curFile := filepath.Join(installBase, name, "current.json")
		if _, err := os.Stat(curFile); err != nil {
			fmt.Printf("缺失 %s/current.json\n", name)
			bad++
			if fix {
				if _, rerr := rebuildCurrent(name); rerr != nil {
					fmt.Printf("  重建失败: %v\n", rerr)
				} else {
					fmt.Printf("  已从 receipt 重建 %s\n", name)
					bad--
				}
			}
			continue
		}
		cur, err := readCurrentRaw(name)
		if err != nil {
			fmt.Printf("损坏 %s/current.json: %v\n", name, err)
			bad++
			if fix {
				if _, rerr := rebuildCurrent(name); rerr == nil {
					fmt.Printf("  已从 receipt 重建 %s\n", name)
					bad--
				}
			}
			continue
		}
		curVer, _ := cur["current"].(string)
		versions, _ := cur["versions"].([]any)
		hasReceipt := false
		if curVer != "" {
			if _, err := os.Stat(filepath.Join(installBase, name, curVer, ".receipt.json")); err == nil {
				hasReceipt = true
			}
		}
		if !hasReceipt {
			fmt.Printf("不一致 %s: current=%s 无 receipt\n", name, curVer)
			bad++
			if fix {
				if _, rerr := rebuildCurrent(name); rerr == nil {
					fmt.Printf("  已从 receipt 重建 %s\n", name)
					bad--
				}
			}
			continue
		}
		_ = versions
		fmt.Printf("OK %s@%s\n", name, curVer)
		// http 风险提示：读 receipt 对应 manifest 的 url？此处扫 registry manifest
	}
	// Registry 可达：检查 repo 目录存在
	regBase := filepath.Join(home, "registry")
	regEntries, _ := os.ReadDir(regBase)
	if len(regEntries) == 0 {
		fmt.Printf("缺失 registry（未添加任何 Registry）\n")
		bad++
	} else {
		for _, e := range regEntries {
			if !e.IsDir() {
				continue
			}
			repo := filepath.Join(regBase, e.Name(), "repo", "registry.yaml")
			if _, err := os.Stat(repo); err != nil {
				fmt.Printf("缺失 registry/%s/repo（需 verx registry update）\n", e.Name())
				bad++
			} else {
				fmt.Printf("OK registry/%s\n", e.Name())
			}
		}
	}
	if bad > 0 && !fix {
		return fmt.Errorf("doctor 发现 %d 处问题，用 --fix 看修复建议", bad)
	}
	if bad > 0 && fix {
		return fmt.Errorf("doctor 仍有 %d 处问题未自动修复", bad)
	}
	fmt.Printf("doctor 全 OK\n")
	return nil
}

func pathContains(path, dir string) bool {
	sep := string(os.PathListSeparator)
	for _, p := range strings.Split(path, sep) {
		if strings.EqualFold(strings.TrimSpace(p), dir) {
			return true
		}
		// Windows 大小写/斜杠容忍
		a := strings.ToLower(strings.TrimRight(p, `/\`))
		b := strings.ToLower(strings.TrimRight(dir, `/\`))
		if a == b {
			return true
		}
	}
	return false
}

func rebuildCurrent(name string) (map[string]any, error) {
	base := filepath.Join(verxHome(), "install", name)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var versions []string
	var registry, current string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, e.Name(), ".receipt.json"))
		if err != nil {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		v, _ := r["version"].(string)
		if v == "" {
			v = e.Name()
		}
		versions = append(versions, v)
		current = v
		if reg, _ := r["registry"].(string); reg != "" {
			registry = reg
		}
	}
	if current == "" {
		return nil, fmt.Errorf("无可用 receipt: %s", name)
	}
	m := map[string]any{
		"name": name, "current": current, "versions": versions,
		"pinned": false, "registry": registry, "bin": []string{},
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(filepath.Join(base, "current.json"), data, 0o644)
	return m, nil
}
