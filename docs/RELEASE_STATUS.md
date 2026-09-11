# 当前部署状态（2026-09-11）

## 当前一体化安装与 Android 界面优化：`installer-polish-20260911.6`

桌面 **2.0.0** 与 Android **2.0.0-preview.13 / 2000013** 已于 **2026-09-11 22:56（北京时间）**部署到官网。Windows、Linux、macOS Intel、macOS Apple Silicon 和 Android 五个平台安装包均可下载；iOS 与原生鸿蒙继续暂停。

- Windows 官网 EXE 已改为独立的一体化安装器：用户不会先看到便携版主界面；点击“立即安装”后只经历一次 Windows 管理员授权，随后程序写入受保护的 `Program Files\YuDesk`、启用既有锁屏桌面服务并直接打开正式程序。
- 安装器自动创建桌面与开始菜单快捷方式，使用本地生成并验证过的蓝色 Yu ICO。安装在后台工作线程执行，等待系统授权时窗口仍响应；重复双击第二个安装器会立即退出，只保留一个安装窗口。没有更新工具或命令行步骤。
- 安装包把正式客户端作为带长度和 SHA-256 摘要的受限载荷附加，启动前和解包后均校验；构建目录中的载荷与安装器外壳不会作为独立下载发布。最终 21,026,360 字节安装器的载荷校验、真实窗口启动和双实例回归通过。未通过自动化点击 UAC 或修改本机已安装服务。
- Android 首页在系统状态栏/刘海安全区之外增加 22dp 顶部留白；API 26 软件模拟器完成安装、冷启动、进程存活和实际截图检查。普通远控与会议仍使用独立的横屏沉浸式全屏，不受首页留白影响。
- Android 四 ABI Go 核心三轮 race/vet、29 项 Java 单测（0 失败）、lint（0 错误）、APK v2 签名及 16 KiB ZIP/ELF 对齐通过。APK 仍是调试签名预览版，尚未覆盖所有品牌物理真机。
- 根模块全量 `go test -short ./...`、`go vet ./...`、Windows 交叉 vet、安装器打包/损坏拒绝测试通过。新增 WebView2 安装界面依赖及间接 loader 的许可证已纳入完整 80 模块、127 份许可文档。
- 本次只原子替换下载文件，没有重启中转；PID 仍为 **10824**，现有连接不因发布被主动中断。`/healthz`、未登录后台 401、公开在线/连接统计和 SQLite `quick_check` 均通过。发布目录 `/Users/yu/bin/yudesk/releases/installer-polish-20260911.6`，可恢复备份 `/Users/yu/bin/yudesk/backups/installer-polish-20260911.6`。
- Windows 与 Android 从公网域名完整重新下载后 SHA-256 与本地一致；Android 1 MiB Range 请求返回 `206` 和正确 `Content-Range`。Linux/macOS 包字节未变，macOS 仍未 Developer ID 签名或公证。

| 当前产物 | SHA-256 |
| --- | --- |
| Windows x64 一体化安装器 | `2919ab9de11c8467a1ac30c6810775c517bcb700ffee759cb7b65da3eda4cca1` |
| Linux x64 DEB | `1cc0cedcdb06ff1906babcfc3e0526358c53e1db0cfd3642d67364d08619d734` |
| macOS Intel PKG | `2a6a8162ae9fb9576a9c901837066723c680fc79d617474e94b309ba1021c624` |
| macOS Apple Silicon PKG | `f7a69436721126b365f8a78637759622ec1dd948916162e7fd528ec6f4d6cb32` |
| Android preview.13 APK | `b93fe2b80994712f37975d6876f874e5be44c905a0d664072dd85098bb82a2ca` |
| macOS 服务端 | `8dd29dc581a64a74e208d45505b324eea206ea0340780d6e6b2efca466dce8cb` |

完整会议范围和限制见 [多人会议说明](CONFERENCE_20260911.md)。

## 当前 Android 与 Windows 热修复：`android-startup-20260910.2`

Android **2.0.0-preview.8 / 2000008** 与 Windows **2.0.0** 已于 **2026-09-10 19:27（北京时间）**部署到官网；Linux 和 macOS 安装包字节保持不变。iOS 与原生鸿蒙仍暂停。

- 官网旧 44,711,068 字节 APK 在受限公网链路中复现过中途截断，残包被 Android 安装器报告为清单解析失败。四 ABI 通用 APK 现使用安装时提取的压缩原生库，`preview.8` 为 **19,240,256 字节**，比旧未压缩包减少约 57%；四 ABI 和各 ELF 的 16 KiB LOAD 对齐不变。
- 下载服务继续为已验证安装包单独提供 10 分钟写入窗口，并增加强 ETag、`Accept-Ranges: bytes`、`no-transform`，去除可能阻碍 Android 下载器保存断点的 `no-store`。公网完整下载通过，字节数和 SHA-256 与本地产物一致；1 MiB Range 请求返回 `206` 与正确 `Content-Range`。
- 用户故障页明确显示部分 Android 11+ 厂商系统在首个内容视图创建前调用 `Window.getInsetsController()` 会因空 `DecorView` 崩溃；重启只是改变时序。`preview.8` 把主界面、故障页和远程全屏的系统栏操作统一移到内容视图安装后，并先确保 `DecorView` 已创建。Android 8 / API 26 x86_64 软件模拟器完成覆盖安装、冷启动、进程存活和 Go 核心上线，显示“已就绪”，没有 YuDesk 崩溃日志。
- Windows 默认安装版不再显示 YuDesk 二次确认，也不再先切换到设置页；用户双击后直接进入系统 UAC 和安装切换。卸载仍保留自定义确认。双端 Windows 完整浏览器/原生进程回归通过，确认安装请求直达、失败不重启、主页面不闪跳及原有远控/会议流程未回归。
- Go 核心三轮 race、`go vet`、29 项 Java 单测、lint、四 ABI gomobile、APK v2 签名与 16KB 校验通过；根模块全量 short 测试及 Relay race 测试通过。模拟器不等于所有品牌真机验收，仍需用户手机的具体安装提示或 logcat 排除旧签名冲突和厂商策略。
- 服务端未重启，PID 仍为 **98308**，SHA-256 仍为 `0d36144672e67549d715d82f66e98d2c1fcb7db5d942396c57b9777369c7f619`；数据库 `quick_check`、六项下载清单和 `/healthz` 均通过。部署前授权设备动态记录为 175，账号/授权码为 0 / 9；本轮未改数据库。可恢复备份位于 `/Users/yu/bin/yudesk/backups/android-startup-20260910.2`。
- 私有 GitHub Release：[v2.0.0-android-preview.8](https://github.com/YUCONGGEN/yudesk/releases/tag/v2.0.0-android-preview.8)，附件包含 APK、Windows 安装程序和本轮 SHA256 清单。

| 当前产物 | SHA-256 |
| --- | --- |
| Android preview.8 APK | `5500fa50bf874c91e5a7e67a149346ff000d7cf9398bebc75cf37fa08c205a99` |
| Windows x64 EXE | `7f192d75b9e793d8585032ce25c7410522b8fffb47ca8d57bf55f19a9ac5c6e5` |
| Linux x64 DEB（不变） | `45d16fdb7c03558499edc5551ed97495cba762d72e93380ed866f99af211025a` |
| macOS Intel PKG（不变） | `ea808d538a1a8538bd3594f790e2c3a3983a870e7f24418382cbb7a4d05b3a33` |
| macOS Apple Silicon PKG（不变） | `397dab466cf9a8dc63e4fdb9d2c83eeef057eec33fb83c7f15b6daa57d5c56e3` |
| macOS 服务端 | `0d36144672e67549d715d82f66e98d2c1fcb7db5d942396c57b9777369c7f619` |

## 上一个 Android 客户端：会议与横屏全屏 `android-meeting-20260910.3`

Android **2.0.0-preview.6 / 2000006** 已于 **2026-09-10 06:31（北京时间）**部署到官网；Windows、Linux 和 macOS 安装包字节保持不变。iOS 与原生鸿蒙仍暂停。

- Android 主界面新增原生紧凑会议区域：主持人先通过 MediaProjection 系统授权共享本机屏幕，再取得单独的 9 位临时会议号；参会者输入会议号直接加入，无需主持人二次确认。会议协议强制只观看，不开放触控、文字、剪贴板或文件命令，也不写入普通最近设备记录。Android 当前不支持系统声音，界面明确提示且不提供无效开关。
- 普通远程控制和会议观看均自动进入沉浸式横屏全屏，仍可从顶部方向按钮切到竖屏；结束、离会或手机返回键会释放输入并恢复进入会话前的主界面方向。主持端共享不强制改变主持手机方向，旋转由 MediaProjection 尺寸回调继续适配。
- 临时会议号创建/结束使用设备 Ed25519 私钥签名，Relay 只在内存保存短期映射；号码还会在端到端加密通道内再次校验。停止共享、主持人结束、最长两小时到期、设备离线或授权失效都会先撤销本机会议凭据并断开参会者。P2P 优先和 TLS 中转回退保持不变。
- Android Go 核心三轮 `-race`、`go vet`、根模块全量 short 测试、四 ABI gomobile、Java 单元测试、SDK 35 编译、lint、APK v2 签名、16KB ZIP 对齐及四个 `libgojni.so` 的 `0x4000` LOAD 对齐均通过。APK 签名证书 SHA-256 仍为 `c8e35f5904ad92ab55a651f940d531c1193042dcfc2ac1a98b7dadf0c3994a6a`，可覆盖 preview.5。当前没有连接 USB 真机，不能把自动化结果表述为所有厂商的 MediaProjection、横屏手势、后台策略或真实公网会议验收。
- 部署后服务 PID **34568**，服务端 SHA-256 为 `090c83aad509a2e367f37064a8fe5a16e5879c53d6adf6b59040d03bee9a5a79`；完整部署事务前后的账号/授权设备/授权码数量均为 **0 / 54 / 9**，数据库 `quick_check` 为 `ok`。最终复核时，持续运行的服务已收到新的设备登记，`licensed_devices` 实时行数为 104，因此不把该动态表数量作为固定发布值；账号和授权码仍为 0 / 9。部署前没有活动会话，桌面下载哈希未变；最终 APK 原子替换未重启服务，可恢复备份位于 `/Users/yu/bin/yudesk/backups/android-meeting-20260910.3`，前两阶段备份也保留。
- 官网发布日期显示 **2026-09-10 06:31（北京时间）**，公网重新下载 APK 后与本地 SHA-256 完全一致。官网 Android 项已更新为“控制 / 会议 / 授权共享 · 预览测试”。

| 当前产物 | SHA-256 |
| --- | --- |
| Android preview.6 APK | `9d8521d4ffca62a0b4c7f147ed3dac0b6c9d35e3e9886574095afb4e275c95af` |
| Windows x64 EXE（不变） | `7f306731b030056c67afcc33c5f5d1c40b8e29e7490df914aece4f97e402cd23` |
| Linux x64 DEB（不变） | `45d16fdb7c03558499edc5551ed97495cba762d72e93380ed866f99af211025a` |
| macOS Intel PKG（不变） | `ea808d538a1a8538bd3594f790e2c3a3983a870e7f24418382cbb7a4d05b3a33` |
| macOS Apple Silicon PKG（不变） | `397dab466cf9a8dc63e4fdb9d2c83eeef057eec33fb83c7f15b6daa57d5c56e3` |
| macOS 服务端 | `090c83aad509a2e367f37064a8fe5a16e5879c53d6adf6b59040d03bee9a5a79` |

## 上一个桌面批次：临时会议 `meeting-20260909.1`

桌面 **2.0.0** 会议版本已于 **2026-09-09 23:52（北京时间）**部署。Windows x64、Linux x64 revision 7、macOS Intel / Apple Silicon revision 7 均已更新；Android 继续使用 **2.0.0-preview.5** 原 APK，iOS 与原生鸿蒙仍暂停。

- 主界面在“远程控制”和“设备列表”之间新增紧凑会议页，提供“快速会议”和“加入会议”。主持人共享本机屏幕，服务器生成独立的 9 位临时会议号；参与者输入会议号后直接加入，无需确认。会议只允许一位参与者，协议端强制仅观看，可选系统声音，不开放输入控制与文件访问。
- 会议目录只在服务器内存保存“会议号 → 完整设备身份”映射。创建/结束必须由设备 Ed25519 身份签名并通过有效设备授权；解析采用校验证书的 TLS，按来源限流。会议号在两小时后、主持人结束、主持设备管理通道离线或服务器重启时失效；媒体仍走现有端到端加密通道，P2P 优先、失败中转。
- 全量 `go test -short ./...`、会议相关 race 检测和 43 项窗口/弹窗回归通过。Windows 最终 EXE 双端联调实际完成“创建 9 位会议号 → 无确认入会 → 真实桌面画面 → 仅观看 → 主持人结束并使会议号失效”，固定窗口无页面滚动或溢出。Linux 包在 Ubuntu 22.04 环境构建；macOS 双架构由真实 Mac SDK 构建，arm64 helper 自检、包展开、架构与 ad-hoc strict/deep 验签通过。没有将构建成功表述为 Linux/macOS 图形真机会议验收。
- 部署后服务 PID **82461**，服务端 SHA-256 为 `c816acd030c2cb1fc0e6668bb5d5eff4dfda3688a7b4822bc5b5f40b4e55ea49`。账号/设备/授权码保持 **0 / 11 / 9**，数据库 `quick_check` 为 `ok`，配置 SHA-256 仍为 `c2ba4b3f89c17519e05a5b9cb778c71d7a3aa1f587d42347c0ef966e5ecda24c`；TCP TLS 1.3 指纹和 UDP 8233 监听保持不变。备份位于 `/Users/yu/bin/yudesk/backups/meeting-20260909.1`。
- 官网显示桌面版本 **2.0.0**、发布日期 **2026-09-09 23:52（北京时间）**。五个公网安装文件均完整重新下载并与本地 SHA-256 一致；Android 是原文件复核，不含本轮桌面会议入口。
- 私有 GitHub Release 目标：[v2.0.0-meeting-20260909](https://github.com/YUCONGGEN/yudesk/releases/tag/v2.0.0-meeting-20260909)。实际源码提交和七个附件状态以该链接为准，旧 Release 不覆盖。

| 产物 | SHA-256 |
| --- | --- |
| Windows x64 EXE | `7f306731b030056c67afcc33c5f5d1c40b8e29e7490df914aece4f97e402cd23` |
| Linux x64 DEB | `45d16fdb7c03558499edc5551ed97495cba762d72e93380ed866f99af211025a` |
| macOS Intel PKG | `ea808d538a1a8538bd3594f790e2c3a3983a870e7f24418382cbb7a4d05b3a33` |
| macOS Apple Silicon PKG | `397dab466cf9a8dc63e4fdb9d2c83eeef057eec33fb83c7f15b6daa57d5c56e3` |
| Android preview.5 APK（不变） | `2caac0f0b2e1e3d2c60978610f6f379b568629af91ac889bba0676f5554ca79d` |
| macOS 服务端 | `c816acd030c2cb1fc0e6668bb5d5eff4dfda3688a7b4822bc5b5f40b4e55ea49` |

该批次的 Android APK 当时尚无会议入口，已由上方 preview.6 取代。当前会议范围仍是“一位主持人共享屏幕 + 一位参与者观看”，不是完整多人音视频会议：尚无摄像头、麦克风、成员列表、聊天、录制、主持权转移或多人屏幕共享。macOS 包仍未 Developer ID 签名/公证，Linux Wayland、Mac 图形真机与 Android 真机需继续验收。

## 当前服务：官网实时状态 `homepage-stats-20260909.2`

下载主页已显示 **在线设备**、**参与连接**及当前会话数。在线设备按活跃设备码去重；参与连接按每个会话的控制端与被控端合计，因此始终等于会话数的两倍。主页请求时服务端直接渲染当前值，外部脚本随后立即校准，并每 **30 秒**异步更新；更新失败保留原数字并显示“更新暂缓”，不刷新整页。公开接口 `/api/public-stats` 仅返回三个非负整数，不公开设备名称、设备码、PIN 或连接地址，拒绝非 GET 请求并禁止缓存。

- 服务器只读取已有中转内存状态，统计采用同一互斥锁快照并按设备 ID 去重，不查询或改写数据库。全量 Go 短测试、目标 race 测试、主页模板/资源/接口测试通过。
- Edge 对本地预览完成 1440、1024、768、390、320 五种视口验收，无横向溢出、外部资源或脚本错误；公网 1440 与 390 视口再次验收，统计卡片、异步接口和布局通过。验收时公网快照为在线 **1**、参与连接 **0**、会话 **0**，这是瞬时值，不是固定数据。
- 仅更新服务器和官网，现有 Windows/Linux/macOS/Android 安装包及 `release.json` 字节不变。当前服务 PID **66687**，服务端 SHA-256 为 `38bb93e423f3eccd35ae018006e0726a6863bfdecd7b0f570c9ae07211884d4b`。
- 账号/设备/授权码数量保持 **0 / 11 / 9**，数据库 `quick_check`、下载清单和 UDP 8233 监听通过；配置 SHA-256 仍为 `c2ba4b3f89c17519e05a5b9cb778c71d7a3aa1f587d42347c0ef966e5ecda24c`。成功部署备份为 `/Users/yu/bin/yudesk/backups/homepage-stats-20260909.2`；首次功能版备份 `homepage-stats-20260909.1` 也保留。

## 当前客户端：固定无边框窗口 `fixed-window-20260909.1`

官网桌面 **2.0.0** 新安装包已上线，显示 **2026-09-09 21:07（北京时间）**。Windows x64、Linux x64 revision 6、macOS Intel / Apple Silicon revision 6 已更新；Android 保持 **preview.5** 字节不变，因为 Android Activity 没有桌面系统边框和固定桌面窗口尺寸。iOS 仍按要求暂停。

- Windows、Linux、macOS 桌面主窗口固定为 **860 × 600 逻辑像素**，禁用拖拽缩放和最大化，保留窗口移动、最小化及远程会话整屏显示。Windows 内容区与原生窗口外框四边坐标一致，实测没有系统标题栏、黑边或额外边框；最大化系统命令和外部改尺寸请求均被拒绝。Linux X11 窗口管理器改尺寸请求实测仍保持 860 × 600，整屏退出恢复固定尺寸；macOS 双架构包使用相同固定内容尺寸和无边框样式。
- 左上角 YuDesk 标识恢复为可点击入口，点击会回到“远程控制”主页。远程会话的设置默认收起，移除了“隐藏”和“×”，仅保留“设置 / 整屏显示 / 结束控制 / 最小化”；结束控制仍回主界面。主界面原有隐藏、最小化和退出语义不变。
- Windows 原生专用窗口检查通过：边缘零差值、固定尺寸、拒绝最大化、导航、最小化/恢复、隐藏/显示十轮；统一端真实 Windows GUI 联调通过。Linux Ubuntu 22.04 + X11/WebKit 完成真实改尺寸拒绝、拖动、整屏三轮及生命周期测试。macOS Intel/Apple Silicon 编译、arm64 自检、安装包展开、架构和 ad-hoc strict/deep 验签通过；没有 Mac 图形真机手动验收。
- 全量 `go test -short ./...`、42 项窗口/弹窗回归及统一端两轮联调通过。修正一项测试自身对异步返回顺序的错误假设，生产输入逻辑未因此改变。四周无黑边采用原生客户区铺满并由 Windows 几何测试验证，不依赖截图裁切。
- 部署后服务 PID **57419**。账号/设备/授权码数量保持 **0 / 11 / 9**，数据库 `quick_check`、下载清单和配置 SHA-256 均通过；配置哈希仍为 `c2ba4b3f89c17519e05a5b9cb778c71d7a3aa1f587d42347c0ef966e5ecda24c`。旧程序、下载和一致性数据库快照保存在 `/Users/yu/bin/yudesk/backups/fixed-window-20260909.1`。
- 公网五个安装包与许可文件全部重新下载并比对 SHA-256；首页版本/北京时间日期、旧 Unix 地址重定向、未登录后台 401 和健康检查通过。TCP 8233 TLS 1.3 固定证书检查通过，UDP 8233 三次 STUN 往返约 **53.85 / 33.73 / 43.00 ms**；这些数值不是两端画面延迟。
- 私有 GitHub Release 目标：[v2.0.0-fixed-window-20260909](https://github.com/YUCONGGEN/yudesk/releases/tag/v2.0.0-fixed-window-20260909)。实际源码提交和七个附件状态以该链接为准，旧 Release 不覆盖。

| 产物 | SHA-256 |
| --- | --- |
| Windows x64 EXE | `0254290a64e0d6bf865ff0a2e72d0750f2d980e4705feb28ce76bfe928dc3983` |
| Linux x64 DEB | `d5c9ece643eed99a738dd86c970dc7222e5b248e7cee9bc9b69f63e89913910b` |
| macOS Intel PKG | `754d721c958a8d50378eab32c8f5d5b0eaec2afbff6f1cddf7bedb9c4770e6e5` |
| macOS Apple Silicon PKG | `b07947b8411d1a35d994d992a50d6791cfee0ee69d6d064eff134f4da2b60627` |
| Android preview.5 APK（不变） | `2caac0f0b2e1e3d2c60978610f6f379b568629af91ac889bba0676f5554ca79d` |
| macOS 服务端 | `3948638cc9a7f2da9629008a22a72e51060697c5bc0575e47571737c61542119` |

macOS 安装包仍未 Developer ID 签名/公证；Linux Wayland、不同缩放多屏及 Mac 图形真机需要继续验收。请完全退出旧 YuDesk 后下载安装新包，旧 EXE/应用不会自行更新。

## 历史：桌面拖动修复 `drag-20260909.1`（已替换）

官网桌面 **2.0.0** 新安装包已上线，显示 **2026-09-09 12:44（北京时间）**。Windows x64、Linux x64 revision 5、macOS Intel / Apple Silicon revision 5 已更新；Android 保持原 **preview.5** 字节不变。本轮修复本地窗口拖动，不宣称降低了物理网络 RTT。

- Windows 主界面、远程页、过渡页各完成两次真实鼠标拖动，六次位移均正确；原生按下与焦点/子窗口通知处理，控件区域不拖动。Linux 两套 X11 环境真实连续拖动通过；macOS 同步事件桥、双架构构建、离屏检查、包结构及 ad-hoc strict/deep 验签通过，但没有真机拖动验收。
- 全量 Go 测试/vet、Windows vet、核心 race、42 项窗口/弹窗测试、23 项输入/帧/文件/音频/在线测试及最终 Windows EXE 统一端联调通过。一次并行高负载窗口测试出现浏览器清理等待超时，后续独立测试与连续三轮通过；异常和验证边界完整保留在 [本轮记录](WINDOW_DRAG_20260909.md)。
- 首次部署遇到启动监听尚未就绪，按预案自动回滚；部署脚本现增加有时限的健康就绪等待。重新部署成功，当前 PID **67439**。账号/设备/授权码数量保持 **0 / 11 / 9**，数据库检查通过，配置 SHA-256 未变。成功部署备份 `/Users/yu/bin/yudesk/backups/drag-20260909.1-retry1`；首次回滚备份亦保留，未替换数据库或删除设备。
- 公网健康检查、未登录后台保护、五个下载文件和许可校验、旧 Unix 下载重定向及页面版本/日期验收通过。网站仍为 `http://www.yucg.cn:8235/`，TCP TLS 固定证书与 UDP 8233 STUN 检查通过。页面截图资源和脚本检查通过。
- 本轮私有 GitHub Release：[v2.0.0-drag-fix-20260909](https://github.com/YUCONGGEN/yudesk/releases/tag/v2.0.0-drag-fix-20260909)，源代码及七个附件的实际发布状态以链接为准，旧 Release 不覆盖。

| 产物 | SHA-256 |
| --- | --- |
| Windows x64 EXE | `d89f5420a43ffd0570e7381d520a9e7684597a0657be5d888ee6006e57d3e791` |
| Linux x64 DEB | `90afe272339557ae4a4c5cf58f097426ba7ce951d41f17cdbad2708b2b1df0f3` |
| macOS Intel PKG | `b265650937bc66eb196bc0d617864df0f603046ead812b1bfcc191c7ea9359aa` |
| macOS Apple Silicon PKG | `883798128564784d3ed25a7b3c2b575d0b99f874d1b84307aca45ccc5cef6b80` |
| Android preview.5 APK（不变） | `2caac0f0b2e1e3d2c60978610f6f379b568629af91ac889bba0676f5554ca79d` |

macOS 安装包仍未 Developer ID 签名/公证；Linux Wayland、不同缩放的多屏、触控和长期高负载需进一步真机验收。请完全退出旧 YuDesk 后重新安装，原身份/授权/设备列表保留；仅重开旧文件不会升级。HTTP 下载请通过可信 GitHub 渠道核对校验值。完整细节见 [Windows/共享修复](WINDOW_DRAG_20260909.md)、[Unix 验证](UNIX_DRAG_20260909.md)。

## 历史：跨平台安装与响应更新 `installed-20260909.1`（已替换）

桌面 **2.0.0** 与 Android **2.0.0-preview.5 / 2000005** 已一起部署。官网发布日期为 **2026-09-09 06:59（北京时间）**，服务端 PID `19796`，网站仍为 `http://www.yucg.cn:8235/`，TCP/UDP 8233 保持不变。

- Windows 默认推荐安装版，经过明确确认及管理员授权；窗口无系统标题栏，修复会话断开误退出与窗口监控恢复。macOS 两个架构提供完整 `.pkg`，Linux x64 提供 `.deb`，均为原生无系统标题栏窗口。Android 是原生 APK，保持 NoActionBar 和真实系统共享授权/通知。iOS 与原生鸿蒙继续暂停。
- 桌面输入使用有界独立队列，截图尺寸发布不被慢输入锁阻塞；Android 仅合并悬停、保留拖动轨迹和释放顺序，采用最新解码帧交接及末帧补采。继续身份与 PIN/审批通过后 P2P 优先，失败中转。
- Linux 安装包 revision 4 修复复现的 WebKitGTK DOM 全屏中止，改为可信操作触发原生窗口全屏，三轮全屏/恢复及 Esc、隐藏复用通过。Mac revision 4 修复 Finder 再次打开、安装标记资源目录及完整 app 签名封装；两个包在真实 Mac 生成并展开严格验签通过，但安装器未 Developer ID 签名、公证。
- 全量 Go 测试/vet、核心 race、Windows vet、23 项输入/画面/文件/声音/兼容测试、14 项弹窗/窗口测试通过。最终 Windows 包统一端联调通过；Windows/Mac 安装标记、匿名管道和会话生命周期测试通过。Linux 真实 WebKit 页面/中文/下载/恢复与最终 Go+helper 生命周期通过。Android 四 ABI、29 项 Java 单测、三轮 Go race、lint、签名与 16KB 对齐通过。
- 官网五个安装包及许可文件从公网完整下载，SHA-256 全部匹配；版本/日期、旧 Unix 地址重定向、健康检查与未登录后台 401 通过。线上页面五个下载入口、功能截图及无脚本错误通过。三次真实公网 STUN 请求/指纹及 TCP TLS 1.3 固定证书验证通过；STUN 往返不是双端画面延迟指标。
- 部署前后账号/设备/授权码记录数均为 **0 / 11 / 9**，SQLite 完整性正常，配置文件哈希不变；在线/连接中均为 **3 / 0**（检查时快照）。备份位置 `/Users/yu/bin/yudesk/backups/installed-20260909.1`，包含旧程序、下载、配置和一致性数据库快照；未删除设备或修改许可。
- GitHub 目标是既有私有仓库 `YUCONGGEN/yudesk` 的新标签 `v2.0.0-update-20260909`；[本轮 Release](https://github.com/YUCONGGEN/yudesk/releases/tag/v2.0.0-update-20260909) 发布五个平台附件及校验/许可，旧 Release 不覆盖。实际推送及附件发布状态以该页面为准。

最终下载 SHA-256：

| 产物 | SHA-256 |
| --- | --- |
| Windows x64 EXE | `a0b17e7dde88638459f55cf68118bb63a8850c144e07851ff964a8fa1c97f428` |
| Linux x64 DEB | `1b3cee47435dc7609faa2243495190e9914c51f9c43f8e647832054ea77030b6` |
| macOS Intel PKG | `37aa9e468a2c2a2b1bf06303ae11dbe0916a3b81366d0daeb901e28a754f8275` |
| macOS Apple Silicon PKG | `3485aefe2782f77350a5816a792bf9c38c5baef3f65b3dfeacf1ed19a26beaae` |
| Android preview.5 APK | `2caac0f0b2e1e3d2c60978610f6f379b568629af91ac889bba0676f5554ca79d` |
| macOS 服务端 | `5e564e13e5d84d1a39f2ca596c3f3020e2e05be2bc71fb8ff26f6af4d9a834c7` |

HTTP 下载不提供来源真实性保证，应通过可信的 GitHub 渠道核对校验值。更新前完全退出旧 YuDesk，再安装新包；两端都需要升级。未替换当前开发电脑正在运行的旧主程序或实际安装服务。

验证限制：没有 Android 真机、Mac Intel 图形真机、完整 Windows 管理员安装/锁屏或长时间跨外网验收；macOS 13+ 的包未公证且被控输入另需 cliclick/辅助功能授权，Linux 完整输入限兼容 X11 桌面。Android 仍是原调试签名预览系列。Mac 系统声音与 Android 文件/系统声音未补齐。约 100ms RTT 基线的输入到画面中位数仍约 120ms，没有证明平均值明显下降，不能承诺零延迟或固定帧率；详情见 [跨平台验证](ALL_PLATFORM_RELEASE_20260909.md)、[桌面基准](DESKTOP_INSTALLED_LATENCY_20260909.md)、[Android 专项](ANDROID_LATENCY_20260909.md)。

## 历史：P2P 网络更新 `p2p-20260908.1`（已替换）

**桌面 2.0.0 与 Android 2.0.0-preview.3 已部署。** 官网标注发布日期为 **2026-09-08 22:07（北京时间）**；服务端 PID `48511`。Windows x64、Linux x64、macOS Intel/Apple Silicon 与 Android 下载均已替换。

- 认证及 PIN/审批通过后优先 ICE/STUN UDP 直连，初始打洞失败自动中转；显示当前线路与真实应用 RTT。直连中途丢失安全结束，重新连接后重新选路；不是无缝迁移。
- 新增公网 UDP 8233，三次真实 STUN 请求与校验指纹通过；原 TCP 8233 及 HTTP 8235 不变，TCP TLS 1.3 固定证书验证通过。UPnP 仅添加这一个 UDP 映射，路由器重启后可能需要重建。
- 后台断开、禁用、删除及授权到期仍能终止直连。部署前在线/连接中为 3/1，服务切换会中断会话；随后后台为 2/0。未创建测试设备或修改许可。数据库 `quick_check` 为 ok，备份及发布后账号/设备/授权码均为 **0/11/9**，配置文件校验一致。
- 使用发布索引导出的独立源码构建，没有混入工作区未完成的 Windows 原生窗口实验；沿用原窗口实现。Linux 全量 race/vet、Windows P2P 套件、实际 macOS STUN 套件、20 项 Node 回归通过。macOS 测试修正了回环别名与 UDP 最大发送量的系统差异，未修改服务器网卡配置。
- Android 版本代码 2000003，签名与旧包一致；四 ABI JNI/APK、Go 核心三轮 race、19 项 Java 测试、lint（无问题）、签名与 16KB ZIP/ELF 对齐通过。仍无手机真机验收，不宣称所有安卓兼容或零延迟；Android 声音、文件与剪贴板功能边界见 [Android 说明](ANDROID.md)。
- 旧程序、下载包、配置及一致性数据库备份：`/Users/yu/bin/yudesk/backups/p2p-20260908.1`。本次发布文件：`releases/p2p-20260908.1`。未覆盖旧 GitHub Release 附件；本次 GitHub 更新是私有仓库 main 的源码提交，安装包以官网为准。
- 完整第三方许可覆盖桌面及 mobile 两套模块图，共 78 个模块版本、124 份许可正文，随 APK 与官网资料发布；许可页面为 `/THIRD_PARTY_NOTICES.txt`。
- 公网完整下载全部五个平台文件、桌面校验清单和许可文档，SHA-256 全部与发布包一致；首页版本/北京时间日期、健康检查与未登录管理页 401 校验通过。

本次校验值：

| 产物 | SHA-256 |
| --- | --- |
| Windows x64 | `288712c50721278c0ebcb6d653ae7f5edd28fe0ae761742b9b2fc4443590a625` |
| Linux x64 | `84fd7f991abd9ccead7b3b073293ba10d4197d2ed3837477d93702f9b3562ced` |
| macOS Intel | `816d5554459886c1cc70d8b12f43f12b1ddcb0826f5355f9e4e1ada0da38e6e7` |
| macOS Apple Silicon | `56294f63163735649226db13e6f3875be5af7e7ef1a954aade234e06db96d714` |
| Android preview.3 | `cc40e6dd7a08a3b642122d143621ef405aa61192d5c1a955a7cc9b943f895f8d` |
| macOS 服务端 | `ffc9db54e68b84456815d63d5ea5ab157555e9359efd24ccd4bd724c628f7e54` |

## 历史：Android preview.2（以下不是当前版本）

**当时桌面版本：`2.0.0`（未替换）；Android：`2.0.0-preview.2`，仅预览。**

2026-09-08 **11:57（北京时间）仅更新 Android APK**。公网完整下载为 25,522,471 字节，SHA-256 与本地签名包一致。桌面下载及其 09:39 发布日期保持不变，中转未重启（PID `48296`），设备、许可、配置及数据库未修改。Android 本轮发布与备份目录分别为 `releases/android-v2.0.0-preview.2`、`backups/android-v2.0.0-preview.2`。

本轮修复：紧凑仪表盘、满底蓝色 Yu 自适应图标、统一自定义弹窗及弹窗清理、横竖屏切换、模拟鼠标、点击前定位、拖动释放、双指滚动和按远端系统区分返回/桌面快捷键。Go race 三轮、19 项 Java 单测、四 ABI 构建、签名及 16KB 对齐通过；lint 为 0 错误、1 条本地空目录提示。**仍没有安卓真机验收，不承诺所有厂商兼容或系统绝对稳定。**

**Windows 无边框修复暂缓发布**：本地 Chrome/Edge 窗口几何、导航、缩放及十次隐藏/恢复通过，但退出后进程句柄未及时 signaled；无窗口嵌入的 Chromium 进程组对照也复现，普通控制台进程对照通过。根因尚未确定，不能把 Job 的 ActiveProcesses 为零当作完全退出。实验性 Windows 改动保留在本地，未混入本次 Android 源码提交，官网 Windows 包仍可能出现用户报告的系统标题栏。完整记录见 [本轮验证与未完成项](UI_STABILITY_20260908.md)。

以下为此前桌面部署与既有能力的记录，不表示本轮重新完成了全部验收：

- 官网与四个平台桌面客户端已部署，服务器 PID `48296`，网站保持 `http://www.yucg.cn:8235/`。官网显示发布日期 **2026-09-08 09:39（北京时间）**，下载名包含版本号。
- 新版官网包含项目简介、三张真实客户端界面的脱敏演示截图、功能布局示意、使用流程与平台边界。五种视口（320～1440px）的首屏下载、图片、无横向溢出、键盘导航及折叠内容检查通过。
- 只输入设备码可申请本次连接，60 秒等待对方同意，拒绝/过期/取消失效，错误 PIN 不降级审批。实际本机双客户端的同意控制、同意观看、拒绝、取消和完整 60 秒超时测试通过。
- Windows 按钮顺序为隐藏、最小化、关闭；最小化保留托盘、隐藏移除托盘；Ctrl+Y+U 或再次双击恢复。所有 X 退出主应用，“结束控制”返回首页。此前测试不足以证明 Chromium 自绘标题栏已完全移除，用户截图已确认该问题仍存在，见本轮未完成项。
- 全量 Go race/vet、Windows 交叉 vet、34 项前端测试、最终桌面二进制统一界面/生命周期/PIN/连接审批联调通过。没有主动锁屏或注入真实鼠标键盘。macOS/Linux 原生图形完整验收和长时间弱网体验仍待完成。
- 发布前在线与连接中均为 0；原设备、许可、配置、证书保留，发布前后记录数为 **0 / 8 / 9（账号 / 设备 / 授权码）**，SQLite 完整性通过。后台时间 `09:45:55` 与同次服务器北京时间一致；分页与 30 秒异步刷新通过。
- 四个平台公网完整下载 SHA-256、版本化文件名、三张截图、网站版本日期、管理登录保护和固定证书 TLS 1.3 校验通过。
- Android 当前为上述 `preview.2`；仍是调试签名，**未真机验收，系统声音/文件传输/剪贴板同步未实现**。详情见 [Android 状态](ANDROID.md)。原生鸿蒙与 iOS 按用户要求暂停。
- 服务端 SHA-256：`4bf3b0226d0254dbc668645052265b93daf80f72be66ab52725697c0835562a1`。桌面发布目录 `releases/v2.0.0`，旧版本和数据库备份 `backups/v2.0.0`；Android 发布目录 `releases/android-v2.0.0-preview.1`。
- 源码已推送既有私有仓库 `YUCONGGEN/yudesk`。GitHub Actions 不等于真机验证；初次 Android CI 因 SDK 工具路径失败，修复后重跑，最终状态以 Actions 为准。

当前下载 SHA-256：

| 平台 | SHA-256 |
| --- | --- |
| Windows x64 | `37e664190517ab7d9a4e2459c6cffb257178fe7375b0e9d3d2beb66dfe30ddd3` |
| Linux x64 | `5bf6aaa39f549464b5e1e1c4348a49fc38b98d6610facfb06e971d2424885944` |
| macOS Intel | `82eeb778ed4d656d7921173e5214214c8ef9df2c3e1368aadf8b6a49a354dcac` |
| macOS Apple Silicon | `d3dd33b154ce385f6f58fdfca5981dd44e9a553a2953e867345ccaf2db35338a` |
| Android 预览 | `f37c7dd04efc412354cfe9b53ce177511762f0425726dacca6c047818de2223f` |

网站“桌面 SHA-256 校验和”仅列四个桌面包；当前 Android 校验值见上表。既有 GitHub `v2.0.0` Release 附件未改写，仍是此前版本，不能当作 preview.2 下载。HTTP 下载不提供来源真实性保障，必须通过可信渠道比对校验值。

## v5 历史部署记录（已替换）

**当前版本：`20260908-stability-v5`，服务端和四个平台客户端均已部署。**

- 眼睛右侧新增同款“更换 PIN”图标；本机生成并保存新 6 位 PIN，后续连接拒绝旧 PIN，不打断已建立的会话。管理心跳同步到服务器，数据库提交后回传确认；后台每 30 秒异步刷新，支持立即刷新。
- 后台统一北京时间 UTC+8；最后在线改为真实心跳时间，后台刷新和授权修改不伪造在线时间。线上显示 `2026-09-08 07:35:37`，同次主机北京时间核对为 `2026-09-08 07:35:37`。
- 修复目录请求乱序、异常大小下载、声音查询重叠、取消监听抢占和静音后写截止时间残留。底部加入半透明设计者/联系方式/特色及居中的指定 ICP 链接和内置小徽标。见 [检查记录](ROBUSTNESS_20260908.md)。
- 全量 Go race/vet、Windows 交叉 vet、20 项 Node 通过。最终发布二进制：10 项旧客户端生命周期、5 项合并生命周期、合并界面/双端 PIN 更换与后台数据库同步、会话 X 返回通过；真实隔离窗口管理连续 3 轮通过。Windows 声音与文件传输回归通过，本机授权安装服务更新后普通权限截图通过。真机验收限制见检查记录。
- 发布前在线与连接中均为 0；短暂重启中转，当前 PID `30815`。账号/设备/授权码数量发布前后均为 `0 / 8 / 9`，SQLite 完整性通过，服务器配置、设备身份、授权与 TLS 证书保留。
- 公网四平台完整下载 SHA-256 全部匹配；健康检查、首页四个入口、旧入口跳转、管理员认证、分页、30 秒异步刷新、固定证书 TLS 1.3 全部通过。
- 服务端 SHA-256：`268cb3230345086c1ca2faf0656fb24aa571eab233cf51869db06221ca423c1d`。发布目录 `releases/20260908-stability-v5`，上一版本与数据备份 `backups/20260908-stability-v5`。
- GitHub 目标为既有私有仓库 `YUCONGGEN/yudesk`；标签 `20260908-stability-v5`，附件为四个平台合并客户端和对应平铺文件名校验清单。推送与发布结果以 GitHub 为准。

当前客户端 SHA-256：

| 平台 | SHA-256 |
| --- | --- |
| Windows x64 | `a2ab7717b1fcbc69eeb22c7201e8c29ee299296fa427b9e4afb91a02b7dcddde` |
| Linux x64 | `ec822b3f7c195554fafb3ad888c58bec3f0f9c72bbdee4b1d870bb59b160e788` |
| macOS Intel | `401d6f8862967b3a98f18093001cc872da68f85bbea42590f4aad3ea90af5496` |
| macOS Apple Silicon | `de0a3d909a05c7f215ce732eae6205fc0920118ea215866d8af534a1545fe7b7` |

## 响应优化 v4 发布记录（已被 v5 替换）

**当前客户端已更新**：`20260908-response-v4`。在下述紧凑界面和声音版本上继续降低响应开销：输入后短时自适应采样、单区域内联编码、一次预分配、Windows DIB 一遍转换复制。固定帧率模式及权限边界不变，设备授权继续位于设置内，不添加隐私屏/虚拟屏。

- 两轮本机合成桌面中位响应从 21.8–24.8ms 改善至 18.9–19.4ms；模拟增加 40ms、100ms RTT 的中位值也改善数毫秒。尾部仍有波动，不承诺零延迟或所有网络分位均变好。完整基线、较好额外基线及限制见 [本轮性能数据](LATENCY_20260908.md)。
- Go 全量 race/vet、Windows 交叉 vet、15 项 Node 通过；实际 Windows 声音加密到浏览器、文件完整性、画面/输入恢复、15 项生命周期、最终合并版界面及会话 X 返回回归通过。Windows 原始像素转换单元和本机服务普通权限截图复测通过。
- 四个平台已构建；只更新下载，中转未重启（PID `67009`），服务端程序、数据、授权和证书不变。发布目录 `releases/20260908-response-v4`，上版备份 `backups/20260908-response-v4`。本机授权安装的 Windows 服务程序同步更新。
- 公网四平台完整下载和证书检查正在发布后进行；GitHub 私有仓库推送在最终校验后执行。
- macOS 系统声音仍不支持，Linux 图形/声音未实机验收；没有主动锁屏/解锁、输入系统密码或注入真实鼠标键盘。既有验证限制继续适用。

当前客户端 SHA-256：

| 平台 | SHA-256 |
| --- | --- |
| Windows x64 | `c2669248c059fcad99ae6e3a7dd1d5d6c9a9961bc4446234d7e35adafa840782` |
| Linux x64 | `02cb5f4caab59e9d9391215ce2ba7c93a50c44e6c25a4ec00bc8efcb40628506` |
| macOS Intel | `515bc9c417897f3c88474674d6c90651a5377a9ea16dfed4b32edb7fd738087e` |
| macOS Apple Silicon | `7226030efd6fcdcd07ad1d57246933963d86e9bd9e0ac0b1054848372482b37d` |

## 紧凑窗口与声音发布记录

`20260908-compact-audio-v3`：四个平台合并客户端下载已替换，中转未重启，PID 为 `67009`；服务器程序、设备数据库、授权及证书未修改。旧客户端备份位于 `backups/20260908-compact-audio-v3`，本次发布目录为 `releases/20260908-compact-audio-v3`。

- 设备授权移入设置，默认折叠。860×600 紧凑窗口、短设备码/PIN 输入、固定不滚动页面、设备和文件分页、蓝色 Yu 图标（Windows EXE 已内嵌并提取验证）。
- 会话 X 返回主界面；主界面 X/退出结束主应用；隐藏真正关闭界面并继续在线，再次双击复用主进程。实际 Chromium target 销毁监听替代托管窗口 HTTP 长连接推断，修复间歇性关闭识别延迟。
- 新版两端按需启动/停止系统声音，采集与发送分离、有界排程、旧播放任务隔离。顶部延迟信号、喇叭开关、连接与声音详情、本机播放音量及失败提示；不添加隐私屏/虚拟屏/关闭显示器、麦克风或摄像头采集。
- Windows 实际 WASAPI 测试音采集通过；本机临时中转的真实端到端加密声音到 Chromium PCM 通过；快速开关、音量及文件上传下载 98,321 字节完整性验证通过。不是跨公网两机听感验收。
- 全量 Go race/vet 和 Windows 交叉 vet、15 项 Node 回归通过；10 项旧客户端及 5 项合并生命周期、最终发布版合并界面、会话 X 返回回归通过；真实隔离 Chromium 窗口管理连续 5 轮通过，无可见测试窗口或真实鼠标键盘注入。
- 经之前授权将本机可选系统服务更新到同一发布二进制；首次就绪检查早于 worker 启动，等待启动后服务与普通权限安装客户端桌面采集复测通过。未主动锁屏/解锁；不能把服务就绪等同于完整锁屏控制验收。
- Windows 声音已按上述范围验证；Linux 仍需 PulseAudio 兼容工具，未做图形/声音真机验收；macOS 本版本**不支持系统声音采集**，显示不可用。Windows/macOS/Linux 平台构建通过不等于各平台全部功能验收。
- 本轮发布时尚无 Git 提交或推送。发布后从公网完整下载四个平台程序，SHA-256 全部匹配本地清单；首页四个入口、旧链接跳转、未登录管理页 401、健康检查及固定证书 TLS 1.3 验证通过。

客户端 SHA-256：

| 平台 | SHA-256 |
| --- | --- |
| Windows x64 | `b7d392a811e05d6da16a8010803b55b61f4ca0a9c1b85532e0ebb832d1c74f92` |
| Linux x64 | `e8f15152804bfff7973af50d17ba67a3ac09bb31311a216de941512ecda27d30` |
| macOS Intel | `239b3df94e6a4d86d0cdedd25a096494fe059a733187b1bc98471409e331c899` |
| macOS Apple Silicon | `049a6fde8a1e8f2aa636bbb47bb178f964bcb8bdd619eed31eba1e024c66b4a9` |

界面与升级行为见 [窗口说明](WINDOW_LIFECYCLE.md)，声音使用和限制见 [声音说明](AUDIO.md)。

## 上一版桌面服务记录

以下记录只描述 `20260908-desktop-service-v2`：仅更新四个平台客户端下载，中转未重启，PID 为 `67009`，服务端及数据库、授权、证书未修改。更早客户端保留在 `backups/20260908-desktop-service-v2`，当时发布包在 `releases/20260908-desktop-service-v2`。

- 新增可选 Windows 管理员授权桌面服务；客户端设置提供安装、切换安装版、卸载和就绪状态提示。
- 修复 Windows 中文剪贴板，采用原生 Unicode API；“应用设置”显示等待、成功与失败，并有页面浮动提示。
- 桌面暂不可用时保持会话、隐藏旧画面和阻止新输入，恢复后强制完整画面同步，不再因一次采集失败永久停流。
- 当前开发电脑经用户明确授权安装了发布版 `C:\Program Files\YuDesk\yudesk.exe` 及 `YuDeskDesktop` 服务。服务启停、子进程回收、真实桌面采集、受限中等完整性令牌客户端和拒绝非安装版访问验证通过；两个后台服务进程无 TCP 连接或监听。
- 全量 Go race/vet、Windows 交叉静态检查、10 项 Node 测试、原生隔离剪贴板测试通过；Windows 体验和合并界面用发布版重新回归通过，文件传输字节校验通过。旧客户端 10 项和合并客户端 5 项生命周期测试通过。
- 发布后通过公网完整下载四个平台程序，SHA-256 全部与上表及本地清单一致；首页四个下载入口、旧链接跳转、管理页未登录拒绝、健康检查及 TLS 1.3 原证书指纹验证通过。
- **没有主动锁屏、输入 Windows 密码或注入真实鼠标键盘。** 因此实际 Windows 登录页、UAC 安全桌面、锁屏后解锁连续性还未完成真机验收。不能把服务管道和普通桌面采集通过等同于整个锁屏控制已验收。
- 仅支持 Windows 活动控制台会话，不支持 Windows RDP/注销后无人值守登录/Ctrl+Alt+Del 安全注意序列。macOS/Linux 已编译，未做图形真机验收。

客户端 SHA-256：

| 平台 | SHA-256 |
| --- | --- |
| Windows x64 | `e296331b667d8741062133d4e33e9f6b5d6e0fb319357e0dbc1e9b02c6d1b9a0` |
| Linux x64 | `98055e549c47b7f1b42c435df43f4c6e768279615b85996bb80915cd99fcbb6f` |
| macOS Intel | `7fa75b78c3263dae71676e8b7d9831db5cf7267f78dd40a1fa4a56a5c2416da2` |
| macOS Apple Silicon | `39911f2402d0973df4169470819c23576b23331e7ee988d258342022feb50249` |

安装与完整测试边界见 [Windows 桌面服务](WINDOWS_DESKTOP_SERVICE.md)。

## 合并版首发记录

**合并版已部署**：`20260908-unified-v1`。服务器 PID `67009`，服务端 SHA-256：`1e74b95eb8cf3cfc526dc1ab9ede90f857c227d94e0a1491cb120b0fe05f5a6d`。

- 首页现在发布 Windows x64、Linux x64、macOS Intel、macOS Apple Silicon 各一个 YuDesk 程序；控制和被控合一。
- 首页与远程会话均使用统一的浅色紧凑界面，包含设备列表、九位数字码、六位数字 PIN、默认折叠的授权输入。
- 保留设备身份、证书、授权和管理配置。更新前后账号/设备/授权码数量为 `0/8/9`，数据库完整性检查通过，旧设备已补齐九位码。
- Windows 原生联调、真实桌面接收、文件传输字节校验、15 项进程生命周期、10 项 Node 回归通过；全量 Go 竞态/静态检查通过，中转关闭与配对回归重复 15 轮通过。
- 管理页登录保护、认证后的设备表、分页和 30 秒异步刷新通过。HTTP 8235 / 固定证书 TLS 8233 不变。
- 公网完整下载四个平台客户端，SHA-256 全部与本地一致；TLS 1.3 证书指纹、首页四个下载入口、旧链接跳转、校验清单和健康检查通过。构建清单为 `dist/SHA256SUMS.txt`，仅包含四个合并客户端。
- 备份保留于服务器 `backups/20260908-unified-v1`；部署包在 `releases/20260908-unified-v1`。未推送 Git 提交。
- macOS/Linux 已构建，尚未完成图形真机验收；跨公网实际延迟、弱网和长期负载仍需验收，不保证零延迟或固定实际帧率。

实现、升级和测试说明见 [合并客户端](UNIFIED_CLIENT.md)。

## 上次发布记录（归档）

以下为 2026-09-07 的历史记录，其中“当前”等表述只指当时版本：

本地已有新的输入与文件修复构建，**尚未部署**；当前 `dist/` 不再是下述公网版本的原样副本。新版说明见 [输入及文件修复](INPUT_FILES_FIX.md)。

以下是上次已完成的公网发布记录，不能当作新版已经上线的证明，也不等于“已达到近乎零延迟”的完整产品验收。

- `dist/` 已用 Go 1.27.1 重新生成四个平台的 Agent、Viewer 和校验清单。Go 全量竞态/静态检查、9 项前端回归以及 Windows 原生体验/10 项进程生命周期测试通过。
- 本轮修复 Windows 构建运行时的定时精度、分散加密写入、逐帧 HTTP 取消，以及区域缓存锁范围和累计大小限制；新旧 Viewer 与 Agent 测试辅助程序交叉互通通过。
- 远程画面及周围黑边统一使用系统箭头，清除旧十字光标样式；Windows 无界面浏览器回归已验证普通、拖动、仅观看样式和全屏状态。四个平台共享此页面并已重新构建，macOS/Linux 未做原生光标实机验收。
- 网站和管理页继续使用 `http://www.yucg.cn:8235`；中转继续使用验证原证书指纹的 TLS 8233。没有新增公网端口，也没有推送 Git 提交。
- 当前客户端为 `20260907-arrow-latency-v2`，服务端叠加 `20260907-download-timeout-v3` 补丁。安装包已更新，旧客户端需退出后重新下载替换，不能只刷新旧程序的浏览器页面。
- 公网发布检查发现下载受 API 的 30 秒写入超时影响。补丁仅将通过白名单且实际存在的安装包响应延长到最多 10 分钟，管理接口超时不变；新增慢写、完整下载和 Range 断点续传回归，全量竞态与静态检查再次通过。
- 数据库、设备授权和原 TLS 证书保留。旧二进制及数据库备份位于服务器 `backups/20260907-arrow-latency-v2` 和 `backups/20260907-download-timeout-v3`，更新前后已有设备和授权码数量一致。
- 发布后从公网完整下载 8 个客户端，SHA-256 全部与本地 `dist/SHA256SUMS.txt` 一致；首页、健康检查及校验清单通过，未登录管理页返回 401。通过 SSH 内的本机请求验证了管理员设备列表、分页及异步刷新脚本，没有在公网测试中传输管理密码。
- 公网 `www.yucg.cn:8233` 的 TLS 1.3 握手及固定证书 SHA-256 校验通过；这是服务可达性和证书检查，不是两台异地机器的完整远控体验验收。最终服务端 SHA-256 为 `08d5ef100eea06400ecfaeeb6c59df63af8632281f19326d5207eb133fbe3d16`。
- 清理提示：本地临时 SSH 密钥副本 `E:\yudesk\.smoke\deploy-20260907-arrow.key` 的自动删除被执行策略拦截，仍需手动删除。该副本已禁用继承并仅授予当前用户访问权限，位于 Git 忽略目录；原始附件密钥未修改。
- 服务器上旧的 `staging/yudesk-low-latency.tgz` 不是本次构建；`releases/20260907-arrow-latency-v2` 中的服务端也未包含后续下载超时补丁。不能把旧暂存包当成当前完整版本重新部署。
- 当前仍是差分区域编码和端到端加密 TCP 中转。WebRTC/UDP、各平台原生硬件视频编码尚未实现。
- 已测的模拟像素响应仍存在尾部波动，异地两机、弱网、大面积滚动和视频场景未验收；不能保证稳定 30 FPS、ToDesk 同等响应或零延迟。

详细实现、剖析数据、测试方法与限制见 [低延迟设计与验收](LOW_LATENCY.md)。
