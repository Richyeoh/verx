# Verx 实现文档（由设计文档生成 · 待确认）

> 本文由 `01-reference`、`02-registry`、`03-cli`、`04-architecture`、`05-review` 翻译而来。
> 只写要做什么、做到什么算完，不重复讲为什么。设计有歧义以本文为准，本文有歧义打回设计。

## 1. 范围

做：`verx` 单二进制，三端（Windows / macOS / Linux），用户态，纯 CLI，无嵌入式 DB，Go 实现。

命令（全部要实现）：

```text
verx registry add/list/update/remove/pin
verx search/info/list/outdated/pin/unpin
verx install/upgrade/remove
verx add                       # 写 verx.json 并装库
verx env                       # 输出 shell 代码，配合 eval
verx integrate cmake|pkg-config
verx services list/start/stop/restart
verx doctor [--fix] / verx gc / verx cache clean
verx sync --lockfile / verx audit / verx verify
verx completion bash|zsh|fish|pwsh
verx which/home/exec
全局：-h/--help、--version、--json、--no-color、--no-progress、--yes/-y、-q、--jobs N、--offline
```

不做：Verx 本体 GUI、系统包接管（apt/dnf）、内核驱动、嵌入式 DB。

包两类：`kind: app`（工具，进 `shims/`）、`kind: lib`（库，留 `install/` 给编译器）。第一批库只保 C/C++（CMake、pkg-config、MSBuild 能用）。

## 2. 仓库与目录

```text
E:\Verx\
  verx\                       # Go 工程（本实现文档的对象）
    cmd/verx/main.go
    internal/
      cli/                    # cobra 命令装配、人读表格、--json 输出、退出码
      manifest/               # YAML 解析 + schema 校验（app/lib 分支）
      registry/               # registry/{repo,meta.json} 管理、git fetch、索引
      resolver/               # 依赖求解 SAT-lite（range/provides/conflicts/replaces）
      fetcher/                # 下载、断点续传、镜像 fallback
      verifier/               # sha256 必验、cosign 可选验
      unpacker/               # zip/tar.gz/tar.xz/7z/dmg/pkg/msi 适配、防穿越
      installer/              # 七步事务编排 + journal + 原子写
      linker/                 # Win 写 shim / *nix 写 symlink + 桌面入口
      env/                    # verx env shell 代码生成（bash/zsh/fish/pwsh）
      integrate/              # cmake/pkg-config 工具链文件安装
      project/                # verx.json/verx.lock 读写、add/install 空参数
      services/               # services 启停（调系统服务接口，不做 GUI）
      doctor/                 # 自检 + --fix/--repair
      gc/                     # 孤儿清理、旧版清理、缓存清理
      version/                # 版本比较（SemVer 优先 + revision）
    docs/design/              # 设计文档（01-05 + 本文件）
    go.mod / go.sum / .goreleaser.yaml
  registry\                   # 远端 Registry 示例仓库（official）
    registry.yaml
    manifests/<name>/<name>.yaml
    policy.yaml / revoked.json（可选）
```

运行时目录（`VERX_HOME`，默认 `~/.verx`，Win `%USERPROFILE%\.verx`）：

```text
~/.verx
  bin/verx(.exe)                          Verx 自身
  shims/                                  包入口（Win shim / *nix symlink），进 PATH
  registry/<name>/repo/                    git 影子（.git、manifests、registry.yaml）
  registry/<name>/meta.json                {name,url,priority,enabled,pinned,lastCommit,lastUpdated}
  install/<name>/<version>/                软件实体，不可变
  install/<name>/<version>/.receipt.json   {name,version,kind,registry,registryCommit,sha256,installedAt}
  install/<name>/current.json              {name,current,versions[],pinned,registry,bin[]}
  cache/downloads/ / cache/index.json
  journal/<txid>.jsonl
  logs/
```

规则：`bin/` 只放 Verx；`shims/` 只放包入口；`registry/` 是能装什么；`install/` 是装了什么。`current.json` 坏了从同目录 `.receipt.json` 重建。

## 3. 关键数据结构（Go struct 即标准）

### 3.1 Manifest（`manifest/<name>.yaml`）

```yaml
api: v1
name: ripgrep                 # ^[a-z0-9][a-z0-9-]{1,63}$，必须等于目录名
kind: app                     # app|lib，默认 app
display: ripgrep
description: 超快 grep 替代   # 必填
homepage: https://...         # 必填 http/https
license: MIT                  # 必填 SPDX
keywords: [search, cli]
version: 14.1.0               # 必填，SemVer 优先
revision: 0
channels: {stable: 14.1.0}
depends:
  - {name: vc-redist, range: ">=14.0", os: [windows]}
provides: [rg]
conflicts: []
replaces: []
platforms:                    # 列表，一项一平台：{os, arch, url, sha256, ...}，os=windows|darwin|linux，arch=amd64|arm64（另加 windows/386）
  - {os: windows, arch: amd64, url: https://...zip, sha256: "<64 hex>", bin: [rg.exe], strip: 1}   # strip：解包去几层顶目录，默认0
  - {os: darwin, arch: arm64, url: ..., sha256: ..., bin: [rg]}
  - {os: linux, arch: amd64, url: ..., sha256: ..., bin: [rg]}
build:                        # 有则 install 默认 review
  system: cargo
  script: ["cargo build --release --locked"]
  network: false                # 默认 false（断网构建）；true 需 review 确认才装
installer:                      # 仅 msi/exe 需要：静默参数+卸载+回滚声明，无则拒进 Registry
  silent: "/S"                  # 静默安装参数
  uninstall: "verx-uninstall-cmd"
test: rg --version | grep 14.1.0   # 冒烟测试，失败整单失败
caveats: 额外说明
shortcuts: [{name: Ripgrep, exec: rg}]
services: [{name: foo, exec: food --serve}]
exports:                      # lib 必填，app 禁止
  include: [include]
  lib: [lib]
  cmake: [lib/cmake/ZLIB]
  pkgconfig: [lib/pkgconfig]
```

校验（`manifest.Validate()`，CI 与本地共用）：未知字段拒；sha256 非 64 hex 拒；url 只许 http/https（企业/离线 `file://` 例外）拒；bin 含 `..`/绝对路径拒；platforms 非列表、空列表、`os/arch` 非法、同一 `os+arch` 重复拒；`arch` 只许 `amd64|arm64`（另许 `windows/386`）；`app` 无 bin / `lib` 无 exports 拒；`app` 带 exports 拒；msi/exe 资产无 `installer.silent+uninstall` 拒；`build.network=true` 无 review 确认安装时拒。缺行即不支持：只写一行就是只支持那一个平台。Official Registry 要求 https，第三方允许 http（靠 sha256 必验兜底，`doctor` 提示非 https 风险）。

### 3.2 Registry

远端：`registry.yaml {name,priority,maintainers[],signing{required},policy,mirrors[]}` + `manifests/<name>/<name>.yaml` + 可选 `policy.yaml/revoked.json`。

本地：`registry/<name>/meta.json {name,url,priority,enabled,pinned,lastCommit,lastUpdated}`。`repo/` 只读影子，`update` 就是 `git fetch`。

选包：`<registry>/<name>` 精确；裸名按 `priority` 小 + 版本新选；同名同版本多 Registry 报错列候选，不猜。

### 3.3 项目（库）

`verx.json`：`{dependencies: ["zlib@1.3.1"]}`。`verx.lock`：`{registryCommit, packages: [{name,version,registry,sha256}]}`。`verx install` 空参数按锁重装（CI 用）。

## 4. 核心流程（必须按此实现）

### 4.1 install（七步，失败按 journal 回滚）

```text
1 resolve   SAT-lite：range/provides/conflicts/replaces，--dry-run 到此为止
2 fetch     并发（--jobs N），镜像 fallback，断点续传
3 verify    sha256 必验，不对退出码 4，直接中断
4 unpack    临时目录，拒绝 ../、绝对路径、逃逸 symlink
5 test      跑 manifest.test，失败整单失败
6 stage     移入 install/<name>/<version>/，写 .receipt.json（不可变）
7 link      app 写 shims/（Win shim / *nix rename symlink）+ 原子写 current.json + journal 记 commit；lib 无 link，停在 install/ 即算好
```

原子写：`tmp + fsync + rename`。并发：fetch/unpack 并行，link 全局文件锁（Win `LockFileEx` / *nix `flock`）。崩溃恢复：启动扫 `journal/*.jsonl`，未 commit 的 resume/rollback，孤儿由 `gc` 清。

`upgrade` = 装新版 + 切 shims + 留旧版；`remove` 删 link + state，`--purge` 才删用户数据；删 lib 时被依赖则报错。

### 4.2 registry

`add <name> <url>` clone 到 `registry/<name>/repo` 并写 `meta.json`；`update` 各 Registry `git fetch` + 增量重建 `cache/index.json`；`pin <name> <sha>` 钉 commit 并进 lock；`search` 默认只读本地索引并按本机平台过滤（缺行即不支持，跨平台查看加 `--os`，如 `verx search mywinlib --os windows`），支持 `--source/--os/--limit`；`info` 显示完整矩阵并标注当前平台能否安装；`install` 在不支持的平台上直接报错退出。

### 4.3 env（shell 集成，输出代码不改文件）

```text
verx env [--project .] [--shell bash|zsh|fish|pwsh] [--auto] [--clear]
```

输出只有三类：`PATH` 加 `shims/`、编译变量（`CMAKE_PREFIX_PATH、PKG_CONFIG_PATH、C_INCLUDE_PATH、LIBRARY_PATH`）、项目库开关。用法：`eval "$(verx env)"`，fish 用 `| source`，pwsh 用 `Invoke-Expression`，rc 里加 `eval "$(verx env --auto)"`。`--auto` 不在项目里不输出，`--clear` 吐反向清理。`env` 永远配合 `eval`，直接看的是代码不是表格。

### 4.4 integrate / services / doctor / gc

`integrate cmake|pkg-config` 装工具链文件，保证 `find_package/pkg-config` 找到 `install/` 下的库。`services list/start/stop/restart` 纯 CLI。`doctor` 查 `bin/shims/PATH、install 实体状态一致、Registry 可达`，`--fix` 修、`--repair` 从 receipt 重建。`gc [--older-than]` 清孤儿 stage、旧版、下载缓存。

## 5. 三端（差异只收两处）

`linker` + 平台杂务。Win：`shims/<bin>.exe + .ps1`，复用同一 shim 二进制；*nix：`rename` 原子切 symlink。压缩：zip/7z/tar.gz 三端，tar.xz 加 *nix，dmg/pkg 需确认挂载，msi/exe 必须声明静默参数+卸载+回滚。系统缺库只检查指引（Linux 给 apt/dnf 命令，macOS 引导 CLT，Win 靠 `depends: vc-redist`），不接管。`GOOS=[windows,darwin,linux] × GOARCH=[amd64,arm64]` + Win 386，GoReleaser 打包 + cosign，自举 scoop bucket/homebrew-tap。

## 6. 安全（默认安全，--yes 只跳交互不绕校验）

hash 必填双检；official 强制签名；含 `build:` 默认打全文确认；默认断网构建；`post-install` 默认只许 `echo/caveats`；token 只读环境不落盘；`audit` 对 `revoked.json`，命中隔离；official 产 SLSA provenance，`verify` 可验；默认零 sudo，仅 `--system` 显式提权+二次确认。

## 7. 测试与验收

单测：manifest 校验 6 条、resolver 冲突解释、原子写、env 各 shell 输出、unpack 防穿越。

e2e（三端必跑）：

```bash
verx registry list && verx registry update
verx search rg --os windows
verx install ripgrep --dry-run && verx install ripgrep && rg --version
verx install zlib && eval "$(verx env)"   # lib 不进 shims，编译变量生效
verx pin ripgrep && verx upgrade --all    # 提示跳过 pin
verx remove ripgrep && verx doctor        # 全 OK
```

退出码：0 成功、1 通用、2 参数、3 依赖冲突、4 校验、5 网络、6 部分成功（批量）。`--json` 字段只增不改。

## 8. 实施顺序（建议，不拆版本，一次交付）

1. `manifest + registry + search/info`（先能读包）。
2. `fetcher + verifier + unpacker + installer + linker`（装一个 app 包跑通）。
3. `upgrade/remove/pin/outdated/doctor/gc`（主链闭环）。
4. `kind: lib + verx.json/lock + env/integrate`（库跑通，zlib 级别）。
5. `services/audit/verify/sync/completion` + 三端 e2e + GoReleaser。

确认项：目录（`bin/shims/registry/install`）、Registry 命名、app/lib 同源、`env+eval` 语义、七步事务、无 DB，这五条点了头就按 §8 开工。
