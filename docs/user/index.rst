用户指南
========================================

.. container:: doc-lead

   sdn-controller 保存全网期望配置，将租户、网络和路由协议配置编译为各节点的任务，
   再通过 sdn-agent 执行。这里按部署、配置、观察和恢复的实际操作组织文档。

.. container:: doc-cards

   .. container:: doc-card

      :ref:`controller-source-install`

      安装 Go 依赖，编译和安装二进制，配置凭据及存储，前台 / systemd 启动并验证。

   .. container:: doc-card

      :ref:`controller-docker-deploy`

      从三个项目源码构建镜像，启动完整 Web / Controller / Agent 环境，填写实际部署 IP。

   .. container:: doc-card

      :doc:`完成第一次 EVPN 实验 <quickstart>`

      纳管 Spine/Leaf，提交示例配置，再验证路由学习和主机通信。

   .. container:: doc-card

      :doc:`管理任务和拓扑 <jobs-topology>`

      区分任务受理、配置成功和业务可达，查看带来源的 EVPN 观测。

   .. container:: doc-card

      :doc:`处理失败与恢复 <recovery>`

      查询未知结果，恢复本地回滚，理解持久化能够保留哪些信息。

.. toctree::
   :maxdepth: 1

   source-install
   release
   docker-deploy
   configuration-file
   deployment
   quickstart
   configuration
   cli
   resources
   jobs-topology
   recovery
   troubleshooting

命令在哪里执行
----------------------------------------

除特别标注，命令在运行 Docker 的 Linux 宿主机、``~/program/github/sdn-controller``
目录执行。浏览器访问 Web；Controller 通过 gRPC 与 Agent 通信。
``sdnc`` 是 Controller 客户端，``sdnctl`` 是 Agent 客户端，两者接口和令牌用途不同。

本手册描述当前代码。根目录 ``agent.md`` 保留更完整的目标设计，
其中分对象存储、自动补偿等规划不能视作已经实现的功能。

