# YuDesk 受管端口映射

YuDesk 使用内嵌 FRP 客户端。设备只能把 `127.0.0.1` 上的 TCP 服务映射到服务器随机分配的 `9000–9500` 端口；每台在线设备最多 5 个，不允许客户端自选公网端口。设备退出、掉线、禁用、删除、授权到期或管理员关闭时，服务器会立即撤销凭据并拒绝新连接。

FRPS 必须使用项目 `third_party/frp` 固定的 [YUCONGGEN/frp](https://github.com/YUCONGGEN/frp) 提交 `8666e3643f4e8cc3ec65780c48e20c8904b17856`（FRP 0.70.1）构建。项目内快照额外修复了嵌入式客户端退出时的并发竞态，补丁说明见 `third_party/frp/YUDESK_PATCHES.md`。配置必须保留：

- `allowPorts = [{ start = 9000, end = 9500 }]`
- `maxPortsPerClient = 5`
- `transport.tls.force = true`
- 本机 `127.0.0.1:8236` 的 YuDesk HTTP 插件，以及全部列出的操作

Mac mini 使用 `deploy/macos-server/frps.toml`。将 `yudesk-port-hook.sh`、`yudesk-port-direct.sh` 和 `upnpc-dispatch.sh` 安装到 `~/bin/frp/`，并给 `yudesk-relay` 增加：

```text
-frp-public www.yucg.cn:8232
-frp-plugin-http 127.0.0.1:8236
-frp-cert /Users/yu/bin/frp/frps.crt
-frp-port-hook /Users/yu/bin/frp/yudesk-port-hook.sh
```

Mac mini 位于 NAT 后面时，路由器只需要固定映射 TCP 8232；用户端口由钩子通过现有 `upnpc` 按需开放。macOS 15 的后台进程可能受“本地网络”权限限制，因此当前 Mac mini 复用现有的 `frp_upnpc_loopback` 受限密钥：`authorized_keys` 的强制命令指向 `upnpc-dispatch.sh`，空命令保持原每日刷新，动态命令只接受 `yudesk-port open/close 9000–9500` 或 `cleanup`。证书必须包含 `DNS:www.yucg.cn` 的 SAN，私钥仅保存在服务器。

Linux 公网服务器开放 TCP 8232 和 9000–9500 防火墙范围即可，通常不填写 `-frp-port-hook`。Linux 位于支持 UPnP 的 NAT 后面时，可安装 `yudesk-port-hook-linux.sh` 并把它的绝对路径传给该参数。生产环境应由 systemd 分别守护 YuDesk 与 FRPS。

端口映射会把本机服务暴露到公网，应用本身仍需密码、TLS 和访问控制。界面及后台明确标注“禁止非法行为”，管理员可查询和关闭全部活动映射。
