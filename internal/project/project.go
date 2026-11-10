package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// VerxJSON 对应 06 §3.3 verx.json。
type VerxJSON struct {
	Dependencies []string `json:"dependencies"`
}

// VerxLock 对应 06 §3.3 verx.lock。
type VerxLock struct {
	RegistryCommit string        `json:"registryCommit"`
	Packages       []LockPackage `json:"packages"`
}

type LockPackage struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Registry string `json:"registry"`
	SHA256   string `json:"sha256"`
}

func loadJSONFile(path string) VerxJSON {
	var v VerxJSON
	data, err := os.ReadFile(path)
	if err != nil {
		return v
	}
	_ = json.Unmarshal(data, &v)
	return v
}

// Add 写进 verx.json（可在项目目录或全局）。
func Add(projectDir, dep string) error {
	if projectDir == "" {
		projectDir = "."
	}
	path := filepath.Join(projectDir, "verx.json")
	v := loadJSONFile(path)
	for _, d := range v.Dependencies {
		if d == dep {
			return nil
		}
	}
	v.Dependencies = append(v.Dependencies, dep)
	data, _ := json.MarshalIndent(v, "", "  ")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Deps 返回项目依赖列表；空目录返回空。
func Deps(projectDir string) []string {
	if projectDir == "" {
		projectDir = "."
	}
	return loadJSONFile(filepath.Join(projectDir, "verx.json")).Dependencies
}

// ParseDep 解析 "name@version"。
func ParseDep(dep string) (name, version string) {
	for i := len(dep) - 1; i >= 0; i-- {
		if dep[i] == '@' {
			return dep[:i], dep[i+1:]
		}
	}
	return dep, ""
}

// ReadLock 读 verx.lock，不存在返回空。
func ReadLock(projectDir string) VerxLock {
	var l VerxLock
	if projectDir == "" {
		projectDir = "."
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "verx.lock"))
	if err != nil {
		return l
	}
	_ = json.Unmarshal(data, &l)
	return l
}

// WriteLock 写 verx.lock。
func WriteLock(projectDir string, l VerxLock) error {
	if projectDir == "" {
		projectDir = "."
	}
	data, _ := json.MarshalIndent(l, "", "  ")
	return os.WriteFile(filepath.Join(projectDir, "verx.lock"), data, 0o644)
}

// UpdateLockPackage 安装成功后把精确版本写进 lock。
func UpdateLockPackage(projectDir string, p LockPackage) error {
	l := ReadLock(projectDir)
	found := false
	for i, e := range l.Packages {
		if e.Name == p.Name {
			l.Packages[i] = p
			found = true
			break
		}
	}
	if !found {
		l.Packages = append(l.Packages, p)
	}
	return WriteLock(projectDir, l)
}

var _ = fmt.Sprintf
