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

1. 打开 **Profiles**，导入订阅 URL 或本地配置文件。
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
flclash ssh add home host --user user --password --local-port 1080
flclash ssh connect home
flc ssh curl https://example.com

# 默认交给 host 的网络策略；-d 只在 host 的 FlClash TUN 关闭时允许直连
flc ssh -d curl https://example.com

# 加密私钥；也可同时保存私钥口令和 SSH 登录密码
flclash ssh add school host --user user --identity ~/.ssh/id_ed25519 --passphrase --password

# 让其他支持 SOCKS5 的本机程序使用同一隧道
ALL_PROXY=socks5h://127.0.0.1:1080 curl https://example.com

flclash ssh list
flclash ssh test home
flclash ssh disconnect home
```

`flc ssh COMMAND` 不主动指定 B 的代理端口：流量在 B 解密后，由 B 的 Clash、TUN、路由规则或其他网络策略决定出口。`flc ssh -d COMMAND` 是严格直连检查：B 必须安装兼容 FlClash 且 Backend 可查询，若 B 开启透明 TUN、状态未知或版本不兼容，命令会拒绝执行而不会误标直连。

SSH 配置也可以直接在 TUI 的 **SSH** 页面管理。主页面同时显示 SSH 配置列表方框和当前配置的 Dashboard；用 Tab 在侧栏、配置列表和 Dashboard 间切换焦点。Username 和 Host 分开填写，避免误用 WSL 当前用户名。未加密私钥无需口令；加密私钥未保存口令时，连接会在 TUI 内安全询问一次且不落盘。密钥和 SSH 登录密码同时存在时先尝试指定密钥，密码仅用于回退或服务器要求的第二因素。OpenSSH 警告只写入 Logs，不会破坏 TUI 画面。列表 Enter 会连接未就绪的配置，已连接时转到 Dashboard；Dashboard 先显示 A 与 B 的 inet IP，再依序显示可验证直连出口和 B 决定的 managed 出口的 IP、延迟、下载速度；透明 TUN 开启时直连卡会明确显示已阻止。

WSL 中不要直接使用权限显示为 `0777` 的 `/mnt/c/...` 私钥；OpenSSH 会拒绝它。先复制到 `~/.ssh/` 并执行 `chmod 600 ~/.ssh/KEY`。

History、Connections 和 Logs 页面支持 `/` 搜索、Enter 查看完整详情；History 可按 `f` 筛选状态，Logs 可按 `f` 筛选级别。清空记录或关闭连接前会要求确认，Logs 同时显示 Backend 持久日志和当前 TUI 事件。

## 常用命令

```bash
flclash status
flclash mode rule|global|direct|silent
flclash port [PORT|off]
flclash sys status|on|off
flclash tun status|user on|system on|off
flclash profile import URL
flclash profile import-file /path/to/nodes.txt
flclash logs --lines 100 --follow
flclash history show --limit 20
flclash exit                       # 完全退出前端、Backend、Core 和 SSH
```

TUI 中 `q` 或关闭其终端只退出当前界面并释放前端记录，`Ctrl+C` 完全退出，`Ctrl+N` 查看通知详情，`?` 查看全部快捷键。

订阅导入兼容 Mihomo/Clash YAML、节点 URI 列表及其 Base64、SIP008、sing-box/Xray JSON，以及常见 Surge/Quantumult X/Loon 节点行；SIP008 的 v2ray-plugin/simple-obfs 参数会保留。本地文件不限制扩展名；重复或内核保留节点名会自动消歧，不支持或损坏的节点会中止整次导入，不会静默漏掉。

完整命令、TUN、Profiles、History、日志、配置目录和故障排查见 [CLI_LINUX.md](CLI_LINUX.md)。程序内可使用 `flclash --help`、`flclash COMMAND --help` 和 `flc --help`。

本项目是 [FlClash](https://github.com/chen08209/FlClash) 的非官方终端衍生版本，许可证与版权信息见 [NOTICE](NOTICE) 和 [LICENSE](LICENSE)。
