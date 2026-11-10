package services

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

type SvcDef struct {
	Name string `json:"name"`
	Exec string `json:"exec"`
}

func installedServices() map[string][]SvcDef {
	out := map[string][]SvcDef{}
	base := filepath.Join(verxHome(), "install")
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		curData, err := os.ReadFile(filepath.Join(base, e.Name(), "current.json"))
		if err != nil {
			continue
		}
		var cur struct {
			Name     string `json:"name"`
			Current  string `json:"current"`
			Registry string `json:"registry"`
		}
		if err := json.Unmarshal(curData, &cur); err != nil {
			continue
		}
		// 从 registry 的 manifest 读 services 段
		mPath := filepath.Join(verxHome(), "registry", cur.Registry, "repo", "manifests", cur.Name, cur.Name+".yaml")
		svcs := parseServicesFromManifest(mPath)
		if len(svcs) > 0 {
			out[cur.Name] = svcs
		}
	}
	return out
}

func parseServicesFromManifest(path string) []SvcDef {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// 轻量解析 services: [{name, exec}]，不引入 yaml 依赖
	var out []SvcDef
	text := string(data)
	// 简单状态机：找 "services:" 块后每行的 name:/exec:
	lines := splitLines(text)
	inSvc := false
	var cur SvcDef
	flush := func() {
		if cur.Name != "" {
			out = append(out, cur)
			cur = SvcDef{}
		}
	}
	for _, ln := range lines {
		t := trimSpace(ln)
		if startsWith(t, "services:") {
			inSvc = true
			continue
		}
		if !inSvc {
			continue
		}
		if t == "" {
			continue
		}
		// services 块结束：遇到顶级键（不以 - 或空格开头且含 :）
		if !startsWith(ln, " ") && !startsWith(ln, "\t") && contains(ln, ":") && !startsWith(t, "-") {
			flush()
			break
		}
		if startsWith(t, "- ") || t == "-" {
			flush()
			t = trimSpace(trimPrefix(t, "-"))
		}
		if startsWith(t, "name:") {
			cur.Name = trimQuotes(trimSpace(trimPrefix(t, "name:")))
		} else if startsWith(t, "exec:") {
			cur.Exec = trimQuotes(trimSpace(trimPrefix(t, "exec:")))
		}
	}
	flush()
	return out
}

// Action 对应 06 §4.4：services list/start/stop/restart，纯 CLI。
func Action(op, name string) error {
	switch op {
	case "list":
		all := installedServices()
		if len(all) == 0 {
			fmt.Printf("(无服务)\n")
			return nil
		}
		for pkg, svcs := range all {
			for _, s := range svcs {
				fmt.Printf("%s/%s: %s\n", pkg, s.Name, s.Exec)
			}
		}
		return nil
	case "start", "stop", "restart":
		if name == "" {
			return fmt.Errorf("需要服务名: verx services %s <pkg/service 或 service>", op)
		}
		return control(op, name)
	default:
		return fmt.Errorf("未知 services 操作: %s", op)
	}
}

func lookupExec(name string) (string, error) {
	all := installedServices()
	// 支持 pkg/svc 或 svc
	for pkg, svcs := range all {
		for _, s := range svcs {
			if s.Name == name || pkg+"/"+s.Name == name {
				return s.Exec, nil
			}
		}
	}
	return "", fmt.Errorf("找不到服务: %s（用 verx services list 查看）", name)
}

func control(op, name string) error {
	execLine, err := lookupExec(name)
	if err != nil {
		// 兼容系统服务名：直接透传到系统管理器
		return controlSystem(op, name)
	}
	switch op {
	case "start":
		cmd := shellCmd(execLine)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fmt.Printf("启动服务 %s: %s\n", name, execLine)
		return cmd.Run()
	case "stop":
		// 通用停服：按名字杀进程（*nix pkill，Windows taskkill）
		fmt.Printf("停止服务 %s\n", name)
		return killByName(name)
	case "restart":
		_ = control("stop", name)
		return control("start", name)
	}
	return nil
}

func controlSystem(op, name string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// sc start/stop <name>
		arg := op
		if op == "restart" {
			_ = exec.Command("sc", "stop", name).Run()
			arg = "start"
		}
		if op == "start" || op == "stop" {
			cmd = exec.Command("sc", arg, name)
		} else {
			return fmt.Errorf("找不到服务: %s", name)
		}
	case "darwin":
		svc := name
		if op == "start" {
			cmd = exec.Command("launchctl", "start", svc)
		} else if op == "stop" {
			cmd = exec.Command("launchctl", "stop", svc)
		} else {
			_ = exec.Command("launchctl", "stop", svc).Run()
			cmd = exec.Command("launchctl", "start", svc)
		}
	default:
		if op == "restart" {
			_ = exec.Command("systemctl", "--user", "restart", name).Run()
			return nil
		}
		cmd = exec.Command("systemctl", "--user", op, name)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("系统服务控制失败: %w", err)
	}
	return nil
}

func shellCmd(line string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", line)
	}
	return exec.Command("sh", "-c", line)
}

func killByName(name string) error {
	base := name
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			base = name[i+1:]
			break
		}
	}
	if runtime.GOOS == "windows" {
		cmd := exec.Command("taskkill", "/F", "/IM", base+"*")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("停止失败（进程可能不在运行）: %w", err)
		}
		return nil
	}
	cmd := exec.Command("pkill", "-f", base)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("停止失败（进程可能不在运行）: %w", err)
	}
	return nil
}

// --- 小工具，避免引入额外依赖 ---

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
func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}
func startsWith(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func contains(s, p string) bool {
	for i := 0; i+len(p) <= len(s); i++ {
		if s[i:i+len(p)] == p {
			return true
		}
	}
	return false
}
func trimPrefix(s, p string) string {
	if startsWith(s, p) {
		return s[len(p):]
	}
	return s
}
func trimQuotes(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}
