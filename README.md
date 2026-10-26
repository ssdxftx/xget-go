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
- [部署教程](#部署教程)（源码构建 / systemd / Docker / 反向代理 / HTTPS）
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
| 平台前缀路由 | 84 个平台（与官方目录全量一致），最长前缀优先匹配（`/pypi/files/` 优先于 `/pypi/`） |
| Range 断点续传 | 原样透传 `Range` 请求头与 `206 Partial Content` 响应；命中缓存时直接切片 |
| 失败重试 | 最多 `MAX_RETRIES` 次，线性退避（`RETRY_DELAY_MS × 第 N 次`） |
| 响应头超时 | 单次尝试 `TIMEOUT_SECONDS` 秒内未收到响应头则重试；**大文件流式传输不限时** |
| HEAD 探测回填 | 上游 `HEAD` 响应缺 `Content-Length` 时自动改用 `Range` 探测回填，保证 `wget`/下载工具显示进度 |
| Git Smart HTTP | `git clone / pull / push` 与 `/info/refs`、`git-upload-pack`、`git-receive-pack` 原样透传 |
| Git LFS | `/info/lfs`、`objects/batch`、SHA-256 对象路径识别与透传 |
| Docker Registry v2 | 17 家容器仓库加速，支持 `daemon.json` 镜像源配置，匿名 token 自动重试 |
| Docker 私有仓库登录链 | `/v2/auth` 完整实现：`docker login` 走本实例完成凭证交换，支持私有镜像 |
| AI 推理代理 | 28 家 `ip-*` 前缀（OpenAI/Anthropic/Gemini/Groq…），POST/流式响应原样透传 |
| Hugging Face API | `/hf/api`、`/hf/token` 协议级透传，`Authorization` 保留 |
| 内容改写 | PyPI 索引页、npm 元数据、Flatpak 描述文件中的上游链接自动改写回本实例 |
| 磁盘缓存层 | 对齐官方三级 TTL 策略（immutable 86400s / metadata 60s / mutable 300s），LRU 驱逐、可配容量 |
| 实例鉴权 | `X-Xget-Token` 头 / `Authorization: Bearer` / `?xget_token=` 三通道，常量时间比较 |
| 请求校验 | 路径穿越拦截（`..`）、控制字符过滤、URL 长度上限、方法白名单、CORS 白名单 |
| 安全响应头 | HSTS、CSP、`X-Content-Type-Options`、`Referrer-Policy` 等 |
| 缓存策略头 | 按资源分级输出 `Cache-Control`（immutable/metadata/mutable），带凭证请求自动降级为 `private, no-store` 并追加 `Vary` |
| 可观测性 | `/healthz` 存活探针、结构化访问日志、`X-Performance-Metrics` 性能头（含 `cache` 字段） |

## 与官方版的差异

v2.0.0 起能力与官方目录**完全对齐**：平台前缀全量一致，官方版的登录链、AI 前缀、内容改写与缓存均已实现（缓存以本地磁盘层替代 Cloudflare 边缘缓存，行为同构、TTL 分级一致）。v2.0.1 对照官方源码逐文件复核了移植一致性，修正了 8 处行为偏差（Jenkins 更新中心路径改写、含百分号编码路径的校验判定、本地生成响应的全局响应头、网络错误耗尽重试的 502 语义、pypi 改写页缓存旁路、npm/flathub 改写内容的缓存键区分、Git/LFS 协议缺省头、Token 服务查询参数编码）。

| 能力 | 官方版 (Workers) | 本移植版 |
|------|-----------------|----------|
| 部署形态 | Cloudflare Workers / Docker(workerd) | **单二进制 / systemd / Docker / 裸机** |
| 平台前缀 | 官方目录 84 项 | ✅ **84 项全量一致** |
| URL 前缀改写 + 转发 | ✅ | ✅ |
| Range 断点续传 | ✅ | ✅ |
| 3 次线性退避重试 + 超时 | ✅ | ✅（超时只约束"响应头到达"，大文件流式传输不限时） |
| HEAD Content-Length 回填探测 | ✅ | ✅ |
| 安全响应头 (HSTS/CSP 等) | ✅ | ✅ |
| 请求校验（穿越/控制字符/方法白名单） | ✅ | ✅ |
| `X-Performance-Metrics` 性能头 | ✅ | ✅（含 `cache` 命中字段） |
| Git 智能 header 优化 / push | ✅ | ✅（header 透传 + 协议缺省头回填，clone/pull/push/LFS 实测可用） |
| Docker 私有仓库登录链 (`/v2/auth`) | ✅ | ✅ 完整实现（含匿名 token 自动重试、合成 401 挑战指向本实例） |
| AI 推理前缀 (`ip-*`) | ✅ | ✅ 28 家全量 |
| 内容改写 (PyPI/npm/Flatpak) | ✅ | ✅ |
| 缓存 | Cloudflare Cache API（边缘） | ✅ 磁盘缓存层（同源 URL 键、同 TTL 分级、LRU 驱逐、改写内容按请求来源区分缓存键） |

> 平台差异（已知限制）：路径中出现**非法百分号序列**（如 `100%.txt`）时，Go 的标准 HTTP 栈在连接解析层直接返回 400，而 Cloudflare Workers 会放行给上游处理。该场景仅影响文件名本身含裸 `%` 的极端 URL；合法的百分号编码（`%2e`、`%2f` 等）行为与官方完全一致。

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
| `sf` | `sourceforge.net` | SourceForge |
| `aosp` | `android.googlesource.com` | Android AOSP |

### AI / 模型仓库

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `hf` | `huggingface.co` | 模型/数据集文件、`HF_ENDPOINT`、`/hf/api` 透传 |
| `civitai` | `civitai.com` | 模型下载 API |

### 包管理

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `npm` | `registry.npmjs.org` | npm/yarn/pnpm registry |
| `pypi` | `pypi.org` | pip 索引 `/pypi/simple/`（HTML 自动改写） |
| `pypi-files` | `files.pythonhosted.org` | pip 包文件下载（手动构造 URL） |
| `conda` | `repo.anaconda.com` | Anaconda 官方频道 |
| `conda-community` | `conda.anaconda.org` | conda-forge 等社区频道 |
| `maven` | `repo1.maven.org` | Maven Central |
| `apache` | `downloads.apache.org` | Apache 发行包 |
| `gradle` | `plugins.gradle.org` | Gradle 插件 |
| `homebrew` | `github.com/Homebrew` | Homebrew 仓库 |
| `homebrew-api` | `formulae.brew.sh/api` | Homebrew API |
| `homebrew-bottles` | `ghcr.io` | Homebrew 二进制瓶 |
| `rubygems` | `rubygems.org` | Bundler 镜像 |
| `cran` | `cran.r-project.org` | R 包 |
| `cpan` | `www.cpan.org` | Perl CPAN |
| `ctan` | `tug.ctan.org` | TeX CTAN |
| `golang` | `proxy.golang.org` | `GOPROXY` |
| `nuget` | `api.nuget.org` | NuGet v3 |
| `crates` | `crates.io` | Cargo（自动改写到 `/api/v1/crates`） |
| `packagist` | `repo.packagist.org` | Composer |
| `flathub` | `dl.flathub.org` | Flatpak（`.flatpakrepo` 自动改写） |

### Linux 发行版

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `debian` | `deb.debian.org` | apt 源 |
| `ubuntu` | `archive.ubuntu.com` | apt 源 |
| `fedora` | `mirrors.kernel.org/fedora` | dnf 源（`/pub/fedora/linux` 前缀自动剥离） |
| `rocky` | `download.rockylinux.org` | Rocky Linux |
| `opensuse` | `download.opensuse.org` | openSUSE |
| `arch` | `geo.mirror.pkgbuild.com` | pacman 源 |

### 其他资源

| 前缀 | 上游 | 典型用途 |
|------|------|----------|
| `arxiv` | `arxiv.org` | 论文 PDF |
| `fdroid` | `f-droid.org` | F-Droid 应用 |
| `jenkins` | `updates.jenkins.io` | Jenkins 插件/更新 |

### AI 推理（28 家，POST/流式透传）

| 前缀 | 上游 | 前缀 | 上游 |
|------|------|------|------|
| `ip-openai` | `api.openai.com` | `ip-huggingface` | `router.huggingface.co` |
| `ip-anthropic` | `api.anthropic.com` | `ip-together` | `api.together.xyz` |
| `ip-gemini` | `generativelanguage.googleapis.com` | `ip-replicate` | `api.replicate.com` |
| `ip-vertexai` | `aiplatform.googleapis.com` | `ip-fireworks` | `api.fireworks.ai` |
| `ip-cohere` | `api.cohere.ai` | `ip-nebius` | `api.studio.nebius.ai` |
| `ip-mistralai` | `api.mistral.ai` | `ip-jina` | `api.jina.ai` |
| `ip-xai` | `api.x.ai` | `ip-voyageai` | `api.voyageai.com` |
| `ip-githubmodels` | `models.github.ai` | `ip-falai` | `fal.run` |
| `ip-nvidiaapi` | `integrate.api.nvidia.com` | `ip-novita` | `api.novita.ai` |
| `ip-perplexity` | `api.perplexity.ai` | `ip-burncloud` | `ai.burncloud.com` |
| `ip-braintrust` | `api.braintrust.dev` | `ip-openrouter` | `openrouter.ai` |
| `ip-groq` | `api.groq.com` | `ip-poe` | `api.poe.com` |
| `ip-cerebras` | `api.cerebras.ai` | `ip-featherlessai` | `api.featherless.ai` |
| `ip-sambanova` | `api.sambanova.ai` | `ip-hyperbolic` | `api.hyperbolic.xyz` |
| `ip-siray` | `api.siray.ai` | | |

### 容器镜像（17 家）

| 前缀 | 上游 | 前缀 | 上游 |
|------|------|------|------|
| `cr-docker` | `registry-1.docker.io` | `cr-cloudsmith` | `docker.cloudsmith.io` |
| `cr-ghcr` | `ghcr.io` | `cr-digitalocean` | `registry.digitalocean.com` |
| `cr-quay` | `quay.io` | `cr-vmware` | `projects.registry.vmware.com` |
| `cr-gcr` | `gcr.io` | `cr-k8s` | `registry.k8s.io` |
| `cr-mcr` | `mcr.microsoft.com` | `cr-heroku` | `registry.heroku.com` |
| `cr-ecr` | `public.ecr.aws` | `cr-suse` | `registry.suse.com` |
| `cr-gitlab` | `registry.gitlab.com` | `cr-opensuse` | `registry.opensuse.org` |
| `cr-redhat` | `registry.redhat.io` | `cr-gitpod` | `registry.gitpod.io` |
| `cr-oracle` | `container-registry.oracle.com` | | |

---

## 快速开始

从 GitHub 仓库克隆源码并构建（**Go 1.23+**，零第三方依赖，纯标准库；三平台二进制约 6 MB）：

```bash
git clone https://github.com/ssdxftx/xget-go.git
cd xget-go
./build.sh v2.0.1        # 产出 linux-amd64 / linux-arm64 / darwin-arm64 三个静态二进制
sudo cp xget-linux-amd64 /usr/local/bin/xget-go   # 按服务器架构选择（uname -m 确认）

# 前台试用（默认 8080 端口、无鉴权 —— 仅限内网/本机！）
xget-go

# 生产运行：务必设置 token
PORT=8080 ACCESS_TOKEN="$(openssl rand -hex 24)" xget-go
```

启动后验证：

```bash
curl http://127.0.0.1:8080/healthz          # → ok（健康探针，免鉴权）
curl -sI "http://127.0.0.1:8080/gh/xixu-me/Xget" | head -5
```

> 完整的生产部署（systemd / Docker / HTTPS 反代）见下一章 [部署教程](#部署教程)。

## 部署教程

本章所有方式都从 GitHub 仓库出发，配套网络配置一并列出。**推荐组合：systemd（或 Docker）+ Nginx/Caddy 反向代理 + HTTPS**。

| 方式 | 适用场景 | 难度 |
|------|----------|------|
| [前台运行](#方式一前台运行试用调试) | 快速试用、本地调试 | ★ |
| [systemd 服务](#方式二systemd-生产部署推荐) | Linux 服务器长期运行 | ★★（推荐） |
| [Docker](#方式三docker-部署) | 容器化环境、与其它服务统一编排（无需本地 Go） | ★★ |

### 第 0 步：获取源码并构建（各方式共同起点）

**安装 Go 1.23+**（仅前台运行与 systemd 方式需要；Docker 方式在容器内编译，可跳过）：

```bash
# 方法 A：官方包（推荐，版本最新）
wget https://go.dev/dl/go1.23.12.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.23.12.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin        # 写入 ~/.bashrc

# 方法 B：发行版包管理器（版本可能偏旧，需确认 ≥1.23）
sudo apt install golang-go
```

**克隆并构建**：

```bash
git clone https://github.com/ssdxftx/xget-go.git
cd xget-go
./build.sh v2.0.1                          # linux-amd64 / linux-arm64 / darwin-arm64
# 或只构建当前平台：
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o xget-linux-amd64 .
```

需要其它平台时交叉编译（零依赖，装好 Go 即可）：

| 目标平台 | 环境变量 |
|----------|----------|
| Linux x86_64 | `GOOS=linux GOARCH=amd64` |
| Linux arm64 | `GOOS=linux GOARCH=arm64` |
| macOS Apple Silicon | `GOOS=darwin GOARCH=arm64` |
| macOS Intel | `GOOS=darwin GOARCH=amd64` |
| FreeBSD | `GOOS=freebsd GOARCH=amd64` |
| Windows | `GOOS=windows GOARCH=amd64`（产物为 `.exe`） |

> 仓库按惯例不含预编译二进制（见 `.gitignore`），构建产物留在本机即可；后续升级执行 `git pull && ./build.sh` 重新构建覆盖。

### 方式一：前台运行（试用/调试）

接[第 0 步](#第-0-步获取源码并构建各方式共同起点)构建产物：

```bash
sudo cp xget-linux-amd64 /usr/local/bin/xget-go   # 按服务器架构选择

PORT=8080 ACCESS_TOKEN="$(openssl rand -hex 24)" xget-go
# Ctrl+C 停止
```

启动日志示例：

```
xget-go v2.0.1 starting — port=8080 auth=token retries=3 timeout=30s platforms=84 cache=/var/lib/xget/cache (max 1024MB/file 512MB)
listening on http://0.0.0.0:8080 — usage: http://<host>:8080/<prefix>/<path>
```

> ⚠️ 前台进程会随 SSH 会话断开而退出，生产环境请使用 systemd 或 Docker。

### 方式二：systemd 生产部署（推荐）

**第 1 步：安装二进制**（接第 0 步构建产物）

```bash
sudo cp xget-linux-amd64 /usr/local/bin/xget-go   # 按服务器架构选择
sudo chmod 0755 /usr/local/bin/xget-go
xget-go --version        # → xget-go v2.0.1 (port of xixu-me/Xget, AGPL-3.0-or-later)
sudo mkdir -p /var/lib/xget /etc/xget
```

**第 2 步：生成配置文件**（推荐用 `EnvironmentFile` 管理 token，避免明文写进 unit）

```bash
sudo sh -c 'echo "ACCESS_TOKEN=$(openssl rand -hex 32)" > /etc/xget/xget.env'
sudo chmod 600 /etc/xget/xget.env
# 可按需追加：PORT=8080、TIMEOUT_SECONDS=30 等，见《配置参考》
```

**第 3 步：安装服务单元**（仓库根目录已提供 `xget.service`，含安全加固）

```bash
sudo cp xget.service /etc/systemd/system/xget.service
```

编辑 `/etc/systemd/system/xget.service`，加载配置文件并设置工作目录权限：

```ini
[Service]
EnvironmentFile=/etc/xget/xget.env        # ← 新增这一行
# 以下为仓库 unit 默认已含的加固项，无需改动：
# DynamicUser=yes / NoNewPrivileges=yes / ProtectSystem=strict ...
```

> 仓库 unit 默认 `ExecStart=/usr/local/bin/xget-go`、`Environment=PORT=8080`；`DynamicUser=yes` 会让服务以随机非特权用户运行。若需自定义端口等，直接在 unit 里加 `Environment=XXX=...` 或写进 `/etc/xget/xget.env`。

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

仓库内含**多阶段构建**的 `Dockerfile`（`golang:1.23-alpine` 编译 → `alpine:3.20` 运行 + ca-certificates）。**全新 clone 直接构建即可，本机无需安装 Go**；x86_64/arm64 由 Go 在容器内原生产出对应架构二进制（多架构构建需 `docker buildx` + QEMU）：

```bash
git clone https://github.com/ssdxftx/xget-go.git
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
| `CACHE_TTL` | `300` | 可变资源的磁盘缓存 TTL（秒），同时作为 `Cache-Control: s-maxage` 提示。与官方 `CACHE_DURATION` 默认值一致 |
| `CACHE_DIR` | `cache` | 磁盘缓存目录（相对工作目录）；设为 `off`/`none` 关闭缓存，回到纯透传模式 |
| `CACHE_MAX_MB` | `1024` | 缓存总容量预算（MB），超出后按 LRU 驱逐 |
| `CACHE_MAX_FILE_MB` | `512` | 单个缓存条目上限（MB），更大的文件只透传不缓存 |
| `ALLOWED_METHODS` | `GET,HEAD` | 非协议请求的方法白名单。需要 npm publish 等写操作时可追加 `PUT,POST` |
| `ALLOWED_ORIGINS` | `*` | CORS 允许来源，逗号分隔。生产建议收紧为具体域名 |
| `HOMEPAGE_URL` | 官方仓库 | 访问 `/` 与未知前缀时 `302` 跳转的目标 |

Git/LFS/Docker/AI/Hugging Face 协议请求不受 `ALLOWED_METHODS` 约束（协议白名单自动放宽为 `GET, HEAD, POST, PUT, PATCH, DELETE`），保证 `git push`、`docker push`、AI 聊天补全等带 body 的方法可用。

### 缓存分级策略

与官方 `cache-policy` 同构，按资源类型自动分级：

| 级别 | 判定 | 磁盘 TTL | 浏览器提示 |
|------|------|----------|------------|
| 不可变产物 | npm `.tgz`、PyPI 版本化包文件、Maven 非 SNAPSHOT、crates `.crate`、rubygems `.gem`、GitHub release 附件（带版本 tag） | 86400s | `max-age=3600, immutable` |
| 元数据/索引 | npm 包元数据、`/pypi/simple/`、`maven-metadata.xml`、flathub summary | 60s | `max-age=0, must-revalidate` |
| 可变资源 | 其余 200 GET | `CACHE_TTL`（默认 300s） | `max-age=0, must-revalidate` |
| 私有 | 请求带 `Authorization`/`Cookie`，或上游返回 `Set-Cookie`/`private` | 不缓存 | `private, no-store` + `Vary` |
| 旁路 | Git/LFS/Docker/AI/HF 协议、非 GET/HEAD | 不缓存 | `no-store` |

命中缓存时 `X-Performance-Metrics` 带 `"cache":"hit"`，未命中为 `"cache":"edge"`。带凭证请求永不缓存，缓存命中同样支持 `Range` 断点切片。

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

### Docker 场景说明

- **私有镜像**：`docker login <实例域名>[/cr/<仓库>]` 后即可拉取私有镜像（凭证经 `/v2/auth` 登录链与上游交换，实例不存储凭证）
- **`daemon.json` 的 `registry-mirrors` 无法携带任何自定义头**，因此开启 `ACCESS_TOKEN` 后镜像源模式不可用（拉取被 401 拒绝）——镜像源场景请用防火墙/安全组限制到可信网段并不设 token，或改用全路径拉取 `docker pull <实例>/cr/<仓库>/<镜像>`（登录链可用）

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

### AI 推理代理（28 家）

前缀即终端，凭证直接使用各家的 API Key（经本实例转发，实例不存储）：

```bash
# OpenAI SDK
export OPENAI_BASE_URL=https://xget.example.com/ip/openai/v1
export OPENAI_API_KEY=sk-...

# Anthropic SDK
export ANTHROPIC_BASE_URL=https://xget.example.com/ip/anthropic

# Google Gemini REST
curl "https://xget.example.com/ip/gemini/v1beta/models?key=$GEMINI_KEY"

# 任意 OpenAI 兼容服务（Groq / OpenRouter / DeepSeek 类）
curl https://xget.example.com/ip/groq/openai/v1/chat/completions \
  -H "Authorization: Bearer $GROQ_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}],"stream":true}'

# Hugging Face Inference Router
export HF_INFERENCE_ENDPOINT=https://xget.example.com/ip/huggingface
```

> POST 请求体与 SSE 流式响应原样透传，不缓存、不改写、不记录 body；错误响应（如 401）原样返回上游格式，便于 SDK 直接解析。

### 语言包管理器

```bash
# npm / yarn / pnpm
npm config set registry https://xget.example.com/npm/

# pip（索引与包文件均已自动联动：索引页 HTML 内的文件链接自动改写到 /pypi/files/）
pip config set global.index-url https://xget.example.com/pypi/simple/
pip config set global.trusted-host xget.example.com   # 仅 HTTP 部署需要

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
| `401 Unauthorized` | 未带 token / token 错误 / Bearer 被 Docker mirror 场景拦截 | 检查 `X-Xget-Token`；Docker 场景见[鉴权详解](#docker-场景说明) |
| `docker pull` 401 但 `curl` 正常 | `daemon.json` mirror 无法带 token | 关 `ACCESS_TOKEN` + 防火墙限内网，或用 `host/cr/...` 全路径拉取（可 `docker login`） |
| 私有镜像 401（已 docker login） | 登录链要求实例可直达上游 token 服务 | 检查上游 registry 与其 token 服务连通性；确认 realm 指向本实例 `/cr/<仓库>/v2/auth` |
| `400` 提示路径非法 | URL 含 `..` 穿越或控制字符 | 属预期防护，检查拼接逻辑 |
| `405 Method not allowed` | 非协议请求用了白名单外方法（如 npm publish 的 `PUT`） | `ALLOWED_METHODS=GET,HEAD,PUT,POST` |
| `414` | URL 超过 `MAX_PATH_LENGTH` | 调大或缩短路径 |
| `502/504` 且日志 `attempts=3` | 上游不可达（服务器出网被限/DNS 故障/上游宕机） | `curl -v <上游URL>` 自测出网；检查安全组出方向 |
| 下载无进度条/无文件大小 | 上游 chunked 无 `Content-Length` | 已内置 HEAD/Range 探测回填；仍缺失则上游本身无长度信息 |
| 缓存不生效 | 带 `Authorization`/`Cookie`（永不缓存）或资源属旁路类 | 去掉凭证头重试；观察 `X-Performance-Metrics` 的 `cache` 字段 |
| 磁盘占用过高 | `CACHE_MAX_MB` 预算过大或热点资源多 | 调小预算（LRU 自动驱逐）或 `CACHE_DIR=off` 关闭缓存 |
| 首字节慢但下载快 | 上游冷启动/重试退避中 | 观察 `X-Performance-Metrics` 的 `attempts`；调小 `RETRY_DELAY_MS` |
| `git push` 被拒 | 上游凭证缺失/权限不足 | xget-go 只透传，凭证问题直接查上游（GitHub 需 PAT） |

## 安全加固清单

- [ ] **设置 `ACCESS_TOKEN`**（`openssl rand -hex 32`），或用防火墙/安全组限制访问源 —— 官方 README 明确建议实例必须置于鉴权或白名单之后
- [ ] **全程 HTTPS**：Caddy 自动证书或 Nginx + certbot；纯 HTTP 会明文暴露 token
- [ ] **后端端口不暴露公网**：`-p 127.0.0.1:8080:8080` / `ufw deny 8080`
- [ ] **收紧 CORS**：`ALLOWED_ORIGINS=https://your-app.example.com`（默认 `*`）
- [ ] **启用 systemd 加固**：仓库 unit 已含 `DynamicUser`、`NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`，请勿移除
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
Dockerfile + .dockerignore   多阶段镜像构建（golang:1.23 编译 → alpine 运行）
```

请求管线（一次代理的完整路径）：

```
healthz? → token 鉴权 → 协议识别(Git/LFS/Docker/AI/HF) → CORS 预检
→ Docker /v2 探针 → 裸前缀 302 → 校验(穿越/长度/方法) → 平台最长前缀匹配
→ 路径改写(crates、/v2/cr 归一化) → 缓存查询 → 上游转发(重试+线性退避)
→ 响应终结(错误包装/文本改写/跳段头剥离/分级Cache-Control/安全头/性能头)
→ 边下载边写缓存(T形流) / 缓存命中直接切片服务
```

## 实测覆盖场景

v2.0.0 发布前以下场景均在真实上游实测通过（40 项集成用例全绿）；v2.0.1 一致性审查后另以 34 项回归用例（覆盖 Jenkins 改写、百分号编码校验、全局响应头、Docker 匿名链、Range/HEAD 回填、真实 git clone、token 鉴权、磁盘缓存命中、pypi/npm 改写与旁路）复验全绿：

- `git clone` / `ls-remote`（GitHub）、Git 协议 header 透传
- GitHub release 下载（HEAD Content-Length 探测回填）、`Range` → `206` 断点续传
- **磁盘缓存**：首次 `miss` 落盘 → 二次 `hit`（0.2ms 级）、缓存命中 Range 切片、带 Cookie 请求 `private/no-store` 不缓存、LRU 驱逐（1MB 预算下旧条目被逐出）
- **Docker 登录链**：`/cr/docker/v2/auth` 换取 token、`/cr/ghcr/v2/auth`、非法 scope `400`、裸 `/v2/auth` `400`、manifest 匿名 token 链 `200`、对拒绝资源返回指向本实例的合成 401 挑战
- **AI 前缀**：`/ip/openrouter/api/v1/models` 200；`/ip/openai` 错误体原样透传（无包装）；POST 透传
- **HF 协议**：`/hf/api/models` 200
- **内容改写**：PyPI HTML → `/pypi/files/`、npm JSON → `/npm/`、flathub `.flatpakrepo` → `/flathub/repo/`（上游直链零残留）
- **新平台**：debian/arch/cran/apache/aosp 可达；fedora `/pub/fedora/linux` 前缀剥离与直路径双形态 200
- 路径穿越 `..` → `400`；方法白名单外 → `405`
- token 鉴权三通道：缺 token `401`、错误 token `401`、`X-Xget-Token`/query 各 `200`、`/healthz` 免鉴权

## 路线图

| 能力 | 状态 | 说明 |
|------|------|------|
| ~~磁盘缓存层~~ | ✅ v2.0.0 | 三级 TTL 分级 + LRU 驱逐，`CACHE_DIR/CACHE_MAX_MB/CACHE_MAX_FILE_MB` 可配 |
| ~~AI 推理前缀 (`ip-*`)~~ | ✅ v2.0.0 | 28 家全量，POST/流式透传 |
| ~~`/v2/auth` 完整登录链~~ | ✅ v2.0.0 | Docker 私有仓库 `docker login` 支持 |
| ~~平台目录全量~~ | ✅ v2.0.0 | 23 → 84 项，与官方目录一致 |
| 监听地址选项 | 规划中 | `BIND_ADDR` 环境变量，支持仅绑定内网 IP |
| Prometheus 指标 | 评估中 | `/metrics` 端点暴露请求计数、缓存命中率与延迟直方图 |

## 致谢与许可证

- 原始设计与算法：[xixu-me/Xget](https://github.com/xixu-me/Xget) 及其社区，感谢作者 [@xixu-me](https://github.com/xixu-me) 的开源贡献
- 本移植版为同一许可证下的衍生作品：**AGPL-3.0-or-later**，任何修改版对外提供服务时须同样开源（详见 [LICENSE](LICENSE)）

Xget-Go · 以单二进制承载 Xget 的加速体验
