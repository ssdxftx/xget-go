# Xget-Go 🚀

**单二进制的多平台开发者资源加速引擎** —— [xixu-me/Xget](https://github.com/xixu-me/Xget)（Cloudflare Workers / JavaScript）核心逻辑的 **Go 移植版**。

Xget-Go 将 Xget 的核心加速能力编译为一个 **约 6 MB 的静态单文件**：不依赖 Node.js、workerd 或任何运行时，拷贝到服务器即可运行。它为代码托管平台、AI 模型仓库、包管理器和容器镜像仓库提供统一的下载/拉取加速入口，**URL 结构与官方 Xget 完全同构**，客户端配置可直接沿用官方生态的既有约定。

当前版本：**v3.0.7**（对齐上游 `0595fad`）。

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
- [鉴权与凭证透传](#鉴权与凭证透传)
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
| AI 推理代理 | 29 家 `ip-*` 前缀（OpenAI/Anthropic/Gemini/Groq…），POST/流式响应原样透传 |
| Hugging Face API | `/hf/api`、`/hf/token` 协议级透传，`Authorization` 保留 |
| 内容改写 | PyPI 索引页、npm 元数据、Flatpak 描述文件中的上游链接自动改写回本实例 |
| 磁盘缓存层 | 对齐官方三级 TTL 策略（immutable 86400s / metadata 60s / mutable 300s），LRU 驱逐、可配容量 |
| 实例鉴权 | **无**（与官方一致）：无实例级门禁，`Authorization` 等凭证头原样透传上游；对外防护交给反代 basic auth / IP 白名单（见[鉴权与凭证透传](#鉴权与凭证透传)） |
| 请求校验 | 路径穿越拦截（`..`）、控制字符过滤、URL 长度上限、方法白名单、CORS 白名单 |
| 安全响应头 | HSTS、CSP、`X-Content-Type-Options`、`Referrer-Policy` 等 |
| 缓存策略头 | 按资源分级输出 `Cache-Control`（immutable/metadata/mutable），带凭证请求自动降级为 `private, no-store` 并追加 `Vary` |
| 可观测性 | `/healthz` 存活探针、结构化访问日志、`X-Performance-Metrics` 性能头（与官方一致的 mark 时间线） |

## 与官方版的差异

v2.0.0 起能力与官方目录**完全对齐**：平台前缀全量一致，官方版的登录链、AI 前缀、内容改写与缓存均已实现（缓存以本地磁盘层替代 Cloudflare 边缘缓存，行为同构、TTL 分级一致）。v2.0.1 对照官方源码逐文件复核了移植一致性，修正了 8 处行为偏差（Jenkins 更新中心路径改写、含百分号编码路径的校验判定、本地生成响应的全局响应头、网络错误耗尽重试的 502 语义、pypi 改写页缓存旁路、npm/flathub 改写内容的缓存键区分、Git/LFS 协议缺省头、Token 服务查询参数编码）。v3.0.1 以官方源码为基准完成第二轮全量审查（84 个平台目录逐项 diff + 请求管线/校验/协议/上游/缓存/改写/响应头逐模块比对，另附 8 项 mock 上游回归测试），修复 12 处偏差：可重试状态耗尽后返回最后一个上游响应（此前 304/5xx 耗尽会错误地得到合成 500，影响带 If-None-Match 的 docker manifest 条件请求）、Docker 匿名 token 重试保留原请求方法、token 服务响应接受任意 2xx、HEAD 不再读磁盘缓存（官方缓存读写均为 GET-only）、非预检响应的 Access-Control-Allow-Methods 恒为配置白名单（此前协议响应误用扩展表）、registry 自带 JSON UNAUTHORIZED 错误原样透传、主页 302 空响应体、nosniff/Accept-Ranges/Cache-Control 仅作用于 2xx、GET/HEAD 请求体不再转发上游、缓存键 basename/release tag 百分号解码、CORS Vary 合并去重、registry 重定向跟随失败并入重试链。v3.0.2 完成第三轮审查，聚焦错误响应契约与配置名对齐：错误包装门槛改为官方 `finalizeErrorResponse` 的“非 2xx”语义（此前仅包装非协议平台，现在 Docker 的非 401 错误——404/429/5xx——与标准平台一样获得 JSON 错误包装，Git/LFS/AI/HF 保持原样透传）、panic 恢复与 `/v2/auth` 内部失败（registry 不可达/挑战解析失败/token 服务不可达）统一为官方顶层 catch 的**纯文本** `500 Internal Server Error`（此前 panic 路径误用 JSON 契约、/v2/auth 失败误用 502）、缓存 TTL 变量改用官方名 `CACHE_DURATION`（`CACHE_TTL` 保留为兼容别名，优先级 CACHE_DURATION > CACHE_TTL），并修复一处**高危磁盘缓存 bug**：缓存条目 body 文件内的 JSON 元数据前导行此前未在命中读取时跳过，导致所有缓存命中响应会把前导与正文一并发送（超出声明的 Content-Length，客户端得到 0 字节或被截断的响应），同时 Range 断点切片的绝对偏移也会切进前导——本轮修复后命中响应与首次透传逐字节一致、命中后 Range 切片与上游原始切片逐字节一致，并新增 2 项 mock 回归（命中字节一致性 + 命中 Range 切片）锁死该行为。v3.0.3 完成第四轮审查，以同一 mock 上游分别驱动官方 Worker 与本移植版逐响应比对（24 项用例，状态/头/体全量 diff），修正 3 处可观测偏差：错误 JSON 的 `timestamp` 改用 `Date#toISOString` 的固定毫秒格式（`2026-10-27T04:12:21.313Z`，此前为无毫秒的 RFC3339）、本地纯文本错误响应改回裸 `text/plain`（去掉 `; charset=utf-8`）、`X-Performance-Metrics` 改为官方的 mark 时间线结构（`attempt_N`/`success`/`client_error`/`docker_auth_challenge`/`cache_hit`/`cache_hit_full_content`/`range_cache_hit_after_full_cache`/`complete`，替换此前自定义的 `total_ms`/`upstream_ms`/`attempts`/`platform`/`cache` 摘要），并补齐 Range 请求在上游返回 200 时“先写全量缓存再切片”的 `206` 路径，新增 3 项 mock 回归锁死这些契约。v3.0.4 继续第四轮复核的模块级源码比对，修正 3 处协议分支差异：`isGitRequest` 的 `service` 查询参数改为短路判定（存在但非两个 pack service 时直接判定为非 Git，此前仍会继续检查 Content-Type 而误判为 Git 并放行 POST）、`/v2/auth` 登录代理改回官方协议分支的响应头集合（仅 CORS，不再附加安全头）、协议类请求的主页 302 改回仅 CORS（安全头只随非协议请求的性能头路径下发），回归扩至 18 项。**v3.0.0 移除了实例级 token 鉴权**，鉴权行为与官方完全一致：无实例门禁，`Authorization` 头作为上游凭证纯透传（此前版本会把 Bearer 消费为实例门禁，与私有仓库凭证透传冲突）——这是一处**破坏性变更**，升级后请改用反代层鉴权（见[鉴权与凭证透传](#鉴权与凭证透传)）。

v3.0.5 修复第五轮审计发现的 8 处运行时缺陷：Docker 直连 3xx（无 401 前置）因重定向请求复用了已被 `cancelBody.Close` 取消的 per-attempt context 而必然 502；匿名 token 重试后重定向跟随失败未检查失败标志导致空指针 panic（应为合成 401）；SSE/AI 流式响应缺少 Flush 导致整体缓冲到响应结束；压缩（gzip/br）缓存条目被 Range 请求按字节切片产生损坏的 206；缓存 416 响应沿用完整资源的陈旧 Content-Length；Range-after-full-cache 的超限回退分支丢失 CORS 头；改写读取上游 body 失败时静默返回截断内容（改为官方语义的纯文本 500）；HEAD 访问 PyPI 索引的 Cache-Control 对齐官方 `no-store`。同时补齐 `cache_hit_full_content` mark、错误响应显式 `Content-Length`，并将超时标志改为 `atomic.Bool`，新增 7 项回归测试。
v3.0.6 完成第六轮审计与修复，重点在自研磁盘缓存与运行时边角：缓存容量计量改为增量维护、超预算时按磁盘重算，修复覆盖写重复计数与过期条目不扣减导致的过度驱逐；对上游 `Vary`（如 `Vary: Origin`）直接旁路，避免 URL 键跨来源串味；缓存 body 改为版本化文件 + 原子 meta，修复并发读写/覆盖时的条目不一致与命中后 body 消失返回 502 的问题；Range-after-full-cache 的临时落盘加上限（超限改流式回放完整 200），消除磁盘耗尽风险；Docker 重定向、匿名 token 重试与 HEAD 长度探测补回 `TIMEOUT_SECONDS`；错误体按 `Content-Encoding` 解压后再进入 JSON 契约；敏感 PyPI 请求保留 `private, no-store` + `Vary`；请求体缓冲上限从 32MB 降到 1MB；`X-Forwarded-Proto` 仅接受 http/https；并修复空 200 不缓存、`bytes=-0` 未返回 416、字面量平台键被错误切片、非法 UTF-8 百分号序列未拒绝、非协议 304 被错误包装等边界。新增缓存与代理回归测试（含并发 `-race`）。
v3.0.7 修复第七轮审计发现的编码协商问题：标准请求此前固定向上游请求 `gzip, deflate, br` 并把压缩体原样转发，导致显式声明 `Accept-Encoding: identity` 的客户端仍收到 gzip 字节，且磁盘缓存会把压缩表示命中给不支持该编码的客户端；现在按客户端 `Accept-Encoding` 向上游协商（缺省 `identity`），磁盘缓存只写入 identity 表示，命中时校验编码兼容性，旧压缩条目仅返回给显式支持该编码的客户端。同时为非协议 304 补回安全响应头、让 HEAD 长度探测与 Docker 重定向/匿名 token 重试复用同一个 attempt 截止时间、把不可变制品判定改为解析目标 URL 的 path、对齐 registry token 空字符串语义，并修正 README 的 AI 推理数量（29 家）与 `.gitignore` 的默认缓存目录。


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
| `X-Performance-Metrics` 性能头 | ✅ | ✅（与官方一致的 mark 时间线） |
| Git 智能 header 优化 / push | ✅ | ✅（header 透传 + 协议缺省头回填，clone/pull/push/LFS 实测可用） |
| Docker 私有仓库登录链 (`/v2/auth`) | ✅ | ✅ 完整实现（含匿名 token 自动重试、合成 401 挑战指向本实例） |
| AI 推理前缀 (`ip-*`) | ✅ | ✅ 29 家全量 |
| 内容改写 (PyPI/npm/Flatpak) | ✅ | ✅ |
| 缓存 | Cloudflare Cache API（边缘） | ✅ 磁盘缓存层（同源 URL 键、同 TTL 分级、LRU 驱逐、改写内容按请求来源区分缓存键） |
| 实例鉴权 | ❌ 无（官方建议自部署实例外置鉴权层） | ✅ **与官方一致**：无内置鉴权，`Authorization` 纯透传上游 |

> 平台差异（已知限制）：路径中出现**非法百分号序列**（如 `100%.txt`）时，Go 的标准 HTTP 栈在连接解析层直接返回 400，而 Cloudflare Workers 会放行给上游处理。该场景仅影响文件名本身含裸 `%` 的极端 URL；合法的百分号编码（`%2e`、`%2f` 等）行为与官方完全一致。

其余运行时层差异（官方源码语义已对齐，差异来自 Go 标准库与 Cloudflare Workers 运行时的能力边界）：

- **URL 点段归一化**：Cloudflare/Node 在请求进入 Worker 之前就把 `/gh/../etc/passwd`、`/gh/%2e%2e/etc/passwd` 归一化为 `/etc/passwd`，官方因此落到 302 首页；Go 收到原始路径，按官方 `validateRequest` 的原始路径检查返回 400 `Invalid path`。直接以原始 URL 调用官方 `validateRequest` 同样返回 400，且官方测试仅要求明文 `..` 不得 500、编码穿越必须 400。
- **`Accept-Encoding`**：标准请求按客户端 `Accept-Encoding` 向上游协商（缺省 `identity`），压缩体与 `Content-Encoding` 原样转发，客户端只会收到自己声明支持的编码。可改写平台（PyPI 索引 / npm 元数据 / Flatpak 描述文件）与带 `Range` 的请求强制 `identity`：前者保证改写基于明文（Go 标准库没有 brotli 解码器），后者避免压缩体被按字节切片。磁盘缓存只写入 identity 表示，命中时对任意客户端安全；压缩响应不入缓存。
- **HEAD + 可改写平台**：官方对 HEAD 响应也会执行改写，`response.text()` 为空导致 `Content-Length: 0`；本移植版跳过 HEAD 改写，保留上游长度，避免把真实长度抹成 0。

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

### AI 推理（29 家，POST/流式透传）

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
./build.sh v3.0.4        # 产出 linux-amd64 / linux-arm64 / darwin-arm64 三个静态二进制
sudo cp xget-linux-amd64 /usr/local/bin/xget-go   # 按服务器架构选择（uname -m 确认）

# 前台试用（默认 8080 端口 —— 仅限内网/本机！）
xget-go
```

启动后验证：

```bash
curl http://127.0.0.1:8080/healthz          # → ok（健康探针）
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

PORT=8080 xget-go
# Ctrl+C 停止
```

启动日志示例：

```
xget-go v3.0.4 starting — port=8080 retries=3 timeout=30s platforms=84 cache=/var/lib/xget/cache (max 1024MB/file 512MB)
auth: none (parity with the official Xget) — expose via a reverse proxy with auth or a firewall/allowlist if the instance must not be public
listening on http://0.0.0.0:8080 — usage: http://<host>:8080/<prefix>/<path>
```

> ⚠️ 前台进程会随 SSH 会话断开而退出，生产环境请使用 systemd 或 Docker。本服务**无内置鉴权**（与官方一致），公网部署务必配合反代鉴权或防火墙（见[鉴权与凭证透传](#鉴权与凭证透传)）。

### 方式二：systemd 生产部署（推荐）

**第 1 步：安装二进制**（接第 0 步构建产物）

```bash
sudo cp xget-linux-amd64 /usr/local/bin/xget-go   # 按服务器架构选择
sudo chmod 0755 /usr/local/bin/xget-go
xget-go --version        # → xget-go v3.0.4 (port of xixu-me/Xget, AGPL-3.0-or-later)
sudo mkdir -p /var/lib/xget /etc/xget
```

**第 2 步：可选配置文件**（需要自定义端口等非默认值时才需要；本服务无内置鉴权，无需配置任何 token）

```bash
sudo sh -c 'echo "PORT=8080" > /etc/xget/xget.env'
# 可按需追加：TIMEOUT_SECONDS=30、CACHE_DIR 等，见《配置参考》
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

> 仓库 unit 默认 `ExecStart=/usr/local/bin/xget-go`、`Environment=PORT=8080`；`DynamicUser=yes` 会让服务以随机非特权用户运行。若需自定义端口等，直接在 unit 里加 `Environment=XXX=...` 或写进 `/etc/xget/xget.env`。**请勿把 8080 端口直接暴露公网**——本服务无内置鉴权，后续步骤的反代/防火墙是事实上的准入层。

**第 4 步：启动并设置开机自启**

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now xget          # 单元名取自文件名 xget.service
sudo systemctl status xget                # 确认 active (running)
```

**第 5 步：本机验证**

```bash
curl http://127.0.0.1:8080/healthz                                        # → ok
curl -sI "http://127.0.0.1:8080/gh/microsoft/vscode/archive/refs/heads/main.zip" | head -3
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
docker build -t xget-go:3.0.1 .

docker run -d --name xget \
  -p 127.0.0.1:8080:8080 \
  --restart unless-stopped \
  xget-go:3.0.1

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
    image: xget-go:3.0.1
    ports:
      - "127.0.0.1:8080:8080"
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/healthz"]
      interval: 30s
      timeout: 3s
```

### 反向代理与 HTTPS

生产环境强烈建议套一层 HTTPS 反代：Git/Docker 客户端在纯 HTTP 下会遇到各种限制（凭证明文、Docker 需要 `insecure-registries` 例外等），且反代可以缓冲慢客户端、隐藏后端进程，**还能承担实例准入控制**（本服务与官方一致无内置鉴权）。

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

#### 可选：反代层 basic auth（对外保护实例）

本服务与官方 Xget 一样**没有内置鉴权**；官方 README 明确建议自部署实例"put it behind authentication, IP allowlists, or both"。在 Nginx 上加两层认证即可：

```nginx
# 全站 basic auth（htpasswd -bc /etc/nginx/xget.htpasswd user）
location / {
    auth_basic "Xget";
    auth_basic_user_file /etc/nginx/xget.htpasswd;
    proxy_pass http://127.0.0.1:8080;
}

# 或仅放行可信网段
# allow 10.0.0.0/8; deny all;
```

> 注意：开启反代 basic auth 后，git clone 写法变为 `git clone https://user:pass@xget.example.com/gh/...`；Docker 场景改用 `docker login xget.example.com`。若实例仅内网使用，用防火墙/安全组限网段即可，无需 basic auth。

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

> xget-go 自身没有"仅绑定内网 IP"的选项（`PORT` 只决定端口），也没有内置鉴权（与官方一致）。若必须直接暴露（无反代），请用防火墙/安全组把端口限制到可信网段。

### 部署验证清单

全部完成后逐项确认：

```bash
curl -s https://xget.example.com/healthz                        # ① ok
curl -sI https://xget.example.com/gh/ | head -1                 # ② 302（裸前缀跳主页）
git clone https://xget.example.com/gh/octocat/Hello-World.git   # ③ git 加速
docker pull xget.example.com/cr/docker/library/alpine:latest    # ④ 镜像加速
curl -s -o /dev/null -w "%{http_code}\n" \
  https://xget.example.com/hf/bert-base-uncased/resolve/main/config.json   # ⑤ 200
```

## 配置参考

全部配置通过环境变量注入（与官方 Workers 版的变量名保持一致），无配置文件。**没有 `ACCESS_TOKEN`**：v3.0.0 起与官方对齐，实例无内置鉴权（对外防护见[鉴权与凭证透传](#鉴权与凭证透传)）。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8080` | 监听端口（监听 `0.0.0.0`） |
| `TIMEOUT_SECONDS` | `30` | 单次尝试的"响应头超时"（秒）。只约束到上游建立连接并收到响应头；之后的流式传输不受此限制，大文件不会中途超时 |
| `MAX_RETRIES` | `3` | 上游请求失败的最大尝试次数 |
| `RETRY_DELAY_MS` | `1000` | 线性退避基数：第 N 次重试前等待 `N × RETRY_DELAY_MS` 毫秒 |
| `MAX_PATH_LENGTH` | `2048` | URL 路径长度上限，超长返回 `414` |
| `CACHE_DURATION` | `300` | 可变资源的磁盘缓存 TTL（秒），同时作为 `Cache-Control: s-maxage` 提示。**官方同名变量**；`CACHE_TTL` 仍可用作兼容别名，两者同时设置时 `CACHE_DURATION` 优先 |
| `CACHE_TTL` | `300` | ⚠️ 已废弃的旧名（v3.0.2 前的本移植版变量名），仅为兼容保留；语义同 `CACHE_DURATION`，建议迁移到官方名 |
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
| 可变资源 | 其余 200 GET | `CACHE_DURATION`（默认 300s） | `max-age=0, must-revalidate` |
| 私有 | 请求带 `Authorization`/`Cookie`，或上游返回 `Set-Cookie`/`private` | 不缓存 | `private, no-store` + `Vary` |
| 旁路 | Git/LFS/Docker/AI/HF 协议、非 GET/HEAD | 不缓存 | `no-store` |
| 旁路（Vary） | 上游返回 `Vary: *`，或 `Vary` 含 `Origin`/`Cookie`/自定义等客户端可变头 | 不缓存 | `no-store` |

磁盘缓存以 URL 为键，无法像 Cloudflare Cache API 那样按 `Vary` 区分表示；因此对 `Origin` 等客户端可变头敏感的上游响应直接旁路，避免不同来源之间串味。缓存只写入 identity 表示，所以 `Vary: Accept-Encoding`/`User-Agent`/`Accept` 等头不会造成跨客户端串味；旧版本遗留的压缩条目仅会返回给显式声明支持该编码的客户端。

`X-Performance-Metrics` 与官方同构，输出按发生顺序记录的 mark 时间线（毫秒）：`attempt_0`/`attempt_1`… 每次上游尝试、`success` 上游 2xx/206、`client_error` 上游 4xx、`docker_auth_challenge` 进入匿名 token 重试、`cache_hit` 缓存命中、`cache_hit_full_content` Range 请求命中全量缓存、`range_cache_hit_after_full_cache` Range 请求在写入全量缓存后切片命中、`complete` 请求结束。例如 `{"attempt_0":12,"success":143,"complete":143}`。带凭证请求永不缓存，缓存命中同样支持 `Range` 断点切片。

### 日志格式

每次代理请求输出一行结构化日志（stdout，systemd 下进 journald）：

```
203.0.113.7 GET /gh/microsoft/vscode/archive/refs/heads/main.zip → https://github.com/... [gh] status=302 attempts=1 fetch_ms=143.2 total_ms=143.4
```

字段：客户端 IP、方法与路径、上游目标、命中平台、上游状态码、尝试次数、上游耗时、总耗时。

## 鉴权与凭证透传

**v3.0.0 起与官方 Xget 完全一致：本服务没有实例级鉴权。** 官方 Xget 自身不做任何访问控制，其 README 建议自部署者"put it behind authentication, IP allowlists, or both"——即把准入控制交给部署层（反代认证、IP 白名单、防火墙），本移植版遵循同一设计。所有进入实例的请求直接按平台前缀转发，不存在 401 门禁，也没有任何需要配置的 token 环境变量。

凭证头按官方语义处理：`Authorization` 在标准加速分支和协议分支都会透传上游；`Cookie`、`Proxy-Authorization` 及其它请求头仅在协议分支（Git/LFS/Docker/AI/HF）和非 GET/HEAD 请求中透传，标准 GET/HEAD 加速分支与官方 `createFetchOptions` 一致，只转发 `Origin`/`Authorization`/`Range` 等少量头。凭证用于上游自身的鉴权（私有仓库下载、Git push、docker login、AI API key 等）。本实例不消费、不校验、不存储任何凭证；Docker 注册表重定向跟随时会剥离 `Authorization`（与官方 `followDockerRedirectIfNeeded` 一致，避免凭证泄漏到重定向后的其它主机）。带凭证的请求也永远不会写入磁盘缓存（`private, no-store` + `Vary: Authorization, Cookie`）。

### 对外保护实例（替代旧版 ACCESS_TOKEN）

从 v2.x 升级的用户请把准入控制移到部署层，任选其一或组合：

**方案一：Nginx basic auth**（适合公网实例，配置见[反向代理与 HTTPS](#可选反代层-basic-auth对外保护实例)）：

```nginx
location / {
    auth_basic "Xget";
    auth_basic_user_file /etc/nginx/xget.htpasswd;
    proxy_pass http://127.0.0.1:8080;
}
```

**方案二：防火墙/安全组限网段**（适合内网或个人使用）：

```bash
sudo ufw allow from 10.0.0.0/8 to any port 8080 proto tcp
sudo ufw deny 8080/tcp
```

**方案三：仅绑定回环**（`-p 127.0.0.1:8080:8080` / 反代本机回环），让实例完全不可被外部直连。

### 上游凭证透传示例

**GitHub 私有仓库下载**（`Authorization` 直达 GitHub）：

```bash
curl -H "Authorization: Bearer $GH_PAT" \
     -LO "https://xget.example.com/gh/owner/private-repo/archive/main.tar.gz"
```

**git clone 私有仓库**（凭证交给 git credential helper，与本实例无关）：

```bash
git clone https://xget.example.com/gh/owner/private-repo.git
# 提示输入用户名密码时填 GitHub 用户名与 PAT
```

**AI 推理 API key**（`/ip/*` 前缀，key 透传给推理商）：

```bash
curl -X POST "https://xget.example.com/ip/openai/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

### Docker 场景说明

- **私有镜像**：`docker login <实例域名>[/cr/<仓库>]` 后即可拉取私有镜像（凭证经 `/v2/auth` 登录链与上游交换，实例不存储凭证）。v3.0.0 起登录链不再被实例鉴权拦截，Basic 凭证畅通无阻
- **`daemon.json` 镜像源模式**：因实例无鉴权，`registry-mirrors` 直接可用，无需任何特殊配置
- **公开镜像加速**：无需登录，直接 `docker pull <实例>/cr/<仓库>/<镜像>`

## 使用示例

以下示例统一使用 `https://xget.example.com`，请替换为你的域名（或 `http://IP:8080`）。实例无鉴权（与官方一致），上游需要的凭证（GitHub PAT、AI API key 等）直接用 `Authorization` 头传递即可，本实例会原样透传（见[鉴权与凭证透传](#鉴权与凭证透传)）。

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

### AI 推理代理（29 家）

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
| `GET /healthz` | 存活探针，恒定返回 `ok`。适合负载均衡健康检查、Uptime Kuma、k8s livenessProbe |
| `X-Performance-Metrics` 响应头 | 非 Git/LFS/Docker 请求附带 mark 时间线 JSON：`{"attempt_0":12,"success":143,"complete":143}`（键名与官方一致），可采样统计各阶段耗时 |

监控建议：对 `/healthz` 做 30s 间隔探活；对热门 release 抽样比较 `success` 与 `attempt_0` 的差值观察上游质量；出现 `attempt_1` 及以上说明发生过重试，可调大 `MAX_RETRIES`/`RETRY_DELAY_MS`。

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
| 私有仓库/镜像 401 | 上游拒绝（凭证无效/过期/无权限） | 本实例不拦截凭证，直接查上游：PAT 是否有效、是否有该仓库权限 |
| 私有镜像 401（已 docker login） | 登录链要求实例可直达上游 token 服务 | 检查上游 registry 与其 token 服务连通性；确认 realm 指向本实例 `/cr/<仓库>/v2/auth` |
| 对外实例被滥用 | 实例无鉴权（与官方一致）且暴露公网 | 反代 basic auth / 防火墙限网段，见[鉴权与凭证透传](#鉴权与凭证透传) |
| `400` 提示路径非法 | URL 含 `..` 穿越或控制字符 | 属预期防护，检查拼接逻辑 |
| `405 Method not allowed` | 非协议请求用了白名单外方法（如 npm publish 的 `PUT`） | `ALLOWED_METHODS=GET,HEAD,PUT,POST` |
| `414` | URL 超过 `MAX_PATH_LENGTH` | 调大或缩短路径 |
| `502/504` 且日志 `attempts=3` | 上游不可达（服务器出网被限/DNS 故障/上游宕机） | `curl -v <上游URL>` 自测出网；检查安全组出方向 |
| 下载无进度条/无文件大小 | 上游 chunked 无 `Content-Length` | 已内置 HEAD/Range 探测回填；仍缺失则上游本身无长度信息 |
| 缓存不生效 | 带 `Authorization`/`Cookie`（永不缓存）或资源属旁路类 | 去掉凭证头重试；观察 `X-Performance-Metrics` 是否出现 `cache_hit` |
| 磁盘占用过高 | `CACHE_MAX_MB` 预算过大或热点资源多 | 调小预算（LRU 自动驱逐）或 `CACHE_DIR=off` 关闭缓存 |
| 首字节慢但下载快 | 上游冷启动/重试退避中 | 观察 `X-Performance-Metrics` 的 `attempt_N` 间隔；调小 `RETRY_DELAY_MS` |
| `git push` 被拒 | 上游凭证缺失/权限不足 | xget-go 只透传，凭证问题直接查上游（GitHub 需 PAT） |

## 安全加固清单

- [ ] **对外防护**：本服务无内置鉴权（与官方一致）——公网实例套反代 basic auth，或防火墙/安全组限可信网段（官方 README 同样建议自部署实例置于鉴权或白名单之后）
- [ ] **全程 HTTPS**：Caddy 自动证书或 Nginx + certbot；纯 HTTP 会明文暴露透传的上游凭证（PAT、API key）
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
internal/proxy/              请求管线：协议检测(Git/LFS/Docker) → 校验 →
                             重试转发 → HEAD 探测 → 响应终结
                             （handle-request / fetch-upstream / validation.js）
internal/security/           安全头、CORS、JSON 错误契约（utils/security.js）
build.sh                     三平台静态构建脚本
xget.service                 systemd 单元（含安全加固）
Dockerfile + .dockerignore   多阶段镜像构建（golang:1.23 编译 → alpine 运行）
```

请求管线（一次代理的完整路径）：

```
healthz? → 协议识别(Git/LFS/Docker/AI/HF) → CORS 预检
→ Docker /v2 探针 → 裸前缀 302 → 校验(穿越/长度/方法) → 平台最长前缀匹配
→ 路径改写(crates、/v2/cr 归一化) → 缓存查询 → 上游转发(重试+线性退避)
→ 响应终结(错误包装/文本改写/跳段头剥离/分级Cache-Control/安全头/性能头)
→ 边下载边写缓存(T形流) / 缓存命中直接切片服务
```

## 实测覆盖场景

v2.0.0 发布前以下场景均在真实上游实测通过（40 项集成用例全绿）；v2.0.1 一致性审查后另以 34 项回归用例（覆盖 Jenkins 改写、百分号编码校验、全局响应头、Docker 匿名链、Range/HEAD 回填、真实 git clone、磁盘缓存命中、pypi/npm 改写与旁路）复验全绿；v3.0.0 鉴权对齐改造后复验（无门禁、凭证透传、git/docker 链路、缓存行为不变）：v3.0.1 二轮审查后以 8 项 mock 上游回归（重试耗尽返回最后上游响应、304 条件透传、匿名重试保留 PUT 方法与 2xx token、HEAD 不读缓存、配置化 ACA-M、302 空体、registry JSON 401 透传、GET 不转发请求体）加 12 项真实上游冒烟复验全绿；v3.0.2 三轮审查后回归扩至 12 项 mock 测试（新增 Docker 非 401 错误获得标准 JSON 包装、Git 协议错误全状态码原样透传、缓存命中体与首次透传逐字节一致、命中 Range 切片字节正确）；v3.0.3 四轮双端比对后回归扩至 15 项（新增错误契约头与毫秒时间戳、性能 mark 时间线、Range 全量缓存后切片与 mark）；v3.0.4 模块级复核后扩至 18 项（新增 Git service 短路、/v2/auth 协议响应头、协议主页 302 头集合），18 项全绿：

- `git clone` / `ls-remote`（GitHub）、Git 协议 header 透传
- GitHub release 下载（HEAD Content-Length 探测回填）、`Range` → `206` 断点续传
- **磁盘缓存**：首次 `miss` 落盘 → 二次 `hit`（0.2ms 级，v3.0.2 起命中体与首次透传**逐字节比对一致**）、缓存命中 Range 切片（v3.0.2 起与上游原始切片逐字节一致）、带 Cookie 请求 `private/no-store` 不缓存、LRU 驱逐（1MB 预算下旧条目被逐出）
- **Docker 登录链**：`/cr/docker/v2/auth` 换取 token、`/cr/ghcr/v2/auth`、非法 scope `400`、裸 `/v2/auth` `400`、manifest 匿名 token 链 `200`、对拒绝资源返回指向本实例的合成 401 挑战
- **AI 前缀**：`/ip/openrouter/api/v1/models` 200；`/ip/openai` 错误体原样透传（无包装）；POST 透传
- **HF 协议**：`/hf/api/models` 200
- **内容改写**：PyPI HTML → `/pypi/files/`、npm JSON → `/npm/`、flathub `.flatpakrepo` → `/flathub/repo/`（上游直链零残留）
- **新平台**：debian/arch/cran/apache/aosp 可达；fedora `/pub/fedora/linux` 前缀剥离与直路径双形态 200
- 路径穿越 `..` → `400`；方法白名单外 → `405`
- 凭证透传（v3.0.0）：带任意 `Authorization` 的请求不再 401，原样到达上游（GitHub 对无效 PAT 返回其自身的 401 语义）；`/healthz` 恒 200

## 路线图

| 能力 | 状态 | 说明 |
|------|------|------|
| ~~磁盘缓存层~~ | ✅ v2.0.0 | 三级 TTL 分级 + LRU 驱逐，`CACHE_DIR/CACHE_MAX_MB/CACHE_MAX_FILE_MB` 可配 |
| ~~AI 推理前缀 (`ip-*`)~~ | ✅ v2.0.0 | 29 家全量，POST/流式透传 |
| ~~`/v2/auth` 完整登录链~~ | ✅ v2.0.0 | Docker 私有仓库 `docker login` 支持 |
| ~~平台目录全量~~ | ✅ v2.0.0 | 23 → 84 项，与官方目录一致 |
| 监听地址选项 | 规划中 | `BIND_ADDR` 环境变量，支持仅绑定内网 IP |
| Prometheus 指标 | 评估中 | `/metrics` 端点暴露请求计数、缓存命中率与延迟直方图 |

## 致谢与许可证

- 原始设计与算法：[xixu-me/Xget](https://github.com/xixu-me/Xget) 及其社区，感谢作者 [@xixu-me](https://github.com/xixu-me) 的开源贡献
- 本移植版为同一许可证下的衍生作品：**AGPL-3.0-or-later**，任何修改版对外提供服务时须同样开源（详见 [LICENSE](LICENSE)）

Xget-Go · 以单二进制承载 Xget 的加速体验
