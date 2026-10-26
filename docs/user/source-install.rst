.. _controller-source-install:

源码编译、安装与启动
============================================================

本页从源码目录开始，完成 Controller 和 CLI 的编译、安装、凭据配置、持久存储选择、
启动与验证。Controller 运行在 Linux；它不需要本机安装 FRR，也不需要接管业务网卡。
真正的 Linux / FRR 操作由各节点上的 Agent 执行。

想一次启动 Web、Controller、三个 Agent 及实验网络，直接使用
:ref:`controller-docker-deploy`，宿主机不必先安装 Go。

.. contents:: 本页步骤
   :local:
   :depth: 2

1. 准备依赖与源码
------------------------------------------------------------

.. list-table:: 源码构建依赖
   :header-rows: 1
   :widths: 28 72

   * - 依赖
     - 用途
   * - Linux、Go 1.25+
     - Go 版本下限来自项目 go.mod；编译 Controller 和 sdnc
   * - GNU Make、CA 证书
     - 使用 Makefile；下载 Go 模块及校验证书
   * - OpenSSL
     - 本教程生成随机令牌
   * - C 编译器
     - 仅运行 ``go test -race`` 时需要；静态发布构建使用 CGO_ENABLED=0
   * - etcd
     - 仅 etcd 存储模式需要；首次本地启动可先用文件模式
   * - 相邻的 sdn-agent 源码
     - 提供公开生成的 Protobuf；当前 go.mod 使用本地 replace

Debian / Ubuntu 可先安装基础工具：

.. code-block:: bash
   :caption: Linux 编译机

   sudo apt-get update
   sudo apt-get install -y make ca-certificates openssl

按 `Go 官方安装说明 <https://go.dev/doc/install>`_ 安装适合 CPU 架构的 Go 1.25 或更高版本，
并将 Go 的 bin 目录加入 PATH。发行版的软件包若低于此版本，不能用于本项目。
验证工具，不使用任何特定开发机的缓存路径：

.. code-block:: bash

   go version
   go env GOOS GOARCH
   make --version

将已有项目源码克隆或复制到同一父目录；本项目尚未提供可代替此目录关系的独立协议模块。
例如：

.. code-block:: text

   ~/program/github/
   ├── sdn-agent/
   │   ├── go.mod
   │   └── api/agent/v1/
   └── sdn-controller/
       ├── go.mod
       ├── Makefile
       └── cmd/

这些命令使用 ``~/program/github`` 作为示例位置，可以替换成自己的源码工作区。
单独编译 Controller 不要求 sdn-web 目录。

2. 下载依赖并编译
------------------------------------------------------------

.. code-block:: bash
   :caption: Linux 编译机：sdn-controller 仓库根目录

   cd ~/program/github/sdn-controller
   go mod download
   make build
   ls -l bin/sdn-controller bin/sdnc
   ./bin/sdn-controller -h
   ./bin/sdnc -h

``go mod download`` 需要能访问 Go 模块源，离线构建需提前准备对应模块缓存。
``make build`` 等价于以下两个静态构建：

.. code-block:: bash

   mkdir -p bin
   CGO_ENABLED=0 go build -o bin/sdn-controller ./cmd/sdn-controller
   CGO_ENABLED=0 go build -o bin/sdnc ./cmd/sdnc

成功标志是两个二进制存在，``-h`` 能打印参数。
普通编译使用仓库已生成的 ``.pb.go``，不需要 protoc。
修改协议后重新生成的工具要求见 :doc:`/developer/protocol`。
编译机和运行机架构应一致；本教程使用 ``go env GOARCH`` 显示的本机架构。

3. 安装程序
------------------------------------------------------------

在运行 Controller 的 Linux 主机安装；如果编译机与部署机不同，先把对应架构的
``bin/sdn-controller`` 和 ``bin/sdnc`` 复制到部署机，再执行：

.. code-block:: bash
   :caption: Linux 部署机：从包含 bin/ 的源码或发布目录执行

   sudo install -m 0755 bin/sdn-controller /usr/local/bin/sdn-controller
   sudo install -m 0755 bin/sdnc /usr/local/bin/sdnc
   /usr/local/bin/sdn-controller -h
   /usr/local/bin/sdnc -h

项目目前没有 ``make install``；这里明确安装到 ``/usr/local/bin``。
运行二进制不再需要 Go、源码目录或 protoc。
Controller 可作为普通用户运行，只需监听端口、访问存储和 Agent 的权限。

4. 创建并保存部署凭据
------------------------------------------------------------

在部署机创建专用配置目录。下面只在文件不存在时生成三种令牌；
重启时继续使用原文件，不要每次启动都生成新令牌：

.. code-block:: bash
   :caption: Linux 部署机：以将要运行前台进程的用户执行

   install -d -m 0700 "$HOME/.config/sdn-controller"
   if [ ! -e "$HOME/.config/sdn-controller/controller.env" ]; then
     (
       umask 077
       printf 'SDN_ADMIN_TOKEN=%s\nSDN_VIEWER_TOKEN=%s\nSDN_AGENT_TOKEN=%s\n' \
         "$(openssl rand -hex 24)" \
         "$(openssl rand -hex 24)" \
         "$(openssl rand -hex 24)" \
         > "$HOME/.config/sdn-controller/controller.env"
     )
   fi
   set -a
   . "$HOME/.config/sdn-controller/controller.env"
   set +a

Controller 支持 ``-config /etc/sdn-controller/controller.conf`` 读取严格 JSON 配置，
可集中保存程序和内置 etcd 的参数；详见 :doc:`configuration-file`。
本页保留原有 CLI 示例，不传 -config 时行为不变。
``controller.env`` 是 shell / systemd 的环境文件；其中 token 会覆盖 controller.conf 的对应值。

``admin`` 用于管理，``viewer`` 用于只读，``agent`` 用于 Agent 注册和南向调用。
三者不能相同；admin / agent 必填且至少 16 字节，viewer 可选但启用时也要独立。
每个 Agent 的 ``SDN_TOKEN`` 必须与 Controller 的 ``SDN_AGENT_TOKEN`` 一致。
Web Gateway 使用相同 admin / viewer 凭据。含密钥的文件不应提交到源码仓库。

5. 选择持久存储并前台启动
------------------------------------------------------------

**文件模式：无需先安装 etcd，适合开发与首次验证。**

.. code-block:: bash
   :caption: Linux 部署机：已加载上一步环境变量

   install -d -m 0700 "$HOME/.local/state/sdn-controller"
   /usr/local/bin/sdn-controller \
     -listen :19090 \
     -state-file "$HOME/.local/state/sdn-controller/controller-state.pb" \
     -insecure

这是隔离实验的明文 gRPC 示例。``-insecure`` 不会关闭令牌认证。
看到 ``sdn-controller listening at ...:19090`` 表示服务开始监听。
此命令占据当前终端，后续验证在另一个终端执行。
保留整个状态目录；不要同时启动第二个进程使用同一个状态文件。

**etcd 模式：使用事先准备好的独立 etcd endpoint。**

若已用文件模式启动，先在原终端按 Ctrl+C 停止，再选择此模式。
下列占位符必须换成实际地址；不能将 ``<etcd服务器IP>`` 原样传入：

.. code-block:: bash
   :caption: Linux 部署机：已加载相同部署凭据

   /usr/local/bin/sdn-controller \
     -listen :19090 \
     -etcd 'http://<etcd服务器IP>:2379' \
     -etcd-prefix /sdn/site-a/controller \
     -insecure

etcd 应先完成服务健康检查，并由本 Controller 独占所选前缀。
项目的 :ref:`controller-docker-deploy` 在 Controller 容器内部启动带持久卷的 etcd；
本段适用于单独部署 Controller 并使用已有 etcd 的情况。
当前参数没有 etcd 用户名、密码或客户端证书选项，不能把这段示例当作完整的外部 etcd 安全配置。

文件与 etcd 是二选一存储，切换参数不会自动迁移原数据库。
etcd 也不会复制 Agent 的 Redis；两者分别保存全局和本地状态。

6. 验证 RPC，并接入 Agent
------------------------------------------------------------

在第二个终端加载同一凭据文件，再查询：

.. code-block:: bash
   :caption: Controller 所在部署机的另一个终端

   set -a
   . "$HOME/.config/sdn-controller/controller.env"
   set +a
   sdnc -addr 127.0.0.1:19090 -insecure nodes
   sdnc -addr 127.0.0.1:19090 -insecure configuration

空安装时应返回空节点清单及 revision 为 0 的配置。
``sdnc`` 使用 gRPC，不能用浏览器直接打开 19090 来检查 Controller。

在另一台 Agent 节点上，按 Agent 手册安装并启动 Agent，关键参数如下：

.. code-block:: bash
   :caption: Agent 节点：替换两处 IP 和部署令牌

   export SDN_TOKEN='<与Controller的SDN_AGENT_TOKEN相同的值>'
   sudo env "SDN_TOKEN=$SDN_TOKEN" /usr/local/bin/sdn-agent -listen :9090 \
     -controller '<部署服务器IP>:19090' \
     -endpoint '<Agent节点管理IP>:9090' \
     -insecure

``<部署服务器IP>`` 指本页 Controller 的管理地址；
``<Agent节点管理IP>`` 必须是 Controller 能够回连的地址。
两端需要双向管理网络连通：Agent → Controller 19090，Controller → Agent 9090。
再次查询节点应看到注册项与 RUNNING；此时还未显式纳管，也没有下发业务网络。
继续 :doc:`quickstart` 或 :doc:`cli` 完成纳管和事务。

7. 可选：安装为 systemd 服务
------------------------------------------------------------

下面适用于使用 systemd 的 Linux。它以独立普通用户运行 Controller；
Agent 的特权需求不适用于 Controller。先停止前台 Controller，避免端口或写入权冲突。

.. code-block:: bash
   :caption: Linux 部署机：创建服务用户并安装凭据

   id sdn-controller >/dev/null 2>&1 || \
     sudo useradd --system --user-group --home-dir /var/lib/sdn-controller \
       --shell /usr/sbin/nologin sdn-controller
   sudo install -d -m 0700 /etc/sdn-controller
   sudo install -d -o sdn-controller -g sdn-controller -m 0700 /var/lib/sdn-controller
   sudo install -o root -g root -m 0600 \
     "$HOME/.config/sdn-controller/controller.env" /etc/sdn-controller/controller.env

``/usr/sbin/nologin`` 应按发行版实际路径调整。
执行 ``sudoedit /etc/systemd/system/sdn-controller.service``，将以下内容保存到该文件：

.. code-block:: ini

   [Unit]
   Description=SDN Controller
   After=network-online.target
   Wants=network-online.target

   [Service]
   Type=simple
   User=sdn-controller
   Group=sdn-controller
   EnvironmentFile=/etc/sdn-controller/controller.env
   WorkingDirectory=/var/lib/sdn-controller
   ExecStart=/usr/local/bin/sdn-controller -listen :19090 -state-file /var/lib/sdn-controller/controller-state.pb -insecure
   Restart=no
   UMask=0077

   [Install]
   WantedBy=multi-user.target

这是新的文件模式实例。若需要继续先前的前台状态，应在旧进程停止后迁移其完整状态文件，
并确保服务用户可读写；不要把空目录当作原实例。
使用 etcd 时，将 ExecStart 的 ``-state-file ...`` 改为原实例的
``-etcd ... -etcd-prefix ...``，保留正确的存储位置和凭据。

.. code-block:: bash

   sudo systemctl daemon-reload
   sudo systemctl enable --now sdn-controller
   sudo systemctl status sdn-controller --no-pager
   sudo journalctl -u sdn-controller -n 50 --no-pager

初次启动失败先检查日志中的认证、端口、存储和写入权错误。
此模板不做自动多实例故障接管。先确认旧进程退出，再恢复原实例。

8. TLS 部署参数
------------------------------------------------------------

已有 CA 和覆盖 Controller DNS/IP 的服务端证书后，移除 ``-insecure``，
在启动命令或 systemd ExecStart 中使用：

.. code-block:: bash

   /usr/local/bin/sdn-controller \
     -listen :19090 \
     -state-file /var/lib/sdn-controller/controller-state.pb \
     -tls-cert /etc/sdn-controller/tls/server.pem \
     -tls-key /etc/sdn-controller/tls/server-key.pem \
     -agent-ca /etc/sdn-controller/tls/agent-ca.pem

此命令须由有权访问状态目录及证书私钥的运行用户启动；systemd 方式需给服务用户对应的读取权限。
``-agent-ca`` 用于验证各 Agent 的服务端证书，不是 Controller 入站证书。
Agent 也需配置自身 ``-cert/-key`` 和信任 Controller 的 ``-ca``。
客户端验证 Controller：

.. code-block:: bash

   sdnc -addr '<Controller证书覆盖的DNS或IP>:19090' \
     -ca /path/to/controller-ca.pem nodes

不要混用明文与 TLS 端口。当前实现是服务端证书校验加 Bearer 鉴权，没有客户端证书身份映射。

9. 停止、重启与升级
------------------------------------------------------------

前台进程按 Ctrl+C；systemd 服务使用：

.. code-block:: bash

   sudo systemctl stop sdn-controller
   sudo systemctl start sdn-controller
   sudo systemctl restart sdn-controller

停止 Controller 不删除数据库，也不主动清除 Agent 的网络；已被 Agent 受理的任务可能继续执行。
重新启动后查看原任务，不以新 request ID 重复提交不确定的变更。

升级前保留相同存储、身份关系与凭据，按 :doc:`recovery` 记录任务和备份边界。
可追溯构建、二进制 / 镜像交付和升级回退检查见 :doc:`release`。
先验证新二进制、停止旧服务，再重新执行安装命令并启动。
项目当前没有通用数据库自动迁移或主机网络自动重建保证。
