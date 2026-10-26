资源模型、编译与验证导入
========================================

分层实现
----------------------------------------

.. code-block:: text

   Vue / Gateway / sdnc
          |
   North gRPC + admin/viewer authorization
          |
   Candidate edits / materialized nodeResources
          |
   Resources validation + Planner diff + dependency order
          |
   Durable Desired / Applied / Job (etcd or File)
          |
   South Agent Begin -> Stage -> Commit -> GetTask
          |
   Agent local Linux / FRR transaction and readback

关键代码：``internal/resources/resources.go`` 负责自然身份、合并、关系和策略；
``internal/controller/resources.go`` 负责 North 候选编辑、查询和导入；
``internal/planner/planner.go`` 负责编译、差异和顺序；``cmd/sdnc/resources.go``
实现命名资源子命令。Agent 私有 internal 包不跨项目引用。

协议合同
----------------------------------------

``Configuration.node_resources`` 为按节点隔离的物化对象集合，
``NodeResources.objects`` 复用 typed Agent Operation 的 payload，
不存储需要反复执行的命令日志。``bridge_policies`` 保存 BD 的逻辑属性。

.. list-table:: RPC
   :header-rows: 1

   * - RPC
     - 合同
   * - GetNodeResources
     - node_id、可选 change_id；返回 resources、全局/候选/Agent revision、adopted、state、mode
   * - EditNodeResources
     - change_id、node_id、edit_revision、operations、bridge_policies；返回更新后的 Change
   * - ImportNodeResources
     - node_id、expected_agent_revision、request_id；返回已建立基线的资源状态
   * - QueryNode
     - 复用 Agent QueryRequest/Response；Controller 校验节点状态、查询能力，再进行有界 RPC

GetNodeResources 和 QueryNode 允许 viewer，编辑和导入仅 admin。
mode 是 resources、legacy、unmanaged。已纳管且 revision=0 的空节点可直接创建资源。

自然键与合并
----------------------------------------

客户端 operation.id 和 depends_on 不决定所有权，由服务器规范化。
例如 VRF/BD/VXLAN 使用名称，地址使用 interface+规范化 prefix，
BD_PORT 使用 port，BD_TENANT 使用 bridge，L3VNI 使用 tenant，
BGP 实例使用 VRF，邻居使用 VRF+neighbor，地址族对象还包含 family。
VRF 默认实例使用空字符串；IPv6 文本进行规范化，地址保留主机位。

同自然键同参数 CREATE/BIND 幂等，不生成重复创建；不同参数报冲突。
SET 合并显式字段，protobuf optional false/0 有效，未设置字段保留。
NODE_NETWORK 的 settings 按 sysctl key 合并。重复字段非空时整体替换；
需要清空没有 presence 的字段可在同一候选中显式删除并重新定义对象，
并检查 Plan 是否支持对应更新。部分身份改变要求先独立删除/解绑后再创建/绑定。

删除未管理的自然键不下发设备 DELETE；只移除 Controller 拥有的目标。
引用检查阻止删除仍被地址、成员、协议或关系引用的对象。
虚拟链路删除时清理同名 INTERFACE 属性。BD/INTERFACE 的 MTU/MAC 在增量编辑时同步，
完整 JSON 中矛盾属性被拒绝。基础物理接口允许被引用，不需要由 Controller 创建。

BD 抑制策略在 Compile 时展开到受管 VXLAN 成员的 neigh_suppress，
保留空 BD 上的意图，也不影响普通接入端口。后加隧道自然继承策略。

事务时序
----------------------------------------

.. code-block:: text

   Client       North         Resources/Planner       Store          Agent
     | Begin ---->|                  |                  |               |
     |<-- Change  |                  |                  |               |
     | Edit ----->| materialize ---->|                  |               |
     |            | save candidate ------------------->|               |
     |<-- Change  |                  |                  |               |
     | Plan ----->| references/diff->|                  |               |
     |<-- Plan    |                  |                  |               |
     | Commit --->| rebuild+revision/plan-id check       |               |
     |            | save Desired + Job --------------->|               |
     |<-- Job ID  |                  |                  |               |
     |            | Begin / Stage / Commit --------------------------->|
     |            |<---------------- task + revision ------------------|
     |            | save Applied + events ------------>|               |
     | Get/Watch ->|                  |                  |               |
     |<-- outcome |                  |                  |               |

增量 Plan 只下发相对 Applied 改变的对象。执行顺序包含
VRF → BD/VXLAN → 链路属性 → 关系 → 地址 → L3VNI → 协议实例 → 协议子项。
删除使用额外的反向依赖，尤其先关闭 Type-5/EVPN 租户配置、解绑 L3VNI，再删 VRF BGP。
全部操作按节点能力与 rollback 支持验证；失败回滚和 UNKNOWN 的处理复用现有 worker。

安全导入与漂移
----------------------------------------

Agent Running 是成功操作的追加日志，不可过滤 CREATE 后直接当成当前配置。
Import 会折叠历史更新、删除与解绑，确认目标可表达完整依赖，再调用
VerifyResources（Agent 在执行锁保护下实际读回），并二次核对 instance/revision。
最后检查 Controller desired revision 与任务屏障，原子保存资源目标和 Applied。

已由旧配置管理的节点只有在原 Applied 与规范化日志一致时才可转换。
转换不发设备操作，并仅去掉该节点的 legacy placements。
已由 resources 管理的节点禁止重新导入。revision 漂移、历史无法折叠、实际读回不匹配、
未知功能、活动任务或回滚失败都拒绝导入；不通过 allow_existing 绕过验证。
只接管 Agent 日志明确描述且验证成功的对象，人工未记录对象仍不受管。

验证
----------------------------------------

单元测试覆盖稳定身份、相同创建幂等、optional false/0、sysctl 合并、IPv6 地址规范化、
悬挂引用、抑制继承、重叠属性、历史折叠、增量不重建、重启恢复、导入读回失败、
能力和查询限制、权限与 CLI 编码。运行：

.. code-block:: bash

   go test -race ./...
   go vet ./...

真实协议收敛、FDB/路由及业务流量由独立 Docker 集成场景验证；
Controller 的配置成功不可替代转发验证。默认部署为 Controller、Web/Gateway、
Spine、Leaf1、Leaf2 五个容器，PC 容器只在可选实验场景启用。

