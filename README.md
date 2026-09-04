# FlClash TUI

[English](README_EN.md) · [完整文档](CLI_LINUX.md) · [下载版本](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

Linux / SSH / 无头机上的 Mihomo 终端管理器。运行 `flclash` 打开全屏 TUI；默认 **silent**，只有 `flc COMMAND` 走代理。

## 适合谁

- 远程 Linux、云主机、实验室机器或 WSL，已经有订阅或本地配置；
- 只要 Codex、Claude、Git、npm 等**指定命令**走代理；
- 不想让其他服务、容器、已有 SSH 会话被全局代理带走；
- 代理入口、Core 或节点不可用时要明确失败，而不是静默直连。

## 安装

安装脚本会识别 AMD64/ARM64，并选择 Debian 包或便携包：

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

强制便携安装：

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

GitHub 拉不下来时，从 [Releases](https://github.com/yqlay/flclash-tui/releases) 下载对应架构的 `.deb` 或 `.tar.gz`，再 scp 到机器上安装。不要写死版本号，始终用最新 Release。

## 最快路径

1. 运行 `flclash` 打开 TUI。
2. 在 **Profiles** 导入订阅 URL 或本地文件，选中后按 Enter 激活。
3. 在 **Proxies** 选择组和节点。选中的组就是 `flc` 出口。
4. Dashboard 保持 **silent**，把焦点放到 **Core**，按 Enter 启动（不要开 System proxy）。
5. 另开一个终端跑需要代理的命令：

```bash
flc codex
flc claude
flc git clone https://github.com/owner/repository.git
flc npm install
flc curl https://example.com
flc bash    # 这个 shell 里的子命令继承代理；退出后环境立刻消失
```

`flc` 只给这条命令（以及 `flc bash` 里的子进程）设置大小写的 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`。silent 使用本机认证后的私有 loopback，不把端口暴露给局域网或公网。入口、Core 或节点不可用时，`flc` 会报错并拒绝执行，不会偷偷直连。

6. 用完后：

```bash
flclash exit    # 停当前用户的前端、Backend、Core 和 SSH
```

TUI 里按 `q` 或关掉那个终端，只退出当前界面，Backend 还在，`flc` 仍可用。

## 桌面 / 全局代理

要让浏览器等普通程序也走代理：在 Dashboard 把模式改成 `rule` 或 `global`，再启用 **System proxy** 或 TUN。无头机优先用上面的 `flc`。

## SSH 代理

只在**需要被代理的那台机器**上运行 FlClash，把另一台已能上网的机器当作 SSH host。之后 `flc ssh COMMAND` 会把这条命令从 host 的网络出口送出去。B 不开放代理端口，A 只需现有 SSH。

```bash
flclash ssh import                 # 从 ~/.ssh/config 导入 Host
flclash ssh add home host --user user --password --local-port 1080
flclash ssh default home
flc ssh curl https://example.com   # 自动连接默认配置；断线会重建隧道

# 默认交给 host 的网络策略；-d 只在 host 的 FlClash TUN 关闭时允许直连
flc ssh -d curl https://example.com

flclash ssh add school host --user user --jump bastion --identity ~/.ssh/id_ed25519 --passphrase --password
flclash ssh test                   # 打开隧道并做 SOCKS5 握手
flclash ssh disconnect home        # 只关 SSH；flclash backend stop 不停隧道
```

没有已打开的隧道时，`flc ssh` 会连接默认（或唯一）配置并保持为持久隧道。TUI 的 **SSH** 页同样能管理配置；`u` 将当前项标为默认。`flc ssh -d` 在远端透明 TUN 开启、状态未知或版本不兼容时会拒绝执行。WSL 不要直接用权限显示为 `0777` 的 `/mnt/c/...` 私钥，先复制到 `~/.ssh/` 并 `chmod 600`。

完整 SSH 字段、密钥口令、Jump 和 Dashboard 测速见 [CLI_LINUX.md](CLI_LINUX.md)。

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
flclash ssh disconnect             # 只关 SSH 隧道
flclash backend stop               # 停代理 Backend+Core，SSH 还在
flclash exit                       # 完全退出前端、Backend、Core 和 SSH
```

TUI：`q` 只退当前界面；`Ctrl+C` 全停；Dashboard 管 Core、模式、flc、端口、TUN 和 System proxy；`?` 查看快捷键；`Ctrl+N` 查看通知。

订阅导入兼容 Mihomo/Clash YAML、节点 URI 列表及其 Base64、SIP008、sing-box/Xray JSON，以及常见 Surge/Quantumult X/Loon 节点行。不支持或损坏的节点会中止整次导入，不会静默漏掉。

完整命令、TUN、Profiles、History、日志、配置目录和故障排查见 [CLI_LINUX.md](CLI_LINUX.md)。程序内可使用 `flclash --help`、`flclash COMMAND --help` 和 `flc --help`。

本项目是 [FlClash](https://github.com/chen08209/FlClash) 的非官方终端衍生版本，许可证与版权信息见 [NOTICE](NOTICE) 和 [LICENSE](LICENSE)。
