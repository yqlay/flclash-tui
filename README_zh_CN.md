# FlClash TUI

[**English documentation**](README.md)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

FlClash TUI 是面向 Linux 的 Mihomo/Clash 终端代理管理器。它同时提供适合日常交互的全屏 TUI，以及适合 SSH、无头服务器和脚本的 CLI。运行 `flclash` 打开 TUI；运行 `flclash COMMAND` 执行管理操作；把 `flc` 放在外部命令前，则只有该命令通过 FlClash。

本项目是 [FlClash](https://github.com/chen08209/FlClash) 的非官方衍生版本，复用了 FlClash 的 Go/Mihomo 集成，但重新设计了终端专用的 Backend、生命周期、命令与界面。它不是 FlClash 或 Mihomo 的官方发行版。版权与许可证见 [NOTICE](NOTICE) 和 [LICENSE](LICENSE)。

## 功能亮点

- 全屏键盘 TUI，包含 Dashboard、Proxies、Profiles、History、Connections、Logs、Settings 与 Maintenance 八个页面。
- CLI 命令与 Dashboard 控件对应，并在适合自动化的命令中提供 JSON/watch 输出。
- 默认 `silent` 模式：普通程序保持直连，只有 `flc 命令` 获得带认证的私有代理入口。
- 每个 Linux UID 只有一个后台 Backend，多个 TUI/CLI 前端安全共享，前端不会竞相改写 YAML。
- 多个用户的用户范围 TUN 可以共存；整机 TUN 需要管理员授权，并且全系统唯一。
- Profile 事务修改、订阅更新、带自动备用端口的热切换，以及重载失败回滚。
- 提供 AMD64 与 ARM64 Linux 原生 Debian 包和便携压缩包。

## 最重要的四种状态

Dashboard 有意将下列状态分成独立选项：

| 状态 | 实际控制内容 | 不代表什么 |
| --- | --- | --- |
| **Backend** | 每用户一个的后台协调进程、私有 IPC、配置事务、共享 History、Core 生命周期和系统代理修改 | Backend 正在运行，不代表代理监听端口已经启动 |
| **Core** | 由 FlClash 管理的 Mihomo 执行器及其监听器 | 启动 Core 不会自动修改桌面系统代理 |
| **System proxy（系统代理）** | 目前通过 GNOME/MATE `gsettings` 设置 Linux 桌面 HTTP/HTTPS 代理 | 不读取系统代理的应用不会因此被接管 |
| **TUN** | 显式开启的用户范围或整机范围网络接管 | Debian 包安装受限 root 辅助服务；Backend/Core 仍以用户 UID 运行 |

因此，`flclash core stop` 只停止 Core 监听器，Backend 仍可继续服务 CLI/TUI。`flclash backend stop`、`flclash shutdown` 或托管 TUI 中的 `Ctrl+C` 才会停止 Backend 和 Core，并断开所有前端。

### Proxy port 与 Mihomo `mixed-port`

界面统一称它为 **Proxy port（代理端口）**。在 Mihomo YAML 中，对应键名是 `mixed-port`，因为同一个 TCP 端口同时接受 HTTP 代理和 SOCKS5 代理协议；“mixed”指混合支持两种协议，并不是流量混乱或随机。例如 Proxy port 为 `7891` 时，HTTP/HTTPS 应用可使用 `http://127.0.0.1:7891`，SOCKS5 应用可使用 `socks5://127.0.0.1:7891`。

## 架构与写入边界

托管模式下，每个 Linux 用户只运行一个 Backend，但可以连接任意数量的 CLI/TUI 前端：

```text
TUI 各页面（含 Dashboard） ─┐
                             ├─ 带 revision 的请求 ─▶ Backend ─▶ 私有 Core socket ─▶ Mihomo
flclash CLI 命令 ───────────┘                              ├─ YAML/Profile 原子写入
                                                               ├─ Linux 系统代理
                                                               └─ 共享 History 与状态
```

Dashboard 只是 TUI 的一个页面，不是另一个守护进程。“所有前端都通过 Backend 修改状态”的准确含义是：CLI 和 TUI 各页面把修改请求发到仅当前用户可访问的 Unix socket；Backend 校验预期 revision 和配置摘要，执行原子写入或系统操作，通知 Core 重载，并在失败时回滚。前端可以读取、展示状态和收集输入，但不能自行覆盖共享 YAML，也不能直接设置系统代理。这样两个终端就不会悄悄互相覆盖修改。

显式连接外部 Core 是高级例外：`flclash tui --no-start --controller ...` 或带 `--controller` 的 `flclash proxy` 可以查看或选择那个外部 Core 的节点，但不拥有 FlClash 共享 YAML、Backend 生命周期或系统代理。

Backend 与托管 Core 默认使用私有 Unix socket，不需要开放 TCP Controller。Backend socket 权限为 `0600`；状态修改包含单调递增的 revision 和请求重试 ID；Profile 编辑还会使用内容摘要与逐 Profile 文件锁。

每个 UID 拥有独立的 Backend、Core、TUI 客户端、运行时配置和连接历史。受管 listener 只监听回环地址，并拒绝 Linux socket 所属 UID 与 Backend 不一致的客户端。配置的 Proxy port 被占用时，Backend 会选择空闲运行端口且不覆盖首选值；两者不同时，状态和 TUI 会同时显示。

TUN 分为两种作用域：`tun user on` 只捕获当前 UID，可与其他用户的用户 TUN 并存；`tun system on` 需要 Polkit 管理员授权，捕获所有用户，并与全部其他 TUN 租约互斥。辅助服务创建设备和策略路由，将文件描述符交给非特权 Backend，并在 Backend 断开后清理。用户 TUN 会在 Backend 重启后恢复；系统 TUN 必须重新授权。

## 安装

### Debian / Ubuntu 安装包

在 [GitHub Releases](https://github.com/yqlay/flclash-tui/releases) 中选择与 `dpkg --print-architecture` 一致的包。0.5.2 的包名为：

```text
flclash-tui_0.5.2_amd64.deb
flclash-tui_0.5.2_arm64.deb
```

AMD64 示例：

```bash
wget https://github.com/yqlay/flclash-tui/releases/download/v0.5.2/flclash-tui_0.5.2_amd64.deb
wget https://github.com/yqlay/flclash-tui/releases/download/v0.5.2/flclash-tui_0.5.2_amd64.deb.sha256
sha256sum -c flclash-tui_0.5.2_amd64.deb.sha256
sudo dpkg -i flclash-tui_0.5.2_amd64.deb
```

安装包提供 `/usr/bin/flclash`、`/usr/bin/flc`、文档以及内置 GeoIP/GeoSite/ASN 数据。Core 初始化前可以从本地安装资源恢复缺失或不可用的 Geo 文件，首次启动不依赖从 GitHub 下载这些基础文件。

### 便携压缩包

Release 同时提供 AMD64 与 ARM64 的 `tar.gz`。便携包不会安装特权 TUN helper；需要用户/整机 TUN 时应使用 Debian 包。AMD64 示例：

```bash
wget https://github.com/yqlay/flclash-tui/releases/download/v0.5.2/flclash-tui_0.5.2_amd64.tar.gz
wget https://github.com/yqlay/flclash-tui/releases/download/v0.5.2/flclash-tui_0.5.2_amd64.tar.gz.sha256
sha256sum -c flclash-tui_0.5.2_amd64.tar.gz.sha256
tar -xzf flclash-tui_0.5.2_amd64.tar.gz
cd flclash-tui_0.5.2_amd64
./flclash
```

请保持内置 `data/` 目录与 `flclash` 位于同一目录。压缩包中的 `flc` 是指向同一可执行文件的符号链接。

### 从源码构建

```bash
git clone --recurse-submodules https://github.com/yqlay/flclash-tui.git
cd flclash-tui
make cli-linux
./dist/flclash
```

构建结果为 `dist/flclash`，`dist/flc` 是指向同一二进制的符号链接，Geo 资源位于 `dist/data/`。移动源码构建的程序时应一并携带 `data/`。终端版本需要 Go、Git、Make 和 Mihomo 子模块，不需要启动 Flutter 图形界面。

## 快速开始

```bash
flclash
```

使用默认数据目录且尚无配置时，Backend 会创建一份最小的 DIRECT 配置。Core 初始保持停止，因此不会立即占用 Proxy port。

1. 打开 **Profiles**，选择 **Import subscription URL**，粘贴订阅地址并按 Enter。
2. 选中下载完成的 Profile，按 Enter 激活。
3. 打开 **Proxies**，进入代理组并选择节点。
4. 默认处于 `silent`；如需普通代理入口，再选择 `rule`、`global` 或 `direct`。
5. silent 下首次运行 `flc 命令` 时会自动选择第一个可用代理组并启动 **Core**；仍可手动改选 FLC outbound。需要桌面应用使用普通代理端口时，先退出 silent，再开启 **System proxy**。

无头服务器上通常没有可用的桌面 `gsettings` 会话，应使用 `flc`、应用自身的代理设置或 TUN，而不是期待“系统代理”影响远程 Shell。

### Dashboard 命令对照

CLI 的短命令有意与 TUI 中显示的控制项对应：

| TUI 状态或设置 | 查看 | 修改 |
| --- | --- | --- |
| Backend | `flclash backend status` | `flclash backend start\|stop\|restart` |
| Core | `flclash core status` | `flclash core start\|stop\|restart` |
| System proxy | `flclash sys status` | `flclash sys on\|off` |
| TUN | `flclash tun status` | `flclash tun user on\|off` 或 `flclash tun system on\|off` |
| Mode | `flclash mode` | `flclash mode rule\|global\|direct\|silent` |
| Proxy port | `flclash port` | `flclash port PORT\|off` |
| FLC outbound | `flclash flc status` | `flclash flc select NAME` |

Backend 是每用户管理进程，Core 是由它控制的 Mihomo 进程。系统代理只修改受支持的 Linux 桌面代理偏好；TUN 则在网络层接管流量。它们是有意分开的开关。

## 流量模式与 `flc`

### 原生模式

- `rule`：已经进入 Mihomo 的流量按照活动 Profile 规则处理。
- `global`：已经进入 Mihomo 的流量使用 Mihomo 全局选择。
- `direct`：已经进入 Mihomo 的流量直接连接。

这些模式只决定**流量进入 Mihomo 后如何处理**，不会单独把整台机器的所有进程自动送入代理。流量入口仍需使用系统代理、显式 Proxy port、`flc` 或 TUN。

### 静默模式（silent）

`silent` 是默认模式，也是 FlClash 的运行时模式，不是写入 Mihomo YAML 的原生模式。其含义是：**不主动接管当前用户的网络连接；普通程序保持直连，只有以 `flc` 开头的命令才获得代理环境**。

进入 silent 后，Backend 会生成一份临时运行时覆盖配置：

- 关闭普通 Proxy port，以及常规 HTTP/SOCKS/redir/tproxy 监听；
- 在运行副本中关闭系统代理、TUN、LAN、DNS 监听、外部 Controller、用户 listeners、tunnels 和服务端监听；
- 配置 FLC outbound 后，仅创建一个随机的、只绑定回环地址的 `flc` mixed listener；未配置时不创建任何流量入口；
- 使用每次运行随机生成的认证信息保护该 listener；
- 让它直接使用选定的 FLC outbound（节点或代理组）；
- 共享 Profile YAML 保持逐字节不变，退出时删除临时覆盖文件。

首次运行 `flc 命令` 时会自动选择第一个可用代理组、创建私有 listener 并启动 Core。只有需要覆盖自动选择时才手动指定出口：

```bash
flclash flc select PROXY  # 可选：覆盖自动选择
flc curl https://example.com
```

`flc COMMAND [ARG...]` 只为其子进程设置大小写形式的 `HTTP_PROXY`、`HTTPS_PROXY` 和 `ALL_PROXY`。silent 模式使用 Backend 提供的带认证私有 URL；`rule`、`global`、`direct` 模式则使用普通 Proxy port。它会确认入口可连接；Backend、Core 或对应 listener 不可用时会**拒绝执行**，绝不会悄悄改成直连运行命令。

```bash
flc curl https://example.com
flc wget https://example.com/file
flc git clone https://github.com/owner/repository.git
flc sh -c 'curl -s https://example.com | jq .'
```

管道和重定向需要像最后一条示例一样显式调用 Shell。子程序必须支持标准代理环境变量；若程序完全忽略这些变量，`flc` 无法强行接管它。GNU Wget 会附加 `--no-config`，避免 `~/.wgetrc` 中的旧代理覆盖实时入口。

`sudo` 通常会清理代理环境变量。如果提权后的子进程必须继续使用 `flc` 入口，且本机 sudo 策略允许保留环境，应把 `sudo -E` 放在 **`flc` 后面**：

```bash
flc sudo -E cc-switch update
```

不要运行 `sudo flc ...`：那会先改变有效用户，使 `flc` 连接 root 自己的 Backend，而不是当前用户的 Backend。即使使用 `-E`，目标程序仍需支持代理环境变量，更严格的 sudo 策略也可能拒绝或继续删除这些变量。

管理命令：

```bash
flclash flc status
flclash flc select 'Proxy Group'
flclash flc test
flclash flc env
```

`flclash flc test` 会真正通过当前命令代理发起 HTTP 请求，而不只是测试端口。`flclash flc env`（以及 `flclash env`）打印可供 Shell 使用的 export；silent 模式下其中包含临时认证信息，不要把输出写入日志或永久 Shell 配置。

Proxy port 在线修改由 Backend 统一执行。配置值作为首选端口；若已被占用，Backend 会选择空闲运行端口。重载后确认新回环 listener 可连接且旧端口已关闭，再把桌面系统代理更新到实际端口。任何一步失败都会恢复原 Profile、Core listener 和受管系统代理；listener 重建期间仍可能有很短的切换间隙。

## 命令详解

程序自身的最新帮助始终可通过 `flclash --help`、`flclash COMMAND --help` 和 `flc --help` 查看。

### Dashboard 与生命周期命令

| 命令 | 含义 |
| --- | --- |
| `flclash` 或 `flclash tui` | 打开一个 TUI 前端；启动/复用 Backend，但不擅自改变 Core 当前运行状态 |
| `flclash core [status]` | 输出 `RUNNING` 或 `STOPPED` |
| `flclash core start` | 启动 Core listeners；Backend 不存在时先启动 Backend |
| `flclash core stop` | 停止 Core 和由 Backend 管理的系统代理，保留 Backend |
| `flclash core restart` | 不替换 Backend，只重启 Core |
| `flclash core reload [--config PATH]` | 校验并重载/切换活动 Profile |
| `flclash backend [status]` | 查看 Backend 和 Core 状态 |
| `flclash backend start` | 启动后台 Backend，不要求同时启动 Core listeners |
| `flclash backend restart` | 替换 Backend，并保留 Core 原先是否运行的状态 |
| `flclash backend stop` | 优雅停止 Backend 与 Core，并断开所有前端 |
| `flclash backend logs` | 读取 Backend 日志，参数与 `logs` 相同 |
| `flclash backend clients` | 列出已连接 TUI 的 PID、TTY 和启动时间 |
| `flclash shutdown` | `flclash backend stop` 的别名 |
| `flclash status [--json] [--watch]` | 显示 Backend PID/版本/revision、Core、Profile、模式、Proxy port、FLC、系统代理和前端数 |
| `flclash logs [--lines N] [--follow]` | 查看或跟随后台 Backend 日志 |

顶层 `start`、`stop`、`restart`、`reload` 保留为对应 Core 操作的兼容快捷命令。

### 与 Dashboard 选项对应的命令

| 命令 | 含义 |
| --- | --- |
| `flclash sys [status]` | 查看 Backend 管理的 Linux 系统代理状态 |
| `flclash sys on` | 必要时启动 Core，然后开启系统代理；silent 模式拒绝该操作 |
| `flclash sys off` | 关闭系统代理，但不停止 Core |
| `flclash tun [status\|on\|off]` | 查看或切换当前用户范围 TUN |
| `flclash tun user on\|off` | 显式控制当前 UID 的流量接管 |
| `flclash tun system on\|off` | 控制整机唯一 TUN；开启时需要 Polkit 授权 |
| `flclash mode` | 输出当前 FlClash/原生流量模式 |
| `flclash mode rule\|global\|direct\|silent` | 切换模式；silent 使用运行时覆盖配置 |
| `flclash port` | 输出配置的 Proxy port；silent 下显示监听关闭，但保留配置值 |
| `flclash port PORT` | 将 Mihomo `mixed-port` 设置为 `1..65535` |
| `flclash port off` | 将普通 Proxy port 设置为 `0` |
| `flclash flc status\|select NAME\|test\|env` | 管理只供命令使用的代理入口 |
| `flclash net show` 或 `net refresh` | 检测公网 IP、国家/地区、内网地址和当前检测路线（DIRECT 或 Proxy port） |
| `flclash net delay` | 测试普通 Proxy-port 路线延迟 |
| `flclash net speed` | 通过普通路线最多下载 100 MB、最多持续 5 秒 |

silent 模式应使用 `flclash flc test`；普通路线的 `net delay` 和 `net speed` 会被有意禁用。

### Profile 与配置

| 命令 | 含义 |
| --- | --- |
| `flclash profile list [--json]` | 列出 `.yaml`/`.yml` Profile；`*` 表示活动项，并标注 local/subscription |
| `flclash profile current` | 输出活动 Profile 路径 |
| `flclash profile import URL` | 通过 Backend 下载、校验并创建已绑定订阅的 Profile |
| `flclash profile use NAME` | 根据名称/路径校验并激活 Profile |
| `flclash profile update [NAME]` | 使用保存的订阅 URL 更新，同时保留本地设置 |
| `flclash profile rename NAME NEW_NAME` | 安全重命名非活动 Profile |
| `flclash profile edit [NAME]` | 用 `$VISUAL`/`$EDITOR` 编辑临时副本，校验后再提交 |
| `flclash profile delete NAME` | 删除非活动 Profile |
| `flclash profile link --config PATH URL` | 为旧版留下的本地 Profile 绑定订阅源 |
| `flclash config path\|show\|validate` | 查看活动 Profile |
| `flclash config edit` | 以事务方式编辑并热重载活动 Profile |
| `flclash config backup` | 通过 Backend 创建带时间戳的备份 |
| `flclash config restore` | 恢复最新有效备份并重载 Core |
| `flclash check --config PATH` | 不启动 Core，仅校验指定 YAML |

Profile 创建、编辑、订阅更新、重命名、删除、备份、恢复、活动项切换、本地设置和代理节点选择都使用 Backend 事务。revision 或内容摘要过期时会拒绝操作，而不是覆盖另一个前端的新修改。

### 代理、History 与活动连接

| 命令 | 含义 |
| --- | --- |
| `flclash proxy groups [--json]` | 列出可选代理组及其当前节点 |
| `flclash proxy nodes GROUP [--json]` | 列出一个组中的节点 |
| `flclash proxy select GROUP NODE` | 校验节点属于该组，经 Backend 选择并保存；保存失败时回滚 Core |
| `flclash proxy delay NODE [--test-url URL]` | 测试单节点延迟 |
| `flclash proxy speed NODE` | 执行 Backend 管理的 100 MB/5 秒下载测试 |
| `flclash history show [--follow] [--json]` | 显示或持续跟随共享连接历史 |
| `flclash history clear` | 只清空 History，不关闭 Core 活动连接 |
| `flclash connections show [--json]` | 显示 Core 当前打开的连接 |
| `flclash connections close ID` | 关闭指定活动连接 |
| `flclash connections close all` | 关闭全部活动连接 |

**History 不是 HTTP 请求正文记录器。** Backend 会定期读取 Mihomo 当前连接，在连接首次出现时记录它，并保存活动与近期结束的记录（最多 500 条）；显示内容包括目标主机/地址、网络类型、代理链和活动状态。所有 TUI/CLI 共享同一份 History；即使没有 TUI 连接，只要 Backend 运行就会继续收集。清空 History 不会关闭 Connections；关闭 Connections 也不会删除 History。

显式操作外部 Controller 时，代理查看/选择命令可使用 `--controller ADDRESS` 和可选的 `--secret SECRET`。托管模式使用 Core 私有 Unix socket，不需要启用 TCP Controller。

### 诊断、Shell 集成与高级命令

| 命令 | 含义 |
| --- | --- |
| `flclash geo status\|update` | 检查内置 Geo 资源或请求 Core 更新 |
| `flclash env [--json]` | 输出当前普通/私有代理入口的环境变量 |
| `flclash doctor [--json]` | 检查 Backend 协议、Core API、Profile 有效性与代理入口 |
| `flclash completion bash\|zsh\|fish` | 生成顶层命令和子命令补全定义 |
| `flclash update --check` | 只检查可信 GitHub Release，不安装 |
| `flclash update [--yes] [--download-only]` | 下载并验证校验和/包元数据，可选择安装 Debian 更新 |
| `flclash run [--config PATH]` | 不使用共享 Backend 的前台 Core 高级模式；`Ctrl+C` 停止，`SIGHUP` 重载 |
| `flclash version` | 输出 CLI 版本 |

Shell 补全安装示例：

```bash
flclash completion bash > ~/.local/share/bash-completion/completions/flclash
flclash completion zsh > ~/.zfunc/_flclash
flclash completion fish > ~/.config/fish/completions/flclash.fish
```

仍保留以下兼容写法：`service` 对应 `backend`，`system-proxy` 对应 `sys`，`outbound-mode` 对应 `mode`，`mixed-port` 对应 `port`，`requests` 对应 `history`，`connections close-all` 对应 `connections close all`，`flclash exec -- COMMAND` 对应 `flc COMMAND`。新文档统一使用较短的主命令。

## TUI 界面说明

### 八个页面

| 快捷键 | 页面 | 内容及 Enter 操作 |
| --- | --- | --- |
| `1` | **Dashboard** | Core、系统代理、TUN、Mode、Proxy port、网络检测、路线测试、内存、流量、History 数量、前端和活动 Profile |
| `2` | **Proxies** | 代理组、节点、Provider、已保存选择、延迟和串行下载测速；Enter 进入/选择/更新 |
| `3` | **Profiles** | 导入订阅、激活、更新、重命名和编辑 Profile |
| `4` | **History** | 共享的活动/近期连接历史；`x` 清空 History，但不关闭连接 |
| `5` | **Connections** | Core 当前连接；`d` 关闭选中项，`x` 关闭全部 |
| `6` | **Logs** | 捕获的 Core 事件；`e` 导出，`x` 清空当前显示缓存 |
| `7` | **Settings** | Mode、FLC outbound、Proxy port、LAN、IPv6、Unified delay、TCP concurrent、日志等级、TUN、Core 和系统代理 |
| `8` | **Maintenance** | 编辑当前 YAML、备份/恢复、更新 Geo、重置流量统计和检查版本 |

每一行先显示当前状态，再显示可执行动作。例如 `Core STOPPED · Enter to start` 表示 Core 现在处于停止状态，并不是按 Enter 后会变成 STOPPED。
在 Dashboard 或 Settings 选中 **Mode** 后，会先打开包含 `rule`、`silent`、`global`、`direct` 的列表；使用 ↑/↓ 或 `w`/`s` 移动，再按 Enter 应用高亮模式。

### 导航与快捷键

```text
← / Esc       聚焦/打开导航                 → / Enter    打开内容或执行选中行
↑↓ 或 w/s    移动选择                      Tab/Shift-Tab 切换侧栏/内容焦点
1..8          直接打开页面                  ?            显示/关闭界内快捷键帮助
r             刷新显示状态                  R            重载活动配置
PgUp/PgDn     滚动紧凑 Dashboard            [ / ]        切换 Groups/Providers

Dashboard:    d 路线延迟 · v 路线测速 · n 刷新网络检测
Proxies:      Enter 进入/选择 · Esc 返回组 · d 节点/整组延迟 · v 测速 · A 整组延迟
Profiles:     Enter 激活/导入 · n 导入 · U 更新已绑定订阅 · F2/u 重命名 · e 编辑
History:      x 清空共享 History
Connections: d 关闭选中连接 · x 关闭全部
Logs:        e 导出 · x 清空显示
Settings:    S 系统代理 · c Core · t TUN · m 模式列表 · p 设置端口 · +/- 调整
Maintenance: b 备份 · B 恢复 · g 更新 Geo · z 重置流量

q             只分离当前 TUI；Backend/Core 和其他前端继续运行
Ctrl+C        托管模式下优雅停止 Backend 与 Core，然后断开所有前端
```

网络测试和 Profile 操作在输入/渲染循环之外执行。整组测速会串行测试节点，避免互相争抢带宽；单次测试最多读取 100 MB，最长持续 5 秒。

界面会根据终端大小自适应。正常尺寸同时显示侧栏和内容；窄终端切换成全宽导航/内容，Dashboard 可滚动。完整功能在 `40x10` 仍可操作；更小终端会显示所需尺寸，而不会绘制错乱边框。

### 多前端与优雅关闭

在另一个终端再次运行 `flclash` 会连接同一个 Backend。Dashboard 显示前端数量，`flclash backend clients` 可列出会话。`q` 只注销并分离当前 TUI。`Ctrl+C` 会等待 Backend 返回 shutdown ACK，关闭受管系统代理和 Core，再关闭 Backend，并让其他前端退出。子进程会被 `Wait` 回收，正常分离、重启和关闭不会留下 Backend 僵尸进程。

显式外部模式（`--no-start`）不拥有外部 Core；此时 `Ctrl+C` 只能退出该前端，不能杀死无关进程。

## Profile、数据与安全

默认数据目录：

```text
~/.config/flclash/
```

其中保存 Profile、活动状态、日志、备份、Core 私有 socket 和工作数据。Backend 管理 socket 与每用户所有权锁通常位于 `/run/user/<UID>/flclash/`；无用户运行目录时使用带 UID 的安全临时目录。`--directory` 和 `--config` 用于选择数据/Profile，但不能绕过“每用户一个 Backend”的限制。

关键行为：

- 共享 Profile 和状态采用原子写入，状态文件仅当前用户可访问；
- 活动 Profile 修改会先校验，再热重载；
- 写入/重载失败时尽可能恢复原文件与 Core 状态；
- 订阅更新保留本地端口、模式、TUN、LAN、IPv6、Unified delay、TCP concurrent 和日志等级；
- Backend 只接受管理目录内的 Profile，并拒绝符号链接或非普通文件作为事务目标；
- silent 私有 listener 仅绑定回环地址、需要认证、参数随机，离开 silent 后不存在；
- 普通 status 输出不会泄露 FLC 私有 URL。

## 常见问题

- **Backend RUNNING、Core STOPPED：**这是正常状态。运行 `flclash core start`；Backend 可以在不占用 Proxy port 的情况下继续服务管理命令。
- **`sys on` 不影响 Shell/服务器程序：**系统代理是桌面偏好。使用 `flc COMMAND`、为应用显式设置 Proxy port，或使用 TUN。
- **`ping` 仍然失败：**ICMP 不读取 HTTP/SOCKS 代理变量。使用 `flclash flc test`、`flclash net delay` 或 `curl` 测试。
- **silent 拒绝系统代理/TUN/普通测速：**这是设计要求；silent 仅提供私有 `flc` 路径。使用 `flclash flc test`。
- **`flc` 拒绝执行：**它采用失败关闭。检查 `flclash status`、`flclash flc status`、FLC outbound 与 `flclash doctor`。
- **修改提示 revision/content conflict：**另一前端已改变状态。刷新（TUI 按 `r` 或重新运行命令）后再操作。
- **不同 `--directory` 被拒绝：**每用户只能有一个托管 Backend。确需切换时先停止当前 Backend。
- **TUN helper 不可用：**安装 `.deb` 并检查 `systemctl status flclash-tun-helper`；便携 tar 包不会安装特权组件。
- **TUN 无法启动：**确认存在 `/dev/net/tun`、cgroup v2、`iproute2` 与 `iptables`，并确认没有冲突的系统 TUN 租约。
- **HTTPS 为何使用 `http://` 代理 URL：**这是正常现象；HTTPS 客户端通过 HTTP `CONNECT` 使用 mixed Proxy port。

## 更新

```bash
flclash update --check
flclash update
```

更新器从 `yqlay/flclash-tui` 读取 Release，选择当前架构 Debian 包，验证 SHA-256 以及包内名称、版本、架构，再调用 `sudo dpkg -i`。`--download-only` 只下载并验证，`--yes` 用于非交互确认。

> 当前版本使用正常时，请勿轻易更新。

## 项目关系与许可

- 原始图形客户端：[chen08209/FlClash](https://github.com/chen08209/FlClash)
- Mihomo/Clash.Meta Core：保留在 `core/Clash.Meta` 子模块
- TUI 交互层包含基于 [SaladDay/cc-switch-cli](https://github.com/SaladDay/cc-switch-cli) 改编的工作
- 许可证：[GNU General Public License v3.0](LICENSE)

第三方组件仍归原作者所有，归属与免责声明见 [NOTICE](NOTICE)。
