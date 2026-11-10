# verx registry-importer 机器人

跑在 GitHub Actions 上（外网无超时），产物提给 `github.com/verx/registry`，
再镜像到 `gitee.com/verx/registry`，国内用户只走 Gitee。

## 目录

- `main.go` 入口：`all | scoop | winget | github`
- `all.go` 全量：Scoop 全桶 + Winget 全枚举 + Arch 全仓 + VCPKG 全枚举 + Brew 全量 + GitHub 精选；全程并发，全部 COLLECT 进合并器，同名按 os/arch 合并后一次落盘
- `scoop.go` A 路：Scoop bucket JSON -> manifest（只产 windows/amd64，hash 复用上游，无 hash 直接拒）+ git/trees 全桶枚举（Main/Extras/Versions 全收，不再手写 apps）
- `winget.go` B 路：winget-pkgs 全枚举 -> 最新版 -> manifest（优先 portable/zip，只产 windows/amd64，msi/exe 自动补 installer 段）
- `github.go` C 路：GitHub latest release -> manifest（assetMap 命中多平台，archOk 防 amd64/arm64 错配，无上游 sha 文件则下载算 hash；唯一需手写模板的源）
- `brew.go` D 路：Homebrew formula.json/cask.json 全量，bottle 复用上游 hash（mac/linux 双平台，只下 2 个大 index，最快）
- `arch.go` E 路：Arch 官方仓 Core+Extra 全仓枚举（pacman/paru 源，只产 linux/amd64）
- `vcpkg.go` F 路：microsoft/vcpkg ports 全枚举，全量转 lib 源码包（三平台同源，下载算 hash）
- `common.go` 下载/hash/合并器/merger/runJobs 并发池/写盘公共函数
- `config.yaml` 同步配置器：`all/scoop/winget/brew/arch/vcpkg/github` 各源独立 `enabled` 开关；0/不写=全量，>0=只收前 N（调试用）；github 默认 `enabled: false` 先不同步

## 本地跑

```bash
# 全量（需 GITHUB_TOKEN 避免限流）
$env:GITHUB_TOKEN="ghp_xxx"
go run ./tools/importer all --config tools/importer/config.yaml --out E:/tmp/registry-out

# 单测一路
go run ./tools/importer scoop --bucket ScoopInstaller/Main --app ripgrep --out E:/tmp/out
go run ./tools/importer winget --package BurntSushi.Ripgrep.MSVC --out E:/tmp/out
go run ./tools/importer github --repo BurntSushi/ripgrep --out E:/tmp/out
```

输出结构与远端 registry 一致：`manifests/<name>/<name>.yaml`，
直接拷进 registry 仓库提 PR 即可。`registry.yaml` 不动。

## 定时任务

`.github/workflows/registry-import.yml` 每天 02:00 UTC 跑 `all`，
有新增/版本变化才自动提 PR 到 `github.com/verx/registry`。
需要 Secret：`REGISTRY_BOT_TOKEN`（能写 registry 仓的 PAT，配在 verx 仓）。
合入后 `registry` 仓库的 `mirror-gitee.yml` 自动推到 Gitee，
需要 Secret：`VERX_GITEE_KEY`（能写 Gitee registry 的 SSH 私钥全文，配在 registry 仓）。

## 为什么这样解超时

- 机器人只在 GitHub Actions 跑：直连 github.com / raw.githubusercontent.com / winget-pkgs，无墙无超时。
- Gitee 只做镜像：`github.com/verx/registry -> gitee.com/verx/registry` 单向推，国内用户 `verx registry add official https://gitee.com/verx/registry.git`。
- hash 全部真实：Scoop/Winget 复用上游 64hex；GitHub 优先找 `.sha256` 文件，找不到才下载算 hash，绝不写假 hash。
