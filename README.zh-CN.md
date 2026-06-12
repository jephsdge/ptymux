# ptymux

[English README](README.md)

`ptymux` 是一个小型命令行 PTY 多路复用工具。它在命名 target 后面维护长期
存活的 shell 进程，因此多次命令可以共享当前目录、环境变量，以及已经进入的
SSH 会话。

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
这个概念，日常命令会更简单。

target 是懒创建的。第一次对某个 target 执行命令时，会自动创建背后的
`/bin/sh` 进程和 PTY。

## 安装

编译静态二进制：

```sh
CGO_ENABLED=0 go build -o ptymux ./cmd/ptymux
```

也可以把它放到 `PATH` 里：

```sh
install -m 0755 ptymux ~/.local/bin/ptymux
```

## 基本用法

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
退出码。普通 shell 命令优先使用这个模式。

### Idle 模式

`idle` 适合进入或退出交互 shell，例如 SSH：

```sh
ptymux idle work "ssh admin@localhost -p 2222"
ptymux work "pwd"
ptymux idle work "exit"
```

Idle 模式不会追加 marker。它发送命令后等待 PTY 输出安静一小段时间，然后返回。

Idle 是启发式判断。像 `sleep 2 && echo done` 这种延迟输出命令，可能会在所有
输出到达前提前返回。

### Send 模式

`send` 用于向 target 写入输入，然后跟随输出：

```sh
ptymux send work "ls"
```

`send` 不追加 marker。它会持续流式输出，直到你用 `Ctrl+C` 停止当前客户端；
target 本身会继续运行。

当 target 位于交互程序或远端 shell 中、marker 不可靠时，这个模式很有用。
例如 SSH 密码提示之后：

```sh
ptymux send work "your-password"
```

对于 SSH 密码，优先使用 SSH key 或 agent。不要长期把密码直接写在命令参数里，
因为它可能进入 shell history，或者短暂出现在进程参数中。

### Ctrl+C

向 target 发送 Ctrl+C：

```sh
ptymux ctrl-c work
```

这会向目标 PTY 写入 ETX 字节，也就是 `0x03`，然后像 `send` 一样跟随输出。
用 `Ctrl+C` 停止观察；target 仍然保留。

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

默认 socket 路径是：

```text
/tmp/ptymux-<uid>.sock
```

如果你想使用独立 daemon，可以指定 socket：

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock stop
```

## 说明

- 每个完整 target 路径对应一个长期存活的 `/bin/sh` 进程，并连接到一个 PTY。
- PTY 输出会像真实终端一样合并 stdout/stderr。
- `send` 和 `ctrl-c` 会持续输出，直到客户端断开。
- 目前还没有完整 attach 模式；输入仍然是一条命令一条命令发送。

## License

MIT
