# ptymux

[English README](README.md)

`ptymux` 是一个小型命令行 PTY 多路复用工具。它在命名 target 后面维护长期
存活的 shell 进程，因此多次命令可以共享当前目录、环境变量，以及已经进入的
SSH 会话。

它提供三个可执行文件：

- `ptymux`：本地 Unix socket client 和自动启动的 daemon；仍可使用可选的
  `local` 前缀。
- `ptymux-server`：前台运行、长期持有远端 target 的 HTTP/WS 服务。
- `ptymux-client`：注册并通过鉴权后操作远端 target 的客户端。

## Target 路径

target 是一个最多三段的路径：

```text
name
name/group
name/group/shell
```

省略的部分会使用 `default`：

```text
work             -> work/default/default
work/main        -> work/main/default
work/main/build  -> work/main/build
```

内部这三段会映射到 `session`、`pane`、`tab`。CLI 对外统一使用 `target`
这个概念，日常命令会更简单。每一段都必须是合法 UTF-8，最多 64 bytes，且不能
包含 `/`、NUL 或控制字符。命令和输入文本最多 128 KiB。

本地 target 是懒创建的。第一次对某个本地 target 执行命令时，会自动创建背后
的 shell 进程和 PTY。远端 target 必须通过
`ptymux-client ... create <target>` 显式创建。

## 安装

编译三个静态可执行文件：

```sh
./scripts/build.sh
```

默认使用 Linux amd64 和 `CGO_ENABLED=0`，输出：

```text
dist/ptymux
dist/ptymux-client
dist/ptymux-server
```

可以覆盖目标平台或输出目录：

```sh
GOOS=linux GOARCH=arm64 ./scripts/build.sh
GOOS=darwin GOARCH=arm64 ./scripts/build.sh
OUT_DIR=. CGO_ENABLED=0 ./scripts/build.sh
```

构建内置 skill wrapper 使用的平台二进制：

```sh
TARGET=skill-all ./scripts/build.sh
```

该命令会把 ignored 的平台二进制写入 `skills/use-ptymux/assets/`。提交到仓库的
`skills/use-ptymux/assets/ptymux` wrapper 会在运行时选择匹配的 Linux 或 macOS
二进制。

等价的手动命令：

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux ./cmd/ptymux
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux-client ./cmd/ptymux-client
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux-server ./cmd/ptymux-server
```

也可以把三个可执行文件安装到 `PATH`：

```sh
install -m 0755 dist/ptymux dist/ptymux-client dist/ptymux-server ~/.local/bin/
```

## Local、Server 和 Client 可执行文件

`ptymux` 只负责 Local。增加可选的 `local` 前缀后行为完全相同：

```sh
ptymux work "pwd"
ptymux local work "pwd"
ptymux send work "ls"
ptymux local send work "ls"
```

Local 命令使用后文描述的自动启动 Unix socket daemon。远端操作使用独立的
`ptymux-client` 和 `ptymux-server` 可执行文件。

预先准备全局 token 后，在 8443 端口以前台方式运行远端 HTTP/WS 服务：

```sh
ptymux-server
```

Server 默认文件路径为：

```text
~/.ptymux/server/token
~/.ptymux/server/clients.json
```

非空全局 token 必须预先存在，ptymux 不会自动生成；registry 及其安全父目录会
自动创建。显式传入 `--token-file` 和 `--client-registry` 可以覆盖默认路径。
Server 保持前台运行，并持续持有 target，直到 target 自然退出、被关闭或 server
停止。Server 或容器重启后不会恢复旧 target。

Token/password 鉴权只提供访问控制。HTTP/WS 不会加密凭据、命令或输出。只能在
可信内网中暴露 ptymux，绝不能直接暴露到公网或其他不可信网络。

Server 默认限制为 256 个尚未成功鉴权的 accepted TCP 连接、256 个 pending 加 active
的已鉴权 WebSocket、每个 client 16 个 WebSocket，以及每个 client 64 个 targets。
注册请求和鉴权失败分别使用独立的按来源地址 token bucket，每个 bucket 每秒补充 5 个
token、burst 为 20；鉴权成功不会消耗 token。可以通过
`--max-pre-auth-connections`、`--max-connections`、
`--max-connections-per-client`、`--max-targets-per-client`、`--auth-rate` 和
`--auth-burst` 覆盖这些默认值。

先把共享 Server Token 安装到 Client 默认路径，再注册 client name。Server 只会
输出一次自动生成的密码：

```sh
install -d -m 0700 ~/.ptymux/client
install -m 0600 /path/to/server-token ~/.ptymux/client/server.token
umask 077
password_tmp="$(mktemp ~/.ptymux/client/tianyijie.password.XXXXXX)"
if ptymux-client register \
  --url http://host:8443 \
  --name tianyijie > "$password_tmp"; then
  mv "$password_tmp" ~/.ptymux/client/tianyijie.password
else
  rm -f "$password_tmp"
  false
fi
```

后续操作会自动使用默认 token 和按 client name 派生的密码路径：

```sh
ptymux-client --url http://host:8443 --name tianyijie create work
ptymux-client --url http://host:8443 --name tianyijie \
  send work "relay-cli login -u tianyijie"
```

仍可显式传入 `--token`、`--token-file`、`--password` 或 `--password-file`，
显式值会覆盖默认文件。

远端操作包括 `create`、`list`、`send`、`text`、`keys`、`read`、`follow`
和 `close`。`send` 会按 Enter，`text` 不会按 Enter，`keys` 用于发送命名按键
序列。用 `Ctrl+C` 结束远端 `follow` 不会关闭 target。除 `create` 和 `list`
外，远端操作要求 target 已经存在。

每个注册 client 都会获得一个内部不可变 owner 身份。它的 target namespace 完全
私有：其他 client 即使使用相同 target 名称，也不能 list、read、write、follow
或 close 这些 target。

## 基本用法

本节命令使用隐式 local 模式。

查看 CLI 帮助：

```sh
ptymux -h
```

在持久 target 里运行命令：

```sh
ptymux work "pwd"
ptymux work "cd /tmp"
ptymux work "pwd"
```

最后一次 `pwd` 会在同一个 shell 里执行，输出里会包含：

```text
/tmp
```

如果你需要拆分不同 shell，可以使用完整 target 路径：

```sh
ptymux work/main/build "go test ./..."
ptymux work/main/shell "pwd"
```

输出是类似真实终端的 transcript。prompt 和命令回显会显示出来，但 ptymux
内部 marker 行会被隐藏。`run`、`idle`、`send` 都会使用 VT 终端模拟器渲染
当前 prompt 行，因此输出看起来像普通终端：

```text
sh-5.3$ pwd
/home/work/Projects/ptymux
sh-5.3$
```

## 命令模式

### Run 模式

默认就是 run 模式：

```sh
ptymux work "git status"
```

它会追加一个内部完成 marker，等待 marker 出现，过滤掉 marker，再返回命令
退出码。Run 模式没有固定执行超时。按 `Ctrl+C` 会中断当前本地命令，等待 shell
重新同步，并保留 target 供后续命令继续使用。命令输出最多捕获 8 MiB；达到上限后
ptymux 会继续排空输出并等待命令结束，同时打印截断提示。普通 shell 命令优先使用
这个模式。

### Idle 模式

`idle` 适合进入或退出交互 shell，例如 SSH：

```sh
ptymux idle work "ssh admin@localhost -p 2222"
ptymux work "pwd"
ptymux idle work "exit"
```

Idle 模式不会追加 marker。它发送命令后等待 PTY 输出安静 500ms，然后返回。
它等价于 `send -t 500ms`。

Idle 是启发式判断。像 `sleep 2 && echo done` 这种延迟输出命令，可能会在所有
输出到达前提前返回。静默时长不是总超时：每次收到输出都会重新计时，因此持续
输出的命令可以让请求一直等待。静默等待最多捕获 8 MiB 输出；达到上限后 ptymux
仍会继续等待真正的静默边界。

### Send 模式

`send` 用于向 target 写入输入，并且不追加完成 marker：

```sh
ptymux send work "ls"
```

默认情况下，`send` 会写入输入并直接返回，不打印输出。后台 reader 会继续维护
当前终端屏幕和有界的 ANSI 终端行历史。

发送后跟随输出：

```sh
ptymux send -f work "ls"
```

`send -f` 会持续流式输出，直到你用 `Ctrl+C` 停止当前客户端；target 本身会
继续运行。

等待输出静默后返回新增输出：

```sh
ptymux send -t 100 work "ls"   # 100ms
ptymux send -t 1s work "ls"    # 1 秒
ptymux send -t 1m work "ls"    # 1 分钟
ptymux send -t 1ms work "ls"   # 1 毫秒
```

没有单位的 duration 会按毫秒解释。`-f` 和 `-t` 互斥，不能同时使用。

当 target 位于交互程序或远端 shell 中、marker 不可靠时，`send` 很有用。
例如 SSH 密码提示之后：

```sh
ptymux send work "your-password"
```

对于 SSH 密码，优先使用 SSH key 或 agent。不要长期把密码直接写在命令参数里，
因为它可能进入 shell history，或者短暂出现在进程参数中。

### Command 模式

`command` 用于发送终端按键序列，并在结尾自动按 Enter：

```sh
ptymux command work "ctrl-c"
ptymux command work "ctrl-o d"
ptymux command -t 500ms work "ctrl-c"
ptymux command -f work "ctrl-o d"
```

空格表示先后按，`-` 表示组合键。ptymux 会在按键序列结束后自动追加 Enter。
例如 `ctrl-o d` 会发送 Ctrl+O，然后发送 `d`，然后发送 Enter。

支持的命名按键包括 `enter`、`esc`、`escape`、`tab`、`backspace` 和 `space`。
`-f` 和 `-t` 的行为与 `send` 一样：持续 follow，或等待输出静默指定时间。

### Text 和 Keys

`text` 用于输入原始文本，不会自动按 Enter：

```sh
ptymux text work "hello"
ptymux keys work "enter"
```

`keys` 用于发送按键序列，也不会隐式追加 Enter：

```sh
ptymux keys work "ctrl-c"
ptymux keys work "up enter"
ptymux keys -t 500ms work "ctrl-c"
ptymux keys -f work "pageup"
```

支持的命名按键包括 `enter`、`esc`、`escape`、`tab`、`backspace`、`space`、
`up`、`down`、`left`、`right`、`home`、`end`、`delete`、`pageup` 和
`pagedown`。

可编程交互优先使用 `text` 和 `keys`，因为它们只做名字表达的动作。需要“发送
这些按键，然后按 Enter”时继续使用 `command`。旧的 `ctrl-c` 命令作为兼容别名
保留。

### Ctrl+C

向 target 发送 Ctrl+C：

```sh
ptymux ctrl-c work
```

这会向目标 PTY 写入 ETX 字节，也就是 `0x03`，然后像 `send` 一样跟随输出。
用 `Ctrl+C` 停止观察；target 仍然保留。

### Read 模式

读取带 ANSI 样式的当前终端屏幕：

```sh
ptymux read work
```

读取最近 `N` 行 ANSI 终端历史：

```sh
ptymux read -n 3 work
```

所选历史行按从旧到新的顺序返回。这是有容量上限、按行记录的终端历史，
不是命令历史或完整 scrollback。`N` 必须在 0 到 4096 之间；零与普通 `read` 一样
读取当前屏幕。alternate screen 激活时，`read -n N` 返回该屏幕最后 `N` 个可见行。
`read` 是只读操作，不会阻塞其他客户端中的命令。

### Follow 模式

不发送输入，只实时转发后续原始 PTY 输出：

```sh
ptymux follow work
```

用 `Ctrl+C` 停止观察；target 仍然保留。`follow` 保留 ANSI 和其他终端控制序列，
不会重放当前屏幕或历史，也不会锁住 target。配合当前版本 daemon 时，本地流式操作
会把 daemon 错误与终端字节分开：错误写入 stderr，并返回非零状态。新版 client 仍可
连接旧 daemon，但 legacy stream 可能把错误混入 stdout，也可能返回零状态。执行
`ptymux stop` 重启 daemon 后即可使用当前行为。

### Kill 模式

关闭一个 target，并从 daemon 中移除：

```sh
ptymux kill work
ptymux kill work/main/build
```

`kill` 会向该 target shell 所在的进程组发送信号，关闭 PTY，并从内存 store
中移除这个 target。下次再使用同一个 target 时，会启动一个新的 shell。

为了兼容旧行为，不带 target 的 `ptymux kill` 会关闭所有 managed shells。

## 查看 Targets

列出所有 targets：

```sh
ptymux list
```

列出某个 target 下的子 group：

```sh
ptymux list work
```

列出某个 group 下的 shell：

```sh
ptymux list work/main
```

## Daemon

`ptymux` 会在需要时自动启动 daemon。通常不需要手动启动。

停止 daemon 并关闭所有 managed shells：

```sh
ptymux stop
```

shutdown 一旦开始，daemon 会先拒绝新操作，再关闭 targets，并等待已接受的请求退出。

只关闭某个 target，不停止 daemon：

```sh
ptymux kill work
```

默认 socket 路径是：

```text
~/.ptymux/sockets/ptymux-default.sock
```

daemon 启动时，`ptymux` 会自动创建 `~/.ptymux/sockets` 目录。自定义 socket 路径
不会覆盖普通文件、目录或符号链接。只有属于当前用户并明确确认 stale 的 Unix socket
才会在启动前删除；停止时也只会删除该 daemon 自己创建的 socket。

如果你想使用独立 daemon，可以指定 socket：

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock stop
```

## 配置和远端别名

Local daemon 配置仍放在 `~/.ptymux/config`，并以
`~/.ptymux/config.json` 作为旧路径 fallback：

```json
{
  "shell": "/bin/bash",
  "auto_release": {
    "enabled": true,
    "target_idle_timeout": "8h",
    "daemon_idle_timeout": "30m"
  }
}
```

远端 alias 只从私有文件 `~/.ptymux/client/config` 读取：

```json
{
  "clients": {
    "relay": {
      "url": "http://host:8443",
      "name": "tianyijie"
    }
  }
}
```

这是不兼容的 alias 路径迁移：`~/.ptymux/config` 和
`~/.ptymux/config.json` 中的远端 `clients` 会被忽略。新的 Client 配置和所有
Client secret 都必须是私有普通文件且不能是 symlink；建议文件使用 `0600`，
`~/.ptymux/client` 使用 `0700`。Alias 仍可配置 `token_file` 和
`password_file` 来覆盖默认 secret 路径。

配置别名后可以缩短远端命令：

```sh
ptymux-client relay create work
ptymux-client relay send work "relay-cli login -u tianyijie"
ptymux-client relay read -n 20 work
ptymux-client relay follow work
ptymux-client relay keys work ctrl-c
ptymux-client relay close work
```

显式连接参数会覆盖别名字段。优先使用 `token_file` 和 `password_file`，不要把
secret 放进 inline `token`、`password` 或 CLI 参数，避免 secret 出现在 shell
history 或进程参数中。Secret 文件必须是私有普通文件、不能是 symlink，建议使用
`0600`。

顶层 `shell` 和 `auto_release` 只作用于 local daemon。`shell` 默认是
`/bin/sh`，`target_idle_timeout` 默认是 `8h`，`daemon_idle_timeout` 默认是
`30m`。把某个 timeout 设置为 `"0"` 可以单独关闭对应行为；把 `enabled` 设置为
`false` 可以完全关闭本地自动释放。修改这些设置后执行 `ptymux stop` 重启 local
daemon。

## 轮换或撤销远端 Client

轮换 client 密码，同时保留 owner 身份和 targets。下面的 `password_file` 必须指向
alias 实际配置的 `password_file`；只有 alias 未覆盖该字段时才使用按名称派生的默认
路径。如果 alias 使用 inline `password`，应先迁移到私有密码文件再轮换。

```sh
umask 077
password_file="$HOME/.ptymux/client/tianyijie.password"
password_tmp="$(mktemp "${password_file}.XXXXXX")"
if ptymux-client relay rotate > "$password_tmp"; then
  mv -- "$password_tmp" "$password_file"
else
  rm -f "$password_tmp"
  false
fi
```

不要把 rotation 输出直接重定向覆盖当前密码文件：shell 会在 ptymux 使用旧密码完成
鉴权前先清空该文件。

轮换会让旧密码以及使用旧 credential generation 建立的连接失效。撤销 client
会只关闭该 owner 的连接和 targets：

```sh
ptymux-client relay revoke
```

被撤销的名称可以重新注册，但会获得新的 owner 身份，不能重新访问旧 targets。

## Relay Docker 镜像

部署 Dockerfile 位于单独的 `xiaogang_pty` checkout，通过 BuildKit named
context 编译 ptymux。显式设置两个 checkout 路径：

```sh
ptymux_src=/path/to/ptymux
xiaogang_pty=/path/to/xiaogang_pty
gomod_cache="$(go env GOMODCACHE)"
docker build --network host \
  --build-context ptymux-src="$ptymux_src" \
  --build-context gomod-cache="$gomod_cache" \
  -t ptymux-relay:dev \
  -f "$xiaogang_pty/Dockerfile" "$xiaogang_pty"
```

只读挂载 token 文件，并使用持久化 registry volume 运行：

```sh
docker run --rm -p 8443:8443 \
  -v "$PWD/secrets/ptymux.token:/run/secrets/ptymux.token:ro" \
  -v ptymux-data:/var/lib/ptymux \
  --name relay-dev \
  ptymux-relay:dev
```

镜像会以非 root `work` 用户运行 `ptymux-server`，自动安装 `relay-cli`，并把
client registry 保存到 `/var/lib/ptymux`。保留 volume 时，替换容器后 registry
仍然存在；运行中的 shell 不会恢复。

## 说明

- 每个完整 target 路径对应一个长期存活的 shell 进程，并连接到一个 PTY。
- PTY 输出会像真实终端一样合并 stdout/stderr。
- 完整命令结果和静默等待结果会清理终端控制序列，并保留普通 prompt 文本。
  `read` 返回带 ANSI 样式的屏幕或历史内容。
- `send -f`、按键 follow 模式和 `ctrl-c` 会持续输出清理后的文本。本地及远端
  `follow` 只转发后续原始 PTY 输出；断开 follower 不会停止 target。
- 远端 client 断开不会关闭 target。需要停止 target 时使用远端 `close`、client
  `revoke` 或停止 server。
- 第一版远端模式不提供完整 attach；输入通过 `send`、`text` 或 `keys` 发送。

## License

MIT
