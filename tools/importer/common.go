package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/verx/verx/internal/manifest"
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

func guessStrip(url string) int {
	// ripgrep 这类官方包顶层多一层目录，默认 strip=1；保守起见统一 1
	_ = url
	return 1
}
