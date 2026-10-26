# Xget-Go 🚀

**单二进制的多平台开发者资源加速引擎** —— [xixu-me/Xget](https://github.com/xixu-me/Xget)（Cloudflare Workers / JavaScript）核心逻辑的 **Go 移植版**。

Xget-Go 将 Xget 的核心加速能力编译为一个 **约 6 MB 的静态单文件**：不依赖 Node.js、workerd 或任何运行时，拷贝到服务器即可运行。它为代码托管平台、AI 模型仓库、包管理器和容器镜像仓库提供统一的下载/拉取加速入口，**URL 结构与官方 Xget 完全同构**，客户端配置可直接沿用官方生态的既有约定。

核心亮点：

- **零依赖**：纯 Go 标准库实现，`CGO_ENABLED=0` 静态编译，无任何第三方包
- **低占用**：空载内存约 10 MB 级，单进程即可支撑高并发流式转发
- **协议兼容**：Git Smart HTTP（clone/pull/push/LFS）、Docker Registry v2 原生透传
- **生产就绪**：内置健康检查、优雅关闭、结构化日志、JSON 错误契约与安全响应头
- **许可证一致**：AGPL-3.0-or-later，与上游 Xget 相同

---

## 目录

- [功能特性](#功能特性)
- [与官方版的差异](#与官方版的差异)
- [支持的平台前缀](#支持的平台前缀)
- [快速开始](#快速开始)
- [部署教程](#部署教程)（systemd / Docker / 源码 / 反向代理 / HTTPS）
- [配置参考](#配置参考)
- [鉴权详解](#鉴权详解)
- [使用示例](#使用示例)
- [运维手册](#运维手册)
- [安全加固清单](#安全加固清单)
- [架构与项目结构](#架构与项目结构)
- [实测覆盖场景](#实测覆盖场景)
- [路线图](#路线图)
- [致谢与许可证](#致谢与许可证)

---

## 功能特性

| 能力 | 说明 |
|------|------|
| 平台前缀路由 | 23 个高频平台，最长前缀优先匹配（`/pypi/files/` 优先于 `/pypi/`） |
| Range 断点续传 | 原样透传 `Range` 请求头与 `206 Partial Content` 响应 |
| 失败重试 | 最多 `MAX_RETRIES` 次，线性退避（`RETRY_DELAY_MS × 第 N 次`） |
| 响应头超时 | 单次尝试 `TIMEOUT_SECONDS` 秒内未收到响应头则重试；**大文件流式传输不限时** |
| HEAD 探测回填 | 上游 `HEAD` 响应缺 `Content-Length` 时自动改用 `Range` 探测回填，保证 `wget`/下载工具显示进度 |
| Git Smart HTTP | `git clone / pull / push` 与 `/info/refs`、`git-upload-pack`、`git-receive-pack` 原样透传 |
| Git LFS | `/info/lfs`、`objects/batch`、SHA-256 对象路径识别与透传 |
| Docker Registry v2 | 六大容器仓库加速，支持 `daemon.json` 镜像源配置，匿名 token 自动重试 |
| 实例鉴权 | `X-Xget-Token` 头 / `Authorization: Bearer` / `?xget_token=` 三通道，常量时间比较 |
| 请求校验 | 路径穿越拦截（`..`）、控制字符过滤、URL 长度上限、方法白名单、CORS 白名单 |
| 安全响应头 | HSTS、CSP、`X-Content-Type-Options`、`Referrer-Policy` 等 |
| 缓存提示 | 200/206 响应附加 `Cache-Control: max-age=CACHE_TTL`；带凭证请求自动降级为 `private` 并追加 `Vary` |
| 可观测性 | `/healthz` 存活探针、结构化访问日志、`X-Performance-Metrics` 性能头 |

## 与官方版的差异

本移植版是 **标准版**：聚焦"前缀加速 + 流式转发"主干，官方版的边缘缓存与部分增值能力不在范围内。

| 能力 | 官方版 (Workers) | 本移植版 |
|------|-----------------|----------|
| 部署形态 | Cloudflare Workers / Docker(workerd) | **单二进制 / systemd / Docker / 裸机** |
| 平台前缀 | 100+ 全量 | 核心 23 个高频平台 |
| URL 前缀改写 + 转发 | ✅ | ✅ |
| Range 断点续传 | ✅ | ✅ |
| 3 次线性退避重试 + 超时 | ✅ | ✅（超时只约束"响应头到达"，大文件流式传输不限时） |
| HEAD Content-Length 回填探测 | ✅ | ✅ |
| 安全响应头 (HSTS/CSP 等) | ✅ | ✅ |
| 请求校验（穿越/控制字符/方法白名单） | ✅ | ✅ |
| `X-Performance-Metrics` 性能头 | ✅ | ✅ |
| Git 智能 header 优化 / push | ✅ | ✅ 基础版（header 透传，clone/pull/LFS 实测可用） |
| Docker 私有仓库登录链 (`/v2/auth`) | ✅ | ❌ 仅公共镜像匿名拉取（含匿名 token 自动重试），`/v2/auth` 返回 `501` |
| AI 推理前缀 (`ip-*`) / 内容重写 | ✅ | ❌ |
| 边缘缓存 (Cloudflare Cache API) | ✅ | ❌（纯透传，依赖上游 CDN） |

## 支持的平台前缀

> 规则：前缀中的 `-` 对应 URL 中的一级目录。例如平台键 `pypi-files` 的 URL 前缀是 `/pypi/files/`。

### 代码托管

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `gh` | `github.com` | GitHub release / archive / raw / git clone |
| `gist` | `gist.github.com` | GitHub Gist |
| `gl` | `gitlab.com` | GitLab |
| `gitea` | `gitea.com` | Gitea |
| `codeberg` | `codeberg.org` | Codeberg |

### AI / 模型仓库

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `hf` | `huggingface.co` | 模型/数据集文件、`HF_ENDPOINT` |
| `civitai` | `civitai.com` | 模型下载 API |

### 包管理

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `npm` | `registry.npmjs.org` | npm/yarn/pnpm registry |
| `pypi` | `pypi.org` | pip 索引 `/pypi/simple/` |
| `pypi-files` | `files.pythonhosted.org` | pip 包文件下载（手动构造 URL） |
| `conda` | `repo.anaconda.com` | Anaconda 官方频道 |
| `conda-community` | `conda.anaconda.org` | conda-forge 等社区频道 |
| `maven` | `repo1.maven.org` | Maven Central |
| `golang` | `proxy.golang.org` | `GOPROXY` |
| `crates` | `crates.io` | Cargo（自动改写到 `/api/v1/crates`） |
| `nuget` | `api.nuget.org` | NuGet v3 |
| `rubygems` | `rubygems.org` | Bundler 镜像 |

### 容器镜像

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `cr-docker` | `registry-1.docker.io` | Docker Hub |
| `cr-ghcr` | `ghcr.io` | GitHub Container Registry |
| `cr-quay` | `quay.io` | Quay.io |
| `cr-gcr` | `gcr.io` | Google Container Registry |
| `cr-mcr` | `mcr.microsoft.com` | Microsoft Artifact Registry |
| `cr-k8s` | `registry.k8s.io` | Kubernetes 镜像 |

---

## 快速开始

从发布包 `xget-go-v1.0.0.zip` 中取出对应架构的二进制（`uname -m` 确认：`x86_64` → `xget-linux-amd64`，`aarch64` → `xget-linux-arm64`，Apple Silicon → `xget-darwin-arm64`）：

```bash
# 前台试用（默认 8080 端口、无鉴权 —— 仅限内网/本机！）
./xget-linux-amd64

# 生产运行：务必设置 token
PORT=8080 ACCESS_TOKEN="$(openssl rand -hex 24)" ./xget-linux-amd64
```

启动后验证：

```bash
curl http://127.0.0.1:8080/healthz          # → ok（健康探针，免鉴权）
curl -sI "http://127.0.0.1:8080/gh/xixu-me/Xget" | head -5
```

> 完整的生产部署（systemd / Docker / HTTPS 反代）见下一章 [部署教程](#部署教程)。

## 部署教程

本章覆盖四种部署方式与配套的网络配置。**推荐组合：systemd（或 Docker）+ Nginx/Caddy 反向代理 + HTTPS**。

| 方式 | 适用场景 | 难度 |
|------|----------|------|
| [前台运行](#方式一前台运行试用调试) | 快速试用、本地调试 | ★ |
| [systemd 服务](#方式二systemd-生产部署推荐) | Linux 服务器长期运行 | ★★（推荐） |
| [Docker](#方式三docker-部署) | 容器化环境、与其它服务统一编排 | ★★ |
| [源码构建](#方式四从源码构建部署) | 自行修改代码、需要其它平台二进制 | ★★★ |

### 前置要求

- 一台可访问公网的服务器（xget-go 需要能连通各上游平台）
- 对应架构的二进制：`x86_64` 用 `xget-linux-amd64`，`aarch64` 用 `xget-linux-arm64`
- （可选）一个解析到服务器 IP 的域名 + `80/443` 端口，用于 HTTPS 反代
- （方式四）Go 1.23+ 工具链

### 方式一：前台运行（试用/调试）

```bash
chmod +x xget-linux-amd64
sudo mv xget-linux-amd64 /usr/local/bin/xget-go

PORT=8080 ACCESS_TOKEN="$(openssl rand -hex 24)" xget-go
# Ctrl+C 停止
```

启动日志示例：

```
xget-go v1.0.0 starting — port=8080 auth=token retries=3 timeout=30s platforms=23
listening on http://0.0.0.0:8080 — usage: http://<host>:8080/<prefix>/<path>
```

> ⚠️ 前台进程会随 SSH 会话断开而退出，生产环境请使用 systemd 或 Docker。

### 方式二：systemd 生产部署（推荐）

**第 1 步：安装二进制**

```bash
sudo mv xget-linux-amd64 /usr/local/bin/xget-go
sudo chmod 0755 /usr/local/bin/xget-go
xget-go --version        # → xget-go v1.0.0 (port of xixu-me/Xget, AGPL-3.0-or-later)
sudo mkdir -p /var/lib/xget /etc/xget
```

**第 2 步：生成配置文件**（推荐用 `EnvironmentFile` 管理 token，避免明文写进 unit）

```bash
sudo sh -c 'echo "ACCESS_TOKEN=$(openssl rand -hex 32)" > /etc/xget/xget.env'
sudo chmod 600 /etc/xget/xget.env
# 可按需追加：PORT=8080、TIMEOUT_SECONDS=30 等，见《配置参考》
```

**第 3 步：安装服务单元**（发布包内已提供 `xget.service`，含安全加固）

```bash
sudo cp xget.service /etc/systemd/system/xget.service
```

编辑 `/etc/systemd/system/xget.service`，加载配置文件并设置工作目录权限：

```ini
[Service]
EnvironmentFile=/etc/xget/xget.env        # ← 新增这一行
# 以下为发布包默认已含的加固项，无需改动：
# DynamicUser=yes / NoNewPrivileges=yes / ProtectSystem=strict ...
```

> 发布包默认 `ExecStart=/usr/local/bin/xget-go`、`Environment=PORT=8080`；`DynamicUser=yes` 会让服务以随机非特权用户运行。若需自定义端口等，直接在 unit 里加 `Environment=XXX=...` 或写进 `/etc/xget/xget.env`。

**第 4 步：启动并设置开机自启**

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now xget          # 单元名取自文件名 xget.service
sudo systemctl status xget                # 确认 active (running)
```

**第 5 步：本机验证**

```bash
curl http://127.0.0.1:8080/healthz                                        # → ok
curl -sI -H "X-Xget-Token: $(sudo cat /etc/xget/xget.env | cut -d= -f2)" \
  "http://127.0.0.1:8080/gh/microsoft/vscode/archive/refs/heads/main.zip" | head -3
# → HTTP/1.1 200 OK（或 302 跟随到 release CDN，均为正常）
```

**第 6 步：继续配置 HTTPS 反向代理**（见下文 [反向代理与 HTTPS](#反向代理与-https)，强烈建议）

常用管理命令：

```bash
sudo systemctl restart xget        # 重启
sudo systemctl stop xget           # 停止
journalctl -u xget -f              # 实时日志
journalctl -u xget --since today   # 今日日志
```

### 方式三：Docker 部署

发布包内含 `Dockerfile`（基于 `alpine:3.20` + ca-certificates，构建上下文只取对应架构的二进制）。

```bash
cd xget-go
docker build -t xget-go:1.0.0 .

docker run -d --name xget \
  -p 127.0.0.1:8080:8080 \
  -e ACCESS_TOKEN="$(openssl rand -hex 32)" \
  --restart unless-stopped \
  xget-go:1.0.0

# 验证
curl http://127.0.0.1:8080/healthz    # → ok
docker logs -f xget                   # 查看日志
```

> `-p 127.0.0.1:8080:8080` 只把端口绑定到本机回环，供反向代理转发；如需直接暴露请改为 `-p 8080:8080` 并自行承担安全风险。

`docker-compose.yml` 写法：

```yaml
services:
  xget:
    build: .
    image: xget-go:1.0.0
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      - ACCESS_TOKEN=${XGET_TOKEN}     # 放在 .env 文件中
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/healthz"]
      interval: 30s
      timeout: 3s
```

arm64 服务器上 `docker build` 会经 BuildKit 的 `TARGETARCH` 自动选用 `xget-linux-arm64`（多架构构建需 `docker buildx` + QEMU）。

### 方式四：从源码构建部署

```bash
git clone https://github.com/ssdxftx/xget-go.git && cd xget-go
./build.sh v1.0.0        # 产出 linux-amd64 / linux-arm64 / darwin-arm64 三个静态二进制
# 或手动构建：
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o xget-linux-amd64 .
```

交叉编译任意平台（零依赖，装好 Go 即可）：

| 目标平台 | 环境变量 |
|----------|----------|
| Linux x86_64 | `GOOS=linux GOARCH=amd64` |
| Linux arm64 | `GOOS=linux GOARCH=arm64` |
| macOS Apple Silicon | `GOOS=darwin GOARCH=arm64` |
| macOS Intel | `GOOS=darwin GOARCH=amd64` |
| FreeBSD | `GOOS=freebsd GOARCH=amd64` |
| Windows | `GOOS=windows GOARCH=amd64`（产物为 `.exe`） |

示例：

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o xget-darwin-amd64 .
```

构建完成后按 [方式二](#方式二systemd-生产部署推荐) 或 [方式三](#方式三docker-部署) 部署即可。

### 反向代理与 HTTPS

生产环境强烈建议套一层 HTTPS 反代：Git/Docker 客户端在纯 HTTP 下会遇到各种限制（明文 token、Docker 需要 `insecure-registries` 例外等），且反代可以缓冲慢客户端、隐藏后端进程。

#### Caddy（自动签发证书，最省事）

`/etc/caddy/Caddyfile`：

```caddy
xget.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

无其它配置 —— Caddy 自动处理证书续期与流式转发。

#### Nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name xget.example.com;

    ssl_certificate     /etc/letsencrypt/live/xget.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/xget.example.com/privkey.pem;

    # —— 以下五项对 Git push / Docker push / 大文件至关重要 ——
    client_max_body_size 0;           # 不限制请求体大小（Git push 大对象）
    proxy_request_buffering off;      # 请求体流式转发，不落盘
    proxy_buffering off;              # 响应流式转发，边收边发
    proxy_http_version 1.1;           # chunked 传输必需
    proxy_set_header Connection "";

    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_connect_timeout 15s;
    proxy_read_timeout 600s;          # 大文件下载/克隆拉长读超时
    proxy_send_timeout 600s;

    location / {
        proxy_pass http://127.0.0.1:8080;
    }
}

server {
    listen 80;
    server_name xget.example.com;
    return 301 https://$host$request_uri;
}
```

证书签发（certbot）：

```bash
sudo certbot --nginx -d xget.example.com
```

### 防火墙配置

```bash
# UFW（Ubuntu/Debian）
sudo ufw allow 80,443/tcp            # 放行反代
sudo ufw deny 8080/tcp               # 禁止直连后端（如未绑定 127.0.0.1）

# firewalld（RHEL/CentOS/Rocky）
sudo firewall-cmd --permanent --add-service={http,https}
sudo firewall-cmd --reload
```

> xget-go 自身没有"仅绑定内网 IP"的选项（`PORT` 只决定端口）。若必须直接暴露（无反代），请用防火墙/安全组把端口限制到可信网段，并**务必**设置 `ACCESS_TOKEN`。

### 部署验证清单

全部完成后逐项确认：

```bash
curl -s https://xget.example.com/healthz                        # ① ok
curl -sI https://xget.example.com/gh/ | head -1                 # ② 302（裸前缀跳主页）
git clone https://xget.example.com/gh/octocat/Hello-World.git   # ③ git 加速
docker pull xget.example.com/cr/docker/library/alpine:latest    # ④ 镜像加速（若未开 token）
curl -s -o /dev/null -w "%{http_code}\n" \
  -H "X-Xget-Token: <token>" \
  https://xget.example.com/hf/bert-base-uncased/resolve/main/config.json   # ⑤ 200
```

## 配置参考

全部配置通过环境变量注入（与官方 Workers 版的变量名保持一致），无配置文件。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8080` | 监听端口（监听 `0.0.0.0`） |
| `ACCESS_TOKEN` | 空（**关闭鉴权**） | 实例访问 token。为空时实例对任何可达者开放，仅限内网 |
| `TIMEOUT_SECONDS` | `30` | 单次尝试的"响应头超时"（秒）。只约束到上游建立连接并收到响应头；之后的流式传输不受此限制，大文件不会中途超时 |
| `MAX_RETRIES` | `3` | 上游请求失败的最大尝试次数 |
| `RETRY_DELAY_MS` | `1000` | 线性退避基数：第 N 次重试前等待 `N × RETRY_DELAY_MS` 毫秒 |
| `MAX_PATH_LENGTH` | `2048` | URL 路径长度上限，超长返回 `414` |
| `CACHE_TTL` | `1800` | 200/206 响应附加的 `Cache-Control: max-age` 提示（秒）。带 `Authorization`/`Cookie` 的请求自动降级为 `private, max-age=…` 并追加 `Vary: Authorization, Cookie` |
| `ALLOWED_METHODS` | `GET,HEAD` | 非 Git/LFS/Docker 协议请求的方法白名单。需要 npm publish 等写操作时可追加 `PUT,POST` |
| `ALLOWED_ORIGINS` | `*` | CORS 允许来源，逗号分隔。生产建议收紧为具体域名 |
| `HOMEPAGE_URL` | 官方仓库 | 访问 `/` 与未知前缀时 `302` 跳转的目标 |

Git/LFS/Docker 协议请求不受 `ALLOWED_METHODS` 约束（协议白名单自动放宽），保证 `git push`、`docker push` 等带 body 的方法可用。

### 日志格式

每次代理请求输出一行结构化日志（stdout，systemd 下进 journald）：

```
203.0.113.7 GET /gh/microsoft/vscode/archive/refs/heads/main.zip → https://github.com/... [gh] status=302 attempts=1 fetch_ms=143.2 total_ms=143.4
```

字段：客户端 IP、方法与路径、上游目标、命中平台、上游状态码、尝试次数、上游耗时、总耗时。

## 鉴权详解

设置 `ACCESS_TOKEN` 后，除 `/healthz` 外的所有请求必须携带实例 token，否则返回：

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="xget"
{"error":"Unauthorized: missing or invalid access token","status":401,"timestamp":"..."}
```

三种携带方式（按优先级依次检测）：

| 方式 | 示例 | 适用场景 |
|------|------|----------|
| `X-Xget-Token` 请求头 **（推荐）** | `curl -H "X-Xget-Token: $TOKEN" ...` | 不占用 `Authorization`，可**同时**透传上游凭证（私有仓库下载） |
| `xget_token` 查询参数 | `?xget_token=$TOKEN` | `wget`、`aria2`、浏览器直接下载等不便自定义头的工具。转发前自动从 URL 剥离，不会泄漏给上游 |
| `Authorization: Bearer <token>` | `curl -H "Authorization: Bearer $TOKEN" ...` | 兼容标准 Bearer。注意：此时 token 被**实例消费**，不会转发到上游 |

Token 校验使用常量时间比较（`crypto/subtle`），防时序侧信道。

### 典型组合

**场景 A：实例开了 token + 下载 GitHub 私有仓库**

```bash
curl -H "X-Xget-Token: $XGET_TOKEN" \
     -H "Authorization: Bearer $GH_PAT" \
     -LO "https://xget.example.com/gh/owner/private-repo/archive/main.tar.gz"
```

`X-Xget-Token` 用于实例鉴权，`Authorization` 原样透传给 GitHub 完成上游鉴权 —— 两者互不干扰。

**场景 B：实例开了 token + git clone**

```bash
git -c http.extraHeader="X-Xget-Token: $XGET_TOKEN" \
    clone https://xget.example.com/gh/owner/repo.git
# GitHub 上游凭证交给 credential helper 处理即可
```

### Docker 场景的特殊限制

`daemon.json` 的 `registry-mirrors` 无法携带自定义头，因此：

- 开启 `ACCESS_TOKEN` 后，**Docker 镜像加速不可用**（拉取会被 401 拒绝）
- 解决方案：用防火墙/安全组把端口限制到可信网段（内网/VPN），这种部署可以不设 token
- `/v2/auth` 私有仓库登录链属完整版能力，标准版返回 `501`（见 [路线图](#路线图)）

## 使用示例

以下示例统一使用 `https://xget.example.com`，请替换为你的域名（或 `http://IP:8080`）。若已开启 token，curl/wget 场景请自行加上 `-H "X-Xget-Token: ..."` 或 `?xget_token=`。

### Git 仓库加速

```bash
# GitHub clone / pull
git clone https://xget.example.com/gh/git/git.git

# GitLab / Gitea / Codeberg
git clone https://xget.example.com/gl/gitlab-org/gitlab.git
git clone https://xget.example.com/codeberg/forgejo/forgejo.git

# push 同样透传（需要上游凭证）
git push origin main

# Git LFS（大文件）自动走 /info/lfs 透传
git lfs clone https://xget.example.com/gh/owner/lfs-repo.git
```

### GitHub 文件下载

```bash
# Release 附件
wget "https://xget.example.com/gh/BurntSushi/ripgrep/releases/download/14.1.0/ripgrep-14.1.0-x86_64-unknown-linux-musl.tar.gz"

# 源码归档
wget "https://xget.example.com/gh/microsoft/vscode/archive/refs/heads/main.zip"

# Raw 文件
curl -LO "https://xget.example.com/gh/torvalds/linux/raw/master/README"

# Gist
curl -LO "https://xget.example.com/gist/user/gist_id/file/name.txt"
```

### AI 模型下载

```bash
# Hugging Face —— 单文件
wget "https://xget.example.com/hf/bert-base-uncased/resolve/main/model.safetensors"

# Hugging Face —— huggingface-cli 整仓下载
export HF_ENDPOINT=https://xget.example.com/hf
pip install -U "huggingface_hub[cli]"
hf download bert-base-uncased

# Civitai 模型
wget "https://xget.example.com/civitai/api/download/models/128713"
```

### 语言包管理器

```bash
# npm / yarn / pnpm
npm config set registry https://xget.example.com/npm/

# pip（索引/元数据加速）
pip config set global.index-url https://xget.example.com/pypi/simple/
pip config set global.trusted-host xget.example.com   # 仅 HTTP 部署需要
# ⚠️ 标准版无内容改写：pip 从索引页解析出的包文件 URL 仍直连
# files.pythonhosted.org（官方版会自动改写到 /pypi/files/）。如需文件也加速，
# 将文件 URL 手动替换为 /pypi/files/ 前缀：
#   https://xget.example.com/pypi/files/packages/py3/r/requests/requests-2.31.0-py3-none-any.whl

# conda（/conda/ 后接上游原始路径；社区频道为 /conda/community/）
conda config --add channels https://xget.example.com/conda/pkgs/main
conda config --add channels https://xget.example.com/conda/community/conda-forge

# Go modules
go env -w GOPROXY=https://xget.example.com/golang,direct

# Maven（~/.m2/settings.xml）
#   <mirror><id>xget</id><mirrorOf>central</mirrorOf>
#     <url>https://xget.example.com/maven/maven2/</url></mirror>

# Cargo（~/.cargo/config.toml，与官方配置同构）
#   [source.crates-io]
#   replace-with = "xget"
#   [source.xget]
#   registry = "https://xget.example.com/crates/"

# NuGet（nuget.config）
#   <packageSources><add key="xget"
#     value="https://xget.example.com/nuget/v3/index.json" /></packageSources>

# Bundler
bundle config mirror.https://rubygems.org https://xget.example.com/rubygems
```

### 容器镜像加速

```bash
# 直接拉取（镜像名与上游仓库内的写法一致，无需携带上游主机名）
docker pull xget.example.com/cr/docker/library/alpine:latest   # Docker Hub
docker pull xget.example.com/cr/docker/alpine:latest           # Docker Hub（library/ 自动补全）
docker pull xget.example.com/cr/ghcr/<owner>/<image>:tag       # GitHub Container Registry
docker pull xget.example.com/cr/quay/calico/node:tag           # Quay.io
docker pull xget.example.com/cr/gcr/distroless/static:latest   # Google
docker pull xget.example.com/cr/mcr/dotnet/runtime:8.0         # Microsoft
docker pull xget.example.com/cr/k8s/pause:3.9                  # Kubernetes

# Docker Hub 常驻镜像源（/etc/docker/daemon.json）
# { "registry-mirrors": ["https://xget.example.com/cr/docker"] }
sudo systemctl restart docker
docker pull alpine:latest        # 自动经镜像源

# containerd / k8s（hosts.toml）
# [host."https://xget.example.com/cr/docker"]
#   capabilities = ["pull", "resolve"]
```

> Docker 请求同时支持 `/cr/<reg>/v2/...` 与 `/v2/cr/<reg>/...` 两种拼法（后者自动归一化），Docker 客户端无需任何特殊配置。

## 运维手册

### 健康检查与监控

| 探针 | 说明 |
|------|------|
| `GET /healthz` | 存活探针，**免鉴权**恒定返回 `ok`。适合负载均衡健康检查、Uptime Kuma、k8s livenessProbe |
| `X-Performance-Metrics` 响应头 | 非 Git/LFS/Docker 请求附带 JSON：`{"total_ms":143.4,"upstream_ms":143.2,"attempts":1,"platform":"gh"}`，可采样统计上游延迟 |

监控建议：对 `/healthz` 做 30s 间隔探活；对热门 release 抽样记录 `upstream_ms` 观察上游质量；`attempts > 1` 频繁出现说明上游不稳，可调大 `MAX_RETRIES`/`RETRY_DELAY_MS`。

### 升级

```bash
# systemd 部署
sudo systemctl stop xget
sudo mv xget-go-new /usr/local/bin/xget-go && sudo chmod 0755 /usr/local/bin/xget-go
sudo systemctl start xget && journalctl -u xget -n 20

# Docker 部署
docker build -t xget-go:新版本 . && docker rm -f xget && \
docker run -d --name xget ... xget-go:新版本
```

进程收到 `SIGTERM`/`SIGINT` 后进入 10 秒优雅关闭：停止接受新连接、等待存量流式传输结束，`journalctl` 可见 `shutting down gracefully...` / `bye`。配合 systemd 的 `Restart=on-failure` 可实现滚动升级零丢弃（进行中的下载由反代兜底）。

### 故障排查

| 症状 | 可能原因 | 处理 |
|------|----------|------|
| `401 Unauthorized` | 未带 token / token 错误 / Bearer 被 Docker mirror 场景拦截 | 检查 `X-Xget-Token`；Docker mirror 见[鉴权详解](#docker-场景的特殊限制) |
| `docker pull` 401 但 `curl` 正常 | `daemon.json` mirror 无法带 token | 关 `ACCESS_TOKEN` + 防火墙限内网，或直接用 `host/cr/...` 全路径拉取 |
| `400` 提示路径非法 | URL 含 `..` 穿越或控制字符 | 属预期防护，检查拼接逻辑 |
| `405 Method not allowed` | 非协议请求用了白名单外方法（如 npm publish 的 `PUT`） | `ALLOWED_METHODS=GET,HEAD,PUT,POST` |
| `414` | URL 超过 `MAX_PATH_LENGTH` | 调大或缩短路径 |
| `502/504` 且日志 `attempts=3` | 上游不可达（服务器出网被限/DNS 故障/上游宕机） | `curl -v <上游URL>` 自测出网；检查安全组出方向 |
| `501` 访问 `/v2/auth` | 标准版无 Docker 登录链 | 属预期；私有镜像需完整版能力 |
| 下载无进度条/无文件大小 | 上游 chunked 无 `Content-Length` | 已内置 HEAD/Range 探测回填；仍缺失则上游本身无长度信息 |
| 首字节慢但下载快 | 上游冷启动/重试退避中 | 观察 `X-Performance-Metrics` 的 `attempts`；调小 `RETRY_DELAY_MS` |
| `git push` 被拒 | 上游凭证缺失/权限不足 | xget-go 只透传，凭证问题直接查上游（GitHub 需 PAT） |

## 安全加固清单

- [ ] **设置 `ACCESS_TOKEN`**（`openssl rand -hex 32`），或用防火墙/安全组限制访问源 —— 官方 README 明确建议实例必须置于鉴权或白名单之后
- [ ] **全程 HTTPS**：Caddy 自动证书或 Nginx + certbot；纯 HTTP 会明文暴露 token
- [ ] **后端端口不暴露公网**：`-p 127.0.0.1:8080:8080` / `ufw deny 8080`
- [ ] **收紧 CORS**：`ALLOWED_ORIGINS=https://your-app.example.com`（默认 `*`）
- [ ] **启用 systemd 加固**：发布包 unit 已含 `DynamicUser`、`NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`，请勿移除
- [ ] **日志轮转**：journald 默认有上限；自管场景可 `logrotate` 或接入 Loki/ELK
- [ ] **保持更新**：关注上游 Xget 的安全修复并同步移植

## 架构与项目结构

```
main.go                      入口：配置加载、优雅关闭、SIGTERM 处理
internal/config/             环境变量解析（对应原版 src/config/index.js）
internal/platform/           平台目录 + 最长前缀路由 + 路径改写
                             （platform-catalog / routing；crates 的 /api/v1 改写、
                              Docker registry 路径归一化 protocols/docker.js）
internal/proxy/              请求管线：协议检测(Git/LFS/Docker) → 鉴权 → 校验 →
                             重试转发 → HEAD 探测 → 响应终结
                             （handle-request / fetch-upstream / validation.js）
internal/security/           安全头、CORS、JSON 错误契约（utils/security.js）
build.sh                     三平台静态构建脚本
xget.service                 systemd 单元（含安全加固）
Dockerfile + .dockerignore   Docker 镜像构建（alpine + ca-certificates）
```

请求管线（一次代理的完整路径）：

```
healthz? → token 鉴权 → 协议识别(Git/LFS/Docker) → CORS 预检
→ Docker /v2 探针 → 裸前缀 302 → 校验(穿越/长度/方法) → 平台最长前缀匹配
→ 路径改写(crates、/v2/cr 归一化) → 上游转发(重试+线性退避)
→ 响应终结(错误包装/跳段头剥离/缓存提示/安全头/性能头) → 流式回写
```

## 实测覆盖场景

v1.0.0 发布前以下场景均在本机实测通过：

- `git clone` / `ls-remote`（GitHub）、Git 协议 header 透传
- GitHub release 下载（80 MB 文件 HEAD Content-Length 探测回填）
- `Range: bytes=...` → `206 Partial Content` 断点续传
- npm / PyPI 元数据与包文件、Go modules（`GOPROXY`）
- Docker manifest 拉取（匿名 token 401→重试链）、`/v2` API 探针
- Hugging Face `resolve` 文件下载
- 路径穿越 `..` → `400`；方法白名单外 → `405`；URL 超长 → `414`
- token 鉴权三通道：缺 token `401`、错误 token `401`、三方式各 `200`
- `/healthz` 免鉴权；未知前缀与裸前缀 → `302` 主页

## 路线图

| 能力 | 状态 | 说明 |
|------|------|------|
| 磁盘缓存层 | 规划中 | 热点资源本地缓存（对齐官方 Cache API 行为），可配容量与 TTL |
| AI 推理前缀 (`ip-*`) | 规划中 | OpenAI/Anthropic 等 API 代理与内容重写 |
| `/v2/auth` 完整登录链 | 规划中 | Docker 私有仓库 `docker login` 支持 |
| 监听地址选项 | 规划中 | `BIND_ADDR` 环境变量，支持仅绑定内网 IP |
| Prometheus 指标 | 评估中 | `/metrics` 端点暴露请求计数与延迟直方图 |

## 致谢与许可证

- 原始设计与算法：[xixu-me/Xget](https://github.com/xixu-me/Xget) 及其社区，感谢作者 [@xixu-me](https://github.com/xixu-me) 的开源贡献
- 本移植版为同一许可证下的衍生作品：**AGPL-3.0-or-later**，任何修改版对外提供服务时须同样开源（详见 [LICENSE](LICENSE)）

Xget-Go · 以单二进制承载 Xget 的加速体验
