# syntax=docker/dockerfile:1
# 基础镜像可用构建参数替换成镜像站（国内拉 Docker Hub 慢时用），例如：
#   docker build --build-arg BASE_GO=docker.m.daocloud.io/library/golang:1.22-alpine \
#                --build-arg BASE_RUN=docker.m.daocloud.io/library/alpine:3.20 .
ARG BASE_GO=golang:1.22-alpine
ARG BASE_RUN=alpine:3.20

# ---------- build ----------
FROM ${BASE_GO} AS build
WORKDIR /src

# 国内网络友好：GOPROXY 走 goproxy.cn（本项目零外部依赖，此步主要用于 go mod 校验）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

COPY go.mod ./
RUN go mod download || true

COPY . .

# 交叉编译：CGO 关闭 → 静态二进制（buildx 传 TARGETOS/TARGETARCH；
# 普通 docker build 时兜底为 linux + 镜像自身架构，避免误编出宿主平台二进制）
ARG TARGETOS
ARG TARGETARCH
RUN set -eux; \
    GOOS_T="${TARGETOS:-linux}"; \
    GOARCH_T="${TARGETARCH:-$(go env GOHOSTARCH)}"; \
    CGO_ENABLED=0 GOOS="$GOOS_T" GOARCH="$GOARCH_T" \
      go build -trimpath -ldflags="-s -w" -o /out/longcat2api ./cmd/server; \
    CGO_ENABLED=0 GOOS="$GOOS_T" GOARCH="$GOARCH_T" \
      go build -trimpath -ldflags="-s -w" -o /out/longcat-login ./cmd/login

# ---------- runtime ----------
FROM ${BASE_RUN}

# ca-certificates: 访问 https://nocode.cn 等上游必需
# tzdata: 正确的时间/日志时区   wget: HEALTHCHECK 探活
RUN apk add --no-cache ca-certificates tzdata wget \
 && adduser -D -u 10001 app \
 && mkdir -p /app/data /app/config \
 && chown -R app:app /app

WORKDIR /app

COPY --from=build /out/longcat2api  /app/longcat2api
COPY --from=build /out/longcat-login /app/longcat-login
COPY config.example.json /app/config.example.json

ENV TZ=Asia/Shanghai \
    LONGCAT_LISTEN=":8090"

# 默认仍以 app 用户运行；compose 里可用 PUID/PGID 覆盖成宿主 uid 以免挂载目录写不进去
USER app

EXPOSE 8090
VOLUME ["/app/data"]

# 探活端口跟随 LONGCAT_LISTEN（形如 ":8090"），改了监听端口也能自检
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD sh -c 'L=${LONGCAT_LISTEN:-:8090}; wget -qO- "http://127.0.0.1:${L#:}/healthz" >/dev/null 2>&1 || exit 1'

ENTRYPOINT ["/app/longcat2api", "-config", "/app/config/config.json"]
