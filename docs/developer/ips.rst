IPS 资源规划与事务
===============================

职责边界
--------

Web / North API 提交 typed operations，Controller 进行资源归一化、
候选版本检查、能力校验和规划；Agent 执行 Prepare / Apply / Verify / Undo。
本功能不新增 Controller 存储服务，也不在 Controller 执行 Snort 或 Lua。

协议由相邻 sdn-agent 模块提供：OpCode 11 为 IPS_RULE，12 为 IPS_INTERFACE；
QueryKind 21/22 为 IPS_STATUS/IPS_RULES，既有编号保持不变。
构建 Controller/Gateway 时必须使用包含这些字段的 Agent 协议源码。

资源模型
--------

``internal/resources/resources.go`` 将 SID 和 VPP 原生接口名作为自然键。
创建排序先规则、后绑定，删除时逆序；已有对象差异编译为 SET，
避免为修改规则或方向先删除对象。规则 SID 非零，文本必须为单行；
最终语法及 SID 一致性校验交给 Agent 的 Snort 后端。

``internal/planner/planner.go`` 生成节点操作；沿用原有能力与回滚检查、
纳管状态、资源归属、版本屏障和幂等提交。只有 Agent 声明对应操作及回滚能力
才允许计划下发，不能只根据网页上出现了按钮判断设备支持。

失败与恢复
----------

Agent 将整个旧规则/绑定快照写入 Undo，执行 Redis → confd → Snort 重载确认。
Controller 以任务和实际回读结果为准。基础配置由宿主机管理员独立管理，
不放入 Controller Desired，也不因规则事务回滚而撤销管理员的基础配置修改。

运行查询透传 Agent 结构化记录；基础文件待加载、基础配置错误与规则执行错误
是不同字段。不得在 Controller 根据文件存在或信号发送成功伪造 RUNNING。

测试与构建
----------

.. code-block:: console

   go test ./internal/resources ./internal/planner
   docker build --build-arg SOURCE_REVISION="$(git rev-parse HEAD)" \
     -f docker/Dockerfile -t alpine/sdn-controller:local ..

``internal/resources/ips_test.go`` 覆盖资源生命周期与绑定顺序。
整栈验收由 Agent 仓库 ``lab/snort_ips.py`` 驱动，经过真实 Gateway/Controller；
浏览器操作由 Web 仓库 ``tests/e2e/snort-ips.cjs`` 验证。
底层接口、校验和恢复协议见 Agent 的 ``docs/developer/ips.rst``。
