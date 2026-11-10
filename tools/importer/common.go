package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
	ver "github.com/verx/verx/internal/version"
)

var httpClient = &http.Client{Timeout: 120 * time.Second}

func getJSON(url string, v any) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "verx-registry-importer/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("GET %s 返回 %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return decodeJSON(resp.Body, v)
}

func getText(url string) (string, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "verx-registry-importer/1.0")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s 返回 %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// downloadSHA256 流式下载计算 sha256，不落盘，专给 GitHub release 资产用。
func downloadSHA256(url string) (string, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "verx-registry-importer/1.0")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	// GitHub release 资产 API 需要 Accept octet-stream，直接用浏览器下载地址即可
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("下载 %s 返回 %d", url, resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(resp.Body, 500<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// tryFetchUpstreamSHA256 尝试找同名 .sha256 / .sha256sum / CHECKSUMS 文件，避免下载大包。
// candidates 为同 release 下的资产名列表，baseURL 为下载前缀。
func tryFetchUpstreamSHA256(assetURL string) string {
	cands := []string{assetURL + ".sha256", assetURL + ".sha256sum", assetURL + ".sha256.txt"}
	for _, u := range cands {
		txt, err := getText(u)
		if err != nil {
			continue
		}
		if h := extractHex64(txt); h != "" {
			return h
		}
	}
	return ""
}

var hex64Re = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

func extractHex64(s string) string {
	return strings.ToLower(hex64Re.FindString(s))
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	// winget 有时会带后缀如 14.1.0.0 -> 取前三段勉强合规？verx 允许任意 version 字符串，保留原样只去 v
	return v
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			b.WriteRune(c)
		case c == '_' || c == '.' || c == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) < 3 {
		out = out + "-cli"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func writeManifest(outDir string, m *manifest.Manifest) error {
	if err := m.Validate(m.Name); err != nil {
		return fmt.Errorf("manifest 校验失败 %s: %w", m.Name, err)
	}
	dir := outDir + "/manifests/" + m.Name
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(dir+"/"+m.Name+".yaml", data, 0o644)
}

// ---- 多源合并：同名包（scoop/brew/arch/github/winget/vcpkg）在 verx 侧必须是一条 manifest ----
// 同一个软件在不同源只有各自平台行（如 scoop 只有 windows/amd64，brew 只有
// darwin/linux，arch 只有 linux/amd64），直接 writeManifest 会互相覆盖。
// 这里用 merger 按包名收齐所有来源，再按 OS/Arch 去重合并后一次落盘。

type merger map[string]*manifest.Manifest

func newMerger() merger { return map[string]*manifest.Manifest{} }

// add 把 incoming 并入同名包：平台按 os/arch 去重（同平台 incoming 覆盖），
// 版本取新者（ver.Compare），元数据缺啥补啥，test 统一去 .exe 跨平台。
func (mg merger) add(incoming *manifest.Manifest) {
	if incoming == nil || incoming.Name == "" {
		return
	}
	ex, ok := mg[incoming.Name]
	if !ok {
		cp := *incoming
		cp.Platforms = append([]manifest.Platform(nil), incoming.Platforms...)
		if len(incoming.Keywords) > 0 {
			cp.Keywords = append([]string(nil), incoming.Keywords...)
		}
		if len(incoming.Provides) > 0 {
			cp.Provides = append([]string(nil), incoming.Provides...)
		}
		if cp.Channels == nil && incoming.Channels != nil {
			cp.Channels = map[string]string{}
			for k, v := range incoming.Channels {
				cp.Channels[k] = v
			}
		}
		normalizeMergedTest(&cp)
		mg[incoming.Name] = &cp
		return
	}
	// 版本：incoming 更新则刷新版本相关元数据
	if ver.Compare(incoming.Version, ex.Version) > 0 {
		ex.Version = incoming.Version
		if incoming.Channels != nil {
			ex.Channels = incoming.Channels
		}
		if strings.TrimSpace(incoming.Description) != "" {
			ex.Description = incoming.Description
		}
		if strings.HasPrefix(incoming.Homepage, "http") {
			ex.Homepage = incoming.Homepage
		}
		if strings.TrimSpace(incoming.License) != "" && incoming.License != "Unknown" {
			ex.License = incoming.License
		}
		if strings.TrimSpace(incoming.Display) != "" {
			ex.Display = incoming.Display
		}
		if ex.Kind == "app" && incoming.Kind == "lib" {
			// app 优先，lib 的 exports 不往 app 里塞
		} else if ex.Kind != incoming.Kind && incoming.Kind != "" {
			// kind 不一致时保留先到者，避免 app/lib 互串导致 Validate 失败
		}
		if incoming.Installer != nil && ex.Installer == nil {
			ex.Installer = incoming.Installer
		}
	}
	// keywords / provides 并集
	ex.Keywords = unionStrings(ex.Keywords, incoming.Keywords)
	ex.Provides = unionStrings(ex.Provides, incoming.Provides)
	if ex.Channels == nil && incoming.Channels != nil {
		ex.Channels = incoming.Channels
	}
	// 平台按 os/arch 合并：同平台 incoming 覆盖（新版本 URL/hash 为准），缺行即补行
	idx := map[string]int{}
	for i, p := range ex.Platforms {
		idx[p.OS+"/"+p.Arch] = i
	}
	for _, p := range incoming.Platforms {
		key := p.OS + "/" + p.Arch
		if j, dup := idx[key]; dup {
			// 同平台：incoming 的 URL/hash 更新（不同版本必然不同）
			ex.Platforms[j] = p
			continue
		}
		ex.Platforms = append(ex.Platforms, p)
		idx[key] = len(ex.Platforms) - 1
	}
	// lib 的 exports：缺啥补啥
	if ex.Kind == "lib" && incoming.Exports != nil {
		if ex.Exports == nil {
			ex.Exports = incoming.Exports
		} else {
			ex.Exports.Include = unionStrings(ex.Exports.Include, incoming.Exports.Include)
			ex.Exports.Lib = unionStrings(ex.Exports.Lib, incoming.Exports.Lib)
			ex.Exports.CMake = unionStrings(ex.Exports.CMake, incoming.Exports.CMake)
			ex.Exports.PkgConfig = unionStrings(ex.Exports.PkgConfig, incoming.Exports.PkgConfig)
		}
	}
	if incoming.Installer != nil && ex.Installer == nil {
		ex.Installer = incoming.Installer
	}
	normalizeMergedTest(ex)
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range a {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range b {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// normalizeMergedTest 合并后的 test 必须跨平台：去掉 .exe 硬编码。
// 如 "rg.exe --version" -> "rg --version"，保证 linux/macos 也能跑。
func normalizeMergedTest(m *manifest.Manifest) {
	t := strings.TrimSpace(m.Test)
	if t == "" {
		t = m.Name + " --version"
	}
	// 首个词去 .exe 后缀
	fields := strings.Fields(t)
	if len(fields) > 0 {
		fields[0] = strings.TrimSuffix(fields[0], ".exe")
		t = strings.Join(fields, " ")
	}
	m.Test = t
	// 平台排序：windows -> darwin -> linux，amd64 在前，保证输出稳定
	sort.Slice(m.Platforms, func(i, j int) bool {
		oi := map[string]int{"windows": 0, "darwin": 1, "linux": 2}[m.Platforms[i].OS]
		oj := map[string]int{"windows": 0, "darwin": 1, "linux": 2}[m.Platforms[j].OS]
		if oi != oj {
			return oi < oj
		}
		if m.Platforms[i].Arch != m.Platforms[j].Arch {
			return m.Platforms[i].Arch < m.Platforms[j].Arch
		}
		return m.Platforms[i].URL < m.Platforms[j].URL
	})
}

// flush 统一落盘：逐个 Validate，坏包只记错不中断。
func (mg merger) flush(outDir string, report func(string, error)) (int, int) {
	ok, fail := 0, 0
	for name, m := range mg {
		if err := writeManifest(outDir, m); err != nil {
			report("merge/"+name, err)
			fail++
			continue
		}
		report("merge/"+name, nil)
		ok++
	}
	return ok, fail
}

func guessStrip(url string) int {
	// ripgrep 这类官方包顶层多一层目录，默认 strip=1；保守起见统一 1
	_ = url
	return 1
}
