开发、测试与验证证据
========================================

开发环境
----------------------------------------

需要 Linux、Go 1.25+ 和相邻 ``sdn-agent`` 源码。
首次准备工具链、编译和安装二进制，按 :ref:`controller-source-install` 执行；
需要完整 Docker 部署时使用 :ref:`controller-docker-deploy`。
race 测试需要 Go race 支持的环境和可用 C 工具链；普通单元测试不需要 Docker。
根目录 Makefile 提供 build、test、vet：

.. code-block:: bash

   cd ~/program/github/sdn-controller
   go mod download
   make build
   make test
   make vet

``make test`` 对应 ``go test -race ./...``。
修改公共协议后还应编译相邻 sdn-agent 和 sdn-web/gateway，防止消费者与生成代码不一致。

配置与容器编排的检查还包括：

.. code-block:: bash

   python3 -m unittest discover -s docker -p '*test*.py' -v

这些 Python 测试验证配置初始化、保留自定义配置、旧数据库容器迁移保护、
镜像构建目标、停止预算和监督逻辑，不连接 Docker 或启动实际数据库。

源码测试分工
----------------------------------------

.. list-table:: 测试位置
   :header-rows: 1
   :widths: 35 65

   * - 路径
     - 重点
   * - ``internal/planner/planner_test.go``
     - 完整目标、更新/删除、引用与作用域冲突、拆除依赖
   * - ``internal/resources/resources_test.go`` / ``internal/planner/resources_test.go``
     - 自然键、物化和历史折叠、资源关系、确定性与逐节点差异
   * - ``internal/controller/controller_test.go``
     - 真实 gRPC 模拟 Agent、候选版本、幂等、任务、未知结果、授权和拓扑
   * - ``internal/controller/recovery_test.go``
     - 节点隔离、无关任务推进、持久 RetryRollback 与重启
   * - ``internal/controller/resources_test.go`` / ``cmd/sdnc/resources_test.go``
     - 资源编辑和导入、能力与查询限制、权限和命名 CLI 参数编码
   * - ``internal/repository/*_test.go``
     - File/etcd、写入互斥、CAS、丢响应、取消与失败关闭

需要真实 etcd 的用例使用开发专用 endpoint：

.. code-block:: bash

   SDNC_TEST_ETCD=http://127.0.0.1:12379 \
     go test -race -count=1 -v ./internal/repository

没有设置此环境变量时，条件用例可能跳过。报告应明确区分通过和跳过，
不能把模拟数据库验证写成真实 etcd 验证。

可靠性组合回归
----------------------------------------

.. code-block:: bash

   python3 lab/reliability.py --go /path/to/go \
     --etcd http://127.0.0.1:12379

脚本在随机独立前缀测试 etcd，在临时 network=none FRR 容器验证真实配置和 Undo。
它还执行 Agent、Controller、Gateway 的 Go 检查，结果在 ``artifacts/reliability/``。
独立 FRR 测试用包装 Driver 注入失败，但实际 vtysh、frr-reload.py 与回读是真实执行。
该脚本不是部署或升级当前运行环境的入口。

源码镜像与真实 EVPN 验收
----------------------------------------

只在专门用于验收的空白 ``sdn-docker`` 环境执行；测试会配置真实网络：

.. code-block:: bash

   python3 docker/manage.py init
   python3 docker/manage.py build
   python3 docker/manage.py lab-up
   python3 docker/test.py

``docker/test.py`` 要求 revision=0、空任务，然后验证 Web/API、OSPF/BGP、
EVPN、双向 ping、资源增删、失败回滚与管理进程重启。
PC、veth、容器等环境资源由夹具负责，Controller 负责其中的节点网络配置。
默认部署为五个核心容器，etcd 在 Controller 内部，Redis / FRR 在各 Agent 内部，
Nginx 和 Gateway 在同一个 Web 容器内。完整流量测试启用可选的两台 PC，总计七个容器。
``python3 docker/manage.py bundled-check`` 只查询服务与挂载并保存检查结果，
用于确认内置数据库、只读外部配置和持久卷；它不提交网络配置。

浏览器用例在相邻项目 ``sdn-web/tests/e2e/browser.cjs``，使用
``SDN_WEB_URL``、``SDN_TEST_TOKENS`` 和 ``SDN_TEST_ARTIFACTS`` 指定环境。
测试结束需要恢复空白状态时才运行 ``docker/manage.py reset``，它会销毁本项目数据卷和业务状态。
如果用户正在手动测试，不应为文档或源码静态检查运行这些步骤。

已有证据
----------------------------------------

已有逐节点资源模型、验证导入、命名 CLI 与权限测试记录：
:download:`Controller 资源扩展验证 <../controller-resource-validation.md>`。

.. list-table:: 记录入口
   :header-rows: 1
   :widths: 37 63

   * - 路径
     - 内容
   * - ``docs/docker-validation.md``
     - 先前独立数据库拓扑的源码 Alpine 镜像历史验收，不能代表后续内置数据库版本
   * - ``docs/bundled-validation.md``
     - 上一轮八容器内置数据库版本的历史实测记录
   * - ``docs/controller-resource-validation.md``
     - 资源功能开发阶段的 Controller 模型、协议、CLI 源码验证，独立于真实网络验收
   * - ``docs/resource-validation.md``
     - 资源功能开发阶段的 57 条端到端检查、修复过程及五核心部署后的只读验证
   * - ``artifacts/docker/``
     - 机器结果、截图、ping、环境、镜像与 clean-state
   * - ``docs/reliability-validation.md``
     - 持久化和 RetryRollback 修复验收范围与未覆盖项
   * - ``artifacts/reliability/``
     - 对应原始日志和汇总
   * - ``docs/validation.md``
     - 先前独立实验的历史验收；环境和版本以报告为准
   * - ``docs/backlog.md``
     - 尚待解决的恢复、容量、HA 和观测问题

这些报告记录各自测试时间和版本。修改文档不表示重新运行全部业务验收，
部署新代码后应生成新的证据，不覆盖失败历史。
资源功能记录：:download:`Controller 资源源码验证 <../controller-resource-validation.md>`、
:download:`独立资源端到端验收 <../resource-validation.md>`。
历史记录：:download:`内置数据库验证 <../bundled-validation.md>`。

增加用例与提交检查
----------------------------------------

资源行为变化应测试成功路径和有意义的失败边界，例如错误引用、删除顺序、
Commit 回复丢失、Undo 失败后重启，而不是只重复实现细节。
持久化测试检查下一次写入仍可正确执行或服务按预期停止，不能只断言一次调用返回值。

提交前运行相关包测试与 vet；协议变化重新生成代码并验证消费者。
文档变化使用 :doc:`build-docs` 的严格 HTML 构建检查引用和格式。
新增测试容器使用独立名称与标签，清理前校验归属，禁止使用全局 Docker prune。

VPP 双栈查询联调
----------------------------------------

资源校验和 Planner 的 dualstack_test.go 覆盖 IPv6 VTEP、IPv6 BGP 邻居、
router-id 约束以及 configureSystem 不再关闭 IPv6。Agent 与 Web 位于同级目录时，
从共同父目录构建 docker/Dockerfile 为 alpine/sdn-controller:vpp-dev，
同时准备 alpine/sdn-web:vpp-dev 与 alpine/sdn-agent:vpp-dev。

在 Agent 仓库运行：

.. code-block:: bash

   SDN_VPP_STACK=1 SDN_VPP_UNDERLAY=6 SDN_VPP_WORKERS=2 python3 lab/vpp_evpn.py

脚本启动独立 Controller/Web，三个 VPP 节点真实注册和接管，使用 viewer 令牌
通过 HTTP 查询九类 VPP 状态，并验证 VXLAN/路由地址族筛选；报告位于 Agent 的
artifacts/vpp/dual-ipv6/stack-query.json。测试不会改动已有部署或物理 NIC。
设置 SDN_VPP_BROWSER=1 时等待 Web 仓库的 tests/e2e/vpp-query.cjs 完成浏览器验收。
数据面正确性另由 Agent 的 L2/IRB/GSO 实验验证，不能由查询成功推导。

