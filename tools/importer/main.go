package main

// verx registry-importer：清单驱动（5000+）-> verx manifest 批量转制。
//
// 定位：跑在 GitHub Actions 上（外网无超时），产物 manifests 直接提给
// github.com/verx/registry，再由机器人推到 Gitee（gitee.com/verx/registry），
// 国内用户只走 Gitee，彻底绕开中国网络直连 GitHub 超时问题。
//
// 用法：
//   go run ./tools/importer all --config tools/importer/config.yaml --out /tmp/registry-out
//   go run ./tools/importer scoop --bucket BurntSushi/ripgrep --app ripgrep --out /tmp/out
//   go run ./tools/importer winget --package BurntSushi.Ripgrep.MSVC --out /tmp/out
//   go run ./tools/importer github --repo BurntSushi/ripgrep --out /tmp/out
//
// 所有转制都必须产出真实 sha256：优先复用上游已给的 64hex，否则下载计算。
// 输出目录结构与远端 registry 一致：manifests/<name>/<name>.yaml。

import (
	"flag"
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintf(os.Stderr, `verx registry-importer

用法:
  importer all [--config <file>] --out <dir>
  importer scoop --bucket <owner/repo或URL> --app <name> --out <dir>
  importer winget --package <Publisher.Package> --out <dir>
  importer github --repo <owner/repo> --out <dir> [--name <verx名>]
  importer validate --dir <registry-out>

说明:
  all      按 config.yaml 跑清单（5000+）：scoop + winget + brew + vcpkg(lib) + github（各源独立 enabled 开关）
  scoop    单个 Scoop bucket JSON 转制（优先本地浅克隆，零 API）
  winget   单个 Winget 包转制（本地 ls-tree 枚举 + CDN 拉文件，零 API，自动找最新版）
  github   单个 GitHub 仓库 latest release 转制（自动猜测资产关键字）
  validate 校验 out/manifests 下全部 yaml 能过 manifest.Validate

环境:
  GITHUB_TOKEN        只给 api.github.com 用（github 一路）；CDN/资产下载不带 token
  VERX_IMPORTER_CACHE 本地浅克隆根目录（CI 建议 actions/cache，避免每次重拉大仓）
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "all":
		fs := flag.NewFlagSet("all", flag.ExitOnError)
		config := fs.String("config", "tools/importer/config.yaml", "配置文件")
		out := fs.String("out", "", "输出 registry 根目录")
		_ = fs.Parse(os.Args[2:])
		if *out == "" {
			fmt.Fprintln(os.Stderr, "缺少 --out")
			os.Exit(2)
		}
		if err := runAll(*config, *out); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "scoop":
		fs := flag.NewFlagSet("scoop", flag.ExitOnError)
		bucket := fs.String("bucket", "", "Scoop bucket：owner/repo 或完整 JSON URL，或本地文件")
		app := fs.String("app", "", "bucket 内 app 名（不带 .json）")
		out := fs.String("out", "", "输出 registry 根目录")
		_ = fs.Parse(os.Args[2:])
		if *bucket == "" || *app == "" || *out == "" {
			fmt.Fprintln(os.Stderr, "用法: importer scoop --bucket <owner/repo|URL|本地目录> --app <name> --out <dir>")
			os.Exit(2)
		}
		m, err := importScoop(*bucket, *app)
		if err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
		if err := writeManifest(*out, m); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
		fmt.Printf("OK %s@%s\n", m.Name, m.Version)
	case "winget":
		fs := flag.NewFlagSet("winget", flag.ExitOnError)
		pkg := fs.String("package", "", "Winget PackageIdentifier，如 BurntSushi.Ripgrep.MSVC")
		out := fs.String("out", "", "输出 registry 根目录")
		_ = fs.Parse(os.Args[2:])
		if *pkg == "" || *out == "" {
			fmt.Fprintln(os.Stderr, "用法: importer winget --package <id> --out <dir>")
			os.Exit(2)
		}
		m, err := importWinget(*pkg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
		if err := writeManifest(*out, m); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
		fmt.Printf("OK %s@%s\n", m.Name, m.Version)
	case "github":
		fs := flag.NewFlagSet("github", flag.ExitOnError)
		repo := fs.String("repo", "", "GitHub owner/repo")
		name := fs.String("name", "", "verx 包名（默认取 repo 名小写）")
		out := fs.String("out", "", "输出 registry 根目录")
		_ = fs.Parse(os.Args[2:])
		if *repo == "" || *out == "" {
			fmt.Fprintln(os.Stderr, "用法: importer github --repo <owner/repo> --out <dir> [--name <verx名>]")
			os.Exit(2)
		}
		m, err := importGitHub(*repo, *name, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
		if err := writeManifest(*out, m); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
		fmt.Printf("OK %s@%s\n", m.Name, m.Version)
	case "validate":
		fs := flag.NewFlagSet("validate", flag.ExitOnError)
		dir := fs.String("dir", "", "registry 输出根目录（含 manifests/）")
		_ = fs.Parse(os.Args[2:])
		if *dir == "" {
			fmt.Fprintln(os.Stderr, "用法: importer validate --dir <dir>")
			os.Exit(2)
		}
		if err := validateDir(*dir); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}
