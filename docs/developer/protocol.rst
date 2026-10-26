North 协议与客户端契约
========================================

协议源文件为 ``api/controller/v1/controller.proto``，服务全名
``sdn.controller.v1.North``。Go 模块为 ``sdn-controller``。
Agent 注册/心跳复用 ``sdn-agent/api/agent/v1`` 的 Controller 服务，
其余南向事务调用 Agent 服务。

RPC 分组
----------------------------------------

.. list-table:: North 接口
   :header-rows: 1
   :widths: 32 38 30

   * - RPC
     - 输入/输出重点
     - 权限
   * - ListNodes / GetNode
     - 分页、身份、能力、状态与 revision
     - admin / viewer
   * - AdoptNode
     - node_id、expected_revision、allow_existing
     - admin
   * - GetConfiguration
     - 完整 Configuration 与 revision
     - admin / viewer
   * - BeginChange
     - base_revision、request_id；返回候选
     - admin
   * - EditChange
     - change_id、完整 configuration、edit_revision
     - admin
   * - GetChange
     - frozen、aborted、job_id 与候选内容
     - admin / viewer
   * - PlanChange
     - change_id；返回 plan_id、节点 op 与 warnings
     - admin
   * - CommitChange
     - change_id、request_id、plan_id；返回 Job
     - admin
   * - AbortChange
     - 只取消未提交候选
     - admin
   * - ListJobs / GetJob
     - 列表摘要 / 完整任务
     - admin / viewer
   * - WatchJob
     - job_id、after_sequence；服务端事件流
     - admin / viewer
   * - RetryRollback
     - job_id、node_id、request_id、expected_job_sequence
     - admin
   * - GetTopology
     - refresh、node_ids；配置和观测图
     - admin / viewer
   * - GetNodeResources / QueryNode
     - 物化目标 / Agent 结构化实际状态查询
     - admin / viewer
   * - EditNodeResources / ImportNodeResources
     - 候选资源增量编辑 / 只读核验后显式建立基线
     - admin

Agent 凭据只允许 ``sdn.agent.v1.Controller/Register`` 和 ``Heartbeat``，
不能操作 North。认证由 ``pkg/transport`` 的 unary/stream 拦截器执行；
请求中的自称角色不参与授权。

版本与幂等
----------------------------------------

``BeginChange`` 绑定全局 ``base_revision``；``EditChange`` 以 ``edit_revision``
防止覆盖同一候选的并发编辑。``PlanChange`` 根据目标、能力、Applied 和执行权生成确定性计划。
``CommitChange`` 重建计划并校验 ID，原子保存 Desired、Job、冻结候选和幂等收据。

收据存储请求的确定性 Protobuf SHA-256 与结果 ID。
同键同内容返回原结果；同键不同内容为 ``AlreadyExists``。
Begin 和 Commit 的 request_id 要求非空且不超过 128 字节；客户端对所有幂等键应采用相同上限。
当前收据按操作类别和 request_id 组织，没有按独立用户隔离的命名空间。

``EditChange`` 没有独立 request_id。响应不确定时先 GetChange 判断编辑结果，
不要盲目用旧 edit_revision 重写。Commit 响应不确定时用原始 Commit 请求重试。

事件契约
----------------------------------------

JobEvent 的 ``sequence`` 单调递增、先持久化后供查询。
客户端按 ``job_id + sequence`` 去重；断线后用上次处理完成的 sequence 续订。
最多保留 128 条事件，超出保留范围或超前游标返回 ``OutOfRange``；
客户端重新 GetJob 建立快照并从当前 sequence 订阅。

事件流结束仅说明服务器完成当前订阅，需要结合最终 Job 状态判断成功或失败。
Controller 重启后事件仍可重放，观测缓存则不同，不保证保存。

错误分类
----------------------------------------

.. list-table:: gRPC 状态
   :header-rows: 1
   :widths: 27 38 35

   * - Code
     - 常见原因
     - 客户端行为
   * - InvalidArgument
     - 字段、地址、配置结构或分页错误
     - 修正请求
   * - Unauthenticated / PermissionDenied
     - 凭据缺失、错误或角色不允许
     - 修复认证；不要换请求 ID 重试已提交任务
   * - NotFound
     - 节点、候选或任务不存在
     - 核对当前环境与身份
   * - AlreadyExists
     - 幂等键被用于不同消息
     - 找回原消息或为新业务生成新键
   * - Aborted
     - 目标、候选、Agent 基线或计划冲突
     - 重新读取、合并和预览
   * - FailedPrecondition
     - 未纳管、回滚屏障、不支持的修改等
     - 处理前置条件；不能强制覆盖
   * - ResourceExhausted
     - 快照/数量上限或已有拓扑刷新
     - 按原因限流或安排迁移
   * - OutOfRange
     - 事件游标超出范围
     - GetJob 后续订
   * - Unavailable / DeadlineExceeded
     - 连接、存储异常或超时
     - 保留原请求和任务 ID，按不确定结果恢复

南向 Commit 意图保存后，认证失败等错误也不能证明之前的 Commit 未受理。
Worker 仅依据明确 Task 终态推进成功或失败。

编码与兼容性
----------------------------------------

SDK 使用生成的 Protobuf 类型。CLI 和 Web Gateway 使用 protojson：
字段名为 lowerCamelCase，uint64 用字符串，避免 JavaScript Number 精度丢失。
新增字段保留原 field number；移除字段时保留编号，不能复用旧号。
改变 op 的前置条件、回滚语义或默认值时，应同步能力声明和兼容性用例。

在项目根目录重新生成：

普通构建直接使用仓库内的生成文件，只有修改 proto 时才需要安装生成工具。
先通过发行版或 protobuf 的发行包准备 ``protoc``，并核对现有生成文件头部记录的版本。
当前文件头记录 protoc-gen-go 1.36.12、protoc-gen-go-grpc 1.6.2；可在开发机安装对应插件：

.. code-block:: bash

   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
   export PATH="$(go env GOPATH)/bin:$PATH"
   protoc --version
   protoc-gen-go --version
   protoc-gen-go-grpc --version

然后运行：

.. code-block:: bash

   protoc -I. -I../sdn-agent \
     --go_out=. --go_opt=paths=source_relative \
     --go-grpc_out=. --go-grpc_opt=paths=source_relative \
     api/controller/v1/controller.proto

需要 protoc、protoc-gen-go 和 protoc-gen-go-grpc 位于 PATH。
生成代码与 proto 一起提交。当前通过 go.mod 的本地 replace 共享 Agent 公开协议，
正式发布独立协议模块尚未完成，不能把 internal 包作为跨项目接口。

