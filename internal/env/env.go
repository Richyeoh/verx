package env

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Options 对应 06 §4.3：verx env [--project .] [--shell ...] [--auto] [--clear]。
type Options struct {
	Project string
	Shell   string
	Auto    bool
	Clear   bool
}

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

func detectShell() string {
	if runtime.GOOS == "windows" {
		return "pwsh"
	}
	sh := os.Getenv("SHELL")
	if strings.Contains(sh, "fish") {
		return "fish"
	}
	if strings.Contains(sh, "zsh") {
		return "zsh"
	}
	return "bash"
}

// Render 只输出三类：PATH 加 shims/、编译变量、项目库开关。不改任何文件。
func Render(opt Options) (string, error) {
	shell := opt.Shell
	if shell == "" {
		shell = detectShell()
	}
	home := verxHome()
	shims := filepath.Join(home, "shims")
	install := filepath.Join(home, "install")

	if opt.Clear {
		switch shell {
		case "fish":
			return "set -e VERX_SHIMS; set -e CMAKE_PREFIX_PATH; set -e PKG_CONFIG_PATH;\n", nil
		case "pwsh":
			return "$env:PATH = ($env:PATH -split ';' | Where-Object { $_ -ne '" + shims + "' }) -join ';'\n", nil
		default:
			return "export PATH=$(echo \"$PATH\" | tr ':' '\\n' | grep -v '" + shims + "' | paste -sd: -)\nunset CMAKE_PREFIX_PATH PKG_CONFIG_PATH C_INCLUDE_PATH LIBRARY_PATH\n", nil
		}
	}

	// --auto：不在项目里不输出
	if opt.Auto && opt.Project != "" {
		hasProj := false
		for _, f := range []string{"verx.json", "verx.lock"} {
			if _, err := os.Stat(filepath.Join(opt.Project, f)); err == nil {
				hasProj = true
				break
			}
		}
		if !hasProj {
			return "", nil
		}
	}

	var b strings.Builder
	switch shell {
	case "fish":
		fmt.Fprintf(&b, "set -gx PATH %s $PATH;\n", shims)
		fmt.Fprintf(&b, "set -gx CMAKE_PREFIX_PATH %s;\n", install)
		fmt.Fprintf(&b, "set -gx PKG_CONFIG_PATH %s;\n", filepath.Join(install, "lib", "pkgconfig"))
		fmt.Fprintf(&b, "set -gx C_INCLUDE_PATH %s;\n", filepath.Join(install, "include"))
	case "pwsh":
		fmt.Fprintf(&b, "$env:PATH = \"%s;\" + $env:PATH\n", shims)
		fmt.Fprintf(&b, "$env:CMAKE_PREFIX_PATH = \"%s\"\n", install)
		fmt.Fprintf(&b, "$env:PKG_CONFIG_PATH = \"%s\"\n", filepath.Join(install, "lib", "pkgconfig"))
	default:
		fmt.Fprintf(&b, "export PATH=\"%s:$PATH\"\n", shims)
		fmt.Fprintf(&b, "export CMAKE_PREFIX_PATH=\"%s\"\n", install)
		fmt.Fprintf(&b, "export PKG_CONFIG_PATH=\"%s\"\n", filepath.Join(install, "lib", "pkgconfig"))
		fmt.Fprintf(&b, "export C_INCLUDE_PATH=\"%s\"\n", filepath.Join(install, "include"))
		fmt.Fprintf(&b, "export LIBRARY_PATH=\"%s\"\n", filepath.Join(install, "lib"))
	}
	return b.String(), nil
}
