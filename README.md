# FlClash TUI

[English](README_EN.md) · [完整 CLI/TUI 文档](CLI_LINUX.md) · [Releases](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

FlClash TUI 是面向 Linux、SSH 和无头服务器的 Mihomo/Clash 终端代理管理器。它提供全屏 TUI、可脚本化 CLI，以及只让单个命令经过代理的 `flc`。

本项目是 [FlClash](https://github.com/chen08209/FlClash) 的非官方终端衍生版本，不是 FlClash 或 Mihomo 的官方发行版。版权与许可证见 [NOTICE](NOTICE) 和 [LICENSE](LICENSE)。

## 特点

- Dashboard（含蓝色上行、绿色下行和青色交界实时曲线）、Proxies、Profiles、History、Connections、Logs、Settings 和 Maintenance 八个 TUI 页面。
- 延迟与测速可用方向键选中后按 Enter；操作反馈显示在右下角，按 `Ctrl+N` 在原界面边框内查看通知历史与详情，并同步写入 Logs。
- 每个 Linux 用户一个 Backend，多个 TUI/CLI 前端安全共享状态。
- 默认 `silent` 模式：普通程序保持直连，只有 `flc COMMAND` 使用带认证的本地代理。
- 订阅 URL 与本地 YAML 文件导入、Profile 原子写入、失败回滚。
- 连接 History 跨 Backend 重启保留，并支持状态、关键字和数量筛选。
- AMD64/ARM64 的 Debian 包与便携压缩包；统一安装脚本自动识别架构。

## 安装

推荐使用统一安装脚本，它会识别 AMD64/ARM64、选择 `.deb` 或便携包，并校验 GitHub 提供的 SHA-256 digest：

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

强制使用便携安装：

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

也可以从 [Releases](https://github.com/yqlay/flclash-tui/releases) 手动下载对应架构的 `.deb` 或 `.tar.gz`。

源码构建：

```bash
git clone --recurse-submodules https://github.com/yqlay/flclash-tui.git
cd flclash-tui
make cli-linux
./dist/flclash
```

## 快速开始

```bash
flclash
```

1. 在 **Profiles** 中选择 `Import subscription URL`，或选择 `Import local YAML file` 导入本地配置。
2. 选中 Profile，按 Enter 激活。
3. 在 **Proxies** 中选择代理组和节点。
4. 默认 silent 模式下直接运行：

```bash
flc curl https://example.com
flc git clone https://github.com/owner/repository.git
```

如需桌面程序使用代理，切换到 `rule`、`global` 或 `direct`，再启用 **System proxy**；无桌面会话的服务器通常应使用 `flc`、应用自身代理设置或 TUN。

## 三个容易混淆的概念

- **Backend**：每用户后台协调进程，负责 Profile 事务、History、Core 生命周期和系统代理修改。
- **Core**：Mihomo 及其流量监听器；Backend 运行不代表 Core 已启动。
- **System proxy**：Linux 桌面代理偏好，不等于代理端口，也不会影响忽略系统代理的程序。

界面中的 **Proxy port** 对应 Mihomo `mixed-port`，同一端口接受 HTTP 和 SOCKS5。普通模式与 silent 模式的认证 FLC listener 共用当前运行端口；silent 下系统代理保持 `DISABLED · locked by silent mode`。

## 常用命令

```bash
flclash                              # 打开 TUI
flclash status
flclash core start|stop|restart
flclash backend status|stop|restart
flclash mode rule|global|direct|silent
flclash port [PORT|off]
flclash sys status|on|off
flclash tun status|user on|system on|off

flclash profile import URL
flclash profile import-file /path/to/config.yaml
flclash profile list

flclash history show --state active --search example --limit 20
flclash history clear
flclash logs --lines 100 --follow
```

完整命令、TUI 快捷键、silent/FLC、TUN、History、日志、数据目录和外部 Core 用法见 [CLI_LINUX.md](CLI_LINUX.md)。程序内帮助以 `flclash --help`、`flclash COMMAND --help` 和 `flc --help` 为准。

## 数据与安全

- 默认数据目录：`~/.config/flclash/`。
- Backend 和 Core 使用当前用户私有的 Unix socket。
- silent 模式的 FLC listener 仅绑定回环地址并使用临时认证信息。
- `.flclash-*-runtime-*.yaml` 是内部运行时文件，不会显示为用户 Profile。
- Logs 不记录订阅 URL、YAML 内容或 FLC 认证信息。

## 致谢

感谢 [FlClash](https://github.com/chen08209/FlClash)、[Mihomo](https://github.com/MetaCubeX/mihomo) 及其贡献者。
