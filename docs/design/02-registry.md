# Registry 设计

## Registry 是什么

Verx 里只有一个 Registry 概念。一个 Registry 就是一个 git 仓库，里面放一堆包描述。

```text
某个 Registry 仓库（远端）
  registry.yaml                Registry 元信息：名字、优先级、签名要求
  manifests/ripgrep/ripgrep.yaml   包描述，一个包一目录
  manifests/git/git.yaml
  policy.yaml                  可选，企业策略
  revoked.json                 可选，撤销名单
```

远端不按分类分目录，包全平铺在 `manifests/<name>/` 下。分类靠包里的 `keywords` 和索引，不靠目录层级，这样加包永远只加一个目录，不会为放哪类吵架。包多了之后靠 git 和本地索引扛，真到上万包再做按首字母分片，索引层屏蔽掉，用户无感。

本地拉下来也要分目录，git 的东西和 Verx 自己的配置不能混：

```text
~/.verx/registry/<name>/
  repo/       git 干净 checkout，里面就是远端的样子（.git、manifests、registry.yaml）
  meta.json   本地配置：url、优先级、开关、钉死 commit、上次更新
```

```json
{"name":"official","url":"https://example.com/verx-official","priority":10,"enabled":true,"pinned":"","lastCommit":"abc123","lastUpdated":"2026-11-10T19:00:00Z"}
```

这样分是因为 `repo/` 是只读影子，`verx registry update` 就是进去 git fetch，不动别的；`meta.json` 是 Verx 写的，删了重建一个就行，不污染 git 状态。索引缓存不放这里，放 `cache/index.json`，删了能重建，`registry/` 只留 Registry 和配置，职责干净。

用户加 Registry 就是加一个 git 地址：

```text
verx registry add community https://example.com/verx-community
verx registry update
verx search rg
```

不分 bucket 还是 tap，不分仓库还是 AUR，就叫 Registry。包的完整名字是 `Registry名/包名`，比如 `official/ripgrep`。直接写 `ripgrep` 时按 Registry 优先级自动选，选不出来就报错让人选，不静默猜。

## 为什么是 git 仓库

三家其实都是这个意思：Scoop 的 bucket 是 git，Brew 的 tap 是 git，AUR 背后也是版本库。直接用 git 有几个好处：更新就是增量拉取，不用重下全量；版本可钉死，出了问题能回到某次提交；人人可建 Registry，去中心化不需要中心注册表批准。

official 只是默认自带的一个 Registry。community、企业私有 Registry 和它在协议上完全平等，只是优先级和默认开关不同。

## 包描述长什么样

一个包一个 YAML 文件，一个文件讲清三端。比如 ripgrep 的大意是：名字、版本、主页、许可证，然后分平台写下载地址、校验值、装完暴露哪几个命令（下例为简化示意，完整必填字段见 `06-implementation.md §3.1`）。

```yaml
name: ripgrep
kind: app
version: 14.1.0
description: 超快 grep 替代
platforms:
  - {os: windows, arch: amd64, url: ..., sha256: ..., bin: [rg.exe]}
  - {os: darwin, arch: arm64, url: ..., sha256: ..., bin: [rg]}
  - {os: linux, arch: amd64, url: ..., sha256: ..., bin: [rg]}
```

关键点：地址和 hash 是必填的，没有 hash 的包进不了 Registry。依赖写成名字加版本范围，复杂逻辑不许写进包描述，需要编译的包把构建步骤声明出来，装的时候摊开给用户确认。

缺行即不支持：`platforms` 有哪行才支持哪个平台，只写一行就是只支持那一个平台。比如只有 Windows 的库这样写就行：

```yaml
platforms:
  - {os: windows, arch: amd64, url: ..., sha256: ...}
```

`search` 默认只按本机平台过滤，在 Linux/macOS 下搜上面这个包就是搜不到；`install` 在不支持的平台上直接报错。想跨平台查看就加条件，比如 `verx search mywinlib --os windows`；`info` 永远显示完整矩阵，并告诉你当前平台能不能装。

## 工具包和库包：同一个 Registry，靠 `kind` 区分

Verx 不另起一套库 Registry。包描述里加一个 `kind` 字段就行：

```yaml
kind: app    # 工具：装完链进 shims/，直接跑（默认，不写就是 app）
kind: lib    # 库：装完不进 shims/，留在 install/ 给编译器用
```

库包长这样（以 zlib 为例，大意）：

```yaml
name: zlib
kind: lib
version: 1.3.1
platforms:
  - {os: windows, arch: amd64, url: ..., sha256: ...}
  - {os: linux, arch: amd64, url: ..., sha256: ...}
exports:
  include: [include]     # 头文件目录，相对包根
  lib: [lib]             # 库文件目录
  cmake: [lib/cmake/zlib]         # 可选，给 find_package 用
  pkgconfig: [lib/pkgconfig]      # 可选，给 pkg-config 用
```

规矩只有三条。第一，`app` 必须有 `bin`，`lib` 必须有 `exports`，写错直接校验失败。第二，平台矩阵就是 vcpkg 的 Triplet：`{os: windows, arch: amd64}` 之于 Verx，就是 `x64-windows` 之于 vcpkg，按目标平台选资产，不靠包名后缀区分。第三，库也走预编译优先、源码兜底：有现成二进制直接下，没有就按 `build:` 现编，编完的 layout 必须也是上面的 `include/lib` 结构，保证编译器找得到。

## Registry 怎么分层

不靠名字区分，靠三件事：优先级、质量门、默认开关。

Official Registry 优先级最高，门最严，强制签名，默认开启。Community Registry 门是基础校验加 hash 强制，含构建步骤的包安装时默认要确认。自己建的第三方 Registry 只做格式校验，默认关闭，必须手动加。企业镜像 Registry 是 official 的只读镜像，用来解决内网和合规问题。

搜索默认 official 优先，community 次之，这样既保持开放，又不让质量失控。

## 发包和更新

发包就是提 PR：fork Registry 仓库，加一个 YAML 文件，CI 过了合并即发布。CI 至少做三件事：格式校验、三端试装、hash 存在性。用户侧 `verx registry update` 拉增量，`verx install` 即可用。

需要复现的场景靠钉死实现：Registry 可以钉到某个 commit，安装结果可以导出 Lockfile，CI 按 Lockfile 重装就是同一批软件。这是企业和 CI 最需要的，也是三家做得 weakest 的地方，Verx 直接做进 Registry 协议里。

## 私有 Registry 和离线

私有 Registry 加一个 token 环境变量即可，token 不落盘不进日志。策略文件可以禁掉第三方 Registry、禁掉源码构建、强制签名。离线环境用 `file://` Registry 加预载缓存，不联网也能装。
