分层、组件与请求时序
========================================

Controller 是单进程模块化服务。分层用于控制依赖，不代表当前已拆成多个微服务。
Web Gateway 作为独立项目的进程，仅使用公开 North 协议。

分层架构
----------------------------------------

.. code-block:: text

                Web Gateway / sdnc / 自动化客户端
                               |
                 +-------------v-------------+
                 | North gRPC + Auth         |
                 | Query / Change / Adopt    |
                 +-------------+-------------+
                               |
          +--------------------v--------------------+
          | Service                                 |
          | 节点登记、候选、Desired、Job、观测拓扑   |
          +----------+-------------------+----------+
                     |                   |
          +----------v----------+  +-----v----------------+
          | Planner             |  | Collector / Topology |
          | 校验、目标op、差异  |  | 按需GetState、解析图 |
          +----------+----------+  +----------+-----------+
                     |                        |
          +----------v----------------+       |
          | Orchestration / Recovery  |       |
          | Stage、Commit、任务轮询   |       |
          +----------+----------------+       |
                     |                        |
          +----------v------------------------v-----------+
          | South：公开 Agent gRPC 客户端与连接缓存       |
          | 同时接收 Agent Register / Heartbeat           |
          +-----------------------+-----------------------+
                                  |
                       sdn-agent -> Linux / FRR

          所有持久状态变更 -> Repository -> etcd / File
          观测结果与连接缓存 -> 当前 Controller 进程内存

代码位置
----------------------------------------

.. list-table:: 组件与源码
   :header-rows: 1
   :widths: 34 66

   * - 路径
     - 职责
   * - ``cmd/sdn-controller``
     - 参数、存储、认证和服务生命周期；Lost 信号停止 RPC/Worker
   * - ``internal/config``
     - 严格 JSON、默认值与 CLI / token 环境优先级、内置 etcd 参数校验
   * - ``docker/supervisor.py``
     - 消费 Go 输出的无令牌运行计划；编排容器内 etcd 与 Controller 生命周期
   * - ``cmd/sdnc``
     - 便捷 CLI、动态 North unary RPC、任务订阅
   * - ``api/controller/v1``
     - 公共协议和持久化 Database 消息
   * - ``pkg/transport``
     - Bearer 角色授权、TLS、gRPC 客户端/服务端
   * - ``internal/controller/controller.go``
     - 注册、心跳、纳管、候选、计划、提交、查询和事件
   * - ``internal/controller/worker.go``
     - Job 轮询、持久意图、南向调用、Applied 更新、失败停止
   * - ``internal/controller/recovery.go``
     - RetryRollback 受理与恢复推进
   * - ``internal/controller/resources.go``
     - 节点资源候选编辑、能力校验后的查询转发和回读验证导入
   * - ``internal/controller/topology.go``
     - 有界采集、FRR Type-2/Type-3 解析、配置和观测图
   * - ``internal/planner``
     - 完整目标编译、差异、拆除顺序、节点执行权校验
   * - ``internal/resources``
     - 节点对象自然键、规范化、合并、引用检查与历史日志折叠
   * - ``internal/repository``
     - 单快照 CAS 或文件持久化，以及写入权丢失通知

这些模块目前共享 Controller 的数据库快照；没有独立 Redis 缓存服务、Service DB、
FPM/ZAPI Server、自动资源池分配或 NETCONF 设备直连实现。
新增能力的逐步流程和需要同步的模块见 :doc:`extensions`。

默认 Controller 容器内包含单成员 etcd，默认 Agent 容器内包含各自 Redis / FRR。
Controller Go 配置解析器是配置语义的单一实现；Python 入口通过 -runtime-plan 获取
无令牌的服务参数，不重复解析 controller.conf，也不把配置拼成 shell 命令。
-check-config 可在启动任何子服务前验证配置；-healthcheck 使用认证 North RPC。
配置优先级和外部挂载见 :doc:`/user/configuration-file`。

内部数据流
----------------------------------------

.. code-block:: text

   North Handler --校验--> Planner.Compile / Build
         |
         +-- update：锁 -> Clone(db) -> 修改副本 -> 容量检查
         |                                          |
         |                                    Store.Save
         |                                          |
         |                               成功后发布内存 db
         |
         +-- Commit 后 notify -> Worker
                                  |
                          保存 Begin/Commit 意图
                                  |
                              Agent RPC
                                  |
                          保存 Task / Applied / Event

   GetTopology(refresh) -> 有界 GetState -> 观测缓存 -> ParseEVPN
                                                    |
                                           合并 Desired 关系返回

   QueryNode -> 节点状态/查询能力 -> 有界 Agent.Query -> 校验响应身份 -> 返回

``update`` 在互斥锁内克隆数据库，通过回调修改新副本，持久化成功后才替换内存指针。
失败时不把未持久化的目标暴露为成功。数据库 snapshot 读取返回克隆，外部使用者不能直接修改共享状态。

Controller 没有 HTTP 响应缓存层。QueryNode 逐次访问 Agent，GetTopology 可复用观测缓存，
Begin / Commit 的持久收据则用于幂等恢复；三者的数据生命周期见 :doc:`storage`。

一次配置请求
----------------------------------------

.. code-block:: text

   Client          North/Planner        Repository       Worker          Agent
     | GetConfiguration  |                   |               |              |
     |<-- revision/目标 -|                   |               |              |
     | BeginChange ----->|-- 保存候选 ------>|               |              |
     |<-- change --------|                   |               |              |
     | EditChange ------>|-- 保存完整目标 -->|               |              |
     | PlanChange ------>|-- Build/保存plan >|               |              |
     |<-- plan_id/op ----|                   |               |              |
     | CommitChange ---->|-- 重校验版本/plan |               |              |
     |                   |-- 保存目标/Job/收据>|               |              |
     |                   |<-- 持久化成功 ----|               |              |
     |                   |-- notify ----------------------->|              |
     |<-- ACCEPTED/Job --|                   |               |              |
     |                   |                   |<-- Begin意图 --|              |
     |                   |                   |               |-- Begin ---->|
     |                   |                   |               |-- Stage ---->|
     |                   |                   |               | 所有节点Stage |
     |                   |                   |<-- Commit意图 -|              |
     |                   |                   |               |-- Commit --->|
     |                   |                   |               |<-- task_id --|
     |                   |                   |               |-- GetTask -->|
     |                   |                   |               |<-- 终态 -----|
     |                   |                   |<-- Applied/Event              |
     | WatchJob/GetJob ->|-- 读取持久结果 -->|               |              |
     |<-- 全局/节点状态 -|                   |               |              |

Controller 实际使用 ``GetTask`` 轮询 Agent，没有消费 Agent ``WatchTasks``。
北向 ``WatchJob`` 重放 Controller 自己保存的事件。
Stage 全节点成功不构成分布式两阶段提交；节点提交和业务路由收敛不是全网原子行为。

并发与生命周期
----------------------------------------

一个 Worker 每 500ms 或收到 kick 信号后按创建时间推进非终态 Job。
不相关节点的 Job 可以同时存在，依次获得推进机会；同节点由执行权约束串行化。
一次 ``advance`` 可以完成多个 Stage，但遇到仍在执行的 Task 后会返回。
每个南向 RPC 默认十秒超时；一个慢调用可能拖延同轮其他 Job。

SIGTERM 或 Store.Lost 停止 RPC 和 Worker，连接在退出时关闭。
这不提供 Agent 端控制权代次隔离，HA 接管需要额外协议设计。

