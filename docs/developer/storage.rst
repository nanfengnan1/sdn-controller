存储、调度与恢复设计
========================================

Repository 接口提供 ``Load``、``Save``、``Lost``、``Close``。
当前数据库是一个 schema_version=1 的 Protobuf Database，保存配置版本、节点、
候选、计划、任务、收据和 Applied。没有实现设计草案中的分对象 etcd key 空间。

持久化所有权
----------------------------------------

.. list-table:: 数据归属
   :header-rows: 1
   :widths: 26 45 29

   * - 位置
     - 内容
     - 恢复方式
   * - Controller Repository
     - Desired / Change / Plan / Job / Node / Applied / Receipt
     - 启动加载原快照
   * - Controller 内存
     - Agent 连接、原始观测缓存
     - 重建连接、显式重新采集
   * - Agent 存储
     - Candidate / Running / Operational / Task / Undo / 身份
     - 通过 Agent RPC 查询或恢复
   * - Linux / FRR
     - 实际接口、路由、协议配置与动态学习
     - 回读与独立业务验证；尚无完整漂移修复器

Controller 不直接写 Agent Redis；etcd 与 Redis 不构成数据库主从关系。

Database 内部结构
----------------------------------------

实际持久消息定义在 ``api/controller/v1/controller.proto`` 的 Database，
没有 SQL 表或可供外部直接写入的数据库 API：

.. list-table:: 快照字段
   :header-rows: 1
   :widths: 28 72

   * - 字段
     - 内容
   * - schema_version / revision
     - 存储模式版本 / 全局 Desired 配置版本；不同于 etcd ModRevision
   * - configuration
     - 当前完整 Desired：旧业务模型和逐节点 node_resources
   * - nodes
     - 按 node_id 保存身份、实例、endpoint、能力、心跳、revision 与纳管状态
   * - changes / plans
     - 候选配置、编辑版本与冻结状态 / 已生成的确定性节点计划
   * - jobs
     - 全局与节点任务、南向事务和任务 ID、持久意图、恢复字段及保留事件
   * - receipts
     - 按操作类别和 request_id 保存请求哈希与结果 ID
   * - applied
     - 按节点保存已确认成功的 Agent revision 和完整目标操作，供下一次差异计算

Desired 是最新受理目标，Applied 是逐节点确认成功的基线。PARTIAL / UNKNOWN 时两者
可能不同；不能把 Desired 已更新当作所有节点已经完成。节点心跳和任务事件会触发快照写入，
但不是每次写快照都增加全局 Desired revision。

查询、缓存与幂等收据
----------------------------------------

Controller 提供 gRPC，HTTP 请求由相邻 sdn-web Gateway 翻译；Controller 不缓存 HTTP 响应。
需要区分以下数据来源：

.. list-table:: 查询的数据生命周期
   :header-rows: 1
   :widths: 27 46 27

   * - 入口
     - 行为
     - 重启后
   * - GetConfiguration / GetJob / GetNodeResources
     - 读取成功持久化后发布的数据库内存视图；属于管理目标和任务记录
     - 从原 Repository 快照重新加载
   * - QueryNode
     - 每次检查节点和查询能力，再限时调用 Agent.Query；不在 Controller 缓存结果
     - 下次查询仍访问 Agent
   * - GetTopology(refresh=false)
     - 使用最近原始观测合并 Desired；返回采集时间、来源和 stale 标记
     - 内存观测为空，需要主动 refresh
   * - GetTopology(refresh=true)
     - 有界 GetState 采集后更新观测；失败保留旧数据并标记过期
     - 不持久化观测
   * - Begin / Commit 幂等重试
     - 校验原请求哈希，依收据找回同一 Change / Job，返回该对象当前状态
     - 收据持久保留，不是第一次响应字节的缓存

拓扑刷新上限为总计 20 秒、每节点 5 秒、最多并发 4 个；重叠刷新被拒绝。
因此 no-cache 的 HTTP 传输不意味着每次拓扑 GET 都采集实际设备，
主动 refresh 也不代表自动修复漂移。观测限制见 :doc:`/user/jobs-topology`。

etcd CAS 与丢响应
----------------------------------------

实际 key 仅为 ``<prefix>/snapshot`` 与 ``<prefix>/writer``。
writer key 绑定 15 秒租约，值为本进程随机 owner。Save 同时比较 snapshot ModRevision
和 writer owner，再写入确定性序列化的快照。

.. code-block:: text

   Save(old_revision, owner, bytes)
              |
        CAS(old_revision && owner)
          /                 \
       成功                  RPC 错误
        |                       |
     保存新revision      独立3秒窗口重试相同CAS
                                |
                  +-------------+-------------+
                  |                           |
                成功                        CAS失败
                  |                           |
             保存新revision      同一事务读取 snapshot + owner
                                              |
                         owner相同、revision更新、bytes完全相同?
                                   /                 \
                                  是                  否/无法核实
                                  |                     |
                           原写已成功，接受          Lost，停止服务

单次读到旧数据不能证明原请求没提交，因为请求可能仍在途。
恢复只重试相同受保护写入，不把旧内存状态自动 rebasing 到另一份新快照。
丢失租约或结果无法核实会关闭 Lost，主进程停止 RPC/Worker，后续启动重新加载持久状态。

File 模式
----------------------------------------

文件存储以 flock 排除同路径多写入者。Save 写临时文件、同步文件、rename 替换，
再同步父目录；成功后才对调用者返回。
rename 已完成而目录同步失败时结果不确定，关闭 Lost，避免继续按旧内存覆盖新磁盘状态。
普通文件重启测试不能等价于断电或磁盘缓存故障测试。

容量控制
----------------------------------------

快照硬上限 1 MiB。新变更入库前预留 256 KiB，还投影所有在途任务最终 Applied 的大小，
以避免已受理任务执行一半无法写入结果。
事件每 Job 保留最近 128 条；任务和收据没有自动清理。
心跳也写整个快照，因此该布局适用于实验和小规模验证。

扩容不能只提高 gRPC 消息限制，还需要考虑 Repository 分对象布局、事务发布边界、
历史归档、收据保留与恢复验证。保持“受理前拥有足够完成空间”的约束。

Worker 恢复不变量
----------------------------------------

* Begin/Commit 的 request ID 与意图先持久化，之后才调用 Agent。
* 已知 transaction ID 可重新 Stage；已知 task ID 查询 Task，未知 task ID 用原 Commit 找回。
* Commit 意图存在后，任何 RPC 错误都按 UNKNOWN 处理，直到得到明确 Task 终态。
* Agent 明确 SUCCEEDED 才更新该节点 Applied 和 revision。
* 失败后只 Abort 从未发送 Commit 的候选；已受理任务不能用 Abort 取消。
* 不把新 Agent instance 的旧观测 revision 当作当前基线，新实例先 INIT 再心跳确认。
* 同实例的迟到心跳不能降低已经观察到的 revision。

默认 STOP/PARTIAL 停止后续节点，保留已成功节点。
Controller 重启后继续未终态任务，但不会自动补偿已成功节点，更不会自动重新下发全部正向操作。

RetryRollback 的持久链路
----------------------------------------

North 请求验证原 Job 终态和 sequence、目标 NodeJob 的 ROLLBACK_FAILED 与 task ID。
原子写入回滚 request ID、目标 Agent sequence、pending 标记、RECOVERING 状态和收据。
Worker 调 Agent RetryRollback，携带原 task ID、基线 revision 与稳定恢复请求 ID。

旧记录缺少 Agent sequence 时，先 GetTask 核实当前失败状态和基线，再持久化 sequence。
回复丢失不会生成新恢复请求；两端重启后仍使用原 ID。
Agent 检查 Undo 和逆序依赖，遇到首个 Undo 失败即停止。

只有 Agent 确认 FAILED_ROLLED_BACK 才解除节点回滚屏障。
恢复写回原 Job，保留失败事件，最终仍为 FAILED/PARTIAL，不自动继续之前的正向工作。
基线变化、缺失 Undo、不安全旧日志和老 Agent 缺少 RPC 都不能通过强制解锁解决。

尚未提供的可靠性能力
----------------------------------------

没有跨节点原子事务、自动全局补偿、Agent 管理代次 fencing、多写 HA、
宿主机重建后的网络自动还原、完整 drift detection 或一键一致性备份。
设计这些能力时必须同时考虑实际数据面和两端任务日志，不能只添加 Controller 状态字段。

