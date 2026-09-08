# Android 延迟专项验证 — 2026-09-09

本次仅修改 `mobile/**` 与本文档。共享工作区内其他人的修改未回滚、未提交；未执行 git commit、push、部署或真机安装。Android 交付为可安装的原生预览 APK，供主任务与其他平台一起发布。

复核后的交付版本为 **2000005 / 2.0.0-preview.5**。review 指出的拖动问题成立：preview.4 的“保留 down/up 和终点”不足以保留画线轨迹，原生 Java 队列原先也有相同问题。本轮修正为仅合并 hover，下面的说明和结果均以 preview.5 为准。官网 preview.3 已实际下载并验签，不能再以本地 preview.2 代替当前官网基线。

## 已完成的改动

| 链路 | 原有问题 | 本次处理与边界 |
| --- | --- | --- |
| Android 控制端 → Go 网络发送 | 连续 hover 积压；preview.4 合并拖动会丢失路径顶点 | 保持 64 项容量，仅在所有鼠标按钮均松开时替换队尾相邻、坐标空间一致的单个 move 批次。按下状态跨出队保留，多个按钮分别跟踪。拖动及多事件批次不合并；down/up、按键、滚轮、文本、release、ACK、请求均为边界。满队列则断开，不能靠丢轨迹继续。 |
| Go 接收 → Android 无障碍输入泵 | Go 与 Java 两级队列都可能丢失拖动点 | 两级均改成只合并 hover。Go/Java 都在成功入队后更新按钮状态，出队不会清除按下状态，拒绝入队的 up 不会错误开启 hover 合并。原有会话编号、观看模式、共享、无障碍、管理授权检查保留；溢出/撤权会清队列、报告并释放。 |
| 原生触控采样 → RemoteView 输入 | PointerController 原有 16ms 节流和相对模式 slop 也会忽略拖动点；RemoteView 未转发历史采样 | 16ms 节流与 slop 仅用于未按下按钮的 hover。直接触控和显式拖动保留每个交给状态机的采样；RemoteView 按时间顺序转发 ACTION_MOVE/ACTION_UP 的历史采样，再处理当前坐标。 |
| 解码 → RemoteView | 解码工作线程等待一个携带旧 Bitmap 的 UI 回调；UI 恢复时先接收旧帧 | 单解码线程保留一张可替换的待显示 Bitmap，只安排一个待执行 UI 回调。回调取最新完成解码的帧。被替换且从未交给 Canvas 的 Bitmap 可回收；已经发布的 Bitmap 继续由图形系统管理。 |
| MediaProjection 采集 | 33ms 限流窗口内直接丢掉新图像；若屏幕随后静止，没有下一次回调可补上末帧 | 保留原 33ms 间隔，窗口内只安排一次到期采样；到期调用 `acquireLatestImage()`。尺寸变化时撤销旧采样并重置节拍。ImageReader 的 2 个图像槽、JPEG 质量、尺寸、帧率和码率配置均未调大。 |
| 标题栏与共享可见性 | AppTheme 已是 NoActionBar，但远程视图使用沉浸标志隐藏系统栏 | 保留 NoActionBar，去掉远程视图隐藏系统栏的标志。共享前台服务、持续通知、停止共享入口、MediaProjection 授权及系统提示继续保留。 |

帧仍协商为完整 JPEG，未启用可独立丢弃的增量块。Go 接收槽与发送槽继续取最新帧，ACK 仍随解码消费者取帧发出。网络中已开始发送的帧不被截断。新的解码交接允许 UI 暂时阻塞时继续解码，因此最多存在一张正在解码、一张待发布和一张当前发布的应用引用；这不是对 RenderThread/GC 内部内存的总量保证，也可能比原先暂停解码消耗更多 CPU。

版本从原 Gradle 源码的 preview.3，经首轮 preview.4 单调升至 `2000005 / 2.0.0-preview.5`，Go `core.Version` 同步为 preview.5。旧 preview.2 与首轮 preview.4 文件均保留；官网下载的 preview.3 存放在独立临时目录。

## 变更文件

- `mobile/core/queue.go`：固定容量、支持相邻移动替换的并发队列。
- `mobile/core/controller.go`：接入发送队列。
- `mobile/core/agent.go`：接入接收队列与可取消的等待。
- `mobile/core/core.go`：队列初始化、释放逻辑与核心版本。
- `mobile/core/core_test.go`：调整原有测试的队列构造与观察方式。
- `mobile/core/queue_test.go`：拥塞、移动合并、坐标空间、关键事件、权限、溢出、并发环绕和取消回归。
- `mobile/core/frame_latency_test.go`：最新帧选择、跳过帧的 ACK、ACK 背压解除后的最新采集帧回归。
- `mobile/android/app/build.gradle`：预览版本号。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/CapturePacer.java`：可测试的单次尾部采样节拍。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/CaptureService.java`：节拍接入及尺寸变化处理。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/FrameSlot.java`：线程安全的待发布最新帧与 Bitmap 所有权。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/RemoteView.java`：移除解码等待 UI 的 latch，使用最新待发布帧。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/InputQueue.java`：原生队列按按钮状态仅合并 hover，保留坐标空间边界。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/RemoteAccessibilityService.java`：将事件类型、按钮与坐标空间接入原生队列。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/PointerController.java`：拖动不再受 hover 节流与 slop 限制。
- `mobile/android/app/src/main/java/cn/yucg/yudesk/MainActivity.java`：保留系统栏可见。
- `mobile/android/app/src/test/java/cn/yucg/yudesk/CapturePacerTest.java`：末帧到期采样、回调洪流、慢采集和尺寸重置。
- `mobile/android/app/src/test/java/cn/yucg/yudesk/FrameSlotTest.java`：阻塞 UI 下的最新帧、单回调、关闭回收和并发所有权。
- `mobile/android/app/src/test/java/cn/yucg/yudesk/InputQueueTest.java`：hover、折线路径、多按钮、溢出与坐标空间边界。
- `mobile/android/app/src/test/java/cn/yucg/yudesk/PointerControllerTest.java`：新增快速直接画线、相对拖动小位移路径回归。
- `docs/ANDROID_LATENCY_20260909.md`：本记录。

## 构建与测试

使用既有容器 `yudesk-android-verify-20260908`；`E:\yudesk` 绑定为 `/src`。Go 为 `go1.27.1 linux/amd64`，Android SDK `/opt/android-sdk`，NDK `27.3.13750724`，build-tools `35.0.0`，Gradle `8.11.1`。

先在 `/src/mobile` 运行 `go test -race ./core` 和 `go vet ./core`，均通过。完整构建采用现有 `mobile/scripts/verify-local-container.sh`，它与 `build-android.sh` 使用相同的四 ABI gomobile 绑定及 Android 构建流程，并复制到容器原生文件系统以避免 Windows 绑定目录的 JAR I/O 开销。脚本未修改。

PowerShell 运行命令：

```powershell
docker exec yudesk-android-verify-20260908 sh -c 'exec flock -n /tmp/yudesk-android-build.lock env ANDROID_HOME=/opt/android-sdk ANDROID_NDK_HOME=/opt/android-sdk/ndk/27.3.13750724 YUDESK_VERIFY_OUTPUT=/src/mobile/build/android-latency-review-20260909 bash /src/mobile/scripts/verify-local-container.sh'
```

preview.5 原生暂存目录为 `/tmp/yudesk-android-final.AZxO1D`；首轮 preview.4 为 `/tmp/yudesk-android-final.YCyVl2`。构建前检查无其他 Android 构建；本次持有独占锁。构建后对比暂存与工作区的 `mobile/core`、Android `app/src` 和 `app/build.gradle`，内容一致。

验证结果：

- `go test -race -count=3 ./core`：通过，涵盖原有 PIN/本机审批/拒绝/过期/旧会话隔离/断网/双向协议/P2P/分片兼容测试及新增测试。
- `go vet ./core`：通过。
- `:app:testDebugUnitTest`：29 项通过，0 失败、0 错误、0 跳过。包括 12 项 PointerController 和 5 项 InputQueue 测试，验证逐点折线、down/drag/up、最终坐标、取消、旋转与坐标缩放。
- `:app:lintDebug`：通过，0 错误；1 项现有资源目录的 `ObsoleteSdkInt` 警告（`mipmap-anydpi-v26` 与 minSdk 26 重复）。构建还输出 SDK XML 工具版本兼容提示和弃用 API 提示，未阻止构建。
- `:app:assembleDebug`：成功；包含 `arm64-v8a`、`armeabi-v7a`、`x86`、`x86_64`。
- `apksigner verify --verbose --print-certs`：通过，v2 签名有效，1 个签名者。
- `zipalign -c -P 16 -v 4`：通过；这是 APK 打包对齐验证，未进行 16KB 页大小设备上的运行验证。
- `aapt2 dump badging/xmltree/resources`：包名 `cn.yucg.yudesk.preview`、minSdk 26、targetSdk 35、版本 2000005、Launcher Activity 和权限配置正确。应用资源沿用已确认的 NoActionBar 主题：`0x7f060000` 的父主题 `0x01030241`；SDK 的 `android.R.style.Theme_Material_Light_NoActionBar` 常量为 16974401（即该十六进制值）。这是源码与打包资源级的无应用标题栏确认，未进行真机截图确认。
- `git diff --check -- mobile`：通过。

新增压力测试的 8,000 次 **hover** 可合并成一个 move。Go 发送、Go 接收、Java 输入队列分别验证 down 已出队后 48 个交替折线顶点全部按序保留；还验证多按钮部分抬起、release/clear、多事件批次、满队列拒绝 up 后仍保持按下状态。PointerController 的 1ms 间隔画线及小于 slop 的显式拖动也逐点验证。10,000 次未显示的解码结果仍只保留最新待发布帧和一个 UI 回调。这些是确定性的队列/采样语义测试，不是帧率或端到端延迟实测。

拖动拥塞到达原有容量时会中止、释放并报告错误，不承诺无限速无损绘图；它不会为了维持表面流畅而悄悄把折线路径改成端点连线。系统实际接收与呈现全部采样的效果仍需真机验证。

## 产物与签名比较

构建完成后将已验证 preview.5 APK 同步到通用产物入口，供后续统一发布流程读取；保留 preview.2/preview.4 原文件。已验证 AAR 也同步到 Android 应用的 `libs` 目录。

| 产物 | Windows 路径 |
| --- | --- |
| 通用发布入口，现为 preview.5 | `E:\yudesk\mobile\build\YuDesk-2.0.0-android-preview.apk` |
| 带版本 APK | `E:\yudesk\mobile\build\YuDesk-2.0.0-preview.5.apk` |
| 本次独立 APK | `E:\yudesk\mobile\build\android-latency-review-20260909\YuDesk-2.0.0-android-preview.apk` |
| 本次 AAR | `E:\yudesk\mobile\build\android-latency-review-20260909\yudesk-core.aar` |
| 应用使用的 AAR | `E:\yudesk\mobile\android\app\libs\yudesk-core.aar` |
| 校验和 | `E:\yudesk\mobile\build\SHA256SUMS.txt` 与 `E:\yudesk\mobile\build\android-latency-review-20260909\SHA256SUMS.txt` |
| 单元测试、lint、签名及对齐报告 | `E:\yudesk\mobile\build\android-latency-review-20260909\verification\` |
| 保留的旧 APK | `E:\yudesk\mobile\build\YuDesk-2.0.0-preview.2.apk` |
| 官网 preview.3 下载副本 | `E:\yudesk\mobile\build\android-review-20260909.o5D8P7\official-preview.apk` |
| 本轮 APK 条目及 Go 构建信息比较 | `E:\yudesk\mobile\build\android-review-20260909.o5D8P7\apk-inventory-reviewed.json` |

新 APK 大小为 **44,572,924 字节**。上述三个 preview.5 APK 文件内容相同，SHA-256：

```text
2caac0f0b2e1e3d2c60978610f6f379b568629af91ac889bba0676f5554ca79d
```

旧 preview.2 APK SHA-256：

```text
f37c7dd04efc412354cfe9b53ce177511762f0425726dacca6c047818de2223f
```

官网 preview.3 通过用户指定的 [下载地址](http://www.yucg.cn:8235/download/android/yudesk.apk) 下载。先用 `mktemp -d /src/mobile/build/android-review-20260909.XXXXXX` 新建私有目录，再以 curl 下载到 `.part` 文件；限制 HTTP/HTTPS、20 秒连接超时、180 秒总超时和 100MiB 上限，完整下载后重命名为 `official-preview.apk`。未运行或安装下载内容。其实际版本为 `2000003 / 2.0.0-preview.3`，大小 **44,504,424 字节**，SHA-256 与 review 给定值一致：

```text
cc40e6dd7a08a3b642122d143621ef405aa61192d5c1a955a7cc9b943f895f8d
```

官网 preview.3、旧 preview.2、已有 preview.1 Gradle 输出及 preview.4/preview.5 的签名证书 SHA-256 一致：

```text
c8e35f5904ad92ab55a651f940d531c1193042dcfc2ac1a98b7dadf0c3994a6a
```

证书 DN 为 `C=US, O=Android, CN=Android Debug`，RSA 2048。包名一致、版本提高、签名证书一致，满足该预览系列覆盖更新的这些静态前提；实际设备上的更新安装未测试。此产物仍是原有调试签名预览 APK，不是应用商店正式签名版本。

官网副本的 `apksigner verify --verbose --print-certs` 与 `aapt2 dump badging` 原始输出保存在下载目录内的 `official-signature.txt`、`official-badging.txt`。

## APK 从约 25MB 增至约 44.6MB 的原因

这里的 25MB 基线是本地 preview.2，当前官网 preview.3 已经约 44.5MB。逐个 ZIP 条目统计如下，单位均为字节；“其他条目”按 ZIP 内实际占用计算：

| 版本 | 整包 | 四 ABI 原生库 | 其他条目 | ZIP/签名/对齐开销 |
| --- | ---: | ---: | ---: | ---: |
| 本地 preview.2 | 25,522,471 | 25,346,256 | 75,270 | 100,945 |
| 官网 preview.3 | 44,504,424 | 44,327,560 | 136,750 | 40,114 |
| 首轮 preview.4 | 44,572,060 | 44,390,488 | 137,196 | 44,376 |
| 本轮 preview.5 | 44,572,924 | 44,394,680 | 138,300 | 39,944 |

preview.2 → 官网 preview.3 整包增加 **18,981,953** 字节，其中四个 `libgojni.so` 增加 **18,981,304** 字节，其他压缩条目增加 61,480 字节，对齐等开销减少 60,831 字节。原生库分别从约 6.1–6.7MB 增至约 10.8–11.8MB。

从 APK 中取固定的 `lib/arm64-v8a/libgojni.so` 供 `go version -m` 只读检查：preview.2 的构建依赖没有 Pion；官网 preview.3 已包含 `github.com/pion/webrtc/v4 v4.2.20`、ICE、DTLS、SCTP、STUN、TURN、datachannel、`anet` 和相关 `golang.org/x/*` 依赖，与新增 P2P/peerpath 协议栈相符。这些依赖继续存在于 preview.4/preview.5。主增量发生于官网 preview.3 已有的 Go 原生协议栈，并非本轮队列修改造成 19MB 增量；未做逐包裁剪重编译，不能将每个字节归因到某一个依赖。

四个版本均包含相同四种 ABI，原生库的 ZIP 压缩方法均为 **0（Stored，不压缩）**；Go 都是 1.27.1，均带 `-s -w` 和 16KB 页对齐链接参数。因而这不是本轮增加 ABI、打开调试符号或切换原生库压缩方式导致的体积变化。

相对于当前官网 preview.3，首轮 preview.4 仅增加 **67,636 字节（约 0.152%）**；本轮 preview.5 增加 **68,500 字节（约 0.154%）**。可复核的 ZIP 条目尺寸、压缩方法、逐文件 SHA-256 和 Go 构建信息均在 `apk-inventory-reviewed.json` 中，检查脚本也保存在同一临时目录。

## 验证限制与交接

未连接或操作真实 Android/Windows 键盘，未安装到真机，未运行设备端 MediaProjection、Bitmap 解码、Accessibility 手势、OEM 隐私指示或端到端网络延迟测量。Java 单元测试使用普通对象验证 Bitmap 生命周期协议，不能替代 Android RenderThread 的设备测试。

本次证据支持“避免这些可确认的旧事件/旧帧积压与末帧丢弃”，不支持“零延迟”或任何具体毫秒收益。真实网络 RTT、视频压缩/解码成本、Android 调度和系统手势耗时仍存在。

Android 源码及本地产物已就绪，统一发布由主任务处理；根 Go 包、桌面窗口、共享构建/发布文件、`packaging/**` 和 `scripts/package-unix.*` 均不属于本次修改范围。
