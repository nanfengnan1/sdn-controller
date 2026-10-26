sdn-controller 文档
============================================================

.. container:: doc-lead

   管理 Leaf / Spine 的全网配置与任务协调。先完成编译部署，再纳管节点和下发租户网络。

.. container:: doc-cards

   .. container:: doc-card

      :ref:`源码编译与安装 <controller-source-install>`

      准备相邻协议源码、Go 工具链和 File / etcd 存储，编译安装 Controller 与 CLI，配置令牌并启动验证。

   .. container:: doc-card

      :ref:`Docker 源码镜像部署 <controller-docker-deploy>`

      从三个项目的源码构建镜像，启动 Controller、Agent、Web 和独立实验网络。

   .. container:: doc-card

      :doc:`用户指南 <user/index>`

      纳管节点、编辑完整配置、预览提交、查询 Job 和 EVPN 拓扑，以及处理失败恢复。

   .. container:: doc-card

      :doc:`开发指南 <developer/index>`

      North 协议、规划器、节点调度、存储恢复、扩展流程和测试。

从哪里开始
------------------------------------------------------------

单独部署时，先启动 Controller，再让 Agent 注册并确认管理地址可达。首次整网实验可按 :doc:`EVPN 入门 <user/quickstart>` 使用 CLI 或 Web 完成纳管、配置和连通性验证。

交付二进制、镜像与手册，以及升级前的兼容性检查，见 :doc:`user/release`。

教程中的 ``<部署服务器IP>`` 请替换为你部署服务的实际地址。
``127.0.0.1`` 表示命令所在机器本身，``controller:19090`` 等容器 DNS
只在对应 Docker 网络内使用，不能直接填入另一台电脑的浏览器。

生成或清理本手册
------------------------------------------------------------

下面命令构建的是 HTML 文档。软件编译、安装、配置和启动请使用上面的部署入口。

首次构建文档先按仓库 ``docs/README.md`` 安装 ``docs/requirements.txt``，
然后在项目目录执行：

.. code-block:: bash

   make -C docs help
   make -C docs html
   # 需要清理生成的文档时执行：
   make -C docs clean

生成入口为 ``docs/html/index.html``，两类手册分别在
``docs/html/user/index.html`` 和 ``docs/html/developer/index.html``。
``clean`` 仅删除文档产物 ``docs/html/`` 与 ``docs/_build/``。
原有 Markdown 设计记录和验收报告继续保留；其中历史环境地址不作为部署参数。

已有独立资源功能的实际验收、修复过程和五核心部署结果见
:download:`资源端到端验收记录 <resource-validation.md>`；
Controller 源码检查另见 :download:`资源源码验证 <controller-resource-validation.md>`。

.. toctree::
   :hidden:
   :maxdepth: 2
   :caption: 用户指南

   user/index

.. toctree::
   :hidden:
   :maxdepth: 2
   :caption: 开发指南

   developer/index
