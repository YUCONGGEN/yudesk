# Unix 原生无标题栏窗口

## 集成契约

`yudesk-window` 与 Go 主程序放在同一目录。macOS 使用 AppKit / WKWebView；Linux 使用 GTK 3 / WebKitGTK 4.1。两者均使用系统维护的 WebKit，不下载或内嵌第三方浏览器，不依赖 Chromium 应用窗口裁剪标题栏。

macOS `.app` 的 `CFBundleExecutable` 必须是 **`yudesk-launcher`**，不能直接指向没有 AppKit 事件循环的 Go `yudesk`。Finder 重新打开正在运行的 `.app` 会发送 reopen AppleEvent，不能假设它重新执行 Go 的 singleton 代码。新增 launcher 没有窗口、网络或额外 Dock 图标，持有一个 Go 主进程；reopen 启动一个短暂同目录 Go secondary，让既有 singleton 协议恢复窗口。secondary 同时最多一个、限时清理；主 Go 退出时 launcher 退出。Go、helper、安装标记的位置均不变。这里的“单实例”指唯一 Go 主核心，不是整个操作系统只有一个进程。

只有匿名 stdin / stdout 管道用于命令与事件。访问令牌不放在 argv、环境变量或诊断文本中。每条消息是一个 UTF-8 JSON 对象，以换行分隔，最长 16 KiB；超长输入报告固定错误并退出，EOF 退出。Go 层另有更严格的 URL 长度检查。

```json
{"action":"open","url":"http://127.0.0.1:8234/?access_token=EXAMPLE"}
{"action":"hide"}
{"action":"show"}
{"action":"minimize"}
{"action":"close"}
```

首次 `open` 只接受明确写出的 `http://127.0.0.1:端口`，端口 1–65535，且恰好有一个非空 `access_token`。拒绝 localhost、IPv6、userinfo、控制字符、初始 URL fragment、重复/空 token、其他协议和其他主机。后续导航、子框架导航和下载目标必须同源；弹出新窗口及跨源重定向不会交给系统浏览器执行。Go 服务仍负责实际授权，helper 的检查不是服务端鉴权替代品。

```json
{"event":"ready"}
{"event":"closed"}
{"event":"error","code":"load_failed"}
```

- `ready`：本地页面完成加载且具备必要的 Canvas、图片解码、Fetch、AudioContext、AbortSignal.timeout、HTML dialog API。共享页面兼容脚本会在这个检查之前执行。
- `closed`：仅用户明确关闭原生窗口或 macOS 明确退出应用；管道 EOF、`close` 命令、加载失败、网页进程异常都不会误报为用户退出。
- `error`：固定安全代码，不输出 URL、文件名、令牌或底层错误文本。Go 父进程也丢弃 WebKit 的 stderr。
- 网页进程退出后自动恢复最多两次，按进程生命周期累计，不因成功加载无限重置。超过上限仍不主动退出 Go 核心。
- 启动、回收超时和 helper 异常退出后的新窗口恢复由 Go 管理；不安装额外守护服务。

窗口默认 860×600，最小 720×480，可调整大小，无系统标题栏。隐藏只隐藏窗口并保留同一 WebView，显示/最小化不会重新加载页面。macOS 隐藏时取消 Dock 展示，恢复时重新展示。Linux 隐藏实际 GTK 窗口，最小化保留任务栏窗口。

Linux **整屏显示使用原生窗口全屏回退**，不是 DOM 元素独占全屏：窗口填满显示器，保留会话工具栏，按钮变为“退出全屏”，再次点击或 Esc 恢复窗口。真实 session 的 `fullscreen` / `exitFullscreen` 两个按钮通过隔离脚本世界接管；只接受主框架的可信事件，原生侧再检查最近真实输入且一次输入只能授权一次切换。网页普通脚本看不到桥，合成点击不能调用原生切换。WebKit 的 DOM fullscreen 请求保持关闭并安全拒绝，不能绕过回退进入已知崩溃路径。macOS 不使用此回退，显式启用公开 `WKPreferences.isElementFullscreenEnabled`（默认关闭，macOS 12.3 起可用），保留引擎手势要求。

拖动只允许真正的主指针左键事件、`[data-window-drag]` 区域并排除按钮、链接、输入、表单、可编辑区域等控件。注入脚本及消息桥位于隔离脚本世界，网页自身看不到桥；原生侧还验证近期真实鼠标按下。Linux 无边框窗口保留 5 像素原生边缘调整大小区域。

原生 User-Agent 追加 `YuDeskNative/2.0.0`，只用于网页交互提示，不授予权限。应用提示使用已有 HTML dialog；意外调用浏览器默认 alert/confirm/prompt 会被处理为安全取消并报告固定诊断，不显示默认浏览器弹窗。文件打开/保存仍使用操作系统文件选择器。下载完成、取消、失败以固定安全文本更新本机页面 `#transferStatus`，不拼接文件名、地址或任意异常到脚本中。

## 构建与最低版本

macOS 构建需要 Apple Command Line Tools 的 Swift、AppKit 和 WebKit SDK：

```sh
sh native/macos/build.sh arm64 /absolute/output/yudesk-window-arm64
sh native/macos/build.sh amd64 /absolute/output/yudesk-window-amd64
sh native/macos/build-launcher.sh arm64 /absolute/output/yudesk-launcher-arm64
sh native/macos/build-launcher.sh amd64 /absolute/output/yudesk-launcher-amd64
/absolute/output/yudesk-window-arm64 --self-test
```

目标部署版本为 **macOS 12.3**（HTML dialog 的系统 WebKit 基线），需要更新到该系统可用的受维护 WebKit / Safari 版本。启动时运行特性检测，不把仅有系统版本号当作浏览器能力保证。实际执行过的 Mac 为 **macOS 15.7.4 arm64**；Intel 版本完成交叉编译，未在 Intel 真机运行。签名、公证和安装包由打包流程处理，helper 不关闭 Gatekeeper 或绕过系统权限。

Linux 最终发布 helper 在 **Ubuntu 22.04 amd64** 编译，构建环境 GTK **3.24.33**、WebKitGTK **2.50.4**、JSON-GLib **1.6.6**。公开 API 依赖 WebKitGTK **4.1 ABI、版本 ≥2.40**，GTK ≥3.24，GLib 的 GUri API；不能使用 WebKitGTK 4.0 替代。最终 ELF 最大 glibc 符号要求为 **GLIBC_2.34**，没有引入 Ubuntu 24.04 的 GLIBC_2.39 要求。

```sh
apt-get install --no-install-recommends build-essential pkg-config \
  libgtk-3-dev libwebkit2gtk-4.1-dev libjson-glib-dev
sh native/linux/build.sh /absolute/output/yudesk-window
/absolute/output/yudesk-window --self-test
objdump -T /absolute/output/yudesk-window | grep GLIBC_
```

Ubuntu 22.04、24.04 均执行过图形测试。Debian 12 的 ABI 条件允许打包，但本次没有执行 Debian 12 图形真机/容器回归，不应写成已验证。GTK/WebKit 动态库由发行版安装器依赖管理。中文界面还需要系统 CJK 字体；精简测试镜像只含西文字体时，中文会显示方框，建议安装器推荐 `fonts-noto-cjk`。

## 已执行验证（2026-09-09）

### Linux 真实 WebKit + Xvfb

测试代码仅在 `-DYUDESK_WINDOW_TEST` 构建中提供 `test-*` 命令；生产构建中完全没有这些入口。生产 helper 的回归明确检查 `test-state` 被拒绝。测试只使用自己创建的 localhost 页面、虚拟 X 显示和测试文件。

```sh
apt-get install --no-install-recommends xvfb xauth dbus-x11 openbox python3 xdotool
sh native/linux/test.sh /tmp/yudesk-native-window-tests
```

Ubuntu 22.04 / WebKitGTK 2.50.4 和 Ubuntu 24.04 / WebKitGTK 2.52.6 均通过：

- 860×600、无原生装饰、隐藏/显示四轮不重建页面、最小化/恢复。
- Canvas 确切像素、PNG 解码、Fetch 本机响应、超时 API、AudioContext 创建与关闭、HTML dialog 真正打开、Clipboard API 存在。
- 剪贴板专项：在 Xvfb 自有虚拟显示中对 fixture 按钮执行 XTEST 点击，JS 验证 `event.isTrusted`；`navigator.clipboard.writeText` 写入的中文设备码 + PIN 与 GTK 读取的字节完全一致。随后由 native fixture 替换成其他应用的剪贴板内容，再点击读取被拒绝。注意 WebKit 可无额外许可读取本页面自己刚写入的 origin-tagged 内容，这不等于泄露其他应用剪贴板。生产保持 `javascript-can-access-clipboard=false`，不自动批准任何权限请求。物理主机鼠标和剪贴板没有参与测试。
- 真正 WebKit 下载管线，测试专用目的地代替真实用户点击文件选择器：保存字节完全匹配、取消、无效路径失败、页面提示正确。未把 UI 文本测试冒充真实下载测试。
- 原生 UA 标记；完成、取消、失败的固定文本提示。
- 非可信合成指针事件不能拖动窗口，普通网页脚本无法访问隔离原生桥。
- 跨源导航、弹窗、HTTP 重定向不触及另一端口的测试服务器。
- 三次网页进程退出：前两次恢复，第三次不循环恢复，helper 保持运行。
- 原生关闭产生 `closed`；父管道 EOF 和 `close` 命令不产生 `closed`。
- 无效地址、重复 open、16 KiB 输入上限、403 响应、生产不存在测试接口。

**容器限制与补验：** Docker 默认 seccomp 禁止 WebKit bubblewrap 创建命名空间。首轮测试临时设置 `WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1`，但随后使用独立非 root 用户、允许命名空间的 `--security-opt seccomp=unconfined` 测试容器，**删除该关闭沙箱变量后完整回归再次通过**。产品仍明确启用 WebKit 沙箱；没有把容器例外写进安装器或用户配置。图形渲染使用 `LIBGL_ALWAYS_SOFTWARE=1`；这不能声称已验证硬件加速、真实声音输出或真实 Wayland 桌面。

主代理提供的 **`TestNativeUnixShellLifecycle`** 与最终 production helper 在 Ubuntu 22.04 独立容器用户 / Xvfb 环境通过（首轮 1.83 秒，WebKit 沙箱启用复验 1.01 秒，**剪贴板设置冻结后的最终复验 1.09 秒**）：同一 helper 三轮隐藏/显示/最小化；终止测试 helper 后 Go 核心不退出；再次 Show 重建 helper；应用 Exit 回收 helper。只加载本机 fixture，不执行物理桌面输入操作。

**原生全屏修复后的最终 production `5806d20…` 再验通过：1.17 秒。** 完整原生安全/下载/剪贴板/恢复套件也在 Ubuntu 22.04 独立非 root 用户、WebKit 沙箱启用状态重新通过。

截图：构建产物目录 `native/linux/build/ubuntu22.04/webkit-fixture.png`，为真实 WebKit snapshot，不是最终主页截图。测试镜像初次未安装 CJK 字体，状态栏中文字形会显示缺字方框；字符串内容通过 DOM 检查验证。

### 最终真实 dashboard 与全屏补验

`test-dashboard.py` / `test-dashboard.sh` 读取项目实际 `dashboard.html`、CSS、JS、`compat.js`、窗口 UI 及 footer；只替换 Go 模板数据和本机 API，设备、PIN、服务状态全部为 synthetic localhost fixture。不开 Go 核心、中转或实际远控会话，也不修改共享网页文件。使用已经编译的 test-only helper，不重新生成 production。

```sh
apt-get install --no-install-recommends fonts-noto-cjk
sh native/linux/test-dashboard.sh /absolute/yudesk-window-test /absolute/test-output
```

Ubuntu 22.04 / WebKitGTK 2.50.4 / Noto Sans CJK SC / Xvfb 1280×800 / Openbox 最终通过：

- 真实首页、设备列表、设置分别完成初始化，无 JS、Promise、console.error、CSP 错误；真实 CSS/JS/图片全部加载，未出现未 mock 的 API。
- 默认 860×600：根节点、body、content、当前 pane 均无横向/纵向溢出与滚动，主要控件无裁切；24px footer 完整。中文真实字体截图人工查看，不仅验证字符串。
- `packageManaged=true` 显示已安装模式并锁定选择，安装/卸载/重启按钮隐藏，授权输入默认折叠；在线/连接中/离线及 4 台设备分页数量正确。
- 从真实 `session.html` 提取两个全屏按钮原始标记，从 `session.js` 提取原始全屏函数挂接测试；其他远控输入、帧和网络服务不参与。三轮可信 XTEST 点击覆盖原生全屏进入、两个按钮分别退出、Esc 退出，1280×800 恢复 860×600，期间隐藏/显示保留同一页面及 IPC 响应；无可信事件的合成点击不会进入原生全屏，原 DOM 请求被安全拒绝。真实物理桌面未参与。

最终产物位于 `native/linux/build/ubuntu22.04/`：`webkit-dashboard.png`、`webkit-devices.png`、`webkit-settings.png`、`webkit-dashboard-report.json`（含实际 UI 文件 SHA、检测结果、`errors: []`）。这是最终 dashboard 截图，与前述简单 canvas fixture 截图不同。测试程序保留的 `test-debug-helper.sh` 只允许虚拟显示及显式诊断路径，绝不参与生产包。

**修复依据与限制：** 旧 `95f05…` 在 WebKitGTK 2.50.4 的可信 DOM fullscreen 请求上确实触发 native UI 进程 `SIGABRT`。GDB 栈为 `pthread_kill → raise → abort → libwebkit2gtk-4.1.so.0` 内部调用，末端 GTK 主循环；记录于 `fullscreen-before-gdb.log`。仅增加 `enter-fullscreen` / `leave-fullscreen` 回调调用 GTK full/unfullscreen 并返回 TRUE，仍复现。不能声称已经证明它只发生于软件渲染，或已定位到上游具体源代码行；没有足够证据给出更精细归因。依据公开 [GTK enter-fullscreen 契约](https://webkitgtk.org/reference/webkit2gtk/stable/signal.WebView.enter-fullscreen.html)，本轮采用独立 GTK 原生窗口全屏避免该路径，并验证实际功能可用，而不是跳过失败或仅禁用按钮。最终回退尚未在真实 Wayland/多显示器桌面、Ubuntu 24.04 上重复全屏专项，不能宣传已做这些验收。

### macOS 离屏能力验证

`WebKitCheck.swift` 是独立诊断程序，不参与生产构建。使用 `.prohibited` 激活策略，不创建 NSWindow、不激活 Dock、不操作鼠标键盘，只在离屏 WKWebView 加载临时 loopback fixture。

```sh
xcrun swiftc -swift-version 5 -O -target arm64-apple-macos12.3 \
  -framework AppKit -framework WebKit native/macos/WebKitCheck.swift \
  -o /absolute/output/webkit-check
python3 native/macos/test-webkit.py /absolute/output/webkit-check
```

Mac 15.7.4 arm64 输出所有特性为 true：`fetch / bitmap / audio / timeout / dialog / clipboard / canvas / pixel / modal / request / fullscreen / fullscreenGestureRequired`。最后两项表示公开 fullscreen API 可用、离屏无手势请求被拒绝；由于离屏 view 还未附着窗口，不能据此单独证明拒绝原因，也不能当成真实显示器全屏转换验收。两种架构的 production helper 完成编译，arm64 URL 自检通过。**未**在真实 Mac 桌面验证拖动、最小化、隐藏恢复、整屏进入/退出、保存文件选择器、音频输出、录屏/输入系统权限；不得将离屏特性检测描述成这些功能的真机验收。

### macOS LaunchServices 无窗口验证

`LauncherFixture.c` 是没有 AppKit、网络和窗口的假核心，用文件锁模拟单实例。`test-launcher.py` 创建唯一临时 `.app`，入口使用实际 production launcher，通过 `open -g` 首次启动，再三次打开同一 `.app`。三次真实 LaunchServices reopen 均触发 secondary，主核心计数始终为一；结束假核心后 launcher 退出。该测试已在 Mac 15.7.4 arm64 通过，没有运行真实 Go/窗口 helper，也没有创建可见窗口或修改生产服务。

```sh
cc -O2 -Wall -Wextra -Werror native/macos/LauncherFixture.c -o /absolute/output/launcher-fixture-core
python3 native/macos/test-launcher.py /absolute/output/yudesk-launcher-arm64 \
  /absolute/output/launcher-fixture-core /absolute/staging-directory
```

## 最终冻结的 5 个原生二进制（2026-09-09）

下面 3 个窗口 helper 和 2 个 macOS launcher 已冻结；Linux/Mac 最终安装包均为 revision 4。此表是打包前输入哈希，Mac 完整应用 ad-hoc 签名后字节会变化；最终包与线上部署以 `RELEASE_STATUS.md` 为准：

| 路径 | SHA-256 |
|---|---|
| `native/linux/build/ubuntu22.04/yudesk-window` | `5806d20e12ce437b8ab2db1613352c778f2fb3a4c4e403ae284701ca0b8a59f5` |
| `native/macos/build/yudesk-window-arm64` | `543a942b99e05072392f7f10b90066122d6af4d28a9782dadbce59868cce7d3f` |
| `native/macos/build/yudesk-window-amd64` | `4f2fc1f577a9bdfdb03c3aae64831dffb67708659dd77e29951a35c7de9a1e08` |
| `native/macos/build/yudesk-launcher-arm64` | `5a13c49f3ac90b1edd17121700c4055fb063dbfa0428aa1a8ac26ca85be3bc24` |
| `native/macos/build/yudesk-launcher-amd64` | `4c5a794d346dd4cc9334a7bf74d08d971df5e86732cf4627d7fd323785c4c5ad` |

macOS 构建服务器只使用 `/Users/yu/bin/yudesk/staging/native-window-20260909.1/`。没有修改生产配置、重启中转、安装服务、提交 Git 或发布。
