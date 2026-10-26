部署方式、地址与认证
========================================

本页是部署方式、地址和认证总览。按下面的独立教程完成完整的编译、安装与部署：

* :ref:`controller-source-install`：准备 Go → 编译 → 安装程序 → 保存凭据 → 选择存储 → 前台 / systemd 启动 → 验证。
* :ref:`controller-docker-deploy`：准备三个源码目录 → 初始化端口 → 编译镜像 → 启动整套环境 → Web / CLI 验证。

文中的 ``<部署服务器IP>`` 需要替换为自己的部署主机管理 IP，不要原样复制尖括号。
它不同于 loopback ``127.0.0.1``、监听范围 ``0.0.0.0`` 和 Docker DNS 服务名。

.. contents:: 本页内容
   :local:
   :depth: 2

部署边界
----------------------------------------

Controller 不需要接管业务接口。它需要能够访问 etcd，以及所有 Agent 注册的管理地址。
当前版本采用一个配置写入实例；Web、Gateway 和 Agent 可以作为独立进程部署。
Agent 的 Redis 与 Controller 的 etcd 各自保存所属状态，不建立主从复制。
默认镜像分别把 Redis 放在各 Agent 容器内、把 etcd 放在 Controller 容器内；
宿主机挂载的 agent.conf / controller.conf 管理程序与内部数据库参数。
Controller 文件配置见 :doc:`configuration-file`。

使用源码 Docker 镜像
----------------------------------------

准备 Linux rootful Docker、Docker Compose、Python 3，以及相邻的三个源码目录：

.. code-block:: text

   ~/program/github/
   ├── sdn-agent/
   ├── sdn-controller/
   └── sdn-web/

在宿主机执行：

.. code-block:: bash

   cd ~/program/github/sdn-controller
   python3 docker/manage.py init
   python3 docker/manage.py build
   python3 docker/manage.py up
   python3 docker/manage.py status

``init`` 创建凭据与缺失的外部启动配置，保留已有凭据和自定义配置。
``up`` 启动五个核心容器和两对 Spine/Leaf veth，生成使用当前节点 UUID 的 ``docker/state/evpn.json``。
Web 容器同时运行 Nginx 与 Gateway。可选 ``python3 docker/manage.py lab-up`` 再启动两台 PC，
添加两对 Leaf/PC 接线，总数为七个容器；完整主机流量实验需要这个模式。
首次使用空卷时，节点未纳管且没有业务配置；复用旧卷时保留原身份、数据库与 FRR 配置，
不会自动清空。脚本只管理 ``sdn-docker`` Compose 项目。

只构建 Controller 镜像时，构建上下文是三个项目的父目录：

.. code-block:: bash

   cd ~/program/github
   docker build -f sdn-controller/docker/Dockerfile -t sdn-controller:alpine .

Dockerfile 从源码编译 ``sdn-controller`` 和 ``sdnc``，运行层使用 Alpine。
依赖的公开 Agent 协议由相邻项目提供；凭据、数据库和已有二进制不进入镜像。

.. list-table:: 默认实验地址
   :header-rows: 1
   :widths: 28 38 34

   * - 服务
     - 地址
     - 访问位置
   * - Web
     - ``http://<部署服务器IP>:28080``
     - 客户端浏览器；填写 Docker 主机的可达管理 IP
   * - Controller gRPC
     - ``127.0.0.1:29090``
     - Docker 宿主机；仅绑定 loopback，外部不能直接访问部署 IP 的 29090
   * - Controller gRPC
     - ``controller:19090``
     - 本项目 Docker 管理网络
   * - Redis / etcd
     - 各自所属 Agent / Controller 容器的 127.0.0.1
     - 不作为独立 Compose 服务，不发布宿主机端口

该 Compose 使用显式明文实验模式。已有的 ``sdnc-it`` 实验使用另外的资源和端口，
不要将它的令牌、节点 UUID 或配置文件混入本环境。

从源码启动
----------------------------------------

以下是构建与前台启动摘要。二进制安装、持久凭据文件、systemd、TLS 和停止步骤见
:ref:`controller-source-install`，首次部署请先按该教程执行。

需要 Linux 和 Go 1.25+；保留相邻 ``sdn-agent`` 目录。
生成的 Protobuf Go 文件已提交，普通构建不需要 protoc：

.. code-block:: bash

   cd ~/program/github/sdn-controller
   go mod download
   make build

创建三种不同的随机凭据，并在后续重启时从受控配置中加载相同值：

.. code-block:: bash

   export SDN_ADMIN_TOKEN="$(openssl rand -hex 24)"
   export SDN_VIEWER_TOKEN="$(openssl rand -hex 24)"
   export SDN_AGENT_TOKEN="$(openssl rand -hex 24)"

选择一种存储模式启动。etcd 必须事先可达，前缀应由本 Controller 独占：

.. code-block:: bash

   ./bin/sdn-controller -listen :19090 \
     -etcd http://127.0.0.1:2379 -etcd-prefix /sdn/lab/controller -insecure

开发文件模式使用独立状态文件：

.. code-block:: bash

   ./bin/sdn-controller -listen :19090 \
     -state-file ./controller-state.pb -insecure

看到 ``sdn-controller listening`` 后，可以用 ``sdnc ... nodes`` 检查 RPC。
文件模式需要保留状态文件所在目录；etcd 模式需要保留数据库卷。
不要用两个 Controller 同时管理同一批 Agent。

凭据如何使用
----------------------------------------

.. list-table:: 令牌与权限
   :header-rows: 1
   :widths: 24 34 42

   * - 令牌
     - 使用者
     - 权限
   * - ``SDN_ADMIN_TOKEN``
     - 管理员 Web / CLI / SDK
     - North 查询、纳管、编辑、计划、提交和回滚恢复
   * - ``SDN_VIEWER_TOKEN``
     - 只读 Web / CLI / SDK
     - North 查询、任务订阅、拓扑刷新
   * - ``SDN_AGENT_TOKEN``
     - Agent 注册与心跳、Controller 出站 Agent RPC
     - Agent 接入链路，不可用于 North 配置

admin 和 agent 必填、互不相同且至少 16 字节。viewer 可不启用；启用时也必须满足
长度和独立性要求。Agent 的 ``SDN_TOKEN`` 必须等于 Controller 的 ``SDN_AGENT_TOKEN``。

Docker 环境的实际凭据保存在 ``docker/state/tokens.json``，字段为
``admin``、``viewer``、``agent``。网页登录填写 admin 或 viewer 的值。
访问令牌是 API 的认证凭据，不是 Linux 登录密码，也不是 node UUID。
本手册仅展示占位符。

TLS 与 Agent 连通性
----------------------------------------

Controller 入站 TLS 使用 ``-tls-cert``、``-tls-key``；出站访问 Agent 使用
``-agent-ca``。``sdnc`` 使用 ``-ca`` 信任 Controller 证书。
不使用 TLS 时，服务端和客户端均需显式指定 ``-insecure``。
当前这些选项实现服务器证书校验与 Bearer 鉴权，没有客户端证书身份映射。
etcd 启动参数目前没有独立的证书、账号和密码选项。

Agent 注册示例参数为 ``-controller controller:19090 -endpoint leaf1:9090``。
``endpoint`` 必须是 Controller 可以解析并访问的 Agent 服务地址，不能填成
仅 Agent 自己能访问的 ``127.0.0.1``。

下一步阅读 :doc:`quickstart`；启动报错见 :doc:`troubleshooting`。

