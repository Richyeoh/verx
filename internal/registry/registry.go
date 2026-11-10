package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/verx/verx/internal/manifest"
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

func registryDir() string { return filepath.Join(verxHome(), "registry") }
func repoDir(name string) string { return filepath.Join(registryDir(), name, "repo") }
func metaFile(name string) string { return filepath.Join(registryDir(), name, "meta.json") }
func indexFile() string { return filepath.Join(verxHome(), "cache", "index.json") }

// Meta 对应 06 §3.2 本地 registry/<name>/meta.json。
type Meta struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	Pinned      string `json:"pinned"`
	LastCommit  string `json:"lastCommit"`
	LastUpdated string `json:"lastUpdated"`
}

// IndexEntry cache/index.json 一行。
type IndexEntry struct {
	Registry    string   `json:"registry"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Platforms   []string `json:"platforms"`
	Provides    []string `json:"provides,omitempty"`
}

// Candidate 选包候选。
type Candidate struct {
	Meta     Meta
	Manifest *manifest.Manifest
}

func List() ([]Meta, error) {
	var out []Meta
	entries, err := os.ReadDir(registryDir())
	if err != nil {
		return out, nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(metaFile(e.Name()))
		if err != nil {
			continue
		}
		var m Meta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, nil
}

func saveMeta(m Meta) error {
	m.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(metaFile(m.Name)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(metaFile(m.Name), data, 0o644)
}

func filePathFromURL(url string) string {
	p := strings.TrimPrefix(url, "file://")
	if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'A' && p[1] <= 'Z') || (p[1] >= 'a' && p[1] <= 'z')) && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// Mirrors 读取 registry/repo/registry.yaml 的 mirrors 字段。
func Mirrors(registry string) []string {
	data, err := os.ReadFile(filepath.Join(repoDir(registry), "registry.yaml"))
	if err != nil {
		return nil
	}
	var obj struct {
		Mirrors []string `json:"mirrors" yaml:"mirrors"`
	}
	// registry.yaml 是 YAML，轻量解析 mirrors 行
	text := string(data)
	var out []string
	inList := false
	for _, ln := range splitLines(text) {
		t := trimSpace(ln)
		if startsWith(t, "mirrors:") {
			rest := trimSpace(trimPrefix(t, "mirrors:"))
			if startsWith(rest, "[") {
				inner := trimSuffix(trimPrefix(rest, "["), "]")
				for _, v := range splitComma(inner) {
					v = trimQuotes(trimSpace(v))
					if v != "" {
						out = append(out, v)
					}
				}
				return out
			}
			inList = true
			continue
		}
		if inList {
			if startsWith(t, "- ") || startsWith(t, "-") {
				v := trimQuotes(trimSpace(trimPrefix(trimPrefix(t, "-"), " ")))
				_ = v
				v2 := trimSpace(t[1:])
				v2 = trimQuotes(trimSpace(v2))
				if v2 != "" {
					out = append(out, v2)
				}
				continue
			}
			if t == "" {
				continue
			}
			break
		}
	}
	_ = obj
	return out
}

func Add(name, url string, priority int) error {
	repo := repoDir(name)
	if _, err := os.Stat(repo); err == nil {
		return fmt.Errorf("registry 已存在: %s", name)
	}
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		return err
	}
	if strings.HasPrefix(url, "file://") {
		src := filePathFromURL(url)
		if err := copyDir(src, repo); err != nil {
			return err
		}
	} else {
		cmd := exec.Command("git", "clone", "--depth=1", url, repo)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git clone 失败: %w", err)
		}
	}
	enabled := true
	if name != "official" && !strings.HasPrefix(url, "file://") {
		enabled = false
	}
	m := Meta{Name: name, URL: url, Priority: priority, Enabled: enabled, LastCommit: headCommit(repo)}
	return saveMeta(m)
}

func Remove(name string) error {
	if err := os.RemoveAll(filepath.Join(registryDir(), name)); err != nil {
		return err
	}
	return RebuildIndex()
}

func Update(name string) error {
	metas, _ := List()
	for _, m := range metas {
		if name != "" && m.Name != name {
			continue
		}
		if !m.Enabled {
			continue
		}
		repo := repoDir(m.Name)
		if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
			c1 := exec.Command("git", "-C", repo, "fetch", "--depth=1", "origin", "HEAD")
			_ = c1.Run()
			c2 := exec.Command("git", "-C", repo, "reset", "--hard", "FETCH_HEAD")
			_ = c2.Run()
			m.LastCommit = headCommit(repo)
			_ = saveMeta(m)
		}
		if strings.HasPrefix(m.URL, "file://") {
			src := filePathFromURL(m.URL)
			_ = os.RemoveAll(repo)
			_ = copyDir(src, repo)
		}
	}
	return RebuildIndex()
}

func Pin(name, sha string) error {
	data, err := os.ReadFile(metaFile(name))
	if err != nil {
		return err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	m.Pinned = sha
	return saveMeta(m)
}

// LoadManifest 读取某 registry 下某包。
func LoadManifest(registry, name string) (*manifest.Manifest, error) {
	p := filepath.Join(repoDir(registry), "manifests", name, name+".yaml")
	return manifest.LoadFile(p, name)
}

func ResolveName(query string) ([]Candidate, error) {
	reg, name := "", query
	if i := strings.Index(query, "/"); i >= 0 {
		reg, name = query[:i], query[i+1:]
	}
	metas, _ := List()
	var out []Candidate
	for _, m := range metas {
		if !m.Enabled {
			continue
		}
		if reg != "" && m.Name != reg {
			continue
		}
		mm, err := LoadManifest(m.Name, name)
		if err != nil {
			continue
		}
		out = append(out, Candidate{Meta: m, Manifest: mm})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("找不到包: %s", query)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Meta.Priority != out[j].Meta.Priority {
			return out[i].Meta.Priority < out[j].Meta.Priority
		}
		return out[i].Manifest.Version >= out[j].Manifest.Version
	})
	return out, nil
}

// Pick 精确选择：registry/name 直接取；裸名 priority 最小者；同优先级同版本多候选报错。
func Pick(query string) (Candidate, error) {
	return PickVer(query, "")
}

// PickVer 支持 name@version 精确版本：wantVer 非空则只保留该版本，否则与 Pick 一致。
func PickVer(query, wantVer string) (Candidate, error) {
	cands, err := ResolveName(query)
	if err != nil {
		return Candidate{}, err
	}
	if wantVer != "" {
		var filtered []Candidate
		for _, c := range cands {
			if c.Manifest.Version == wantVer {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			return Candidate{}, fmt.Errorf("找不到包版本: %s@%s", query, wantVer)
		}
		cands = filtered
	}
	if strings.Contains(query, "/") {
		return cands[0], nil
	}
	if len(cands) > 1 && cands[0].Meta.Priority == cands[1].Meta.Priority && cands[0].Manifest.Version == cands[1].Manifest.Version {
		var names []string
		for _, c := range cands {
			names = append(names, c.Meta.Name+"/"+c.Manifest.Name)
		}
		return Candidate{}, fmt.Errorf("同名多 Registry，请精确指定: %s", strings.Join(names, ", "))
	}
	return cands[0], nil
}

// Search 只读本地索引并按本机平台过滤（缺行即不支持）。osFilter 为空用本机 GOOS，all 则不过滤。
func Search(keyword, osFilter string) ([]IndexEntry, error) {
	idx, err := readIndex()
	if err != nil {
		if err := RebuildIndex(); err != nil {
			return nil, err
		}
		idx, _ = readIndex()
	}
	if osFilter == "" {
		osFilter = runtime.GOOS
	}
	var out []IndexEntry
	kw := strings.ToLower(keyword)
	for _, e := range idx {
		if kw != "" {
			hitKw := strings.Contains(strings.ToLower(e.Name), kw) || strings.Contains(strings.ToLower(e.Description), kw)
			if !hitKw {
				for _, p := range e.Provides {
					if strings.Contains(strings.ToLower(p), kw) {
						hitKw = true
						break
					}
				}
			}
			if !hitKw {
				continue
			}
		}
		if osFilter != "all" {
			hit := false
			for _, p := range e.Platforms {
				if strings.HasPrefix(p, osFilter+"/") {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func RebuildIndex() error {
	metas, _ := List()
	var idx []IndexEntry
	for _, m := range metas {
		if !m.Enabled {
			continue
		}
		base := filepath.Join(repoDir(m.Name), "manifests")
		entries, _ := os.ReadDir(base)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			mm, err := LoadManifest(m.Name, e.Name())
			if err != nil {
				continue
			}
			var plats []string
			for _, p := range mm.Platforms {
				plats = append(plats, p.OS+"/"+p.Arch)
			}
			idx = append(idx, IndexEntry{
				Registry: m.Name, Name: mm.Name, Kind: mm.Kind,
				Version: mm.Version, Description: mm.Description, Platforms: plats, Provides: mm.Provides,
			})
		}
	}
	data, _ := json.MarshalIndent(idx, "", "  ")
	if err := os.MkdirAll(filepath.Dir(indexFile()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(indexFile(), data, 0o644)
}

func readIndex() ([]IndexEntry, error) {
	var idx []IndexEntry
	data, err := os.ReadFile(indexFile())
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &idx)
	return idx, err
}

func headCommit(repo string) string {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		if c != '\r' {
			cur += string(c)
		}
	}
	out = append(out, cur)
	return out
}
func trimSpace(s string) string { return strings.TrimSpace(s) }
func startsWith(s, p string) bool { return strings.HasPrefix(s, p) }
func trimPrefix(s, p string) string { return strings.TrimPrefix(s, p) }
func trimSuffix(s, p string) string { return strings.TrimSuffix(s, p) }
func trimQuotes(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}
func splitComma(s string) []string { return strings.Split(s, ",") }
