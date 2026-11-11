# verx registry-importer 机器人（v3：清单驱动，5000+）

跑在 GitHub Actions 上（外网无超时），产物提给 `github.com/verx/registry`，
再镜像到 `gitee.com/verx/registry`，国内用户只走 Gitee。

目标体量：`config.yaml` 内 `packages` 清单 5479 个（存量 registry 4502 全量并入 + 新增约 1000），
与 scoop/winget/brew 全量对齐后能到 5000-10000 级别，和主流包管理器掰手腕。
跑法只有一种：`importer all` 只拉清单命中的包，不再全桶/全仓/全站枚举。

## 目录

- `main.go` 入口：`all | scoop | winget | github | validate`
- `all.go` 清单驱动调度：去重清单 -> scoop -> winget -> brew -> vcpkg(lib) -> github(repos)，
  全部 COLLECT 进合并器，统一落盘；结束打印 GitHub API 调用统计，失败列表只打印前 50 条
- `scoop.go` Windows 路：本地浅克隆 3 个桶后内存建索引，只拉清单命中的包（Main > Extras > Versions 首个成功即收），
  只产 windows/amd64，hash 复用上游，无 hash 直接换下一个桶试，全缺失静默跳过
- `winget.go` Windows 路：winget-pkgs 先 `git clone --no-checkout --filter=blob:none`，
  一次 `git ls-tree -r` 建索引（PackageIdentifier/尾段/归一化三索引），只拉清单命中的包；
  单个包的 installer/locale 走 `raw.githubusercontent.com` CDN 按需拉；优先 portable/zip，只产 windows/amd64
- `brew.go` mac/Linux 路：formulae.brew.sh 全索引拉一次 + 内存过滤命中清单的包，
  bottle 复用上游 hash（mac/linux 双平台）；formulaOnly=true 默认跳过 cask，caskAllowlist 例外放行
- `github.go` 兜底路：只拉 `config.github.repos` 显式列表（owner/repo），默认空=跳过自动发现，
  latest release 转制（默认关键字+archOk 防错配，无上游 sha 则下载算 hash；无 release/无资产静默跳过）；
  **唯一消耗 GitHub API 的一路**，workers 建议 8
- `vcpkg.go` lib 路：只拉 `config.vcpkg.libPackages` 显式列表，转 kind=lib 源码包（三平台同源，下载算 hash），
  与 app 清单抢名也不合并（merger 按 kind 隔离直接丢弃）
- `git.go` 本地克隆公共函数：`sparseClone`（scoop 桶 / vcpkg）+ `cloneNoCheckout`（winget-pkgs 百万文件仓）+ `gitLsTreeDirs` / `gitLsTreeAllDirs`（纯本地枚举）；根目录可用 `VERX_IMPORTER_CACHE` 覆盖，CI 做 `actions/cache`
- `common.go` 下载/hash/合并器 merger/runJobs 并发池/写盘公共函数；`getJSON/getText` 自带指数退避重试 + 限流计数；token 只发往 `api.github.com`，CDN/资产下载一律不带 `Authorization`；`APIStats()` 汇总打印
- `config.yaml` v3：`packages` 清单（5000+）+ `platforms` 平台权威 + `kinds` kind 隔离 + 各源 `enabled`/`workers` 开关；`scoop.buckets` 只剩 repo（无 limit）；`github.repos` 默认空；`vcpkg.libPackages` 默认空

## 平台权威 + kind 隔离（merger）

`config.platforms` 决定同 os/arch 谁赢，不再后写覆盖：

- `windows/amd64: [github, scoop, winget]`
- `darwin/amd64, darwin/arm64, linux/amd64, linux/arm64: [github, brew]`

同源则新版本覆盖旧版本；低优先级即使版本更新也不抢行。
`app` 与 `lib` 永不合并，先到者赢、后到直接丢弃。

## API 预算（v3 清单驱动）

| 源 | 枚举 | 转制 | GitHub API |
|----|------|------|------------|
| scoop x3 桶 | 本地克隆 `bucket/` + 内存索引 | CDN 单文件/命中包 | 0 |
| winget | `git ls-tree -r` 一次 + 内存索引 | CDN 2 文件/命中包 | 0 |
| brew | formulae.brew.sh 2 个大 index + 内存过滤 | 无下载（复用 bottle hash） | 0 |
| vcpkg lib | 本地克隆 `ports/`（仅显式列表才读） | 1 次源码下载/包 | 0 |
| github | 无（只拉 repos 显式列表） | releases/latest + repo + 若干资产下载 | 2~3 次/仓库，默认空=0 |

## 本地跑

```bash
# 清单全量（GITHUB_TOKEN 只给 github repos 一路用；本地克隆默认走系统临时目录）
$env:GITHUB_TOKEN="ghp_xxx"
go run ./tools/importer all --config tools/importer/config.yaml --out E:/tmp/registry-out

# 缓存克隆目录（避免每次重拉 winget-pkgs/vcpkg/scoop）
$env:VERX_IMPORTER_CACHE="E:/tmp/verx-importer"

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

工作流带 `actions/cache` 缓存 `/tmp/verx-importer`（scoop/winget/vcpkg 克隆），
命中后只做浅增量更新；日志尾部有 `GitHub API 调用 x 次，限流命中 y 次` 统计，
如果限流命中持续 >0，优先调小 `github.workers`。

## 为什么这样解超时 / 限流 / 体积

- 机器人只在 GitHub Actions 跑：直连 github.com / raw.githubusercontent.com，无墙无超时。
- 枚举零 API 化：scoop/winget/vcpkg 全走 git 协议浅克隆 + 本地索引，
  brew 走 formulae.brew.sh，github 默认不自动发现。
- 未命中静默跳过：5000 清单里大量包在某个源不存在是常态，不算 FAIL，避免几千条刷屏。
- Token 最小暴露：只有 `api.github.com` 带 `Authorization`，CDN 与 release 资产下载一律不带，避免 PAT 泄漏到第三方域名。
- 重试与退避：`getJSON/getText` 403/429/5xx 自动退避重试 + 计数，全量跑一半不再直接失败。
- Gitee 只做镜像：`github.com/verx/registry -> gitee.com/verx/registry` 单向推，国内用户 `verx registry add official https://gitee.com/verx/registry.git`。
- hash 全部真实：Scoop/Winget/Brew 复用上游 64hex；GitHub/VCPKG 优先找 `.sha256` 文件，找不到才下载算 hash，绝不写假 hash。
