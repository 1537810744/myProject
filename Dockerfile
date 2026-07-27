# ============================================================
# DeltaCrypto 资金费率套利工具 - 多阶段构建
#
# 阶段 1（frontend）：构建 React+TS 像素风前端（Vite）
# 阶段 2（builder） ：编译 Go 后端二进制
#   - ccxt Go SDK 体积巨大，编译需要约 4GB+ 内存、几分钟时间，
#     请确保 Docker Desktop 内存 >= 4GB（建议 6GB+）
# 阶段 3（runtime） ：极简 Alpine 运行镜像
# ============================================================

# ---------- 阶段 1：前端构建 ----------
FROM node:24-alpine AS frontend

WORKDIR /fe

# 先拷贝依赖清单并安装（利用 Docker 层缓存）
COPY web/frontend/package.json web/frontend/package-lock.json ./
RUN npm ci

# 拷贝前端源码并构建（产物输出到 /fe/../static 即 /static）
COPY web/frontend ./
RUN npm run build

# ---------- 阶段 2：后端编译 ----------
FROM golang:1.25-alpine AS builder

WORKDIR /build

# 先拷贝依赖清单并下载（利用 Docker 层缓存：源码变动不重新下载依赖）
COPY go.mod go.sum ./
RUN go mod download

# 拷贝后端源码（不含前端目录，见 .dockerignore）
COPY . .
# CGO_ENABLED=0：纯静态编译（sqlite 用纯 Go 驱动 modernc.org/sqlite，无需 CGO）
# -ldflags="-s -w"：去除调试符号，减小二进制体积
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/deltacrypto ./cmd/server

# ---------- 阶段 3：运行 ----------
FROM alpine:3.21

# 时区数据（日志时间按北京时间显示）+ CA 证书（访问交易所 HTTPS 必须）
RUN apk add --no-cache tzdata ca-certificates
ENV TZ=Asia/Shanghai

WORKDIR /app

# 拷贝后端二进制 与 阶段 1 构建的前端静态文件
COPY --from=builder /out/deltacrypto /app/deltacrypto
COPY --from=frontend /static /app/web/static

# 容器内监听所有网卡（宿主机通过端口映射访问）
# 数据目录 /app/data 存放 SQLite，建议挂载卷持久化
ENV LISTEN_ADDR=0.0.0.0:8080 \
    DB_PATH=/app/data/deltacrypto.db

VOLUME ["/app/data"]
EXPOSE 8080

# 启动命令
CMD ["/app/deltacrypto"]
