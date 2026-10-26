查看任务、节点和拓扑
========================================

节点 RUNNING 表示管理连接最近有心跳，不表示业务网络已经配置。
Job SUCCEEDED 表示相关 Agent 配置事务成功，转发和协议收敛仍需要业务验收。

节点身份与纳管
----------------------------------------

节点包含稳定 ``nodeId``、注册身份 ``registrationId`` 和本次进程 ``instanceId``。
Agent 首次请求的 node ID 冲突时 Controller 会分配未占用 ID；相同注册身份重连沿用原节点记录。
新实例登记进入 INIT，心跳确认后 RUNNING，超过默认 8 秒未见心跳时查询显示 DOWN。

``adopt`` 会主动读取 Agent 状态并核对身份和版本。
非零 revision 需要显式 ``-allow-existing``，表示接受已有 Agent 基线。
这不会导入或清理人工配置。已有受管基线不能通过重复纳管来抹平 revision 漂移。
即使 revision 为 0，也需要确认 Linux/FRR 是否存在人工配置。

Job 与 NodeJob
----------------------------------------

.. list-table:: 常见全局 Job 状态
   :header-rows: 1
   :widths: 23 77

   * - 状态
     - 含义
   * - ``ACCEPTED``
     - 目标配置和执行计划已持久化
   * - ``STAGING``
     - 为各节点建立候选并执行 Stage 校验
   * - ``APPLYING``
     - 节点 Commit 已发送或正在查询 Task
   * - ``UNKNOWN``
     - RPC 结果尚不确定，保留原请求持续查询/重试
   * - ``RECOVERING``
     - 正在重试原失败节点的持久化 Undo
   * - ``SUCCEEDED``
     - 本 Job 计划内的所有节点事务已成功
   * - ``FAILED``
     - 执行停止，未记录成功节点或未恢复回滚状态
   * - ``PARTIAL``
     - 部分节点成功，或有节点处于回滚失败状态，需要处理

节点级状态还包括 ``FAILED_NO_CHANGE``、``FAILED_ROLLED_BACK``、``ROLLBACK_FAILED``。
这些状态位于 Job 的 ``nodes[]``，不能只看全局错误字符串。
协议/CLI 也保留 ``ROLLED_BACK``、``COMPENSATION_FAILED`` 终态名称，
但当前正常编排没有跨节点自动补偿功能。

每个 Job 先 Stage 所有相关节点，再按确定顺序 Commit；Spine 优先，随后按节点 ID 排序。
一台失败后停止后续节点。已经成功的节点保持已应用结果，Controller 不自动将其回退。

不相关节点能否继续操作
----------------------------------------

活动 Job 占用计划内的所有节点。UNKNOWN 也保留这些执行权，避免在不确定基线上重复配置。
终态 Job 中的 ROLLBACK_FAILED 只隔离失败节点。

新 Change 是完整配置，必须保留被占用节点的目标操作不变；满足这一点时，可以修改无关节点。
Plan 会提示跳过由旧任务负责的节点。新 Job 成功不等于旧 Job 已完成。
调度采用一个 Worker 轮流推进任务，尚不是每节点独立执行线程；慢 RPC 会带来轮询延迟。

查询与事件
----------------------------------------

``GetJob`` 返回计划、节点进度和保留事件；``ListJobs`` 为控制响应大小省略大计划和事件。
``WatchJob`` 使用 ``afterSequence`` 重放事件，任务终态后关闭流。
每个任务保留最新 128 个事件。游标过旧或超前返回 ``OutOfRange`` 时，
客户端先 ``GetJob`` 获取快照，再从当前 sequence 重新订阅。

``sdnc watch`` 从初始游标订阅；需要自定义游标的程序应使用 North SDK。
CLI 超时或关闭终端不会停止已受理 Job。

拓扑数据来自哪里
----------------------------------------

.. list-table:: 拓扑来源
   :header-rows: 1
   :widths: 25 39 36

   * - 来源
     - 内容
     - 能说明什么
   * - inventory
     - 登记节点与名称
     - Controller 知道哪些 Agent
   * - desired
     - 租户、网络、placement
     - 用户希望如何部署
   * - FRR EVPN 观测
     - Type-2 / Type-3、VTEP、MAC/IP、RD/RT/NLRI
     - 此来源节点当前报告的 Overlay 路由关系

普通查询读缓存；``-refresh topology`` 才主动调用 Agent ``GetState``。
观测记录包含采集时间、``stale``、错误、Agent revision 和原始数据。
默认 30 秒以上或节点 DOWN 时标过期；采集失败保留旧数据并标明原因。
Controller 重启后观测缓存需要重新采集。

一次刷新总上限 20 秒、每节点 5 秒、最多四个并发采集；并发发起第二次刷新会被拒绝。
输出最多 2000 条 EVPN 路由，原始观测数据预算为 512 KiB，截断会给出 warnings。
需要缩小范围时用 North ``GetTopology`` 的 ``nodeIds`` 指定节点。

同一条远端主机路由可能被多个来源节点观测到，保留来源是正常行为。
无法唯一映射网络时保留路由上下文，不把任意 RT 硬解释为租户或 VNI。
目前没有 LLDP 物理链路采集、FPM/ZAPI 接收或周期采集服务。

