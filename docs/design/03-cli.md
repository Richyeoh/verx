# 命令行设计

## 感觉上像什么

目标是用户不用学 Verx。会装、会卸、会查、会升，就够了。动词沿用三家已经被验证的叫法，不发明新词。

```text
verx search rg
verx install ripgrep
verx upgrade --all
verx remove ripgrep
```

装多个就空格连写，查详情用 info，看能升的用 outdated，锁版本用 pin，体检用 doctor。需构建的包安装前会停下来，把要执行的构建步骤打出来问一句确认，CI 才用跳过确认的方式。

## 几个关键体验决定

第一，歧义不猜。搜到多个同名包就列出来让人选，装错软件比多打一行命令严重得多。

第二，试运行先行。`--dry-run` 只演算依赖和下载，不写盘。依赖冲突时把原因和建议一起打出来，而不是甩一个错误码。

第三，输出分两种。人看的默认简洁表格，管道和 CI 自动降级；机器看的 `--json` 字段只增不改，方便脚本解析。

第四，破坏性操作二次确认。删配置、清缓存、动 Registry，都要再问一句，除非用户明确说不用问。

## 日常流程走一遍

```text
verx registry list
verx registry update
verx search rg --os windows
verx install ripgrep --dry-run
verx install ripgrep
rg --version
verx pin ripgrep
verx upgrade --all
verx remove ripgrep
verx doctor
```

这一串能跑通，Verx 就算好用了。doctor 负责兜底：`bin/` 和 `shims/` 进 PATH 了没、包入口全不全、`install/` 里实体和状态对不对得上、Registry 通不通，一键修。

## 给编程语言装依赖：借 vcpkg，但不另起一套

Verx 不只装工具，还要给别的语言提供原生依赖。做法是工具和库走同一个 Registry、同一个 install，靠包里的 `kind: app/lib` 区分：

- `app`：装完链进 `shims/`，人直接跑。
- `lib`：装完不进 `shims/`，留在 `install/<name>/<version>/`，给编译器用。

给项目用时多两个动作就够了。项目根放一个 `verx.json` 声明要哪些库：

```json
{"dependencies": ["zlib@1.3.1", "openssl@^3.0"]}
```

```text
verx add zlib openssl       # 写进 verx.json 并装上（可在项目目录或全局）
verx install                # 空参数时按 verx.json + verx.lock 装齐（给 CI 用）
verx env                    # 输出 shell 代码，配合 eval 一次生效（见下）
verx integrate cmake        # 一次性把 CMake 工具链接好，之后 cmake 直接 find_package
```

`verx env` 不是只打印看看的，它吐的是 shell 代码，`eval` 一下就进当前 shell，这叫 shell 集成：

```bash
eval "$(verx env)"                        # bash/zsh：一次生效，PATH、编译变量全进当前 shell
eval "$(verx env --project .)"            # 只带项目 verx.json 里的库，不带全局
verx env --shell fish | source            # fish
Invoke-Expression (& verx env --shell pwsh | Out-String)   # PowerShell
```

往 shell 启动文件里加一行，以后打开就是配好的，不用每次粘：

```bash
echo 'eval "$(verx env --auto)"' >> ~/.bashrc     # bash
echo 'eval "$(verx env --auto)"' >> ~/.zshrc      # zsh
```

`--auto` 是静默模式：没进项目目录就不输出，`cd` 到有 `verx.json` 的目录再 `eval` 才有东西，配 `chpwd/direnv` 之类自动触发也行。退出用 `verx env --clear` 吐反向代码，`eval` 一下就清掉，不留尾巴。

规矩：`verx install <name>` 不分 app 还是 lib，装完的行为由 `kind` 决定；`verx remove` 删 lib 时不断掉别的包的依赖要报错；`search/info/list` 直接标出 `app/lib`，找库一眼看出来。

## 服务管理

服务沿用同样的感觉，不另起一套：

```text
verx services list
verx services start caddy
```

只是纯命令，没有界面。这是完整版里的一部分，不是另起的 GUI。
