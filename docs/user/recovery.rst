持久化与失败恢复
========================================

Controller 保存目标、候选、计划、节点、任务、Applied 和幂等收据；
Agent 保存本地事务、Undo、Running、Operational 与身份。
保存这些数据可以恢复管理任务，但不会自动重建已丢失的 Linux 网络命名空间。

正常重启
----------------------------------------

保持原 Controller etcd 前缀或状态文件、各 Agent 状态目录与 Redis 数据，
并确认旧写入进程已退出。启动后 Controller 加载原任务，使用原事务 ID 和 request ID
查询或重试 Agent RPC。不要生成一套新身份、空数据库，再假定它仍拥有原来的网络配置。

Docker 中每个 Agent 有独立 Redis AOF、身份和 FRR 配置卷；Controller 使用 etcd 卷。
Redis 进程位于对应 Agent 容器内部，etcd 进程位于 Controller 容器内部，
相应配置文件由宿主机只读挂载。进程位置变化不替代原有数据库和身份备份。
实验 Redis 使用 ``appendonly yes``、``appendfsync always``。
容器重建、主机重启与单纯管理进程重启的影响不同：前两者可能丢失 veth、VRF、Bridge、VXLAN，
当前没有完整自动重建功能。FRR 配置卷也不能代替 Linux 接口恢复。

提交结果未知
----------------------------------------

当 Controller 已保存 Commit 意图，任何后续 RPC 错误都可能发生在 Agent 已经受理之后。
UNKNOWN 保留原节点占用，Worker 使用原请求重试或查询。
先检查 Agent、认证和网络连通性，再查询原 Job；不要删除任务或使用新 request ID 重复提交。

只要结果仍未知，就不能认定配置失败或回滚成功。
客户端响应超时同样不构成取消，恢复方法见 :doc:`cli`。

重试失败回滚
----------------------------------------

适用于原 NodeJob 为 ``ROLLBACK_FAILED``，且已经排除 FRR/接口等后端不可用原因。
先查询原 Job，记录当前 sequence 和失败 node ID，再保存 ``retry-rollback.json``：

.. code-block:: json

   {
     "jobId": "<原 Job ID>",
     "nodeId": "<失败节点 UUID>",
     "requestId": "repair-001",
     "expectedJobSequence": "<当前 Job sequence>"
   }

在宿主机使用管理员令牌执行：

.. code-block:: bash

   ./bin/sdnc -addr 127.0.0.1:29090 -insecure \
     -method RetryRollback -file retry-rollback.json rpc
   ./bin/sdnc -addr 127.0.0.1:29090 -insecure -job '<原 Job ID>' watch
   ./bin/sdnc -addr 127.0.0.1:29090 -insecure -job '<原 Job ID>' job

恢复意图先写入 Controller，再交给 Agent 重试已保存的 Undo。
Agent 逆序恢复，跳过已经完成的步骤；遇到第一个 Undo 失败即停止，避免继续破坏依赖关系。
响应丢失时保留相同请求内容和 ID，允许继续查询或重复提交该文件。

恢复成功后节点为 ``FAILED_ROLLED_BACK``，解除该节点屏障；原 Job 仍是 FAILED/PARTIAL，
保留最初失败审计，不自动继续原正向配置。接下来应重新读取目标、预览并提交新 Change。
若再次失败，节点继续隔离。

Undo 丢失、基线改变、旧版本出现不安全的乱序恢复记录、Agent 协议不支持等情形会拒绝自动恢复。
这些情况下需要核对实际状态，不能修改数据库把历史任务伪装为成功。
当前 Web 没有专用 RetryRollback 按钮，使用此 CLI 或 North RPC。

Controller 存储异常
----------------------------------------

etcd 写响应丢失时，Controller 用独立的三秒窗口重试相同 CAS。
如果原写已成功，会在同一事务中核实快照和写入者，确认内容相同后更新本地存储 revision。
无法核实、遇到不同数据或写入者时停止 RPC 和 Worker，避免旧内存覆盖持久状态。

处理顺序是恢复存储连通性、确认旧进程退出、重新启动原 Controller 并检查原 Job。
File 模式在 rename 后目录同步失败时也停止写入。
etcd writer 租约是单写保护，没有给 Agent 命令附加管理代次 fencing，
因此当前不能直接部署自动多实例接管。

容量与备份
----------------------------------------

首版使用一个最多 1 MiB 的 Protobuf 快照；新写入还要预留 256 KiB，
并计入在途任务最终 Applied 的空间。每个任务只保留最新 128 条事件，
但任务、候选和幂等收据没有自动归档清理接口。
ResourceExhausted 时不要直接删除任务历史以腾空间，应停止新增变更并评估存储迁移。

备份必须包含 Controller 存储、Agent 身份与任务日志、FRR 配置，并记录对应的业务配置版本。
目前没有项目内的一键一致性备份/恢复工具。恢复管理数据后还必须核对实际 Linux/FRR 状态，
不能把备份文件存在视为已验证业务恢复。

