# CoinMark AWS 部署指南（方案 1：S3 + CloudFront + EC2）

本文目标：把 CoinMark 做到“能真实上线 + 可演示”，从而在简历里**名正言顺**写到：
- `AWS S3`（静态站点托管）
- `AWS CloudFront`（CDN 加速）
- `AWS EC2`（后端容器化部署）

> 约定：前端为 Vite 构建产物（静态文件），后端为 FastAPI（HTTP API）。  
> 说明：MVP 不涉及下单/交易；不在仓库中保存任何密钥、私钥、登录口令。

---

## 0. 前置准备

### 0.1 你需要准备
- AWS 账号（建议单独建一个“练习/作品集”账号或使用独立预算）
- 一个域名（可选，但强烈建议：利于 HTTPS 与展示；Route 53/Cloudflare 都可以）
- 本地已能跑通：`docker compose up -d`（后端 + 数据库/缓存）

### 0.2 安全建议（重要）
- **不要**把 AWS Access Key 写进代码或 `.env` 并提交
- EC2 安全组只开放必要端口（建议只开 `80/443`，SSH 限制到你的 IP）
- HTTPS 用 Let's Encrypt（ACME），证书自动续期

---

## 1. 部署前端：S3 静态托管 + CloudFront 加速

### 1.1 构建前端产物
在前端目录执行（示例）：
- `npm ci`
- `npm run build`

产物通常在 `dist/`。

### 1.2 创建 S3 Bucket
建议命名：`coinmark-<yourname>-web`（全局唯一）
- 关闭 Public Access（保持桶私有）
- 开启静态托管：不直接用 S3 Website（推荐用 CloudFront + OAC 访问私有桶）

### 1.3 上传构建产物到 S3
方式任选其一：
- AWS Console 手动上传（适合第一次）
- AWS CLI：`aws s3 sync dist s3://<bucket-name> --delete`

### 1.4 创建 CloudFront Distribution
关键点：
- Origin 选择你的 S3 bucket
- 启用 OAC（Origin Access Control），让 CloudFront 访问私有桶
- Default root object：`index.html`
- SPA 路由：把 403/404 重写到 `/index.html`（前端路由用 history 模式时必需）

### 1.5 绑定自定义域名 + HTTPS（可选但推荐）
两种常见方式：
- Route 53：创建 A/AAAA（Alias）指向 CloudFront
- Cloudflare：CNAME 到 CloudFront 域名

证书：
- ACM 证书必须在 `us-east-1` 申请（CloudFront 要求）

---

## 2. 部署后端：EC2 + Docker Compose + Nginx + HTTPS

### 2.1 创建 EC2
推荐配置（MVP 够用即可）：
- AMI：Ubuntu 22.04 LTS
- 实例：`t3.small` 起步（视预算调整）
- 磁盘：30GB（日志 + Docker 镜像空间）

安全组建议：
- Inbound：`80/443` 对公网开放
- SSH：`22` 只允许你的公网 IP（或使用 SSM Session Manager）

### 2.2 安装 Docker / Docker Compose
在 EC2 上执行（示例思路）：
- 安装 Docker Engine
- 安装 docker compose 插件
- 将你的用户加入 docker group（可选）

### 2.3 部署后端服务（推荐结构）
建议在 EC2 上目录：
- `/opt/coinmark/`：后端代码与 `compose.yml`
- `/opt/coinmark/.env`：运行时环境变量（不要提交到 git）

运行：
- `docker compose up -d --build`

建议做一个健康检查接口：
- `GET /healthz` 返回 `{"ok": true}`（用于 Nginx/监控）

### 2.4 配置 Nginx 反向代理
Nginx 职责：
- 终止 TLS（HTTPS）
- 反代到 FastAPI（例如 `http://127.0.0.1:8000`）
- 配置基础安全头（可选）

### 2.5 申请 HTTPS 证书（Let's Encrypt）
推荐使用 `certbot`：
- 首次签发证书
- 配置定时续期（systemd timer/cron）

---

## 3. 前后端联通与 CORS

两种推荐方式（任选其一）：
1) **同域名不同路径（推荐）**
   - `https://coinmark.example.com/`（前端）
   - `https://coinmark.example.com/api/...`（后端，经 Nginx 转发）
   - 优点：几乎不需要 CORS，部署更稳
2) 不同子域名
   - `https://app.example.com` + `https://api.example.com`
   - 需要正确配置 CORS、Cookie/鉴权策略（MVP 更容易踩坑）

---

## 4. 可观测性（可选但很加分）

### 4.1 CloudWatch Logs
收集：
- Nginx access/error logs
- API 容器日志（docker logs 输出到文件或 journald）

### 4.2 基础告警（可选）
- 5xx 比例异常
- p95 延迟超阈值（后续有指标再做）

---

## 5. 回滚策略（面试很加分）

至少做到以下两点：
- 前端：保留上一版构建产物（或用版本化目录），出问题可快速回滚
- 后端：镜像 tag 固定版本（不要只用 latest），出问题可 `docker compose down && docker compose up -d` 指向旧 tag

---

## 6. 你可以在简历里怎么写（务必真实）

当你完成部署后，才建议写：
- 将前端静态站点部署到 `AWS S3 + CloudFront`，提供全球加速与 HTTPS；配置 SPA 路由回退与缓存失效策略。
- 在 `AWS EC2` 上以 Docker Compose 部署后端 API，并通过 `Nginx` 实现 HTTPS 反向代理与健康检查；实现基础回滚流程与日志采集（CloudWatch 可选）。

