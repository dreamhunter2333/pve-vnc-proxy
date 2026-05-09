# pve-vnc-proxy

无状态 PVE VNC 代理网关。把标准 VNC 客户端（TigerVNC / RealVNC）的 TCP 连接桥接到 Proxmox VE 的 `vncwebsocket` 端点，让你能用本地 VNC 客户端直接访问任意 PVE VM 的控制台，无需浏览器和 noVNC。

## 特性

- **单端口多 VM**：一个监听端口服务任意数量的 VM，路由信息由 VNC 客户端在握手时携带
- **零持久化凭证**：代理本身不保存任何 PVE token，每次连接由客户端用户名/密码字段提供
- **协议级桥接**：本地走标准 RFB 3.8 + VeNCrypt-Plain，上游走 PVE 的 RFB + VNCAuth(ticket)，由代理完成两侧握手翻译
- **透明转发**：握手完成后纯 `io.Copy` 双向桥接，无协议解析开销
- **仅依赖标准库 + `golang.org/x/net/websocket`**

## 工作流程

```
  VNC Client                  Proxy                       PVE
  ──────────                  ─────                       ───
   TCP ────────────────────────►
                            RFB 3.8 + VeNCrypt-Plain
                            ◄──── user/pass ─────
                            解出 node/vmid/token-id/secret
                                                POST /vncproxy
                                                ─────────────────►
                                                ◄──── ticket+port
                                                Dial wss://...?vncticket=
                                                ─────────────────►
                                                ◄──── 101 Switching
                                                RFB + VNCAuth(ticket DES)
                                                ─────────────────►
                            ◄──── SecurityResult OK ─────
                            
                            ═══ io.Copy 双向透明转发 ═══
```

代理不存储任何 PVE 凭证。所有身份信息由 VNC 客户端在握手时通过 VeNCrypt-Plain 的用户名/密码字段提供，使用完即弃。

## 编译

```bash
go build -o pve-vnc-proxy .
```

## 启动

```bash
export PVE_HOST=https://pve.example.com:8006
./pve-vnc-proxy -listen 127.0.0.1:5901
```

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| `-host` | `PVE_HOST` | 必填 | PVE 地址，例 `https://pve.example.com:8006`，缺省端口自动补 `:8006` |
| `-listen` | `PVE_LISTEN` | `127.0.0.1:5900` | 本地 TCP 监听地址 |
| `-insecure` | `PVE_INSECURE` | `false` | 跳过 PVE TLS 证书校验（自签证书时启用）|
| `-max-conns` | `PVE_MAX_CONNS` | `256` | 最大并发客户端连接数 |

> macOS 上端口 `5900` 通常被 Screen Sharing 占用，建议用 `5901+`。

## 创建 PVE API Token

Web UI：`Datacenter` → `Permissions` → `API Tokens` → `Add`，**取消勾选** Privilege Separation 后保存，复制 Token ID 与 Secret（只显示一次）。

或在 PVE 主机上：
```bash
pveum user token add root@pam vncproxy --privsep 0
```

如启用了 Privilege Separation，需另外授权：
```bash
pveum acl modify /vms --tokens 'root@pam!vncproxy' --roles PVEVMAdmin
```

## 使用 VNC 客户端连接

以 TigerVNC 为例，连接到代理监听地址（如 `localhost:5901`），客户端弹出登录窗口时填：

| 字段 | 内容 | 示例 |
|------|------|------|
| 用户名 | `<node>@<vmid>@<token-id>` | `pve@105@root@pam!vncproxy` |
| 密码 | `<token-secret>` | `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` |

切换不同 VM 只需改用户名里的 `vmid`，无需重启代理。

### 用户名格式解析

```
pve@105@root@pam!vncproxy
└┬┘ └┬┘ └─────┬─────────┘
 │   │        └── token-id（保留原样，含 @ 与 !）
 │   └── vmid
 └── PVE 节点名
```

代理用 `strings.Index` 从左切两刀：第一个 `@` 切 node，第二个 `@` 切 vmid，剩余全部当作 token-id。

## 客户端兼容性

需要支持 **VeNCrypt + Plain** 子类型的 VNC 客户端：

- ✅ TigerVNC（vncviewer）
- ✅ RealVNC Viewer
- ❌ Apple "Screen Sharing"（仅 VNC Auth）
- ❌ 老版 RealVNC Free（仅 VNC Auth）

## 安全

- **TLS 校验默认开启**：受信 CA 证书直接可用；自签场景请显式 `-insecure` 或 `PVE_INSECURE=true`
- **VeNCrypt-Plain 明文凭证**：本地 TCP 监听仅绑定 `127.0.0.1` 时安全；对外暴露请加 stunnel/SSH tunnel
- **代理不持久化任何 token**：内存中 token 仅在单次连接生命周期内存在
- **握手超时 15s**：防止慢速握手 DoS
- **`-max-conns` 上限**：默认 256，超过排队等待
- **上游响应大小受限**：API 响应 ≤1 MiB，RFB reason ≤4 KiB，防 OOM
- **日志不打印 token / secret**：仅记录 node、vmid、远端地址

## 限制

- 仅支持 PVE QEMU VM（`/nodes/{node}/qemu/{vmid}/vncproxy`）。容器（LXC）需把路径里的 `qemu` 换成 `lxc`，目前未实现
- VNC 客户端必须支持 VeNCrypt-Plain
- 不支持代理本身的 TLS（请用 stunnel 等前置）

## Docker

CI 推 tag 时通过 GitHub Actions 自动构建并发布多架构镜像（`linux/amd64`、`linux/arm64`）到 GHCR：

```bash
docker run --rm -p 5900:5900 \
  -e PVE_HOST=https://pve.example.com:8006 \
  ghcr.io/<owner>/pve-vnc-proxy:latest
```

容器内默认监听 `0.0.0.0:5900`。镜像由 `Dockerfile.ci` 构建，依赖 `.github/workflows/ci-docker-build.yml` 的交叉编译产物。

### docker compose

```yaml
services:
  pve-vnc-proxy:
    image: ghcr.io/<owner>/pve-vnc-proxy:latest
    container_name: pve-vnc-proxy
    restart: unless-stopped
    ports:
      - "5900:5900"
    environment:
      PVE_HOST: https://pve.example.com:8006
      PVE_LISTEN: 0.0.0.0:5900
```

启动：`docker compose up -d`。VNC 客户端连接 `<docker-host>:5900`，用户名 `<node>@<vmid>@<token-id>`，密码 `<token-secret>`。
