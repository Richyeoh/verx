package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Platform 一项一平台：{os, arch, url, sha256, ...}。
type Platform struct {
	OS     string   `yaml:"os"`
	Arch   string   `yaml:"arch"`
	URL    string   `yaml:"url"`
	SHA256 string   `yaml:"sha256"`
	Bin    []string `yaml:"bin,omitempty"`
	Strip  int      `yaml:"strip,omitempty"`
}

type Depend struct {
	Name  string   `yaml:"name"`
	Range string   `yaml:"range,omitempty"`
	OS    []string `yaml:"os,omitempty"`
}

type Build struct {
	System  string   `yaml:"system,omitempty"`
	Script  []string `yaml:"script,omitempty"`
	Network bool     `yaml:"network,omitempty"`
}

type Shortcut struct {
	Name string `yaml:"name"`
	Exec string `yaml:"exec"`
}

type Service struct {
	Name string `yaml:"name"`
	Exec string `yaml:"exec"`
}

type Exports struct {
	Include   []string `yaml:"include,omitempty"`
	Lib       []string `yaml:"lib,omitempty"`
	CMake     []string `yaml:"cmake,omitempty"`
	PkgConfig []string `yaml:"pkgconfig,omitempty"`
}

type Installer struct {
	Silent    string `yaml:"silent,omitempty"`
	Uninstall string `yaml:"uninstall,omitempty"`
}

// Manifest 对应 manifests/<name>/<name>.yaml，字段与 06 §3.1 一一对应。
type Manifest struct {
	API         string            `yaml:"api"`
	Name        string            `yaml:"name"`
	Kind        string            `yaml:"kind"`
	Display     string            `yaml:"display,omitempty"`
	Description string            `yaml:"description"`
	Homepage    string            `yaml:"homepage"`
	License     string            `yaml:"license"`
	Keywords    []string          `yaml:"keywords,omitempty"`
	Version     string            `yaml:"version"`
	Revision    int               `yaml:"revision,omitempty"`
	Channels    map[string]string `yaml:"channels,omitempty"`
	Depends     []Depend          `yaml:"depends,omitempty"`
	Provides    []string          `yaml:"provides,omitempty"`
	Conflicts   []string          `yaml:"conflicts,omitempty"`
	Replaces    []string          `yaml:"replaces,omitempty"`
	Platforms   []Platform        `yaml:"platforms"`
	Build       *Build            `yaml:"build,omitempty"`
	Installer   *Installer        `yaml:"installer,omitempty"`
	Test        string            `yaml:"test,omitempty"`
	Caveats     string            `yaml:"caveats,omitempty"`
	Shortcuts   []Shortcut        `yaml:"shortcuts,omitempty"`
	Services    []Service         `yaml:"services,omitempty"`
	Exports     *Exports          `yaml:"exports,omitempty"`
}

// LoadFile 读取并校验，dirName 为 manifests/<name> 目录名，必须等于 name。
func LoadFile(path, dirName string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("yaml 解析失败: %w", err)
	}
	if err := m.Validate(dirName); err != nil {
		return nil, err
	}
	return &m, nil
}

func validOS(s string) bool { return s == "windows" || s == "darwin" || s == "linux" }

func validArch(os_, arch string) bool {
	if arch == "amd64" || arch == "arm64" {
		return true
	}
	return arch == "386" && os_ == "windows"
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Validate 与 06 §3.1 校验段逐条对应。
func (m *Manifest) Validate(dirName string) error {
	if m.API == "" {
		m.API = "v1"
	}
	if m.API != "v1" {
		return fmt.Errorf("api 只支持 v1")
	}
	if len(m.Name) < 3 || len(m.Name) > 64 {
		return fmt.Errorf("name 长度非法: %q", m.Name)
	}
	for _, c := range m.Name {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return fmt.Errorf("name 非法: %q", m.Name)
		}
	}
	if dirName != "" && m.Name != filepath.Base(dirName) && m.Name != dirName {
		return fmt.Errorf("name %q 必须等于目录名 %q", m.Name, dirName)
	}
	if m.Kind == "" {
		m.Kind = "app"
	}
	if m.Kind != "app" && m.Kind != "lib" {
		return fmt.Errorf("kind 只许 app|lib")
	}
	if strings.TrimSpace(m.Description) == "" {
		return fmt.Errorf("description 必填")
	}
	if !strings.HasPrefix(m.Homepage, "http://") && !strings.HasPrefix(m.Homepage, "https://") {
		return fmt.Errorf("homepage 只许 http/https")
	}
	if strings.TrimSpace(m.License) == "" {
		return fmt.Errorf("license 必填 SPDX")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("version 必填")
	}
	if len(m.Platforms) == 0 {
		return fmt.Errorf("platforms 至少保留一行（缺行即不支持）")
	}
	seen := map[string]bool{}
	for i, p := range m.Platforms {
		if !validOS(p.OS) {
			return fmt.Errorf("platforms[%d].os 非法: %q", i, p.OS)
		}
		if !validArch(p.OS, p.Arch) {
			return fmt.Errorf("platforms[%d].arch 非法: %q", i, p.Arch)
		}
		key := p.OS + "/" + p.Arch
		if seen[key] {
			return fmt.Errorf("platforms 重复: %s", key)
		}
		seen[key] = true
		if !strings.HasPrefix(p.URL, "http://") && !strings.HasPrefix(p.URL, "https://") && !strings.HasPrefix(p.URL, "file://") {
			return fmt.Errorf("platforms[%d].url 只许 http/https（企业/离线 file:// 例外）", i)
		}
		if !isHex64(p.SHA256) {
			return fmt.Errorf("platforms[%d].sha256 非 64 hex", i)
		}
		for _, b := range p.Bin {
			if b == "" || strings.Contains(b, "..") || filepath.IsAbs(b) {
				return fmt.Errorf("platforms[%d].bin 含 .. 或绝对路径: %q", i, b)
			}
		}
		if m.Kind == "app" && len(p.Bin) == 0 {
			return fmt.Errorf("platforms[%d]: app 必须有 bin", i)
		}
		lower := strings.ToLower(p.URL)
		if strings.HasSuffix(lower, ".msi") || strings.HasSuffix(lower, ".exe") {
			if m.Installer == nil || m.Installer.Silent == "" || m.Installer.Uninstall == "" {
				return fmt.Errorf("platforms[%d]: msi/exe 需 installer.silent+uninstall", i)
			}
		}
	}
	if m.Kind == "lib" {
		if m.Exports == nil || (len(m.Exports.Include) == 0 && len(m.Exports.Lib) == 0) {
			return fmt.Errorf("lib 必须有 exports")
		}
	} else {
		if m.Exports != nil {
			return fmt.Errorf("app 禁止带 exports")
		}
	}
	return nil
}

// ForPlatform 缺行即不支持：只返回命中的那一行。
func (m *Manifest) ForPlatform(goos, goarch string) *Platform {
	for i := range m.Platforms {
		if m.Platforms[i].OS == goos && m.Platforms[i].Arch == goarch {
			return &m.Platforms[i]
		}
	}
	return nil
}

// Supports 报当前平台能否安装。
func (m *Manifest) Supports(goos, goarch string) bool { return m.ForPlatform(goos, goarch) != nil }
