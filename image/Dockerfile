# syntax=docker/dockerfile:1

# BASE_IMAGE 无默认值：digest 唯一来源是 image/base.lock，由 Makefile / CI 读取后注入
ARG BASE_IMAGE
ARG S6_OVERLAY_VERSION=3.2.3.2

# ---------- stage 1：编译 sidecar（CGO 关闭，静态单二进制，musl/glibc 通吃） ----------
# 架构用 uname -m 自探测：兼容 classic builder（无 TARGETARCH 注入）；
# buildx 多架构构建时本 stage 在模拟的目标架构内执行，uname 结果与 GOARCH 一致
FROM golang:1.23-alpine AS build-sidecar
WORKDIR /src
COPY sidecar/go.mod sidecar/go.sum* ./
RUN go mod download
COPY sidecar/ ./
RUN set -eux; \
    case "$(uname -m)" in \
      x86_64) GOARCH=amd64 ;; \
      aarch64) GOARCH=arm64 ;; \
      *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    CGO_ENABLED=0 GOOS=linux GOARCH=${GOARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ssc-sidecar .

# ---------- stage 2：下载 s6-overlay ----------
# base 镜像内无 curl、wget 损坏（assignment-findings ④），须在有可用 wget 的阶段下载
FROM alpine:3.22 AS fetch-s6
ARG S6_OVERLAY_VERSION
RUN set -eux; \
    case "$(uname -m)" in \
      x86_64) S6_ARCH=x86_64 ;; \
      aarch64) S6_ARCH=aarch64 ;; \
      *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    wget -q -O /tmp/s6-noarch.tar.xz "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz"; \
    wget -q -O /tmp/s6-arch.tar.xz "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${S6_ARCH}.tar.xz"

# ---------- final ----------
FROM ${BASE_IMAGE}
ARG S6_OVERLAY_VERSION

# base 镜像把 LD_LIBRARY_PATH 写进镜像 ENV，污染所有 RUN（apk 会撞上镜像捆绑的
# 不完整 glibc libssl/libcrypto 而崩溃）。清空恢复 musl 工具链；
# subconverter/run 按架构自行设置 LD_LIBRARY_PATH，不受影响
ENV LD_LIBRARY_PATH=

# Extended 是 glibc 二进制；nginx/xz 为 musl 原生版，互不影响（assignment-findings ④）
RUN apk add --no-cache nginx xz

COPY --from=fetch-s6 /tmp/s6-noarch.tar.xz /tmp/s6-arch.tar.xz /tmp/
# s6 tarball 含符号链接与权限位，须 -p 保留
RUN xz -dc /tmp/s6-noarch.tar.xz | tar -C / -xpf - && \
    xz -dc /tmp/s6-arch.tar.xz | tar -C / -xpf - && \
    rm -f /tmp/s6-noarch.tar.xz /tmp/s6-arch.tar.xz

COPY --from=build-sidecar /out/ssc-sidecar /usr/bin/ssc-sidecar

# 完整自包含 nginx 配置，不 include http.d（规避 Alpine 默认 default.conf 抢占 80）
COPY image/nginx.conf /etc/nginx/nginx.conf

# 前端静态产物：构建发生在 docker build 之前（Makefile / CI 先跑 npm build）
COPY frontend/dist /usr/share/nginx/html

# s6 服务树（nginx / sidecar / subconverter 三个 longrun + managed-config oneshot）+ user bundle
# 注意：s6-rc oneshot 的 up 文件是 execline 脚本（shebang 不生效），故只放一行指向 sh 脚本
COPY image/s6/ /etc/s6-overlay/
RUN chmod +x /etc/s6-overlay/s6-rc.d/*/run /etc/s6-overlay/s6-rc.d/*/up /etc/s6-overlay/scripts/*.sh

# base 镜像继承的 Cmd 是 /usr/local/bin/start-subconverter，必须清空，否则它会变成 /init 的参数
ENTRYPOINT ["/init"]
CMD []
EXPOSE 80
