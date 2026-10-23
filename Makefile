# Smart SubConverter 本地开发入口
# 约定：宿主机不装 go/node，构建与测试全部走 docker（见 docs/implementation/）
SHELL := /bin/sh

IMAGE      := smart-subconverter:dev
RUN_NAME   := ssc-local
LOCAL_PORT := 18080

# base 镜像 digest 唯一来源是 image/base.lock（Dockerfile 的 BASE_IMAGE build-arg 从这里注入）
BASE_IMAGE := $(shell grep -m1 '^aethersailor' image/base.lock)

.PHONY: build-sidecar build-frontend-dist build-image run-local smoke-local stop-local

# docker 化 go vet+test+build（-e CGO_ENABLED=0：alpine 的 go 镜像无 gcc，CGO 开着编不过 net/http）
# 源码经 tar 流送入容器而非 bind mount：本机 colima VM 未挂载 /Volumes，bind mount 会挂成空目录
build-sidecar:
	tar -C sidecar -cf - . | docker run --rm -i -e CGO_ENABLED=0 \
		-v gomodcache:/go/pkg/mod -w /src \
		golang:1.23-alpine \
		sh -c 'tar -xf - && go vet ./... && go test ./... && go build -o /dev/null .'

# 前端产物：有 package.json 就真实构建（与 CI 同逻辑），否则放占位页（frontend/dist 为 gitignored）
# 真实构建失败时回退占位页并警告：本地开发不因前端 lane 半成品被卡死（CI 侧不回退，红就是红）
# 注意用子 shell cd 而非 npm --prefix：--prefix 会把依赖装进仓库根目录 node_modules（npm 已知怪癖）
# 占位页写入必须在同一逻辑行内（make 的每行 recipe 是独立 shell，独立行会无条件覆盖真构建产物）
build-frontend-dist:
	@if [ -f frontend/package.json ] && \
	   (cd frontend && (npm ci --no-audit --no-fund || npm install --no-audit --no-fund)) && \
	   (cd frontend && npm run build); then \
		exit 0; \
	fi; \
	echo "!!! 前端构建失败（或前端未就绪），回退占位页"; \
	mkdir -p frontend/dist && printf 'placeholder\n' > frontend/dist/index.html

build-image: build-frontend-dist
	docker build -f image/Dockerfile --build-arg BASE_IMAGE='$(BASE_IMAGE)' -t $(IMAGE) .

run-local:
	docker rm -f $(RUN_NAME) 2>/dev/null || true
	docker run -d --name $(RUN_NAME) -p 127.0.0.1:$(LOCAL_PORT):80 $(IMAGE)

smoke-local:
	sh image/smoke.sh http://127.0.0.1:$(LOCAL_PORT)

stop-local:
	docker rm -f $(RUN_NAME) 2>/dev/null || true
