package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
	ver "github.com/verx/verx/internal/version"
)

var (
	httpClient = &http.Client{Timeout: 120 * time.Second}

	// GitHub API 调用计数：runAll 结束时打印，方便在 CI 日志里看预算。
	apiCalls     int64
	apiCallsMu   sync.Mutex
	rateLimitHit int64
)

// githubToken 只在 api.github.com 上发送，避免把 PAT 泄漏给
// raw.githubusercontent.com / objects.githubusercontent.com 等 CDN 域名。
func githubToken() string { return strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) }

func shouldAuthForURL(url string) bool {
	u := strings.ToLower(strings.TrimSpace(url))
	// 只有 api.github.com 需要鉴权；其他域名一律不带 Authorization。
	return strings.Contains(u, "://api.github.com/")
}

func countAPICall(url string) {
	if !strings.Contains(strings.ToLower(url), "api.github.com") {
		return
	}
	apiCallsMu.Lock()
	apiCalls++
	apiCallsMu.Unlock()
}

// APIStats 返回 (api调用次数, 限流命中次数)，runAll 汇总用。
func APIStats() (int64, int64) {
	apiCallsMu.Lock()
	defer apiCallsMu.Unlock()
	return apiCalls, rateLimitHit
}

func noteRateLimitHit() {
	apiCallsMu.Lock()
	rateLimitHit++
	apiCallsMu.Unlock()
}

func newGitHubRequest(method, url string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "verx-registry-importer/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	if shouldAuthForURL(url) {
		if tok := githubToken(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return req, nil
}

func getJSON(url string, v any) error {
	return getJSONRetry(url, v, 3)
}

func getJSONRetry(url string, v any, retries int) error {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		req, err := newGitHubRequest("GET", url)
		if err != nil {
			lastErr = err
			continue
		}
		countAPICall(url)
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("GET %s 失败: %w", url, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return decodeJSON(bytes.NewReader(body), v)
		}
		// 限流：403 + Remaining=0 或 429 -> 等待后重试一次
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			noteRateLimitHit()
			preview := strings.TrimSpace(string(body))
			if len(preview) > 300 {
				preview = preview[:300]
			}
			lastErr = fmt.Errorf("GET %s 返回 %d: %s", url, resp.StatusCode, preview)
			// 二次请求里拿不到 header 了，这里保守等待后重试
			if attempt < retries {
				time.Sleep(time.Duration(5*(attempt+1)) * time.Second)
				continue
			}
			return lastErr
		}
		if resp.StatusCode >= 500 && attempt < retries {
			lastErr = fmt.Errorf("GET %s 返回 %d", url, resp.StatusCode)
			continue
		}
		preview := strings.TrimSpace(string(body))
		if len(preview) > 300 {
			preview = preview[:300]
		}
		return fmt.Errorf("GET %s 返回 %d: %s", url, resp.StatusCode, preview)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("GET %s 失败", url)
	}
	return lastErr
}

func getText(url string) (string, error) {
	return getTextRetry(url, 2)
}

func getTextRetry(url string, retries int) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "verx-registry-importer/1.0")
		// 文本/CDN 一律不带 token：只允许 api.github.com 鉴权。
		if shouldAuthForURL(url) {
			if tok := githubToken(); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("GET %s 失败: %w", url, err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return string(b), nil
		}
		if (resp.StatusCode == 403 || resp.StatusCode == 429) && attempt < retries {
			noteRateLimitHit()
			lastErr = fmt.Errorf("GET %s 返回 %d", url, resp.StatusCode)
			time.Sleep(time.Duration(5*(attempt+1)) * time.Second)
			continue
		}
		if resp.StatusCode >= 500 && attempt < retries {
			lastErr = fmt.Errorf("GET %s 返回 %d", url, resp.StatusCode)
			continue
		}
		return "", fmt.Errorf("GET %s 返回 %d", url, resp.StatusCode)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("GET %s 失败", url)
	}
	return "", lastErr
}

// downloadSHA256 流式下载计算 sha256，不落盘，专给 GitHub release 资产用。
// 注意：release 资产在 github.com / objects.githubusercontent.com 上，
// 一律不带 GITHUB_TOKEN，避免 PAT 泄漏到 CDN。
func downloadSHA256(url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	req.Header.Set("User-Agent", "verx-registry-importer/1.0")
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

// ---- 多源合并：清单驱动 + 平台权威 + kind 隔离 ----
// 同一个软件在不同源只有各自平台行（scoop/winget 只有 windows/amd64，
// brew 只有 darwin/linux），直接 writeManifest 会互相覆盖。
// merger 按包名收齐所有来源，再按 平台权威顺序 + 版本对齐 合并后一次落盘。
//
// 平台权威（config.platforms）：同 os/arch 只留优先级最高的源。
// 同源则新版本覆盖旧版本；低优先级源即使版本更新也不抢行，避免 scoop/winget
// 盲覆盖 github/brew 的新版行。
// kind 隔离：app 与 lib 永不合并，先到者赢，后到直接丢弃。

type merger struct {
	mu       sync.Mutex
	m        map[string]*manifest.Manifest
	src      map[string]map[string]string // pkg -> "os/arch" -> source
	ver      map[string]map[string]string // pkg -> "os/arch" -> version
	platform map[string][]string          // os/arch -> [source...] 按权威排序
}

func newMerger() *merger {
	return &merger{m: map[string]*manifest.Manifest{}, src: map[string]map[string]string{}, ver: map[string]map[string]string{}}
}

func (mg *merger) setPlatformPriority(p map[string][]string) {
	mg.platform = p
}

func sourceRank(order []string, src string) int {
	for i, s := range order {
		if s == src {
			return i
		}
	}
	return 1 << 30
}

// add 把 incoming 并入同名包：线程安全，平台按权威取赢家，同源新版本覆盖，
// kind 不一致直接丢弃，test 统一去 .exe 跨平台。
func (mg *merger) add(incoming *manifest.Manifest, source string) {
	if incoming == nil || incoming.Name == "" {
		return
	}
	mg.mu.Lock()
	defer mg.mu.Unlock()
	ex, ok := mg.m[incoming.Name]
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
		mg.m[incoming.Name] = &cp
		if mg.src[incoming.Name] == nil {
			mg.src[incoming.Name] = map[string]string{}
		}
		for _, p := range incoming.Platforms {
			mg.src[incoming.Name][p.OS+"/"+p.Arch] = source
		}
		return
	}
	// kind 隔离：app/lib 永不互串
	if ex.Kind != incoming.Kind {
		return
	}
	// 版本：取新者刷新版本元数据（平台行按权威另行裁决，不在这里盲覆盖）
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
	// 平台按权威合并：同 os/arch 只留优先级最高的源；同源则新版本覆盖旧版本。
	// 低优先级即使版本更新也不抢行，避免 scoop/winget 盲覆盖 github/brew 的新版行。
	if mg.src[ex.Name] == nil {
		mg.src[ex.Name] = map[string]string{}
	}
	if mg.ver[ex.Name] == nil {
		mg.ver[ex.Name] = map[string]string{}
	}
	idx := map[string]int{}
	for i, p := range ex.Platforms {
		idx[p.OS+"/"+p.Arch] = i
	}
	for _, p := range incoming.Platforms {
		key := p.OS + "/" + p.Arch
		order := mg.platform[key]
		j, dup := idx[key]
		if !dup {
			ex.Platforms = append(ex.Platforms, p)
			idx[key] = len(ex.Platforms) - 1
			mg.src[ex.Name][key] = source
			mg.ver[ex.Name][key] = incoming.Version
			continue
		}
		curSrc := mg.src[ex.Name][key]
		if curSrc == "" {
			curSrc = source
		}
		ri, rc := sourceRank(order, source), sourceRank(order, curSrc)
		if ri < rc {
			ex.Platforms[j] = p
			mg.src[ex.Name][key] = source
			mg.ver[ex.Name][key] = incoming.Version
			continue
		}
		if source == curSrc && ver.Compare(incoming.Version, mg.ver[ex.Name][key]) >= 0 {
			ex.Platforms[j] = p
			mg.ver[ex.Name][key] = incoming.Version
			continue
		}
		// 低权威或同源旧版本：跳过，不覆盖
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
func (mg *merger) flush(outDir string, report func(string, error)) (int, int) {
	ok, fail := 0, 0
	mg.mu.Lock()
	names := make([]string, 0, len(mg.m))
	for name := range mg.m {
		names = append(names, name)
	}
	sort.Strings(names)
	manifests := make([]*manifest.Manifest, 0, len(names))
	for _, n := range names {
		manifests = append(manifests, mg.m[n])
	}
	mg.mu.Unlock()
	for i, m := range manifests {
		if err := writeManifest(outDir, m); err != nil {
			report("merge/"+names[i], err)
			fail++
			continue
		}
		report("merge/"+names[i], nil)
		ok++
	}
	return ok, fail
}

// size 返回已收包数（调试用）。
func (mg *merger) size() int {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return len(mg.m)
}

// runJobs 并发跑 tasks（worker 池），单任务失败只 report 不中断。
// 全量动辄上万个包，串行跑几小时，并发 16 基本压到 10 分钟级。
func runJobs[T any](tasks []T, workers int, fn func(T)) {
	if workers <= 0 {
		workers = 16
	}
	if len(tasks) == 0 {
		return
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}
	ch := make(chan T)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				fn(t)
			}
		}()
	}
	for _, t := range tasks {
		ch <- t
	}
	close(ch)
	wg.Wait()
}

func guessStrip(url string) int {
	// ripgrep 这类官方包顶层多一层目录，默认 strip=1；保守起见统一 1
	_ = url
	return 1
}
