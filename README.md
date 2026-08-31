# FlClash TUI

[English](README_EN.md) · [完整文档](CLI_LINUX.md) · [下载版本](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

面向 Linux、SSH 和无头服务器的 Mihomo/Clash 终端代理管理器。运行 `flclash` 使用全屏 TUI，使用 `flc COMMAND` 只代理指定命令。

## 安装

安装脚本会自动识别 AMD64/ARM64，并选择 Debian 包或便携包：

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

强制便携安装：

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

也可以前往 [Releases](https://github.com/yqlay/flclash-tui/releases) 手动下载。

## 开始使用

```bash
flclash
```

1. 打开 **Profiles**，导入订阅 URL 或本地 YAML。
2. 选中 Profile，按 Enter 激活。
3. 打开 **Proxies**，选择代理组和节点。
4. 默认是 `silent` 模式，使用 `flc` 运行需要代理的命令：

```bash
flc curl https://example.com
flc git clone https://github.com/owner/repository.git
flc sh -c 'curl -s https://example.com | jq .'
```

桌面程序需要代理时，将模式切换为 `rule` 或 `global`，然后在 Dashboard 启用 **System proxy**。无桌面环境通常使用 `flc`、程序自身的代理设置或 TUN。

## SSH 代理

只在需要代理流量的机器上运行 FlClash。下面的例子表示本机流量通过 `user@host` 的网络出口访问互联网：

```bash
flclash ssh add home user@host --password --local-port 1080
flclash ssh connect home
flc ssh curl https://example.com

# 加密私钥；也可同时保存私钥口令和 SSH 登录密码
flclash ssh add school user@host --identity ~/.ssh/id_ed25519 --passphrase --password

# 让其他支持 SOCKS5 的本机程序使用同一隧道
ALL_PROXY=socks5h://127.0.0.1:1080 curl https://example.com

flclash ssh list
flclash ssh test home
flclash ssh disconnect home
```

SSH 配置也可以直接在 TUI 的 **SSH** 页面管理。列表按 Enter 进入独立 Dashboard，可查看 SSH 出口 IP、延迟、下载速度、活动连接以及该 SSH SOCKS5 端口的实时/累计流量；统计不依赖 root 或 eBPF。`Key passphrase` 是私钥口令，`SSH password` 是服务器登录密码，两者可同时设置；已连接配置为只读，先断开再修改。

History、Connections 和 Logs 页面支持 `/` 搜索、Enter 查看完整详情；History 可按 `f` 筛选状态，Logs 可按 `f` 筛选级别。清空记录或关闭连接前会要求确认，Logs 同时显示 Backend 持久日志和当前 TUI 事件。

## 常用命令

```bash
flclash status
flclash mode rule|global|direct|silent
flclash port [PORT|off]
flclash sys status|on|off
flclash tun status|user on|system on|off
flclash profile import URL
flclash profile import-file /path/to/config.yaml
flclash logs --lines 100 --follow
flclash history show --limit 20
flclash exit                       # 完全退出前端、Backend、Core 和 SSH
```

TUI 中 `q` 只退出当前界面，`Ctrl+C` 完全退出，`Ctrl+N` 查看通知详情，`?` 查看全部快捷键。

完整命令、TUN、Profiles、History、日志、配置目录和故障排查见 [CLI_LINUX.md](CLI_LINUX.md)。程序内可使用 `flclash --help`、`flclash COMMAND --help` 和 `flc --help`。

本项目是 [FlClash](https://github.com/chen08209/FlClash) 的非官方终端衍生版本，许可证与版权信息见 [NOTICE](NOTICE) 和 [LICENSE](LICENSE)。
