# Android 启动与下载修复（2026-09-10）

## 结论

用户报告“安卓打不开”后，服务器最近上线记录没有出现新的 Android 设备，故障发生在管理通道建立之前。官网旧 `preview.6` APK 的签名、清单和静态构建检查均正常，但在受限速的公网/容器链路复测时，44,711,068 字节的下载在约 18.3 MB 处中断；残缺文件安装时报 `INSTALL_PARSE_FAILED_UNEXPECTED_EXCEPTION`，这是与用户现象一致的一条可复现失败路径，但在未取得用户手机安装器提示或 logcat 前不声称它是唯一原因。

`preview.7 / 2000007` 同时修复下载和启动健壮性：

- 四 ABI 仍为 `arm64-v8a`、`armeabi-v7a`、`x86_64`、`x86`，原生库改为压缩存储并由 Android 安装时提取，通用 APK 降为 19,240,228 字节，比上一版减少约 57%。各 `libgojni.so` 的 ELF LOAD 段仍按 16 KiB 对齐。
- 下载响应保留 10 分钟写入期限，增加强 ETag、`Accept-Ranges: bytes` 和 `no-transform`，去除会阻止部分 Android 下载器保留残片的 `no-store`。公网 650 KB/s 限速完整下载得到 19,240,228 字节及正确 SHA-256；1 MiB Range 请求返回 `206` 和正确 `Content-Range`。
- Android 11 的系统栏 API和 Android 14 的 MediaProjectionConfig 移入按版本调用的专用类，降低 Android 8/9 厂商运行时提前解析新类的风险。
- 首次原生核心加载或首屏构造使用 `Throwable` 边界；JNI/ABI 链接错误不再直接退出，而显示统一样式的可恢复错误页，提供重试和退出。

## 验证

- Android 核心：`go test -race -count=3 ./core`、`go vet ./core`。
- Android UI：29 项 Java 单元测试、SDK 35 编译、lint、四 ABI gomobile、APK v2 签名、zipalign/16KB 检查。
- Android 8.0 / API 26 x86_64 软件模拟器：覆盖安装成功，`MainActivity` 冷启动成功，进程持续存活，版本为 `2.0.0-preview.7 / 2000007`，没有 YuDesk `AndroidRuntime` 崩溃；Go 核心成功连接服务器并显示“已就绪”。模拟器因容器无硬件虚拟化透传曾出现系统 System UI 无响应，这不是 YuDesk 进程崩溃。
- 根模块全量 `go test -short ./...` 和 Relay race 测试通过；下载服务数据库 `quick_check`、下载清单及公网完整下载通过。

APK SHA-256：`8b91875ff3109e7cc96cb58f6162bd2d77d842b494ed52a30c57c23c5d3c4fc0`。签名证书 SHA-256 保持 `c8e35f5904ad92ab55a651f940d531c1193042dcfc2ac1a98b7dadf0c3994a6a`。

## 仍需真机信息

本次没有连接 USB 真机，不能排除特定手机的安装来源限制、旧签名冲突、存储不足或厂商安全策略。如果手机显示“应用未安装”，先删除此次未完整下载的 APK并重新下载；如果明确提示签名冲突，需确认旧包来源后再决定是否卸载，卸载会清除该手机的 YuDesk 设备身份。若安装成功后仍立即退回桌面，应采集该手机的 Android 版本、型号和 `adb logcat`，不能用模拟器结论代替。
