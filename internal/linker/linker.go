package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func shimsDir() string { return filepath.Join(verxHome(), "shims") }

func shimBase(b string) string {
	base := filepath.Base(b)
	lower := strings.ToLower(base)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		if strings.HasSuffix(lower, ext) {
			return base[:len(base)-len(ext)]
		}
	}
	return base
}

func shimName(b string) string {
	if runtime.GOOS == "windows" {
		return shimBase(b) + ".exe"
	}
	return shimBase(b)
}

// Link app 写 shims/：Win 写 shim / *nix 写 symlink（rename 原子）+ 桌面入口由调用方按 shortcuts 处理。
func Link(pkg, dest string, bins []string) error {
	if err := os.MkdirAll(shimsDir(), 0o755); err != nil {
		return err
	}
	for _, b := range bins {
		target, err := findBin(dest, b)
		if err != nil {
			return err
		}
		shim := filepath.Join(shimsDir(), shimName(b))
		_ = os.Remove(shim)
		if runtime.GOOS == "windows" {
			if err := writeWinShim(shim, target); err != nil {
				return err
			}
		} else {
			tmp := shim + ".tmp"
			_ = os.Remove(tmp)
			if err := os.Symlink(target, tmp); err != nil {
				return err
			}
			if err := os.Rename(tmp, shim); err != nil {
				return err
			}
			_ = os.Chmod(target, 0o755)
		}
	}
	return nil
}

func Unlink(bins []string) error {
	entries, _ := os.ReadDir(shimsDir())
	for _, b := range bins {
		base := shimBase(b)
		want := map[string]bool{
			base + ".exe": true, base + ".cmd": true, base + ".ps1": true,
			b: true, b + ".exe": true, b + ".cmd": true, b + ".ps1": true,
		}
		for _, e := range entries {
			n := e.Name()
			if want[n] || strings.HasPrefix(n, base+".") || strings.HasPrefix(n, b+".") {
				_ = os.Remove(filepath.Join(shimsDir(), n))
			}
		}
		_ = os.Remove(filepath.Join(shimsDir(), base+".exe"))
		_ = os.Remove(filepath.Join(shimsDir(), base+".cmd"))
		_ = os.Remove(filepath.Join(shimsDir(), base+".ps1"))
	}
	return nil
}

func findBin(dest, want string) (string, error) {
	base := filepath.Base(want)
	var found string
	_ = filepath.Walk(dest, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == base || info.Name() == strings.TrimSuffix(base, ".exe") {
			found = p
		}
		return nil
	})
	if found == "" {
		cand := filepath.Join(dest, want)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
		return "", fmt.Errorf("找不到 bin 入口: %s", want)
	}
	return found, nil
}

// WriteShortcuts 桌面入口：按 manifest shortcuts 生成（Win .lnk 简化为 .cmd 快捷方式，*nix .desktop）。
func WriteShortcuts(pkg, dest string, pairs [][2]string) error {
	if len(pairs) == 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		desktop, _ := desktopDir()
		for _, pr := range pairs {
			name, execName := pr[0], pr[1]
			target := filepath.Join(shimsDir(), shimBase(execName))
			// 优先找实际可执行
			for _, cand := range []string{target + ".exe", target + ".cmd", filepath.Join(dest, execName)} {
				if _, err := os.Stat(cand); err == nil {
					target = cand
					break
				}
			}
			content := fmt.Sprintf("@echo off\r\nstart \"\" \"%s\" %%*\r\n", target)
			_ = os.WriteFile(filepath.Join(shimsDir(), shimBase(name)+"-shortcut.cmd"), []byte(content), 0o644)
			if desktop != "" {
				_ = os.WriteFile(filepath.Join(desktop, shimBase(name)+".cmd"), []byte(content), 0o644)
			}
		}
		return nil
	}
	// *nix .desktop
	appsDir := filepath.Join(verxHome(), "share", "applications")
	_ = os.MkdirAll(appsDir, 0o755)
	for _, pr := range pairs {
		name, execName := pr[0], pr[1]
		target := filepath.Join(shimsDir(), shimBase(execName))
		desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=%s\nTerminal=false\n", name, target)
		_ = os.WriteFile(filepath.Join(appsDir, shimBase(name)+".desktop"), []byte(desktop), 0o644)
	}
	return nil
}

// RemoveShortcuts 删除桌面入口。
func RemoveShortcuts(names []string) error {
	for _, n := range names {
		base := shimBase(n)
		_ = os.Remove(filepath.Join(shimsDir(), base+"-shortcut.cmd"))
		_ = os.Remove(filepath.Join(verxHome(), "share", "applications", base+".desktop"))
		if desktop, _ := desktopDir(); desktop != "" {
			_ = os.Remove(filepath.Join(desktop, base+".cmd"))
			_ = os.Remove(filepath.Join(desktop, base+".lnk"))
		}
	}
	return nil
}

func desktopDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		d := filepath.Join(h, "Desktop")
		if _, err := os.Stat(d); err == nil {
			return d, nil
		}
		// OneDrive Desktop
		d2 := filepath.Join(h, "OneDrive", "Desktop")
		if _, err := os.Stat(d2); err == nil {
			return d2, nil
		}
		return "", fmt.Errorf("no desktop")
	}
	d := filepath.Join(h, ".local", "share", "applications")
	_ = os.MkdirAll(d, 0o755)
	return d, nil
}

// writeWinShim 生成 .cmd + .ps1 转发。不写假 .exe，避免 PATHEXT 优先命中坏文件。
// 真 shim 二进制（复用同一 shim.exe）在正式发版时由 goreleaser 产物替换；
// 此处 .cmd/.ps1 已可直接用，保证装完即能跑。
func writeWinShim(shim, target string) error {	base := strings.TrimSuffix(shim, ".exe")
	// 旧残留占位 .exe 若存在且是文本占位则删掉
	if data, err := os.ReadFile(shim); err == nil {
		if strings.HasPrefix(string(data), "verx-shim-placeholder") {
			_ = os.Remove(shim)
		}
	}
	cmdContent := fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", target)
	psContent := fmt.Sprintf("& \"%s\" @args\n", target)
	if err := os.WriteFile(base+".cmd", []byte(cmdContent), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(base+".ps1", []byte(psContent), 0o644); err != nil {
		return err
	}
	// 若目标本身就是 .exe，为让 `name` 直接解析到，额外建硬链接（同盘）否则拷贝
	if strings.HasSuffix(strings.ToLower(target), ".exe") {
		_ = os.Remove(shim)
		if err := os.Link(target, shim); err != nil {
			data, rerr := os.ReadFile(target)
			if rerr != nil {
				return nil // .cmd/.ps1 已可用，不拦安装
			}
			_ = os.WriteFile(shim, data, 0o755)
		}
	}
	return nil
}
