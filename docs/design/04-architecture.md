# 架构与三端设计（Go）

## 为什么是 Go

三端发一个静态二进制，不带运行时，这是 Go 最省心的地方。交叉编译成熟，写一次处处编，标准库加少量依赖就能覆盖下载、解包、建索引。构建发布用 GoReleaser，一次打出三端包，再自举进 scoop bucket 和 homebrew-tap。

## 软件放在哪里

默认放在用户自己家，不用管理员：

```text
~/.verx                              家目录就是 VERX_HOME，Win 下是 %USERPROFILE%\.verx
  bin/verx(.exe)                     Verx 自身，只放自己
  shims/                             包入口：Win 是 shim，macOS/Linux 是 symlink，都要进 PATH
  registry/<name>/                   Registry 能装什么，每个 Registry 独立目录
    repo/                            git 干净 checkout（.git、manifests、registry.yaml）
    meta.json                        本地配置：url、优先级、开关、钉死 commit、上次更新
  install/<name>/<version>/          装了什么，软件实体，不可变，每个版本独立一份
  install/<name>/<version>/.receipt.json    收据：版本、Registry、hash、manifest 快照
  install/<name>/current.json        本包状态：当前版本、pin、Registry
  cache/downloads/                   下载缓存
  cache/index.json                   本地搜索索引，可删可重建
  journal/<txid>.jsonl               事务日志
  logs/
```

这样分意思很清楚。`bin/` 只属于 Verx 自己，升级重装不碰包；`shims/` 只属于包入口，装卸包不碰 Verx，查 PATH 问题也好分：`bin/` 丢了是 Verx 没装好，`shims/` 对不上是包没链上。`registry/` 是 Registry 能装什么，跟你仓库顶层的 `registry/` 对上；`install/` 是本地装了什么，实体和状态放一起，扫一遍就知道装了什么，`current.json` 坏了就从同目录的 `.receipt.json` 反推，不用来回对两棵树。

## 三端差异收在哪里

差异只收在两个地方：怎么把命令送到 `shims`，以及平台特有的杂务。

Windows 用 shim 小程序转发，避开符号链接要特权的问题，也好干净卸载。macOS 和 Linux 用符号链接，原子切换。桌面入口只是给被安装的软件顺手生成的快捷方式，Verx 自己永远是命令行。

压缩包三端都解常见的 zip 和 tar，macOS 的 dmg 和 pkg 需要用户确认才挂载，Windows 的静默安装包必须声明卸载方式才允许进 Registry。系统缺库只检查和指引，不替系统包管理器装东西。

## 安装过程

装一个包走七步：算依赖、下载、验 hash、解包、冒烟测试、进 install、切入口。前六步都是准备，最后一步切换是原子的，任何一步失败都不动现有的可用版本。`kind: app` 的切入口是写 `shims/`，`kind: lib` 没有切入口这一步，装完留在 `install/<name>/<version>/` 就算好，`current.json` 照样写。

状态不用数据库。`install` 里各版本的 `.receipt.json` 就是真相，`current.json` 只是缓存，`journal` 记事务意图。写文件用临时文件加改名保证原子，读坏了就按缺失处理由 doctor 从同目录 receipt 重建。崩溃重启扫一遍 journal，没走完的继续或回滚，孤儿文件由 gc 清理。三家都没用嵌入式数据库，这个量级扫文件加内存索引完全够用，还少了损坏和三端锁的麻烦。

## 库怎么被编译器找到

`kind: lib` 的包不进 `shims/`，留在 `install/<name>/<version>/`，目录结构固定为 `include/`、`lib/`、`lib/cmake/`、`lib/pkgconfig/`。Verx 只做三件事，不替构建系统包办一切：

```text
verx env                               # 输出 shell 代码，配合 eval 进当前 shell
verx env --project .                   # 只带项目 verx.json 里的库
verx env --shell fish|pwsh|bash|zsh    # 指定 shell，不指定就自动判断
verx env --auto                        # 静默模式：不在项目里就不输出，给 rc 文件用
verx env --clear                       # 输出反向清理代码，eval 一下清掉
verx integrate cmake                   # 装 CMake 工具链文件，之后 find_package 直接能找到 install/ 下的库
verx integrate pkg-config              # 同理，保证 pkg-config 能搜到 install/ 下的 .pc
```

`env` 吐的是可执行的 shell 代码，不是给人看的表格，所以 `verx env` 永远配合 `eval` 用。進 shell 的东西只有三类：`PATH` 加 `shims/`，编译变量（`CMAKE_PREFIX_PATH、PKG_CONFIG_PATH、C_INCLUDE_PATH、LIBRARY_PATH`），项目库的开关。`env` 自己不改任何文件，不写 rc，只管输出，写 rc 那行由用户自己加，这是和 direnv 同一个做法：工具负责算，shell 负责吃。

项目级用 `verx.json + verx.lock` 钉死：`verx.json` 写要哪些库，`verx.lock` 记精确版本加 Registry commit，CI 用 `verx install` 空参数按锁重装。第一批只保证 C/C++ native 库（CMake、pkg-config、MSBuild 能用），其他语言复用同一套 `exports`，不另起协议。

## 安全底线

下载 hash 必验，不对就停。official Registry 强制签名，community 推荐签名。含构建步骤的包默认摊开确认，默认断网构建。私有 Registry 口令只读环境。撤销名单定期对全量已装扫一遍，命中就告警隔离。
