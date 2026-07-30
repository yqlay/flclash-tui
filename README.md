# FlClash CLI

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-cli?style=flat-square)](https://github.com/yqlay/flclash-cli/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-cli/total?style=flat-square)](https://github.com/yqlay/flclash-cli/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-cli?style=flat-square)](LICENSE)

FlClash CLI 是一款面向 Linux 和无头主机的 Clash/Mihomo 代理客户端。它以全屏终端界面（TUI）提供接近图形客户端的使用体验，同时保留适合脚本、SSH 和服务器环境的命令行功能。

本项目基于 [FlClash](https://github.com/chen08209/FlClash) 衍生，复用其 Go/Mihomo 核心，并针对纯终端使用重新设计了配置、订阅、服务控制和状态查看流程。运行 `flclash-cli` 即可进入界面，不要求用户先手写 `config.yaml`。

> 本项目是非官方衍生版本，不是 FlClash 官方发布版本，也不代表 FlClash 或 Mihomo 维护者。版权和许可证说明见 [NOTICE](NOTICE) 与 [LICENSE](LICENSE)。

## 适合哪些场景

- 通过 SSH 管理 Linux 服务器、工作站或 WSL 环境
- 不安装桌面环境，只在终端中管理代理
- 希望在一台主机上运行多个相互隔离的 Mihomo 实例
- 既需要交互式界面，也需要命令行检查、启动和切换节点

## 主要功能

- **全屏 TUI**：固定布局、键盘操作，不会不断向终端追加界面内容
- **订阅与配置管理**：在界面中导入/更新订阅、切换配置、重命名配置和编辑 YAML
- **完整代理控制**：查看代理组和 Provider、切换节点，并对单节点或整组执行延迟与下载速度测试
- **后台 Service**：TUI 与 Core 进程分离；退出界面后代理可以继续运行，再次进入会自动重连
- **常用设置可视化**：支持模式、Mixed Port、TUN、Allow LAN、IPv6、日志等级、Unified Delay 和 TCP Concurrent
- **状态与诊断**：显示公网/内网 IP、实时流量、连接、请求、日志、系统内存、本进程和 Core 内存
- **状态持久化**：保存当前 Profile、代理组选择及配置设置，下次进入时自动恢复
- **安全更新**：从本仓库的 GitHub Releases 检查更新，校验 SHA-256 后再安装
- **脚本化命令**：支持前台运行、配置校验、查询代理组和切换节点

界面包含以下栏目：

| 栏目 | 用途 |
| --- | --- |
| Dashboard | 服务、系统代理、TUN、模式、端口、网络、延迟、下载速度和内存状态 |
| Proxies | 代理组、节点、Provider、节点切换、延迟测试和下载速度测试 |
| Profiles | 导入/更新订阅、激活/重命名配置、编辑 YAML |
| Requests | 查看本次运行期间的活动请求和近期请求 |
| Connections | 查看和关闭当前连接 |
| Logs | 查看、清空和导出 Core 日志 |
| Tools | 完整设置、备份恢复、Geo 数据库和版本检查 |

## 安装

目前 Release 提供 Debian/Ubuntu 的 `amd64` 和 `arm64` 安装包。

AMD64：

```bash
wget https://github.com/yqlay/flclash-cli/releases/download/v0.3.10/flclash-cli_0.3.10_amd64.deb
wget https://github.com/yqlay/flclash-cli/releases/download/v0.3.10/flclash-cli_0.3.10_amd64.deb.sha256
sha256sum -c flclash-cli_0.3.10_amd64.deb.sha256
sudo dpkg -i flclash-cli_0.3.10_amd64.deb
```

ARM64：

```bash
wget https://github.com/yqlay/flclash-cli/releases/download/v0.3.10/flclash-cli_0.3.10_arm64.deb
wget https://github.com/yqlay/flclash-cli/releases/download/v0.3.10/flclash-cli_0.3.10_arm64.deb.sha256
sha256sum -c flclash-cli_0.3.10_arm64.deb.sha256
sudo dpkg -i flclash-cli_0.3.10_arm64.deb
```

查看本机 CPU 架构：

```bash
dpkg --print-architecture
```

输出为 `amd64` 或 `arm64` 时，选择名称中架构一致的安装包。其他发行版
可以从源码构建。

## 快速开始

安装后直接运行：

```bash
flclash-cli
```

首次启动会自动创建一个安全的 DIRECT 配置，但不会启动代理服务，也不会占用 Mixed Port。安装包已经包含 FlClash 自带的 `GEOIP.metadb`、`GEOIP.dat`、`GEOSITE.dat` 和 `ASN.mmdb`；缺失或损坏的基础 Geo 数据会在 Core 初始化前从本地安装包恢复，不依赖首次访问 GitHub。推荐的使用顺序是：

1. 进入 **Profiles**。
2. 选择 **Import subscription URL**，粘贴订阅链接并按 Enter。
3. 选中下载完成的 Profile 并激活。
4. 在 **Proxies** 中选择需要的节点。
5. 在 **Dashboard** 中确认模式和端口，然后将 **System proxy** 切换为 ON。

开启 **System proxy** 时，程序会自动应用当前设置、启动 Service/Core，再设置桌面系统代理，不需要手动先启动核心。停止 Service 时，由当前 TUI 开启的系统代理也会关闭。

`q` 只退出 TUI 并返回 Shell；如果 Service 已启动，代理会继续在后台运行且不占用终端。再次执行 `flclash-cli` 会自动连接该 Service。`Ctrl+C` 则退出 TUI 并停止由 FlClash CLI 管理的 Service/Core。也可以在 Shell 中执行 `flclash-cli stop`。

导入成功后，程序会安全保存该 Profile 与订阅 URL 的绑定。在 **Profiles** 中选中它并按 `U`，程序会直接从已保存的 URL 重新拉取配置，不会再次要求输入链接。活动 Profile 会立即热重载，Service 运行时不会停止监听端口；更新会保留端口、模式、TUN、IPv6 等本地设置，验证或热重载失败时自动回滚。旧版本留下的未绑定 Profile 会被明确标记为本地配置，可用 `flclash-cli profile link --config PROFILE URL` 一次性建立绑定。

## 基本操作

```text
← 聚焦侧栏       → 打开栏目并聚焦内容       Tab 切换焦点
↑↓/ws             移动选择                  Enter 打开/执行
1～7             快速打开对应栏目           r 刷新
[/]              切换代理组/Provider        Esc 返回代理组
d                Dashboard 测当前路由延迟；Proxies 测全组/单节点延迟
v                Dashboard 测当前路由速度；Proxies 测全组/单节点速度
S                开关系统代理               c 启动/停止 Service
U                刷新已绑定订阅             n 导入新订阅
q                仅退出 TUI                 Ctrl+C 停止 Service 并退出
```

所有主要功能都可以通过界面中的可选行完成，快捷键只是辅助操作，不要求记忆。

下载速度测试请求最多 `100 MB` 数据并持续最多 `5 秒`。若在 5 秒内完成，
结果按 `100 MB / 实际耗时` 计算；否则按 `实际下载量 / 5 秒` 计算。整组测速
会逐个节点串行执行，避免节点互相争抢带宽；最大流量约为节点数乘以 100 MB。

## 系统代理与 TUN

- **System proxy**：设置桌面环境的 HTTP/HTTPS 系统代理，当前主要支持 GNOME 和 MATE 的 `gsettings`。
- **TUN**：在网络层接管更多流量，适合不读取系统代理设置的应用；Linux 下可能需要额外网络权限。
- **Global 模式**：决定被代理流量统一走所选节点，不等同于自动接管系统全部流量。

`ping` 使用 ICMP，不经过 HTTP/SOCKS 系统代理，因此不能用 `ping google.com` 判断本工具的代理是否可用。可以使用：

```bash
curl -I --max-time 10 https://www.google.com
curl -x http://127.0.0.1:7890 -I --max-time 10 https://www.google.com
```

第二条命令中的端口应替换为界面里显示的 Mixed Port。

## 命令行模式

打开 TUI（默认行为）：

```bash
flclash-cli
flclash-cli tui --directory ~/.config/flclash-work
flclash-cli tui --config /path/to/config.yaml
```

以前台进程运行 Core：

```bash
flclash-cli run --config /path/to/config.yaml
```

检查配置：

```bash
flclash-cli check --config /path/to/config.yaml
```

连接已有的 Mihomo Controller：

```bash
flclash-cli proxy list --controller 127.0.0.1:9090
flclash-cli proxy select --controller 127.0.0.1:9090 GROUP NODE
```

停止由 TUI 留在后台的 Service：

```bash
flclash-cli stop
```

为旧版本的本地 Profile 一次性绑定订阅来源：

```bash
flclash-cli profile link --config ~/.config/flclash/profile.yaml 'https://example.com/subscription'
```

一台主机运行多个实例时，应为每个实例指定不同的数据目录、Mixed Port 和 Controller：

```bash
flclash-cli tui --directory ~/.config/flclash-a
flclash-cli tui --directory ~/.config/flclash-b
```

更完整的参数和操作说明见 [CLI_LINUX.md](CLI_LINUX.md)。

## 更新

仅检查是否有新版本：

```bash
flclash-cli update --check
```

下载、校验并安装新版本：

```bash
flclash-cli update
```

更新器只接受本仓库 GitHub Releases 中与当前 CPU 架构匹配的 Debian 包，并在安装前验证 SHA-256。

> **如果当前版本使用正常，请勿轻易更新。**

## 从源码构建

需要 Go、Git、Make，并需要初始化 Mihomo 子模块：

```bash
git clone --recurse-submodules https://github.com/yqlay/flclash-cli.git
cd flclash-cli
make cli-linux
./dist/flclash-cli
```

构建产物位于 `dist/flclash-cli`，离线 Geo 数据位于 `dist/data/`，移动二进制时需将该目录一起移动。本仓库保留了上游 FlClash Flutter 工程，因为 CLI 复用了其中的核心集成和平台代码；构建纯 CLI 不需要启动 Flutter 图形界面。

## 数据位置

默认数据目录为：

```text
~/.config/flclash/
```

其中保存配置文件、下载的 Profile、运行状态、日志和备份。也可以使用 `--directory` 或 `--config` 指定其他位置。多实例运行时，请勿让不同实例共用相同的数据目录和监听端口。

## 项目关系与许可

- 上游图形客户端：[chen08209/FlClash](https://github.com/chen08209/FlClash)
- Mihomo/Clash.Meta Core：以 Git 子模块形式保留在 `core/Clash.Meta`
- TUI 交互层参考并改编自 [SaladDay/cc-switch-cli](https://github.com/SaladDay/cc-switch-cli)
- 本项目遵循 [GNU General Public License v3.0](LICENSE)

各上游项目及第三方组件仍归原作者所有，具体归属、许可证和免责声明请查看 [NOTICE](NOTICE)。
