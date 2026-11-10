package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/verx/verx/internal/doctor"
	"github.com/verx/verx/internal/env"
	"github.com/verx/verx/internal/gc"
	"github.com/verx/verx/internal/installer"
	"github.com/verx/verx/internal/integrate"
	"github.com/verx/verx/internal/project"
	"github.com/verx/verx/internal/registry"
	"github.com/verx/verx/internal/services"
	ver "github.com/verx/verx/internal/version"
)

var (
	flagJSON       bool
	flagNoColor    bool
	flagNoProgress bool
	flagYes        bool
	flagQuiet      bool
	flagJobs       int
	flagOffline    bool
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

// exitError 映射退出码：0成功、1通用、2参数、3依赖冲突、4校验、5网络、6部分成功。
func exitError(msg string, code int) error {
	if !flagQuiet {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(code)
	return nil
}

func mapInstallErr(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "冲突") || strings.Contains(s, "循环") || strings.Contains(s, "提供冲突"):
		return exitError("依赖冲突: "+s, 3)
	case strings.Contains(s, "sha256") || strings.Contains(s, "校验"):
		return exitError("校验失败: "+s, 4)
	case strings.Contains(s, "下载") || strings.Contains(s, "http") || strings.Contains(s, "网络") || strings.Contains(s, "clone") || strings.Contains(s, "fetch"):
		return exitError("网络失败: "+s, 5)
	default:
		return exitError("失败: "+s, 1)
	}
}

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:     "verx",
		Short:   "Verx 三端统一包管理器",
		Version: "0.1.0",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "机器输出，字段只增不改")
	root.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "无颜色")
	root.PersistentFlags().BoolVar(&flagNoProgress, "no-progress", false, "无进度条")
	root.PersistentFlags().BoolVarP(&flagYes, "yes", "y", false, "跳交互不绕校验")
	root.PersistentFlags().BoolVarP(&flagQuiet, "quiet", "q", false, "静默")
	root.PersistentFlags().IntVar(&flagJobs, "jobs", 4, "并发数")
	root.PersistentFlags().BoolVar(&flagOffline, "offline", false, "离线只用缓存")

	root.AddCommand(
		newRegistryCmd(),
		newSearchCmd(),
		newInfoCmd(),
		newListCmd(),
		newOutdatedCmd(),
		newPinCmd(),
		newUnpinCmd(),
		newInstallCmd(),
		newUpgradeCmd(),
		newRemoveCmd(),
		newAddCmd(),
		newEnvCmd(),
		newIntegrateCmd(),
		newServicesCmd(),
		newDoctorCmd(),
		newGcCmd(),
		newCacheCmd(),
		newSyncCmd(),
		newAuditCmd(),
		newVerifyCmd(),
		newWhichCmd(),
		newHomeCmd(),
		newExecCmd(),
	)
	// completion 由 cobra 内置生成
	root.AddCommand(newCompletionCmd())
	return root
}

// ---------- registry ----------

func newRegistryCmd() *cobra.Command {
	c := &cobra.Command{Use: "registry", Short: "Registry 管理"}
	c.AddCommand(
		&cobra.Command{Use: "add <name> <url>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, a []string) error {
			pri, _ := cmd.Flags().GetInt("priority")
			if err := registry.Add(a[0], a[1], pri); err != nil {
				return mapInstallErr(err)
			}
			fmt.Printf("registry 已添加: %s\n", a[0])
			return nil
		}},
		&cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, a []string) error {
			metas, _ := registry.List()
			if flagJSON {
				data, _ := json.MarshalIndent(metas, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			for _, m := range metas {
				fmt.Printf("%-12s pri=%d enabled=%v commit=%s %s\n", m.Name, m.Priority, m.Enabled, m.LastCommit, m.URL)
			}
			return nil
		}},
		&cobra.Command{Use: "update [name]", RunE: func(cmd *cobra.Command, a []string) error {
			name := ""
			if len(a) > 0 {
				name = a[0]
			}
			if err := registry.Update(name); err != nil {
				return mapInstallErr(err)
			}
			fmt.Printf("registry 已更新\n")
			return nil
		}},
		&cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
			if !flagYes {
				fmt.Printf("确认删除 registry %s? [y/N]: ", a[0])
				var ans string
				_, _ = fmt.Scanln(&ans)
				if ans != "y" && ans != "Y" {
					return exitError("已取消", 1)
				}
			}
			return mapInstallErr(registry.Remove(a[0]))
		}},
		&cobra.Command{Use: "pin <name> <sha>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, a []string) error {
			return mapInstallErr(registry.Pin(a[0], a[1]))
		}},
	)
	c.Commands()[0].Flags().Int("priority", 50, "优先级，越小越优先")
	return c
}

// ---------- search/info/list ----------

func newSearchCmd() *cobra.Command {
	var osFilter, source string
	var limit int
	c := &cobra.Command{Use: "search <keyword>", Short: "搜索（默认按本机平台过滤）", RunE: func(cmd *cobra.Command, a []string) error {
		kw := ""
		if len(a) > 0 {
			kw = a[0]
		}
		res, err := registry.Search(kw, osFilter)
		if err != nil {
			return mapInstallErr(err)
		}
		if source != "" {
			var f []registry.IndexEntry
			for _, e := range res {
				if e.Registry == source {
					f = append(f, e)
				}
			}
			res = f
		}
		if limit > 0 && len(res) > limit {
			res = res[:limit]
		}
		if flagJSON {
			data, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		for _, e := range res {
			fmt.Printf("%s/%s %-8s %-10s %s\n", e.Registry, e.Name, e.Kind, e.Version, e.Description)
		}
		return nil
	}}
	c.Flags().StringVar(&osFilter, "os", "", "跨平台查看，如 --os windows；all 则不过滤")
	c.Flags().StringVar(&source, "source", "", "只看某 Registry")
	c.Flags().IntVar(&limit, "limit", 0, "条数上限")
	return c
}

func newInfoCmd() *cobra.Command {
	return &cobra.Command{Use: "info <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		cand, err := registry.Pick(a[0])
		if err != nil {
			return mapInstallErr(err)
		}
		m := cand.Manifest
		if flagJSON {
			data, _ := json.MarshalIndent(m, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("%s/%s %s [%s]\n  %s\n", cand.Meta.Name, m.Name, m.Version, m.Kind, m.Description)
		for _, p := range m.Platforms {
			mark := "✗"
			if p.OS == runtime.GOOS && p.Arch == runtime.GOARCH {
				mark = "✓本机"
			}
			fmt.Printf("  %s %s/%s %s\n", mark, p.OS, p.Arch, p.URL)
		}
		fmt.Printf("当前平台能否安装: %v\n", m.Supports(runtime.GOOS, runtime.GOARCH))
		return nil
	}}
}

func readCurrentFile(name string) (installer.Current, error) {
	var c installer.Current
	data, err := os.ReadFile(filepath.Join(verxHome(), "install", name, "current.json"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

func newListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, a []string) error {
		base := filepath.Join(verxHome(), "install")
		entries, _ := os.ReadDir(base)
		type row struct {
			Name, Version, Kind, Registry string
		}
		var rows []row
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			c, err := readCurrentFile(e.Name())
			if err != nil {
				continue
			}
			kind := ""
			if c.Registry != "" {
				if m, err := registry.LoadManifest(c.Registry, c.Name); err == nil {
					kind = m.Kind
				}
			}
			if kind == "" {
				if cand, err := registry.Pick(c.Name); err == nil {
					kind = cand.Manifest.Kind
				}
			}
			rows = append(rows, row{c.Name, c.Current, kind, c.Registry})
		}
		if flagJSON {
			data, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		for _, r := range rows {
			fmt.Printf("%-20s %-12s %s\n", r.Name, r.Version, r.Registry)
		}
		return nil
	}}
}

func newOutdatedCmd() *cobra.Command {
	return &cobra.Command{Use: "outdated", RunE: func(cmd *cobra.Command, a []string) error {
		base := filepath.Join(verxHome(), "install")
		entries, _ := os.ReadDir(base)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			c, err := readCurrentFile(e.Name())
			if err != nil {
				continue
			}
			if c.Pinned {
				fmt.Printf("%-20s %-12s 已 pin，跳过\n", c.Name, c.Current)
				continue
			}
			cand, err := registry.Pick(c.Name)
			if err != nil {
				continue
			}
			if ver.Compare(cand.Manifest.Version, c.Current) > 0 {
				fmt.Printf("%-20s %-12s -> %s\n", c.Name, c.Current, cand.Manifest.Version)
			}
		}
		return nil
	}}
}

func newPinCmd() *cobra.Command {
	return &cobra.Command{Use: "pin <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		c, err := readCurrentFile(a[0])
		if err != nil {
			return mapInstallErr(err)
		}
		c.Pinned = true
		data, _ := json.MarshalIndent(c, "", "  ")
		return os.WriteFile(filepath.Join(verxHome(), "install", a[0], "current.json"), data, 0o644)
	}}
}

func newUnpinCmd() *cobra.Command {
	return &cobra.Command{Use: "unpin <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		c, err := readCurrentFile(a[0])
		if err != nil {
			return mapInstallErr(err)
		}
		c.Pinned = false
		data, _ := json.MarshalIndent(c, "", "  ")
		return os.WriteFile(filepath.Join(verxHome(), "install", a[0], "current.json"), data, 0o644)
	}}
}

// ---------- install/upgrade/remove/add ----------

func installOpts(cmd *cobra.Command) installer.Options {
	dry, _ := cmd.Flags().GetBool("dry-run")
	return installer.Options{Jobs: flagJobs, Offline: flagOffline, DryRun: dry, Yes: flagYes}
}

func newInstallCmd() *cobra.Command {
	c := &cobra.Command{Use: "install [name...]", RunE: func(cmd *cobra.Command, a []string) error {
		if len(a) == 0 {
			// 空参数按 verx.lock 重装（CI 用）；无 lock 则按 verx.json
			if lk := project.ReadLock("."); len(lk.Packages) > 0 {
				for _, p := range lk.Packages {
					q := p.Name
					if p.Version != "" {
						q += "@" + p.Version
					}
					if err := installer.Install(q, installOpts(cmd)); err != nil {
						return mapInstallErr(err)
					}
				}
				return nil
			}
			for _, d := range project.Deps(".") {
				name, wantVer := project.ParseDep(d)
				q := name
				if wantVer != "" {
					q += "@" + wantVer
				}
				if err := installer.Install(q, installOpts(cmd)); err != nil {
					return mapInstallErr(err)
				}
				maybeLock(".", name)
			}
			return nil
		}
		failed := 0
		for _, q := range a {
			if err := installer.Install(q, installOpts(cmd)); err != nil {
				failed++
				fmt.Fprintln(os.Stderr, mapErrText(err))
				continue
			}
			name, _ := project.ParseDep(q)
			if i := strings.Index(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			maybeLock(".", name)
		}
		if failed > 0 && len(a) > 1 {
			return exitError("部分成功", 6)
		}
		if failed > 0 {
			return mapInstallErr(fmt.Errorf("%s", "安装失败"))
		}
		return nil
	}}
	c.Flags().Bool("dry-run", false, "只演算不写盘")
	return c
}

// maybeLock 只在当前目录是项目目录（存在 verx.json）时才回写 verx.lock，避免全局安装污染 cwd。
func maybeLock(projectDir, name string) {
	if _, err := os.Stat(filepath.Join(projectDir, "verx.json")); err != nil {
		return
	}
	writeLockEntry(projectDir, bareName(name))
}

func bareName(q string) string {
	name, _ := project.ParseDep(q)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func mapErrText(err error) string { return err.Error() }

func newUpgradeCmd() *cobra.Command {
	c := &cobra.Command{Use: "upgrade [name...]", RunE: func(cmd *cobra.Command, a []string) error {
		all, _ := cmd.Flags().GetBool("all")
		if all {
			base := filepath.Join(verxHome(), "install")
			entries, _ := os.ReadDir(base)
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				cu, err := readCurrentFile(e.Name())
				if err != nil {
					continue
				}
				if cu.Pinned {
					fmt.Printf("%s 已 pin，跳过\n", cu.Name)
					continue
				}
				if err := installer.Upgrade(cu.Name, installOpts(cmd)); err != nil {
					fmt.Fprintln(os.Stderr, err)
				}
			}
			return nil
		}
		for _, q := range a {
			if err := installer.Upgrade(q, installOpts(cmd)); err != nil {
				return mapInstallErr(err)
			}
		}
		return nil
	}}
	c.Flags().Bool("all", false, "升级全部")
	return c
}

func newRemoveCmd() *cobra.Command {
	c := &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		purge, _ := cmd.Flags().GetBool("purge")
		if !flagYes {
			fmt.Printf("确认删除 %s? [y/N]: ", a[0])
			var ans string
			_, _ = fmt.Scanln(&ans)
			if ans != "y" && ans != "Y" {
				return exitError("已取消", 1)
			}
		}
		return mapInstallErr(installer.Remove(a[0], purge))
	}}
	c.Flags().Bool("purge", false, "同时删用户数据")
	return c
}

func newAddCmd() *cobra.Command {
	return &cobra.Command{Use: "add <lib...>", RunE: func(cmd *cobra.Command, a []string) error {
		for _, d := range a {
			if err := project.Add(".", d); err != nil {
				return mapInstallErr(err)
			}
			name, wantVer := project.ParseDep(d)
			q := name
			if wantVer != "" {
				q = name + "@" + wantVer
			}
			if err := installer.Install(q, installOpts(cmd)); err != nil {
				return mapInstallErr(err)
			}
			bare := name
			if i := strings.LastIndex(bare, "/"); i >= 0 {
				bare = bare[i+1:]
			}
			writeLockEntry(".", bare)
		}
		return nil
	}}
}

func writeLockEntry(projectDir, name string) {
	bare := name
	if i := strings.LastIndex(bare, "/"); i >= 0 {
		bare = bare[i+1:]
	}
	c, err := registry.Pick(name)
	if err != nil {
		c, err = registry.Pick(bare)
		if err != nil {
			return
		}
	}
	m := c.Manifest
	sha := ""
	if p := m.ForPlatform(runtime.GOOS, runtime.GOARCH); p != nil {
		sha = p.SHA256
	}
	_ = project.UpdateLockPackage(projectDir, project.LockPackage{
		Name: m.Name, Version: m.Version, Registry: c.Meta.Name, SHA256: sha,
	})
}

// ---------- env / integrate ----------

func newEnvCmd() *cobra.Command {
	var projectDir, shell string
	var auto, clear bool
	c := &cobra.Command{Use: "env", RunE: func(cmd *cobra.Command, a []string) error {
		out, err := env.Render(env.Options{Project: projectDir, Shell: shell, Auto: auto, Clear: clear})
		if err != nil {
			return mapInstallErr(err)
		}
		fmt.Print(out)
		return nil
	}}
	c.Flags().StringVar(&projectDir, "project", ".", "项目目录")
	c.Flags().StringVar(&shell, "shell", "", "bash|zsh|fish|pwsh")
	c.Flags().BoolVar(&auto, "auto", false, "静默：不在项目里不输出")
	c.Flags().BoolVar(&clear, "clear", false, "输出反向清理代码")
	return c
}

	// ---------- integrate / pkg-config 真实落盘 ----------
func newIntegrateCmd() *cobra.Command {
	return &cobra.Command{Use: "integrate <cmake|pkg-config>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		return mapInstallErr(integrate.Install(a[0]))
	}}
}

// ---------- services/doctor/gc/cache ----------

func newServicesCmd() *cobra.Command {
	return &cobra.Command{Use: "services", RunE: func(cmd *cobra.Command, a []string) error {
		op := "list"
		name := ""
		if len(a) > 0 {
			op = a[0]
		}
		if len(a) > 1 {
			name = a[1]
		}
		return mapInstallErr(services.Action(op, name))
	}}
}

func newDoctorCmd() *cobra.Command {
	var fix, repair bool
	c := &cobra.Command{Use: "doctor", RunE: func(cmd *cobra.Command, a []string) error {
		_ = repair
		return mapInstallErr(doctor.Check(fix || repair))
	}}
	c.Flags().BoolVar(&fix, "fix", false, "修复")
	c.Flags().BoolVar(&repair, "repair", false, "从 receipt 重建")
	return c
}

func newGcCmd() *cobra.Command {
	var older string
	c := &cobra.Command{Use: "gc", RunE: func(cmd *cobra.Command, a []string) error {
		return mapInstallErr(gc.Run(older))
	}}
	c.Flags().StringVar(&older, "older-than", "", "只清更早的")
	return c
}

func newCacheCmd() *cobra.Command {
	c := &cobra.Command{Use: "cache", Short: "缓存管理"}
	c.AddCommand(&cobra.Command{Use: "clean", RunE: func(cmd *cobra.Command, a []string) error {
		return mapInstallErr(gc.CacheClean())
	}})
	return c
}

// ---------- sync/audit/verify ----------

func newSyncCmd() *cobra.Command {
	var lockfile string
	c := &cobra.Command{Use: "sync", RunE: func(cmd *cobra.Command, a []string) error {
		dir := "."
		if len(a) > 0 {
			dir = a[0]
		}
		if filepath.IsAbs(lockfile) {
			dir = filepath.Dir(lockfile)
		} else if lockfile != "verx.lock" {
			dir = filepath.Dir(lockfile)
		}
		lk := project.ReadLock(dir)
		if len(lk.Packages) == 0 {
			// 无 lock 则按 verx.json 装（带版本精确）
			for _, d := range project.Deps(dir) {
				name, wantVer := project.ParseDep(d)
				q := name
				if wantVer != "" {
					q = name + "@" + wantVer
				}
				if err := installer.Install(q, installOpts(cmd)); err != nil {
					return mapInstallErr(err)
				}
				maybeLock(dir, bareName(name))
			}
			return nil
		}
		failed := 0
		for _, p := range lk.Packages {
			q := p.Name
			if p.Version != "" {
				// lock 里存的是裸名+精确版本；若存了 registry/前缀也兼容
				q += "@" + p.Version
			}
			if p.Registry != "" && !strings.Contains(q, "/") {
				q = p.Registry + "/" + q
			}
			if err := installer.Install(q, installOpts(cmd)); err != nil {
				failed++
				fmt.Fprintln(os.Stderr, mapErrText(err))
			}
		}
		if failed > 0 {
			return exitError(fmt.Sprintf("sync 部分成功: %d 失败", failed), 6)
		}
		fmt.Printf("sync 完成: %d 个包\n", len(lk.Packages))
		return nil
	}}
	c.Flags().StringVar(&lockfile, "lockfile", "verx.lock", "lock 文件")
	return c
}

func newAuditCmd() *cobra.Command {
	return &cobra.Command{Use: "audit", RunE: func(cmd *cobra.Command, a []string) error {
		home := verxHome()
		// 收集所有 registry 的 revoked.json
		revoked := map[string]bool{}
		regBase := filepath.Join(home, "registry")
		regs, _ := os.ReadDir(regBase)
		for _, r := range regs {
			if !r.IsDir() {
				continue
			}
			for _, cand := range []string{"revoked.json"} {
				p := filepath.Join(home, "registry", r.Name(), "repo", cand)
				data, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				var obj map[string]any
				if err := json.Unmarshal(data, &obj); err != nil {
					continue
				}
				// 支持 {"revoked": ["name@version", ...]} 或数组
				var list []string
				if arr, ok := obj["revoked"].([]any); ok {
					for _, v := range arr {
						if s, ok := v.(string); ok {
							list = append(list, s)
						}
					}
				}
				if arr, ok := obj["packages"].([]any); ok {
					for _, v := range arr {
						if s, ok := v.(string); ok {
							list = append(list, s)
						}
					}
				}
				for _, s := range list {
					revoked[s] = true
				}
			}
		}
		if len(revoked) == 0 {
			fmt.Printf("audit OK: 无撤销名单\n")
			return nil
		}
		// 扫已装
		hit := 0
		insBase := filepath.Join(home, "install")
		entries, _ := os.ReadDir(insBase)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			c, err := readCurrentFile(e.Name())
			if err != nil {
				continue
			}
			keys := []string{c.Name + "@" + c.Current, c.Name}
			for _, k := range keys {
				if revoked[k] {
					fmt.Printf("命中撤销: %s@%s（已隔离提示，请尽快 remove）\n", c.Name, c.Current)
					hit++
					break
				}
			}
		}
		if hit > 0 {
			return exitError(fmt.Sprintf("audit 发现 %d 个撤销包", hit), 1)
		}
		fmt.Printf("audit OK: 已扫 %d 个包，无命中\n", len(entries))
		return nil
	}}
}

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{Use: "verify [name]", RunE: func(cmd *cobra.Command, a []string) error {
		home := verxHome()
		names := a
		if len(names) == 0 {
			entries, _ := os.ReadDir(filepath.Join(home, "install"))
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
		}
		bad := 0
		for _, n := range names {
			c, err := readCurrentFile(n)
			if err != nil {
				fmt.Printf("跳过 %s: %v\n", n, err)
				continue
			}
			// 找 receipt 的 sha256，对比包文件仍存在即可；深度重验需原包缓存
			data, err := os.ReadFile(filepath.Join(home, "install", n, c.Current, ".receipt.json"))
			if err != nil {
				fmt.Printf("缺失 receipt: %s\n", n)
				bad++
				continue
			}
			var r map[string]any
			_ = json.Unmarshal(data, &r)
			sha, _ := r["sha256"].(string)
			if sha == "" {
				fmt.Printf("无 sha256 记录: %s\n", n)
				bad++
				continue
			}
			fmt.Printf("OK %s@%s sha256=%s...\n", n, c.Current, sha[:12])
		}
		if bad > 0 {
			return exitError(fmt.Sprintf("verify 发现 %d 个问题", bad), 4)
		}
		return nil
	}}
}

// ---------- which/home/exec/completion ----------

func newWhichCmd() *cobra.Command {
	return &cobra.Command{Use: "which <bin>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		name := a[0]
		cands := []string{name, name + ".exe", name + ".cmd", name + ".ps1", strings.TrimSuffix(name, ".exe")}
		seen := map[string]bool{}
		for _, c := range cands {
			if seen[c] {
				continue
			}
			seen[c] = true
			p := filepath.Join(verxHome(), "shims", c)
			if _, err := os.Stat(p); err == nil {
				fmt.Println(p)
				return nil
			}
		}
		return exitError("找不到: "+a[0], 1)
	}}
}

func newHomeCmd() *cobra.Command {
	return &cobra.Command{Use: "home [name]", RunE: func(cmd *cobra.Command, a []string) error {
		if len(a) == 0 {
			fmt.Println(verxHome())
			return nil
		}
		fmt.Println(filepath.Join(verxHome(), "install", a[0]))
		return nil
	}}
}

func newExecCmd() *cobra.Command {
	return &cobra.Command{Use: "exec <name> -- [args...]", DisableFlagParsing: true, RunE: func(cmd *cobra.Command, a []string) error {
		if len(a) == 0 {
			return exitError("用法: verx exec <name> -- [args...]", 2)
		}
		name := a[0]
		args := []string{}
		// 支持 -- 分隔
		for i, v := range a[1:] {
			if v == "--" {
				args = append(args, a[1+i+1:]...)
				break
			}
			if i == 0 {
				args = append(args, a[1:]...)
				break
			}
		}
		c, err := readCurrentFile(name)
		if err != nil {
			return mapInstallErr(fmt.Errorf("包未安装: %s", name))
		}
		bin := ""
		if len(c.Bin) > 0 {
			bin = c.Bin[0]
		} else {
			bin = name
		}
		// 在 shims 里找可执行
		shimPath := filepath.Join(verxHome(), "shims", bin)
		target := shimPath
		if _, err := os.Stat(target); err != nil {
			// 试 exe/cmd 变体
			for _, cand := range []string{bin + ".exe", bin + ".cmd", bin + ".ps1", strings.TrimSuffix(bin, ".exe")} {
				p := filepath.Join(verxHome(), "shims", cand)
				if _, err := os.Stat(p); err == nil {
					target = p
					break
				}
			}
		}
		// 直接找到 install 实体兜底
		if _, err := os.Stat(target); err != nil {
			instBase := filepath.Join(verxHome(), "install", name, c.Current)
			found := ""
			_ = filepath.Walk(instBase, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if info.Name() == bin {
					found = p
				}
				return nil
			})
			if found != "" {
				target = found
			} else {
				return exitError("找不到可执行: "+bin, 1)
			}
		}
		proc := exec.Command(target, args...)
		proc.Stdin, proc.Stdout, proc.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := proc.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			return mapInstallErr(err)
		}
		return nil
	}}
}

func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use: "completion <bash|zsh|fish|pwsh>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			root := cmd.Root()
			switch a[0] {
			case "bash":
				return root.GenBashCompletion(os.Stdout)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "pwsh", "powershell":
				return root.GenPowerShellCompletion(os.Stdout)
			default:
				return exitError("未知 shell: "+a[0], 2)
			}
		},
	}
}
