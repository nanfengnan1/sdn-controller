controller.conf 与容器内置 etcd
============================================================

Controller 镜像现在同时运行 Controller 和本容器独享的 etcd。
每个 Agent 镜像在自己的容器内运行 Redis 与 FRR；数据库不再作为默认 Compose 的独立服务。
数据库仍有各自的持久化卷，不能把“在同一个容器”理解成“数据只保存在容器可写层”。

.. contents:: 本页内容
   :local:
   :depth: 2

文件位置与加载顺序
------------------------------------------------------------

``controller.conf`` 使用严格 JSON 格式，虽然扩展名是 .conf，也不能填写 INI、YAML 或 JSON 注释。
未知字段、重复键、null、类型错误和多个 JSON 文档会被拒绝。
示例文件位于源码 ``docker/controller.conf``，镜像内默认路径是
``/etc/sdn-controller/controller.conf``。

* 原生程序：显式传 ``-config /path/to/controller.conf`` 才读取文件；不传时保留原有 CLI 行为。
* Docker：入口默认使用镜像内配置，命令行参数可以覆盖其中的 Controller 参数。
* 优先级：明确给出的 CLI 参数覆盖配置文件；SDN_ADMIN_TOKEN / SDN_VIEWER_TOKEN / SDN_AGENT_TOKEN
  覆盖文件内对应令牌；未设置的项使用程序默认值。
* 环境变量即使显式设为空，也会清空文件里的对应令牌。必填令牌因此为空时启动失败。
* 修改挂载文件不会自动热更新服务，校验后需重启 Controller 容器。配置中的字符串不执行 shell 或环境变量展开。

文件内配置自己的参数和 etcd
------------------------------------------------------------

下面是一个使用容器内 etcd 的完整示意。令牌字段的占位符必须替换为部署凭据，
或者删除这些令牌字段并使用已有 SDN_*_TOKEN 环境变量传入。

.. code-block:: json

   {
     "controller": {
       "listen": ":19090",
       "etcd": "http://127.0.0.1:2379",
       "etcdPrefix": "/sdn/site-a/controller",
       "stateFile": "",
       "insecure": true,
       "adminToken": "<替换为管理员令牌>",
       "viewerToken": "<替换为只读令牌>",
       "agentToken": "<替换为Agent令牌>"
     },
     "etcd": {
       "enabled": true,
       "name": "sdn-docker",
       "clientPort": 2379,
       "peerPort": 2380,
       "dataDir": "/etcd-data",
       "startupTimeoutSeconds": 30,
       "shutdownTimeoutSeconds": 20
     }
   }

``controller`` 参数与现有 CLI 对应：listen、etcd、etcdPrefix、stateFile、
tlsCert、tlsKey、agentCa、insecure，以及三种 token。
TLS 路径指容器内可读路径，证书与私钥也应由部署方挂载。
使用 TLS 时设置 tlsCert / tlsKey，关闭 insecure，并用 agentCa 信任 Agent。

``etcd.enabled`` 控制 Docker 入口是否启动内置 etcd。clientPort 与 peerPort 必须不同，
也不能与 Controller 监听端口冲突。内置 etcd 固定只监听本容器的 127.0.0.1，
不向 Docker 管理网开放数据库端口。没有额外开启跨节点数据库复制或 HA。

省略 controller.etcd 时，内置模式按 clientPort 自动生成连接地址；显式填写时必须匹配该端口。
内置 etcd 与 controller.stateFile 同时启用、或者把 controller.etcd 指到其他服务器，都会在启动服务前被拒绝。
dataDir 必须是独立绝对目录；超时范围为 1–300 秒。

从容器外挂载覆盖
------------------------------------------------------------

完整 Compose 会生成 ``sdn-controller/docker/state/config/controller/controller.conf``，
以只读方式挂载到 ``/etc/sdn-controller/controller.conf``。
编辑这份宿主机文件即可管理本次部署的 Controller 和内置 etcd 参数。
Agent 的配置位于各自的节点配置目录，不能把同一个 node 身份目录给多个 Agent 共用。

独立启动一个 Controller 容器时，可在 Controller 源码目录准备：

.. code-block:: bash
   :caption: Linux Docker 主机：首次准备独立实例

   install -d -m 0700 ./config
   install -m 0600 docker/controller.conf ./config/controller.conf

修改 controller.conf，按上面的 JSON 写入实际令牌，或者准备权限 0600 的 ``controller.env``：

.. code-block:: text

   SDN_ADMIN_TOKEN=<部署的管理员令牌>
   SDN_VIEWER_TOKEN=<部署的只读令牌>
   SDN_AGENT_TOKEN=<部署的Agent令牌>

使用环境文件时执行以下命令；若令牌全部由 controller.conf 提供，删除 --env-file 参数：

.. code-block:: bash

   docker run --rm --env-file ./controller.env \
     -v "$PWD/config/controller.conf:/etc/sdn-controller/controller.conf:ro" \
     sdn-controller:alpine -check-config

   docker run -d --name sdn-controller-demo --stop-timeout 60 \
     --env-file ./controller.env \
     -p 127.0.0.1:29090:19090 \
     -v "$PWD/config/controller.conf:/etc/sdn-controller/controller.conf:ro" \
     -v sdn-controller-demo-etcd:/etcd-data \
     sdn-controller:alpine

容器入口先验证所有配置，再启动 etcd，等待其可用，最后启动 Controller。
``-check-config`` 和 ``-runtime-plan`` 不会启动数据库或 Controller；前者只报告配置是否合法，
后者只输出无令牌的运行参数。原生程序也支持这些诊断选项。

.. code-block:: bash

   docker logs --tail 80 sdn-controller-demo
   docker inspect --format '{{.State.Health.Status}}' sdn-controller-demo
   docker exec sdn-controller-demo python3 /usr/local/lib/sdn-controller/supervisor.py --healthcheck

健康检查同时验证 etcd 和带认证的 Controller RPC，并读取实际启动参数，
因此改用命令行 -listen 时检查的也是该地址。
``sdnc`` 客户端不会读取服务端 controller.conf；使用 CLI 时仍要提供匹配的令牌环境变量。

权限、持久化与生命周期
------------------------------------------------------------

内置数据库镜像以容器 root 运行，以读取宿主机挂载的 0600 配置和兼容原 etcd 数据卷，
不要求 privileged 或宿主机网络权限，也不会递归修改旧数据目录的所有权。
源码方式启动 Controller 仍可以使用普通服务账号。

默认持久目录保持为 /etcd-data。已有数据卷应复用原目录内容和正确的 etcd 成员名称，
迁移前先停止原服务并备份；不能同时让旧独立 etcd 和新容器内 etcd 使用同一卷。
修改 name、端口、prefix 或 dataDir 不会自动迁移数据。配置文件与数据卷都应保存。

收到 SIGTERM 时先停止 Controller，让它关闭任务与释放写入租约，然后停止 etcd。
任一必需子进程意外退出，会终止整个容器，避免出现“容器活着但只剩数据库”的状态。
关闭两者最多各等待 shutdownTimeoutSeconds，部署层的停止宽限期应大于两者之和。

从旧卷启动时，原进程遗留的 writer 租约可能尚未到期。Controller 启动阶段会保持 etcd 运行，
只对明确的“已有 writer”结果做有界等待，并继续通过原来的 CAS 尝试获取写入权。
它不会删除旧锁或撤销别人的租约。默认 startupTimeoutSeconds=30 时，为配置读取和健康检查预留
5 秒，获取写入权最多等待 25 秒；等待可由 SIGTERM 取消。仍有存活 writer 持续保活时，
本次启动会超时失败，不能同时成为第二个 writer。运行中丢失写入权仍立即停止，不采用此启动重试。

只重启 Controller 容器不会重建 Agent 的网络命名空间；停止 Agent 容器的网络恢复限制
仍然适用，参见 :doc:`recovery`。不要用删除数据卷代替处理启动或恢复错误。

原生方式或外部存储
------------------------------------------------------------

原生 Go 程序读取自身配置，但不会独立拉起 etcd 进程；内置服务编排由 Docker 入口负责。
原生部署使用已有 etcd 时设 ``etcd.enabled: false``，配置 controller.etcd；
使用文件存储时同样关闭内置 etcd 并设置 controller.stateFile。
例如，下面文件与原有文件模式 CLI 等价，令牌仍从环境变量获取：

.. code-block:: json

   {
     "controller": {
       "listen": ":19090",
       "stateFile": "/var/lib/sdn-controller/controller-state.pb",
       "insecure": true
     },
     "etcd": {"enabled": false}
   }

.. code-block:: bash

   sdn-controller -config /etc/sdn-controller/controller.conf -check-config
   sdn-controller -config /etc/sdn-controller/controller.conf

配置文件可与已有 systemd EnvironmentFile 一起使用；将 ExecStart 改成上述 -config 启动方式。
共享凭据、TLS 和首次安装过程见 :doc:`source-install`。
