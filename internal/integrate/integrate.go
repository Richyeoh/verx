package integrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Install 安装工具链文件，保证 find_package/pkg-config 找到 install/ 下的库。
func Install(tool string) error {
	install := filepath.Join(verxHome(), "install")
	switch tool {
	case "cmake":
		dir := filepath.Join(verxHome(), "share", "verx")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		var prefixes []string
		prefixes = append(prefixes, install)
		// 每个已装 lib 的版本目录都可作为前缀（find_package 按 CMAKE_PREFIX_PATH 搜索）
		if entries, err := os.ReadDir(install); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				curData, err := os.ReadFile(filepath.Join(install, e.Name(), "current.json"))
				if err != nil {
					continue
				}
				var cur struct {
					Current string `json:"current"`
				}
				if err := json.Unmarshal(curData, &cur); err != nil || cur.Current == "" {
					continue
				}
				prefixes = append(prefixes, filepath.Join(install, e.Name(), cur.Current))
			}
		}
		var b strings.Builder
		b.WriteString("# verx cmake toolchain: verx integrate cmake 生成\n")
		for _, p := range prefixes {
			fmt.Fprintf(&b, "list(APPEND CMAKE_PREFIX_PATH %q)\n", p)
		}
		b.WriteString("list(REMOVE_DUPLICATES CMAKE_PREFIX_PATH)\n")
		if err := os.WriteFile(filepath.Join(dir, "verx-toolchain.cmake"), []byte(b.String()), 0o644); err != nil {
			return err
		}
		fmt.Printf("已生成 %s（%d 个前缀）\n用法: cmake -DCMAKE_TOOLCHAIN_FILE=%s ..\n",
			filepath.Join(dir, "verx-toolchain.cmake"), len(prefixes), filepath.Join(dir, "verx-toolchain.cmake"))
		return nil
	case "pkg-config":
		// 收集所有已装 lib 的 pkgconfig 目录，写 share/verx/pkgconfig-path.txt 并校验 .pc 存在
		var paths []string
		var pcs []string
		if entries, err := os.ReadDir(install); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				curData, err := os.ReadFile(filepath.Join(install, e.Name(), "current.json"))
				if err != nil {
					continue
				}
				var cur struct {
					Current string `json:"current"`
				}
				if err := json.Unmarshal(curData, &cur); err != nil || cur.Current == "" {
					continue
				}
				base := filepath.Join(install, e.Name(), cur.Current)
				for _, cand := range []string{
					filepath.Join(base, "lib", "pkgconfig"),
					filepath.Join(base, "lib64", "pkgconfig"),
					filepath.Join(base, "share", "pkgconfig"),
				} {
					if st, err := os.Stat(cand); err == nil && st.IsDir() {
						paths = append(paths, cand)
						if files, err := os.ReadDir(cand); err == nil {
							for _, f := range files {
								if strings.HasSuffix(f.Name(), ".pc") {
									pcs = append(pcs, filepath.Join(cand, f.Name()))
								}
							}
						}
					}
				}
			}
		}
		dir := filepath.Join(verxHome(), "share", "verx")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		out := strings.Join(paths, string(os.PathListSeparator))
		if err := os.WriteFile(filepath.Join(dir, "pkgconfig-path.txt"), []byte(out), 0o644); err != nil {
			return err
		}
		if len(paths) == 0 {
			fmt.Printf("未发现带 pkgconfig 的已装库；已写空 %s\n", filepath.Join(dir, "pkgconfig-path.txt"))
			fmt.Printf("用法: export PKG_CONFIG_PATH=\"$(cat %s):$PKG_CONFIG_PATH\"\n", filepath.Join(dir, "pkgconfig-path.txt"))
			return nil
		}
		fmt.Printf("发现 %d 个 pkgconfig 目录，%d 个 .pc：\n", len(paths), len(pcs))
		for _, p := range paths {
			fmt.Printf("  %s\n", p)
		}
		fmt.Printf("用法: export PKG_CONFIG_PATH=\"%s:$PKG_CONFIG_PATH\"\n或 eval \"$(verx env)\"\n", out)
		return nil
	default:
		return fmt.Errorf("未知 integrate 目标: %s（只支持 cmake|pkg-config）", tool)
	}
}
