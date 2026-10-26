.. _controller-docker-deploy:

从源码构建镜像并部署完整环境
============================================================

本页部署一套独立的 ``sdn-docker`` 环境：Web（同容器运行 Nginx 与 Gateway）、
Controller（内置 etcd）和三个 Agent（各自内置 Redis / FRR），默认共五个容器。
可选 ``lab-up`` 增加两台测试 PC，共七个容器。三个镜像直接由三个项目源码编译，
自有服务运行层采用 Alpine；不要求宿主机先安装 Go、Node.js 或 pnpm。

文中的 ``<部署服务器IP>`` 是运行 Docker 的 Linux 主机上、浏览器可以访问的管理 IP。
请替换为自己的地址；它不是某个固定实验服务器，也不是业务 VTEP 地址。

.. contents:: 本页步骤
   :local:
   :depth: 2

1. 准备主机、目录和依赖
------------------------------------------------------------

需要 Linux rootful Docker、支持 ``docker compose`` 的 Compose 插件及 Python 3。
Docker 需支持 BuildKit，Compose 需支持 ``up --wait``；构建脚本使用这些功能。
Agent 和接线辅助容器使用 privileged，需能创建 VRF、bridge、VXLAN、veth 等 Linux 网络对象。
Docker Desktop、rootless Docker 和非 Linux 宿主机不是本实验脚本的验证环境。

执行用户必须能访问 Docker daemon，并能拉取基础镜像、下载 Go / pnpm 构建依赖。
先进行只读检查：

.. code-block:: bash
   :caption: Linux Docker 部署机

   docker version
   docker compose version
   python3 --version
   docker info

按如下目录关系放置现有项目源码：

.. code-block:: text

   ~/program/github/
   ├── sdn-agent/docker/Dockerfile
   ├── sdn-controller/
   │   └── docker/
   │       ├── Dockerfile
   │       ├── compose.yaml
   │       └── manage.py
   └── sdn-web/docker/Dockerfile

完整部署必须有三个项目；Docker 构建上下文是它们的共同父目录。
不要只复制 sdn-controller/docker 目录到其他位置。
默认 Compose 项目名和容器名前缀固定为 ``sdn-docker``；
在同一 Docker daemon 上不要用两份源码同时管理这个名字的环境。

2. 初始化凭据和端口
------------------------------------------------------------

.. code-block:: bash
   :caption: Linux Docker 部署机：sdn-controller 仓库根目录

   cd ~/program/github/sdn-controller
   python3 docker/manage.py init

生成的文件为：

* ``docker/.env``：三种令牌和发布端口，权限为 0600。
* ``docker/state/tokens.json``：供操作者查看的 admin / viewer / agent 凭据。
* ``docker/state/``：本项目运行状态目录，不进入构建上下文。

Controller 配置在 ``docker/state/config/controller/controller.conf``，
以只读文件挂载到容器内；各 Agent 也挂载自己的 agent.conf。
配置格式、加载优先级和内置数据库生命周期见 :doc:`configuration-file`。

``init`` 保留已有 .env 中的凭据；不是令牌轮换命令。
需要修改端口时，在首次启动前编辑 .env 的以下项，保留其他密钥行：

.. code-block:: text

   SDN_WEB_BIND=0.0.0.0
   SDN_WEB_PORT=28080
   SDN_GRPC_PORT=29090

``0.0.0.0`` 是监听地址，不是浏览器要访问的 IP。
管理脚本在宿主机用 127.0.0.1 探测 Web，因此本流程保留 0.0.0.0；
设置为 127.0.0.1 会只允许本机访问。不要直接把 SDN_WEB_BIND 替换成部署服务器 IP，
否则管理脚本的 loopback 探测可能无法访问服务。
修改端口后，后续 URL 也要使用修改后的值。

3. 从源码编译全部镜像
------------------------------------------------------------

.. code-block:: bash

   python3 docker/manage.py build

这一步会进行 Go 编译与前端 pnpm 构建，不是复制宿主机上已有的 bin / dist。
构建完成后可只读检查：

.. code-block:: bash

   docker image inspect sdn-agent:alpine sdn-controller:alpine sdn-web:alpine \
     --format '{{.RepoTags}} {{.Id}}'

需要分别构建时，在共同父目录执行：

.. code-block:: bash

   cd ~/program/github
   docker build -f sdn-agent/docker/Dockerfile -t sdn-agent:alpine .
   docker build -f sdn-controller/docker/Dockerfile -t sdn-controller:alpine .
   docker build -f sdn-web/docker/Dockerfile --target web -t sdn-web:alpine .

Dockerfile 专用 ignore 文件排除了凭据、数据库、预编译二进制、node_modules 等。
基础镜像 tag 可通过各 Dockerfile 的 build args 调整；发布复现应记录实际镜像 digest。
``manage.py up`` 使用已构建镜像，不会替代这一步源码构建。

4. 启动并验证服务
------------------------------------------------------------

.. code-block:: bash

   cd ~/program/github/sdn-controller
   python3 docker/manage.py up
   python3 docker/manage.py status
   docker logs --tail 50 sdn-docker-controller
   docker exec sdn-docker-controller sdnc -insecure nodes

``up`` 启动五个核心服务、建立两对 Spine/Leaf 实验 veth、等待三个 Agent 注册，
然后生成 ``docker/state/evpn.json``。
需要完整主机流量实验时使用 ``python3 docker/manage.py lab-up``，新增两台 PC
和两对 Leaf/PC veth。``host10`` / ``host20`` 端口来自这个可选实验接线。
正常状态应有三个 RUNNING 节点；在全新环境里，它们尚未纳管、revision 为 0，
尚未配置租户、业务 IP、OSPF 或 BGP。

Controller 容器已通过环境加载 admin 令牌，上面的 sdnc 查询不需要再把密钥拼进命令行。
启动失败时查看对应服务日志，不用 reset 掩盖问题。

5. 用部署地址打开 Web
------------------------------------------------------------

浏览器打开 ``http://<部署服务器IP>:28080``，填入本环境
``docker/state/tokens.json`` 的 admin 值。viewer 只用于查看。

.. list-table:: 地址与访问位置
   :header-rows: 1
   :widths: 24 38 38

   * - 服务
     - 默认地址
     - 使用位置
   * - Web
     - ``http://<部署服务器IP>:28080``
     - 外部浏览器
   * - Web
     - ``http://127.0.0.1:28080``
     - Docker 宿主机本机
   * - Controller gRPC
     - ``127.0.0.1:29090``
     - Docker 宿主机 CLI；仅绑定 loopback
   * - Controller gRPC
     - ``controller:19090``
     - 同一 Compose 管理网络中的 Gateway / Agent
   * - Agent gRPC
     - ``spine:9090``、``leaf1:9090``、``leaf2:9090``
     - 管理网络内部；默认不发布宿主机端口
   * - Gateway
     - Web 容器内部 loopback
     - 与 Nginx 同容器的独立进程，没有单独 Gateway 服务容器
   * - Redis / etcd
     - 各自所属 Agent / Controller 容器的 loopback 地址
     - 没有独立数据库服务容器，不发布宿主机端口

``controller`` 和 ``leaf1`` 是 Docker DNS 服务名，不要求替换成部署 IP；
``127.0.0.1`` 只代表正在运行命令的机器。不要在另一台电脑上直接访问
``<部署服务器IP>:29090``，因为默认映射没有监听外部地址。
远程 CLI 可先使用 SSH 本地端口转发，再连接本机映射：

.. code-block:: bash
   :caption: 远程 CLI 所在电脑：保留此 SSH 会话

   ssh -N -L 29090:127.0.0.1:29090 '<用户名>@<部署服务器IP>'

新开终端，配置对应 admin / viewer 环境变量后，
使用 ``sdnc -addr 127.0.0.1:29090 -insecure nodes``。
Web 无法打开时还要检查宿主机防火墙是否允许所选 Web 端口。

6. 配置业务网络
------------------------------------------------------------

服务健康不代表 EVPN 已配置。
先运行 ``python3 docker/manage.py lab-up`` 启用测试 PC，再按 :doc:`quickstart`
在 Web 纳管节点、粘贴本轮 ``docker/state/evpn.json``、
预览并提交。PC 的地址由 ``python3 docker/manage.py hosts`` 显式配置，
不会在 up 时自动配置。协议收敛和双向 ping 需要单独验证。

这套 Compose 是隔离实验的明文配置；分离部署或启用 TLS 时，
参考 :doc:`source-install` 和相邻 sdn-web 的部署指南。
它不是已完成 HA、备份编排和主机自动恢复的生产集群模板。

7. 日常停止、再启动与清空
------------------------------------------------------------

只暂停管理面、保留 Agent 容器和业务命名空间时，在项目根目录执行：

.. code-block:: bash

   docker compose --project-name sdn-docker --env-file docker/.env \
     -f docker/compose.yaml stop web controller

恢复管理服务并核验现有接线：

.. code-block:: bash

   docker compose --project-name sdn-docker --env-file docker/.env \
     -f docker/compose.yaml start controller web
   python3 docker/manage.py status

这不会新建一份期望配置。原任务需要查询确认；管理断线不会主动清除 Agent 的网络。

如果确实要停止整套实验：

.. code-block:: bash

   docker compose --project-name sdn-docker --env-file docker/.env \
     -f docker/compose.yaml stop

卷会保留，但停止 / 重建 Agent 容器可能使 Linux 网络命名空间与业务对象消失。
当前 Agent 不会自动重建全部 Linux 配置；不能把再次 up 理解为无损恢复。
已有业务的恢复先阅读 :doc:`recovery`，并核对实际网络与任务基线。

只有明确要清空并重新开始实验时才使用：

.. code-block:: bash

   python3 docker/manage.py reset
   python3 docker/manage.py clean-check

``reset`` 验证资源归属后删除并重建本 Compose 项目的容器、数据卷和节点身份，
保留镜像、凭据和验收产物。它不会管理其他 Compose 项目。
清空后须重新使用生成的新 UUID 配置文件。
``make -C docs clean`` 则只删除文档 HTML，与实验清空无关。

持久卷分别保存 Controller etcd、各 Agent Redis、身份与 FRR 配置。
源码重新 build 不会自动迁移或备份这些数据。当前恢复边界和后续工作见
:doc:`recovery` 与 :doc:`/developer/storage`。
