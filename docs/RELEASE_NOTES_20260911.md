# YuDesk 2.0.0 多人音视频会议

本版本把临时共享升级为最多 8 人的小型音视频会议，并同步发布 Windows、Linux、macOS 与 Android `2.0.0-preview.10`。

主要更新：

- 填写姓名，使用 9 位会议号直接入会；发起人也会进入会议；
- 麦克风、摄像头、成员列表、主持人共享屏幕、转交主持人和本地录制；
- 桌面与 Android 横屏全屏会议界面；
- 原创浅色紧凑 UI，视觉语言参考 WeLink，不包含其品牌或资源；
- WebRTC/DTLS-SRTP 点对点媒体，Relay 只传经设备签名认证的信令；
- 修复合并欢迎报文丢失、无媒体入会 SDP 失败、成员离开后迟到 ICE 误断开三项稳定性问题。

验证：全量 Go short/vet、Android Go race/vet、65 项协议与窗口回归、Windows 原生双客户端会议联调、Windows/Android 公网会议联调、Android 四 ABI/Java/lint/签名/16 KiB 对齐、macOS 双架构真实工具链构建与验签、Ubuntu 22.04 Linux 构建均通过。

已知边界：会议为 P2P mesh，没有 TURN/SFU；严格 NAT 可能导致部分媒体无法直连，8 人高清视频也受设备和上行带宽限制。Android 录制不含会议/系统音频。macOS 包尚未 Developer ID 签名或公证，Android 尚未覆盖所有厂商物理真机。

部署批次：`multiparty-conference-20260911.1`
官网：http://www.yucg.cn:8235/
