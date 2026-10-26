版本发布、交付与升级
============================================================

本页说明如何把已验证的 Controller 源码交付为 Linux 二进制、Docker 镜像和 HTML 手册。
首次安装和启动见 :doc:`source-install`，完整实验部署见 :doc:`docker-deploy`。
当前没有 make install / make release 或自动发布流水线，下面是手工发布流程。
示例版本 ``v0.1.0`` 仅用于说明命名，不表示已有公开发行版；发布时替换为实际版本。

.. note::

   ``v1.0`` 已发布带文档的 Docker 离线包。维护者制作 ``.tar.xz``、发布 Git tag、
   创建 Release 及补传附件的完整命令见 :doc:`/developer/release`。
   本页保留原生二进制、镜像仓库分发和部署升级说明，下面的 ``v0.1.0`` 为示例占位符。

依赖与验证范围
------------------------------------------------------------

.. list-table:: 交付各阶段的依赖
   :header-rows: 1
   :widths: 26 74

   * - 环节
     - 依赖
   * - 原生编译
     - Linux、Go 1.25+、GNU Make、模块依赖，以及相邻 sdn-agent 的公开协议源码
   * - 单元 / race 检查
     - Go、支持 race 的平台和 C 编译器；容器监督逻辑检查使用 Python 3
   * - 原生运行
     - Controller / sdnc 二进制、部署凭据、可达的 Agent；选择 etcd 模式时另需 etcd
   * - Docker 构建
     - Docker / BuildKit、相邻 Agent 协议源码；完整部署另需 sdn-web 源码及 Compose、Python 3
   * - 文档
     - Python 3.10+ 和 docs/requirements.txt 中的独立 Sphinx 依赖
   * - 归档
     - Git、tar、sha256sum；发布镜像还需自己的镜像仓库及推送权限

Controller 本机不需要 Linux 网络管理权限、FRR 或 Redis。
默认 Controller 镜像包含单成员 etcd；原生二进制不会自动启动数据库。
Agent 执行本地网络操作，Web 提供浏览器入口，三者分别交付并记录兼容版本。

当前 go.mod 使用 ``replace sdn-agent => ../sdn-agent``，因此至少记录 Controller 和 Agent
两个源码提交。先把计划交付的源码、生成协议、模板、测试和文档纳入 Git，
从干净工作目录构建；尚未初始化 Git 的源码副本应先完成项目版本管理，再进行正式发布。
凭据、私钥、Controller 快照、Agent 身份、数据库卷和运行目录不进入发布包。

.. code-block:: bash
   :caption: Linux 发布机：sdn-controller 根目录，已安装 Go / C 工具链并激活文档虚拟环境

   go mod download
   make test
   make vet
   make build
   python3 -m unittest discover -s docker -p '*test*.py' -v
   make -C docs html

``make test`` 已包含 ``go test -race ./...``，没有单独的 make race 目标。
单元测试、真实 etcd 条件测试和 Docker 网络验收的区别见 :doc:`/developer/testing`。
涉及资源行为、存储恢复或基础镜像变化时，在专属测试环境补充相应集成验证；
不能把历史验收报告或本次文档构建结果作为新版转发、升级或降级证据。

Linux 二进制交付
------------------------------------------------------------

以下命令在与目标相同架构的 Linux 发布机执行。它创建新的版本目录，不覆盖已有发布。
软件目前没有内置版本查询参数，使用 RELEASE.txt 和 Go 构建信息追溯版本。

.. code-block:: bash
   :caption: Bash：sdn-controller 根目录，前述验证已通过

   set -eu
   SDNC_VERSION='v0.1.0'
   case "$SDNC_VERSION" in ''|*[!A-Za-z0-9._-]*|.|..) echo 'invalid release version' >&2; exit 1;; esac
   SDNC_COMMIT="$(git rev-parse HEAD)"
   SDNC_AGENT_COMMIT="$(git -C ../sdn-agent rev-parse HEAD)"
   test -z "$(git status --porcelain)"
   test -z "$(git -C ../sdn-agent status --porcelain)"
   test "$(go env GOOS)" = linux
   test "$(go env GOHOSTOS)" = linux
   test "$(go env GOARCH)" = "$(go env GOHOSTARCH)"
   SDNC_ARCH="$(go env GOARCH)"
   SDNC_PACKAGE="sdn-controller-${SDNC_VERSION}-linux-${SDNC_ARCH}"
   SDNC_RELEASE_DIR="$PWD/artifacts/releases/$SDNC_VERSION"
   test ! -e "$SDNC_RELEASE_DIR"
   mkdir -p "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/bin"

   CGO_ENABLED=0 go build -trimpath -o "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/bin/sdn-controller" ./cmd/sdn-controller
   CGO_ENABLED=0 go build -trimpath -o "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/bin/sdnc" ./cmd/sdnc
   "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/bin/sdn-controller" -h
   "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/bin/sdnc" -h
   cp README.md "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/"
   printf '%s\n' "version=$SDNC_VERSION" "controller_commit=$SDNC_COMMIT" "agent_protocol_commit=$SDNC_AGENT_COMMIT" "platform=linux/$SDNC_ARCH" > "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/RELEASE.txt"
   go version -m "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/bin/sdn-controller" > "$SDNC_RELEASE_DIR/$SDNC_PACKAGE/BUILD-INFO.txt"
   tar -C "$SDNC_RELEASE_DIR" -czf "$SDNC_RELEASE_DIR/$SDNC_PACKAGE.tar.gz" "$SDNC_PACKAGE"
   (cd "$SDNC_RELEASE_DIR" && sha256sum "$SDNC_PACKAGE.tar.gz" > SHA256SUMS)

交付 tar.gz、SHA256SUMS、同版本完整 HTML 手册，以及测试和 Agent / Web 兼容记录。
二进制包不包含 etcd 或部署凭据。部署方按 :doc:`configuration-file` 创建自己的原生配置，
不要直接把内置 etcd 已启用的 Docker 模板当作原生服务启动数据库的方法。

.. code-block:: bash
   :caption: Linux 接收机：交付文件所在目录，替换实际版本和架构

   sha256sum -c SHA256SUMS
   tar -xzf sdn-controller-v0.1.0-linux-amd64.tar.gz
   ./sdn-controller-v0.1.0-linux-amd64/bin/sdn-controller -h
   ./sdn-controller-v0.1.0-linux-amd64/bin/sdnc -h

然后按 :ref:`controller-source-install` 将两个程序安装到 /usr/local/bin，
配置稳定凭据、File / etcd 存储并启动，查询 nodes / configuration 验证认证和服务。
运行机不再需要 Go 或相邻源码；其他架构必须单独构建并在目标平台验证。

Docker 镜像发布
------------------------------------------------------------

Controller Dockerfile 从共同父目录读取 sdn-controller 与 sdn-agent/api。
先对干净源码和待用镜像完成验证，再执行下面的实际推送步骤。
``registry.example.com`` 是占位符，替换成自己的镜像仓库。

.. code-block:: bash
   :caption: Bash：共同源码父目录，例如 ~/program/github

   set -eu
   SDNC_VERSION='v0.1.0'
   SDNC_REGISTRY='registry.example.com'
   case "$SDNC_VERSION" in ''|*[!A-Za-z0-9._-]*|.|..) echo 'invalid release version' >&2; exit 1;; esac
   SDNC_IMAGE="$SDNC_REGISTRY/network/sdn-controller:$SDNC_VERSION"
   docker build -f sdn-controller/docker/Dockerfile -t "$SDNC_IMAGE" .
   docker run --rm --entrypoint /usr/local/bin/sdn-controller "$SDNC_IMAGE" -h
   docker run --rm --entrypoint /usr/local/bin/sdnc "$SDNC_IMAGE" -h
   docker login "$SDNC_REGISTRY"
   docker push "$SDNC_IMAGE"
   docker image inspect "$SDNC_IMAGE" --format '{{json .RepoDigests}}'

两个 -h 命令仅检查程序入口，不启动内置数据库，不等于服务健康或业务验收。
镜像构建参数 GO_IMAGE、ALPINE_IMAGE、ETCD_IMAGE 可固定为已验证的 digest；
同时记录实际 Go、Alpine 软件包和 etcd 版本，不能仅凭可变 tag 宣称可复现。
保存推送返回的仓库 digest，同一发布版本不要重新指向另一份镜像内容。

离线交付使用同一终端中的镜像变量：

.. code-block:: bash

   SDNC_IMAGE_ARCH="$(docker image inspect "$SDNC_IMAGE" --format '{{.Architecture}}')"
   SDNC_IMAGE_TAR="sdn-controller-${SDNC_VERSION}-${SDNC_IMAGE_ARCH}.image.tar"
   test ! -e "$SDNC_IMAGE_TAR"
   docker save -o "$SDNC_IMAGE_TAR" "$SDNC_IMAGE"
   sha256sum "$SDNC_IMAGE_TAR" > "$SDNC_IMAGE_TAR.sha256"

.. code-block:: bash
   :caption: Linux 接收机：镜像归档所在目录，替换实际版本和架构

   sha256sum -c sdn-controller-v0.1.0-amd64.image.tar.sha256
   docker load -i sdn-controller-v0.1.0-amd64.image.tar

在线接收方使用 docker pull。独立 Controller 部署按 :doc:`configuration-file`，
把示例的 sdn-controller:alpine 替换成交付镜像或 ``仓库路径@sha256:摘要``。
完整部署的 :doc:`docker-deploy` 默认使用本地源码镜像；交付部署定义时需显式替换
controller 的 image 并保存相应 Compose 配置，避免后续 build 覆盖既定发布引用。
完整系统另含兼容 Agent 和 Web 镜像；默认 Web 是 Nginx 与 Gateway 同容器运行。

HTML 手册交付与预览
------------------------------------------------------------

按 :doc:`/developer/build-docs` 安装本项目文档依赖。交付整个 html 目录，包含页面、
样式、脚本、搜索索引和下载的说明文件；不要仅复制 index.html。

.. code-block:: bash
   :caption: Bash：sdn-controller 根目录，已激活文档虚拟环境

   set -eu
   SDNC_VERSION='v0.1.0'
   make -C docs html
   mkdir -p artifacts/releases
   case "$SDNC_VERSION" in ''|*[!A-Za-z0-9._-]*|.|..) echo 'invalid release version' >&2; exit 1;; esac
   SDNC_DOC_ARCHIVE="sdn-controller-docs-${SDNC_VERSION}.tar.gz"
   test ! -e "artifacts/releases/$SDNC_DOC_ARCHIVE"
   tar -C docs -czf "artifacts/releases/$SDNC_DOC_ARCHIVE" html
   (cd artifacts/releases && sha256sum "$SDNC_DOC_ARCHIVE" > "$SDNC_DOC_ARCHIVE.sha256")

接收方校验对应 sha256 文件后解压，把完整 html 目录放入自己的静态站点目录。
站点地址和访问控制由部署方设置。发布前可本机预览：

.. code-block:: bash
   :caption: 文档预览机：sdn-controller 根目录

   python3 -m http.server 8000 --bind 127.0.0.1 --directory docs/html

在同机浏览器访问 http://127.0.0.1:8000/，检查用户 / 开发手册、表格和搜索。
Ctrl+C 停止文档预览。这个服务只提供手册，不启动 Controller 或 Web 管理界面。

升级与回退
------------------------------------------------------------

当前没有通用数据库自动迁移、自动降级或完整主机网络重建工具。
Controller 快照 schema_version=1 并不保证所有版本都兼容；
还要检查 Operation payload、Applied、历史任务和幂等收据能否由新旧版本正确解释。

1. 保存旧程序或镜像 digest、源码提交、部署配置、凭据及新旧兼容性验证记录。
   协调 Agent / Web 协议版本，并在隔离环境验证已有快照和未终态任务的读取与恢复。
2. 暂停新配置提交，记录 Desired revision、各节点 Agent revision、任务 ID 和状态。
   优先等待正常任务结束；UNKNOWN / ROLLBACK_FAILED 按 :doc:`recovery` 处理，
   不能删除旧 Job 或重新生成请求 ID 绕过不确定结果。
3. 确认唯一 Controller 进程停止后，为原存储制作一致备份。
   File 模式保留原状态文件；etcd 使用与所部署版本匹配的快照 / 恢复流程。
   同时记录 Agent 身份、任务日志、FRR 配置和实际网络状态；无统一跨服务备份工具。
   内置 etcd 的镜像升级也要核对 etcd 数据兼容性，不在线随意复制数据库目录。
4. 原生部署按安装教程替换二进制；Docker 仅更新本次计划涉及的服务镜像，
   保留原 etcd 卷、成员名称、key 前缀、令牌、端口和外部配置。
   禁止让旧、新数据库进程同时打开同一卷。不要用 reset 或删除卷完成升级。
5. 使用原配置启动唯一 Controller，检查配置预检、健康、节点列表、Desired 和原 Job。
   触发拓扑 refresh 重建内存观测，并独立核对实际网络；只升级 Controller 不会重建 Agent 网络命名空间。
   若同时重建 Agent 容器，先准备网络恢复方案，参见 :doc:`recovery`。
6. 只有旧版本能读取当前状态且降级路径已验证时，才停止新版并换回旧程序 / 镜像。
   恢复旧数据库不会撤销已执行的网络操作，需协调实际数据面、Agent 日志和 Controller 基线，
   验证结果一致后再恢复新变更提交。

发布、目标部署、升级和回退演练应分别保留实际结果；本页命令和文档构建不代表这些动作已经执行。
