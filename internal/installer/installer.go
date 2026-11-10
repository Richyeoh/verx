package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/verx/verx/internal/fetcher"
	"github.com/verx/verx/internal/linker"
	"github.com/verx/verx/internal/manifest"
	"github.com/verx/verx/internal/registry"
	"github.com/verx/verx/internal/resolver"
	"github.com/verx/verx/internal/unpacker"
	"github.com/verx/verx/internal/verifier"
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

func installDir() string { return filepath.Join(verxHome(), "install") }
func packageDir(name, version string) string { return filepath.Join(installDir(), name, version) }
func receiptFile(name, version string) string {
	return filepath.Join(packageDir(name, version), ".receipt.json")
}
func currentFile(name string) string { return filepath.Join(installDir(), name, "current.json") }
func journalDir() string             { return filepath.Join(verxHome(), "journal") }

// Receipt 对应 06 §2 install/<name>/<version>/.receipt.json。
type Receipt struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Kind           string `json:"kind"`
	Registry       string `json:"registry"`
	RegistryCommit string `json:"registryCommit"`
	SHA256         string `json:"sha256"`
	InstalledAt    string `json:"installedAt"`
}

// Current 对应 06 §2 install/<name>/current.json。
type Current struct {
	Name     string   `json:"name"`
	Current  string   `json:"current"`
	Versions []string `json:"versions"`
	Pinned   bool     `json:"pinned"`
	Registry string   `json:"registry"`
	Bin      []string `json:"bin"`
}

// JournalEntry journal/<txid>.jsonl 一行。
type JournalEntry struct {
	TxID    string `json:"txid"`
	Op      string `json:"op"`
	Package string `json:"package"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status"`
	Time    string `json:"time"`
}

// Options 安装选项。
type Options struct {
	Jobs    int
	Offline bool
	DryRun  bool
	Yes     bool
}

var linkMu sync.Mutex

func atomicJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendJournal(e JournalEntry) {
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339)
	}
	_ = os.MkdirAll(journalDir(), 0o755)
	line, _ := json.Marshal(e)
	f, err := os.OpenFile(filepath.Join(journalDir(), e.TxID+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func readCurrent(name string) (Current, error) {
	var c Current
	data, err := os.ReadFile(currentFile(name))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

// RebuildCurrent current.json 坏了从同目录 .receipt.json 重建。
func RebuildCurrent(name string) (Current, error) {
	dir := filepath.Join(installDir(), name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Current{}, err
	}
	var c Current
	c.Name = name
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(receiptFile(name, e.Name()))
		if err != nil {
			continue
		}
		var r Receipt
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		c.Versions = append(c.Versions, r.Version)
		c.Current = r.Version
		c.Registry = r.Registry
	}
	if c.Current == "" {
		return c, fmt.Errorf("无可用 receipt: %s", name)
	}
	_ = atomicJSON(currentFile(name), c)
	return c, nil
}

func registryCommit(reg string) string {
	metas, _ := registry.List()
	for _, m := range metas {
		if m.Name == reg {
			return m.LastCommit
		}
	}
	return ""
}

// Install 七步：resolve -> fetch -> verify -> unpack -> test -> stage -> link。
func Install(query string, opt Options) error {
	if err := os.MkdirAll(installDir(), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(journalDir(), 0o755); err != nil {
		return err
	}
	recoverJournal()
	order, err := resolver.Resolve(query)
	if err != nil {
		return err
	}
	if opt.DryRun {
		for _, n := range order {
			fmt.Printf("would install %s@%s (%s)\n", n.Manifest.Name, n.Manifest.Version, n.Registry)
		}
		return nil
	}
	// fetch 并发（--jobs N）：先并行预取，失败直接中断
	if jobs := opt.Jobs; jobs > 1 && len(order) > 1 {
		sem := make(chan struct{}, jobs)
		errCh := make(chan error, len(order))
		var wg sync.WaitGroup
		for _, n := range order {
			wg.Add(1)
			go func(nn resolver.Node) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				plat := nn.Manifest.ForPlatform(runtime.GOOS, runtime.GOARCH)
				if plat == nil {
					errCh <- fmt.Errorf("包 %s 不支持当前平台 %s/%s", nn.Manifest.Name, runtime.GOOS, runtime.GOARCH)
					return
				}
				if _, err := fetcher.Fetch(nn.Manifest.Name, nn.Manifest.Version, plat.OS, plat.Arch, plat.URL, registry.Mirrors(nn.Registry), opt.Offline); err != nil {
					errCh <- err
					return
				}
				errCh <- nil
			}(n)
		}
		wg.Wait()
		close(errCh)
		for e := range errCh {
			if e != nil {
				return e
			}
		}
	}
	txid := fmt.Sprintf("tx-%d", time.Now().UnixNano())
	for _, n := range order {
		if err := installOne(txid, n.Registry, n.Manifest, opt); err != nil {
			appendJournal(JournalEntry{TxID: txid, Op: "rollback", Package: n.Manifest.Name, Status: "start"})
			// 回滚：删掉本次 stage 的目录
			_ = os.RemoveAll(packageDir(n.Manifest.Name, n.Manifest.Version))
			return err
		}
	}
	appendJournal(JournalEntry{TxID: txid, Op: "commit", Package: query, Status: "done"})
	return nil
}

func installOne(txid, reg string, m *manifest.Manifest, opt Options) error {
	appendJournal(JournalEntry{TxID: txid, Op: "install", Package: m.Name, Version: m.Version, Status: "start"})
	plat := m.ForPlatform(runtime.GOOS, runtime.GOARCH)
	if plat == nil {
		return fmt.Errorf("包 %s 不支持当前平台 %s/%s", m.Name, runtime.GOOS, runtime.GOARCH)
	}
	if m.Build != nil && len(m.Build.Script) > 0 && !opt.Yes {
		fmt.Printf("包 %s 含构建步骤:\n", m.Name)
		for _, s := range m.Build.Script {
			fmt.Printf("  $ %s\n", s)
		}
		if m.Build.Network {
			fmt.Printf("  (需要联网构建 network=true)\n")
		}
		fmt.Printf("确认继续? [y/N]: ")
		var ans string
		_, _ = fmt.Scanln(&ans)
		if ans != "y" && ans != "Y" {
			return fmt.Errorf("用户取消构建包安装")
		}
	}
	archive, err := fetcher.Fetch(m.Name, m.Version, plat.OS, plat.Arch, plat.URL, registry.Mirrors(reg), opt.Offline)
	if err != nil {
		return err
	}
	if err := verifier.VerifySHA256(archive, plat.SHA256); err != nil {
		return err
	}
	lowerURL := strings.ToLower(plat.URL)
	if strings.HasSuffix(lowerURL, ".msi") || strings.HasSuffix(lowerURL, ".exe") {
		return installNative(txid, reg, m, plat, archive, opt)
	}
	tmp, err := os.MkdirTemp(installDir(), "verx-"+m.Name+"-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := unpacker.Unpack(archive, tmp, plat.Strip); err != nil {
		return err
	}
	// 源码构建：跑 build.script（产物 layout 必须满足 exports）
	if m.Build != nil && len(m.Build.Script) > 0 {
		if err := runBuildScripts(m, tmp); err != nil {
			return err
		}
	}
	if m.Test != "" {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", m.Test)
		} else {
			cmd = exec.Command("sh", "-c", m.Test)
		}
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("冒烟测试失败: %v\n%s", err, string(out))
		}
	}
	dest := packageDir(m.Name, m.Version)
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		if err := copyDirContents(tmp, dest); err != nil {
			return fmt.Errorf("stage 失败: %w", err)
		}
	}
	_ = atomicJSON(receiptFile(m.Name, m.Version), Receipt{
		Name: m.Name, Version: m.Version, Kind: m.Kind,
		Registry: reg, RegistryCommit: registryCommit(reg), SHA256: plat.SHA256,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	})
	// current 保留旧版：装新版 + 留旧版
	cur := Current{Name: m.Name, Current: m.Version, Registry: reg, Bin: plat.Bin}
	if old, err := readCurrent(m.Name); err == nil {
		cur.Pinned = old.Pinned
		seen := map[string]bool{m.Version: true}
		cur.Versions = []string{m.Version}
		for _, v := range old.Versions {
			if !seen[v] {
				// 旧版目录仍存在才保留
				if _, err := os.Stat(packageDir(m.Name, v)); err == nil {
					cur.Versions = append(cur.Versions, v)
					seen[v] = true
				}
			}
		}
	} else {
		cur.Versions = []string{m.Version}
	}
	linkMu.Lock()
	defer linkMu.Unlock()
	if err := atomicJSON(currentFile(m.Name), cur); err != nil {
		return err
	}
	if m.Kind == "app" {
		if err := linker.Link(m.Name, dest, plat.Bin); err != nil {
			return err
		}
		if err := linker.WriteShortcuts(m.Name, dest, shortcutPairs(m)); err != nil {
			return err
		}
	}
	appendJournal(JournalEntry{TxID: txid, Op: "install", Package: m.Name, Version: m.Version, Status: "done"})
	return nil
}

func shortcutPairs(m *manifest.Manifest) [][2]string {
	var out [][2]string
	for _, s := range m.Shortcuts {
		out = append(out, [2]string{s.Name, s.Exec})
	}
	return out
}

func runBuildScripts(m *manifest.Manifest, dir string) error {
	for _, s := range m.Build.Script {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", s)
		} else {
			cmd = exec.Command("sh", "-c", s)
		}
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "VERX_PREFIX="+dir)
		fmt.Printf("$ %s\n", s)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("构建失败: %v", err)
		}
	}
	return nil
}

// installNative msi/exe 静默安装：必须有 installer.silent+uninstall（manifest 已校验）。
func installNative(txid, reg string, m *manifest.Manifest, plat *manifest.Platform, archive string, opt Options) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("msi/exe 只支持 Windows 平台")
	}
	silent := ""
	uninst := ""
	if m.Installer != nil {
		silent = m.Installer.Silent
		uninst = m.Installer.Uninstall
	}
	var cmd *exec.Cmd
	lower := strings.ToLower(archive)
	if strings.HasSuffix(lower, ".msi") {
		args := []string{"/i", archive, "/quiet", "/norestart"}
		if silent != "" && silent != "/S" {
			args = append(args, strings.Fields(silent)...)
		}
		cmd = exec.Command("msiexec", args...)
	} else {
		args := strings.Fields(silent)
		cmd = exec.Command(archive, args...)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	fmt.Printf("静默安装: %s %s\n", archive, silent)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("静默安装失败: %w", err)
	}
	dest := packageDir(m.Name, m.Version)
	_ = os.MkdirAll(dest, 0o755)
	// 记录卸载命令，供 remove 调用
	unFile := filepath.Join(dest, ".uninstall.txt")
	_ = os.WriteFile(unFile, []byte(uninst), 0o644)
	_ = atomicJSON(receiptFile(m.Name, m.Version), Receipt{
		Name: m.Name, Version: m.Version, Kind: m.Kind,
		Registry: reg, RegistryCommit: registryCommit(reg), SHA256: plat.SHA256,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	})
	cur := Current{Name: m.Name, Current: m.Version, Versions: []string{m.Version}, Registry: reg, Bin: plat.Bin}
	if old, err := readCurrent(m.Name); err == nil {
		cur.Pinned = old.Pinned
	}
	linkMu.Lock()
	defer linkMu.Unlock()
	if err := atomicJSON(currentFile(m.Name), cur); err != nil {
		return err
	}
	appendJournal(JournalEntry{TxID: txid, Op: "install-native", Package: m.Name, Version: m.Version, Status: "done"})
	return nil
}

// dependentsOf 找已装包中依赖 name 的包。
func dependentsOf(name string) []string {
	var out []string
	entries, err := os.ReadDir(installDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "verx-") {
			continue
		}
		if e.Name() == name {
			continue
		}
		cur, err := readCurrent(e.Name())
		if err != nil {
			continue
		}
		mm, err := registry.LoadManifest(cur.Registry, e.Name())
		if err != nil {
			continue
		}
		for _, d := range mm.Depends {
			if d.Name == name {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

// Remove 删 link + state；--purge 才删用户数据。
// 默认：删当前版本的 link 与实体，保留其他旧版并切回；--purge：整包删干净。
func Remove(name string, purge bool) error {
	cur, err := readCurrent(name)
	if err != nil {
		if _, err2 := RebuildCurrent(name); err2 != nil {
			return fmt.Errorf("包未安装: %s", name)
		}
		cur, _ = readCurrent(name)
	}
	if deps := dependentsOf(name); len(deps) > 0 {
		return fmt.Errorf("删 lib 被依赖则报错: %s 被 %s 依赖", name, strings.Join(deps, ", "))
	}
	// msi/exe：跑卸载命令
	if unData, err := os.ReadFile(filepath.Join(packageDir(name, cur.Current), ".uninstall.txt")); err == nil {
		un := strings.TrimSpace(string(unData))
		if un != "" && runtime.GOOS == "windows" {
			c := exec.Command("cmd", "/C", un)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			_ = c.Run()
		}
	}
	_ = linker.Unlink(cur.Bin)
	_ = linker.RemoveShortcuts(shortcutNames(name))
	if purge {
		return os.RemoveAll(filepath.Join(installDir(), name))
	}
	// 非 purge：删当前版本目录，保留旧版，切 current 到剩余最新
	_ = os.RemoveAll(packageDir(name, cur.Current))
	remain := []string{}
	entries, _ := os.ReadDir(filepath.Join(installDir(), name))
	for _, e := range entries {
		if e.IsDir() {
			remain = append(remain, e.Name())
		}
	}
	if len(remain) == 0 {
		_ = os.RemoveAll(filepath.Join(installDir(), name))
		return nil
	}
	cur.Current = remain[len(remain)-1]
	cur.Versions = remain
	// 恢复旧版 bin 链接
	if mm, err := registry.LoadManifest(cur.Registry, name); err == nil {
		if p := mm.ForPlatform(runtime.GOOS, runtime.GOARCH); p != nil {
			cur.Bin = p.Bin
			if mm.Kind == "app" {
				_ = linker.Link(name, packageDir(name, cur.Current), p.Bin)
			}
		}
	}
	return atomicJSON(currentFile(name), cur)
}

func shortcutNames(name string) []string {
	cur, err := readCurrent(name)
	if err != nil {
		return nil
	}
	mm, err := registry.LoadManifest(cur.Registry, name)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range mm.Shortcuts {
		out = append(out, s.Name)
	}
	return out
}

// Upgrade 装新版 + 切 shims + 留旧版。
func Upgrade(query string, opt Options) error { return Install(query, opt) }

// recoverJournal 崩溃恢复：启动扫 journal，未 commit 的 resume/rollback，孤儿由 gc 清。
func recoverJournal() {
	entries, err := os.ReadDir(journalDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(journalDir(), e.Name()))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		committed := false
		var lastPkg, lastVer string
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			var je JournalEntry
			if err := json.Unmarshal([]byte(ln), &je); err != nil {
				continue
			}
			if je.Op == "commit" && je.Status == "done" {
				committed = true
			}
			if je.Op == "install" && je.Status == "start" {
				lastPkg, lastVer = je.Package, je.Version
			}
		}
		if !committed && lastPkg != "" && lastVer != "" {
			// 未完成的事务：孤儿 stage 清掉（正式 stage 已在 installOne 失败时删）
			_ = os.RemoveAll(filepath.Join(installDir(), "verx-"+lastPkg+"-*"))
		}
	}
}

// copyDirContents 跨盘 stage 兜底：递归拷贝 tmp 内容到 dest。
func copyDirContents(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyDirContents(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, data, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}
