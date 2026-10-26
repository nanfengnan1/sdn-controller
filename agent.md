# sdn-controller 设计文档

## 当前资源级管理扩展（2026-10-25）

Controller 已增加逐节点独立资源管理；本节覆盖后文“完整配置是唯一入口”等旧实现边界。
完整 `nodes/tenants/networks` 入口继续兼容，并允许 `l3Vni=0` 的基础租户。
同一节点使用一种所有权模型；新 `nodeResources` 保存规范化后的对象和关系，
不把 Agent 操作日志直接当作目标配置。使用说明见 [资源操作手册](docs/user/resources.rst)。

```text
      Vue/Web Gateway                  sdnc CLI
             \                          /
              +---- North gRPC --------+
              | Auth / Candidate API   |
              | EditNodeResources      |------ QueryNode ------+
              +-------------+----------+                       |
                            v                                  |
             Resources: 自然键 / 字段合并 / 关系 / BD策略        |
                            |                                  |
             Planner: 依赖校验 / Applied差异 / 能力校验          |
                            |                                  |
             Store: Desired / Applied / Candidate / Job         |
                            |                                  |
             South: Begin -> Stage -> Commit -> GetTask         |
                            |                                  |
                            +------ Agent gRPC <---------------+
                                      |
                                  Linux / FRR
```

内部职责分配：

- `internal/resources`：对象自然键、IPv6 prefix 规范化、optional 字段合并、引用保护。
  BD 抑制策略持久化并编译到其 VXLAN 成员，后加入成员继承；host 端口不随之启用。
  BD/INTERFACE 重叠 MTU/MAC 同步；删除虚拟链路自动清理其接口属性，业务引用必须显式解除。
- `internal/controller/resources.go`：资源候选编辑、资源查询、Agent 实际状态查询、验证导入。
- `internal/planner`：同节点资源按依赖排列；只下发相对 Applied 的变化；不重复创建已有 VRF。
  删除反向依赖包含 Type-5、EVPN 租户、L3VNI、BGP/OSPFv2/v3、成员和地址。
- `cmd/sdnc/resources.go`：tenant、bd、vxlan、interface、route、bgp、ospf 命名子命令，
  默认单操作提交，也支持 `--change` 批量候选；Web 使用相同 North 模型。

```text
Client          North              Planner             Store           Agent
  | Begin ------>|                    |                   |               |
  |<-- Change    |                    |                   |               |
  | Edit ------->| materialize target |                   |               |
  |              | save candidate ---------------------->|               |
  |<-- Change    |                    |                   |               |
  | Plan ------->| refs/diff/caps ---->|                   |               |
  |<-- Plan      |                    |                   |               |
  | Commit ----->| rebuild and compare revisions/plan     |               |
  |              | save Desired+Job -------------------->|               |
  |<-- Job ID    |                    |                   |               |
  |              | Begin/Stage/Commit ---------------------------------->|
  |              |<-------------------- task result / revision ---------|
  |              | save Applied+events ----------------->|               |
  | Get/Watch -->|                    |                   |               |
  |<-- result    |                    |                   |               |
```

事务保持既有含义：Desired 是已提交意图，Applied 是成功配置基线。
Agent 回滚后，Controller 保留失败 Desired 和任务供检查；用新候选修正意图。
本地事务回滚由 Agent 执行，跨节点保持 STOP/PARTIAL，不能将成功回执等同协议收敛。

新 RPC：`GetNodeResources`、`EditNodeResources`、`ImportNodeResources`、`QueryNode`。
查询复用 Agent 的 typed QueryRequest/Response，包含来源、时间、revision、分页与失败信息；
未声明的查询能力返回 Unimplemented。admin 可以写，viewer 仅查询。

现有 Agent 配置通过 ImportNodeResources 显式纳管：按自然键折叠成功日志（包括删除、解绑），
调用 Agent VerifyResources 核验真实状态、再次核对 epoch/revision，再原子持久化 ownership。
旧完整配置节点须原 Applied 和日志一致才可转换。活动任务、回滚失败、漂移、不支持或无法验证
都会拒绝导入；allow_existing 不能替代验证。手工未记录对象保持不受管。

默认容器部署为 Controller、Web/Gateway、Spine、Leaf1、Leaf2 共五个容器；
每个 Controller/Agent 容器管理其内置存储服务，PC 是额外的实验 profile。
协议和测试边界详见 [资源开发文档](docs/developer/resources.rst)。

更新日期：2026-10-25。

项目路径：`~/program/github/sdn-controller`；当前 Windows 共享路径为 `X:\program\github\sdn-controller`。

本文定义 sdn-controller 的分层架构、内部组件和请求响应时序。2026-10-25 已实现下述首版代码；其余章节继续描述完整目标架构，实际实现边界以本节和 [README](README.md) 为准。已运行的 sdn-agent 协议是对接基线。

## 0. 首版实现与设计的映射

| 设计组件 | 当前代码 | 已实现边界 |
| --- | --- | --- |
| North / Agent Gateway Server | [controller.proto](api/controller/v1/controller.proto)、[controller.go](internal/controller/controller.go) | North gRPC、注册/心跳、节点纳管、候选事务、任务查询和订阅 |
| Auth / Transport | [transport.go](pkg/transport/transport.go) | admin/viewer/agent 三类独立 token、TLS 或显式实验 insecure；无用户数据库/OIDC |
| Resource / Planner | [planner.go](internal/planner/planner.go) | 结构化 nodes/tenants/networks，校验 VNI/RD/RT、IP/端口作用域，生成 typed Agent op 和显式顺序；只支持已列明的更新类型 |
| Orchestrator / Recovery | [worker.go](internal/controller/worker.go)、[recovery.go](internal/controller/recovery.go) | 每个 Job 全部 Stage 后按节点顺序 Commit；节点执行权隔离，无关 Job 轮流推进；持久意图、原请求重试、GetTask 轮询、UNKNOWN、PARTIAL、RetryRollback 和重启恢复 |
| Repository | [repository.go](internal/repository/repository.go) | 真实 etcd 单快照 CAS/写入租约；开发 File 使用 flock/fsync/rename；非后文分 key 的扩展布局 |
| Collector / Topology | [topology.go](internal/controller/topology.go) | 按请求有界采集、内存缓存、FRR 10 Type-2/Type-3、配置与观测图；没有周期采集或物理 LLDP |
| CLI / 实验输入 | [sdnc](cmd/sdnc/main.go)、[evpn.json](examples/evpn.json) | 完整结构化 EVPN 实验示例、显式提交、原始 North RPC、任务观察 |

当前关键差异：

- 可靠性修复：etcd 提交丢响应会核实相同 CAS 结果，无法核实则停止写入；回滚重试保留原失败审计，只在 Undo 验证成功后解除节点屏障。具体操作见 [恢复说明](docs/recovery.md)，尚未解决的持久化/重建问题见 [待办](docs/backlog.md)。

- 首版没有跨节点自动补偿；采用 STOP/PARTIAL，已成功节点通过用户提交新目标配置回退。Agent 的本地回滚仍有效。
- 单快照容量上限 1 MiB；接收新配置时投影最终 Applied 并预留 256 KiB 执行空间。心跳也更新快照，因此不是大规模控制器存储方案。
- 每任务保留最新 128 个事件，旧游标明确返回 OutOfRange；最终任务、幂等收据保留。观测缓存不持久化。
- 纳管不会自动导入现有配置；revision=0 不能证明 Linux/FRR 没有人工配置。显式身份和采用基线后，只处理 Controller 生成的资源。
- North Configuration 为完整文档替换；需先 GetConfiguration，Edit 校验候选 edit_revision，Commit 校验 desired base_revision 和 plan_id。
- Lease 仅用于首版单写实例排他；Agent 尚无管理代次隔离，未实现自动 HA 接管。

启动、RPC 字段、测试命令、容量边界和不支持的修改参见 [README](README.md)。后文中的自动补偿、分 key Repository、周期 Collector 等均属于尚未实现的目标设计。

首版采用 Go、单进程模块化实现、单个配置写入实例、etcd 持久化。Controller 与 Agent 是两个独立项目，公共协议需要按明确版本共享，不依赖对方的 `internal` 包。

## 1. 分层架构

### 1.1 职责边界与首版范围

| 项目/组件 | 职责 |
| --- | --- |
| sdn-controller | 管理全网 Leaf/Spine、全局资源和期望配置，生成节点配置计划，协调任务，汇总拓扑 |
| sdn-agent | 管理本节点，执行 Linux/FRR 等后端配置，进行本地回读、失败补偿和状态查询 |
| FRR | 执行 OSPF、BGP EVPN 等分布式路由协议，负责动态路由交换和选路 |
| Controller etcd | 保存全局资源、版本、节点登记、分配记录、部署基线、任务及恢复信息 |
| Agent Redis | 保存本节点 Candidate、Running、Operational、Task 与事件，Controller 通过 Agent API 访问 |

首版目标是完成“创建租户/网络 → 部署到多个节点 → 查询任务与实际状态 → 输出 Overlay 与主机位置视图”的完整流程。先使用现有实验中 Leaf、Spine 上的 Agent；同时在 South 层保留设备直连接口，供后续管理未部署 Agent 的 Spine。

首版北向采用 gRPC + CLI；REST、NETCONF 作为后续 North Adapter。VPP、交换机数据面由 Agent 的后端适配器扩展，Controller 根据节点能力生成支持的计划。

### 1.2 分层架构图

```text
                     CLI / 网管 / 自动化系统
                               |
                               v
              +--------------------------------+
              |           North 层             |
              | 资源编辑、计划预览、提交        |
              | 资源/节点/拓扑/任务查询         |
              | 首版 gRPC + CLI                |
              +----------------+---------------+
                               |
                               v
       +------------------------------------------------+
       |                 Service 层                    |
       |                                                |
       | Node Manager       身份、角色、能力、管理状态   |
       | Resource Manager   租户、网络、绑定、资源分配   |
       | Topology Service   Overlay、主机位置、数据来源  |
       +-----------------------+------------------------+
                               |
                               v
              +--------------------------------+
              |          Planning 层           |
              | 全局校验、配置差异、节点选择   |
              | 生成每节点 op DAG 与跨节点依赖 |
              +----------------+---------------+
                               |
                               v
              +--------------------------------+
              |        Orchestration 层        |
              | 任务调度、事务提交、结果跟踪   |
              | 幂等重试、补偿、重启恢复       |
              +----------------+---------------+
                               |
                               v
       +------------------------------------------------+
       |                  South 层                      |
       |                                                |
       | Agent Gateway            Device Adapter        |
       |                          预留设备直连接口      |
       | gRPC Server：接收 Register / Heartbeat          |
       | gRPC Client：事务、状态查询、任务事件订阅       |
       +-----------------------+------------------------+
                               |
                  +------------+------------+
                  |                         |
                  v                         v
              Leaf Agent                Spine Agent
                  |                         |
              Linux / FRR               Linux / FRR

       Storage 层：各模块通过 Repository 访问 etcd
       保存全局期望配置、节点登记、资源分配、任务与进度

       观测链路：
       Agent GetState -> Collector -> Normalizer -> Topology Service
```

图中的层是逻辑模块，首版不拆成多个微服务。North 接口处理器负责协议和鉴权；资源规则集中在 Service；节点 op 生成集中在 Planner；所有设备交互统一经过 South。

### 1.3 数据归属与配置版本

Controller 区分以下三种数据，不将一次任务受理等同于配置已生效：

| 数据 | 含义 | 更新时机 |
| --- | --- | --- |
| Desired | 用户已经 commit 的全局期望资源及部署关系 | Controller CommitChange 成功持久化时 |
| Applied | 每个节点最后确认应用成功的受管资源基线 | 对应 Agent task 明确 SUCCEEDED 后 |
| Observed | 从节点采集的实际接口、路由、EVPN 等信息 | 查询/采集完成时，包含来源、时间和错误 |

`desired_revision` 是 Controller 的业务配置版本；`agent_revision` 是节点本地 Running 版本；`etcd revision` 是存储版本。三者分别记录，不直接做数值相等比较。每个节点的部署记录保存目标业务版本、所依据的 Agent revision、成功后的 Agent revision 和规范化资源快照。

## 2. 内部架构

### 2.1 内部组件关系图

```text
                         North gRPC / CLI
                                |
                                v
                      API Handler / Auth
                                |
                 +--------------+--------------------+
                 |                                   |
                 v                                   v
           Change Service                       Query Service
                 |                                   |
                 v                          +--------+--------+
          Resource Manager                  |                 |
                 |                     Node Inventory    Topology Index
                 v                          ^                 ^
          Global Planner                    |                 |
          /      |       \                  |             Normalizer
   Validator   Diff    Op Compiler          |                 ^
          \      |       /                  |                 |
                 v                          |              Collector
          Immutable Plan                    |                 |
                 |                          |                 |
                 v                          |                 |
          Job Orchestrator                  |                 |
          /      |       \                  |                 |
   Scheduler  Task Tracker  Recovery        |                 |
          \      |       /                  |                 |
                 v                          |                 |
          Southbound Manager                |                 |
                 |                          |                 |
        +--------+--------------------------+-----------------+
        |                  Agent Gateway                      |
        |                                                     |
        | Client Pool / Transaction Client / State Client      |
        | Registration Server / Heartbeat Handler              |
        +-------------------------+---------------------------+
                                  |
                                  v
                              sdn-agent

  各组件 -> Repository -> etcd
             |
             +-- Desired / Candidate / Allocation / Applied
             +-- Node / Registration / Job / Event / Request receipt

  Collector / Normalizer -> 内存观测缓存与拓扑索引
  进程重启后重新采集；后续可增加 Redis 缓存
```

### 2.2 组件职责与内部接口

下表是拟实现的逻辑接口，最终 Go 类型和方法签名在编码时定义。

| 组件 | 主要职责 | 逻辑接口或输出 |
| --- | --- | --- |
| API Handler / Auth | 解码北向请求，鉴权、请求大小与身份校验 | 调用 Change/Query Service |
| Change Service | 管理候选资源变更、基础版本、幂等键，冻结已提交变更 | BeginChange、EditChange、PlanChange、CommitChange、AbortChange |
| Resource Manager | 校验租户/网络/端口归属、资源引用、分配范围 | 规范化资源、分配提案、变更前快照 |
| Node Manager | 注册、UUID 冲突处理、角色与接入方式、能力、心跳 | NodeRecord、可调度性与管理状态 |
| Global Planner | 对比 Desired 与 Applied，生成创建/修改/删除计划 | GlobalPlan、NodePlan、跨节点依赖 |
| Op Compiler | 将节点资源计划转换为现有 Protobuf Operation | operations[]、depends_on、前置条件 |
| Scheduler | 按跨节点依赖调度，同一节点保持一个在途配置事务 | 可执行 NodePlan |
| Task Tracker | 保存 transaction_id/task_id，订阅事件，查询最终结果 | NodeJob 状态、事件游标、Applied 更新 |
| Recovery Manager | Controller 重启后重建任务；处理响应丢失、未知结果与补偿 | 恢复动作或需要人工处理的原因 |
| Agent Gateway Server | 实现现有 Controller.Register/Heartbeat | 注册结果与心跳应答 |
| Agent Gateway Client | 维护连接并调用 Agent RPC，统一 deadline、重试与错误转换 | 事务结果、任务事件、状态快照 |
| Device Adapter | 预留 NETCONF/REST 等直接管理设备的通道 | 声明实际支持的能力和事务语义 |
| Collector | 定期或按需调用 GetState，采集失败时保留最后快照并标记过期 | 带来源和时间的原始观测 |
| Normalizer | 按来源/版本解析 FRR 和 Linux JSON，标准化数据 | VTEP、VNI、MAC/IP、接口、邻居等对象 |
| Topology Service | 合并配置关系与观测，提供 Overlay 和主机视图 | 可查询拓扑、来源、完整性和新鲜度 |
| Repository | 封装 etcd 访问、版本比较、原子发布与存储错误 | 业务模型存取接口 |

### 2.3 全局资源模型

```text
Fabric
  |
  +-- Node: leaf1 / leaf2 / spine
  |
  +-- Tenant
        |
        +-- L2Network
        |     |
        |     +-- Attachment: node_id + 接入口
        |
        +-- TenantPlacement: node_id + 本地 VRF/L3VNI 映射

Change -> GlobalPlan -> Job
                         |
                         +-- NodeJob(leaf1) -> Agent transaction/task
                         +-- NodeJob(leaf2) -> Agent transaction/task
                         +-- NodeJob(spine) -> Agent transaction/task
```

| 对象 | 建议字段与语义 |
| --- | --- |
| Fabric | fabric_id、名称、节点集合、受管 Underlay 链路、VTEP 地址、ASN/RR 配置及资源池 |
| Node | node_id、registration_id、instance_id、角色、接入方式、endpoint、协议版本、能力、管理状态 |
| Tenant | tenant_id、fabric_id、名称、L3VNI、导入/导出 RT、路由策略引用 |
| L2Network | network_id、tenant_id、L2VNI、子网、网关、二层策略 |
| TenantPlacement | tenant_id、node_id、本地 VRF 名称、table_id、L3 bridge/VXLAN 名称、Router MAC、RD |
| Attachment | attachment_id、network_id、node_id、接入口、本地 BD 与网关配置 |
| RoutingProfile | OSPF/BGP/RR 参数；首版限制为 Agent 已支持的功能 |
| Change | change_id、request_id、base_revision、候选资源变更、变更前快照、冻结状态 |
| GlobalPlan | plan_id、输入版本、能力快照、各节点不可变 op 计划、跨节点依赖、失败策略 |
| Job / NodeJob | job_id、node_id、目标版本、Agent 基础版本、RPC 幂等键、transaction_id、task_id、事件游标、执行与补偿结果 |

资源分配与校验遵循明确的作用域：

- `tenant_id`、`network_id` 等为 Controller 业务标识，不直接作为节点接口名。
- VNI 按 Fabric 的分配策略管理；共享同一网络的多个节点使用相同 VNI。
- Linux VRF table_id 与接口名按节点分配；不同节点可以使用相同 table_id。
- RD 按节点及 EVPN 实例生成；RT 按导入/导出关系配置，允许多个节点共享，不能一律要求全局唯一。
- 接入口归属与设备能力一起校验；首版同一端口不同时作为多个普通 bridge 的成员。
- IP 子网重叠按租户/路由域判断，不把不同租户的相同私网地址直接当作冲突。

### 2.4 计划生成与依赖

用户的北向输入描述资源和部署目标，Planner 输出可被 Agent 执行的节点 op。北向不要求用户手写 Linux 命令或 FRR CLI。

```text
创建 TENANT-A，部署到 leaf1 和 leaf2
  L3VNI 50000
  网络 A：L2VNI 10010，接入 leaf1/host10
  网络 B：L2VNI 10020，接入 leaf2/host20
                         |
                         v
                  Global Planner
                         |
          +--------------+--------------+
          |                             |
          v                             v
     NodePlan leaf1                NodePlan leaf2
     TENANT CREATE                 TENANT CREATE
     BD / BD_TENANT                BD / BD_TENANT
     VXLAN / BD_PORT               VXLAN / BD_PORT
     ADDRESS / INTERFACE           ADDRESS / INTERFACE
     TENANT_L3VNI                  TENANT_L3VNI
     BGP / EVPN                    BGP / EVPN
          |                             |
          v                             v
     本节点 depends_on DAG         本节点 depends_on DAG
```

上述为功能顺序概览，实际 DAG 需要包含引用和具体参数。例如配置地址前要创建接口；绑定 VXLAN 前要创建 VXLAN 和目标 BD；L3-SVI 在本实验模式下不配置 IP。后端之间的执行顺序由 Agent 的 Registry 管理，Controller 下发统一 op，不直接选择其内部 callback。

跨节点依赖与节点内部依赖分开建模。首版优先采用确定性串行调度，协议收敛和转发验收使用独立检查条件；任务成功首先表示相关 Agent 配置事务成功，不能仅由该状态推导 BGP 邻居已建立或主机已可达。

删除计划由资源引用生成：先撤销路由配置引用、解绑端口/租户、移除地址，再删除 VXLAN、BD、VRF。不能对所有 CREATE 机械替换成 DELETE；必须考虑修改前值、共享资源和 Agent 的删除前置条件。

### 2.5 存储、任务与恢复

建议的 etcd key 空间：

```text
/sdn/v1/nodes/<node_id>                  节点登记与稳定属性
/sdn/v1/registrations/<registration_id>  注册身份到 node_id 的映射
/sdn/v1/changes/<change_id>              候选资源变更
/sdn/v1/desired/...                     全局目标资源及版本
/sdn/v1/allocations/...                 VNI、节点 table_id 等分配记录
/sdn/v1/applied/<node_id>                最后确认应用成功的受管资源基线
/sdn/v1/plans/<plan_id>/...              冻结后的执行计划
/sdn/v1/jobs/<job_id>/...                全局任务、节点任务、RPC 意图与结果
/sdn/v1/events/<job_id>/<sequence>       持久化任务事件
/sdn/v1/requests/<caller>/<request_id>   北向幂等收据
```

CommitChange 发布前重新检查基础版本、资源分配和计划有效性，原子发布 Desired 变更、分配记录、冻结计划的引用、Job 和幂等收据。计划较大时可预写不可变计划数据，确认完整后再原子发布引用；Worker 只执行已发布的 Job。

每次有副作用的 Agent RPC 发送前，先保存调用意图、固定请求内容与幂等键；成功响应后保存对应 ID。重试沿用原请求，不能重新生成基础版本或幂等键后当成同一次请求。

| 状态 | 全局任务含义 |
| --- | --- |
| ACCEPTED | 全局变更与任务已持久化，等待调度 |
| STAGING | 创建各节点候选并暂存 op，尚未向相应节点 Commit |
| APPLYING | 一个或多个节点事务已提交，正在执行或等待结果 |
| SUCCEEDED | 本任务要求的全部节点事务已确认成功 |
| FAILED | 任务失败，已确认没有留下本任务的已成功节点变更；Agent 未恢复的修改须另行标明 |
| PARTIAL | 部分节点成功，其余失败，或存在已知未恢复的变更；暂停相关自动推进 |
| UNKNOWN | 存在无法确认结果的节点；可恢复状态，优先查询结果 |
| COMPENSATING | 根据变更前基线执行新的补偿事务 |
| ROLLED_BACK | 全局补偿完成，原目标未达成 |
| COMPENSATION_FAILED | 补偿失败或不能确认恢复，保留实际进度并暂停相关变更 |

节点管理状态仍为 `INIT / RUNNING / DOWN`。任务失败、配置阻塞和观测过期作为独立字段呈现，不扩展成新的节点管理状态。

首版默认失败策略为停止后续依赖任务、保存部分结果，提供明确的重试/恢复入口。自动跨节点补偿只用于声明可补偿、具有变更前基线、且结果已确定的操作集合。已成功的 Agent 事务通过新事务补偿；现有 Agent 没有“撤销任意已提交 task”的 RPC。

Desired 保留用户提交的目标。补偿后若目标仍未达成，应显式标记并暂停该目标版本的自动推进，等待用户重试或提交新的目标；不得在后台静默改写用户目标，也不得补偿完成后立刻自动重推同一个失败目标。

Controller 重启后先恢复 NodeJob、RPC 意图和事件游标，再查询或订阅 Agent。对未知 Commit 结果，用原 transaction_id/request_id 重试 Commit 找回原 task；已有 task_id 时使用 GetTask。对未 Commit 的候选可按任务策略继续或 Abort。Agent 的 ROLLBACK_FAILED 在首版需要人工核实处理，Controller 不能调用一个并不存在的解锁接口。

首版仅允许一个配置写入实例；自动多实例接管是后续能力。启用 HA 前，需要让 Agent 校验控制权代次并拒绝旧 Controller 的写请求，仅在 etcd 选主不足以隔离仍能访问 Agent 的旧实例。

### 2.6 节点接入与观测

新节点注册只建立身份和能力记录。Controller 随后查询实际状态，确认或导入受管资源基线；在未完成纳管前，不因 Controller 数据库为空就删除或覆盖节点已有配置。当前 Agent 的 Running 是成功操作日志，Controller 不能直接假定它已经是规范化的资源表。

首版沿用 TLS/token 的通信方式。node_id 是资源标识，鉴权凭据单独校验；role、fabric_id 等现有注册消息没有的字段，先由 Controller 节点配置维护。克隆 registration_id、丢失本地身份等情况需要明确的重新纳管流程。

Agent 上报的 endpoint 必须能由 Controller 访问。当前实验的 `leaf1:9090` 等地址属于 Docker 管理网络；部署 Controller 时应加入可达网络或配置可达地址。双向 Client/Server 表示双方各自提供服务，不代表自动建立反向隧道。

Collector 首版按需或定期调用 GetState；Heartbeat 只报告身份、实例、endpoint 和 Agent revision，不能把它当作完整网络状态上报。采集结果包含 node_id、instance_id、采集时间、来源、解析版本和错误。采集失败时保留最后观测并标记过期，不能把失败返回当作空表处理。

Type-2/Type-3 用于构建 Overlay 和主机位置视图。路由键保留来源节点、RD、路由类型、完整 NLRI/路径等信息，再通过显式租户/VNI/RT 映射关联业务对象；不能只按 MAC/IP 跨租户去重。物理拓扑需要 LLDP 或已确认的链路清单。高频原始路由/FDB 表优先进入可重建的观测缓存，不默认逐条写入 etcd。

### 2.7 拟采用的项目目录

以下为后续实现布局；当前创建的是设计文档。

```text
sdn-controller/
  agent.md
  api/controller/v1/          北向资源、任务与查询协议
  cmd/sdn-controller/         Controller 进程入口
  cmd/sdn-controllerctl/      北向 CLI
  internal/north/            北向协议适配
  internal/service/          资源、节点、变更与查询服务
  internal/model/            全局资源与任务模型
  internal/planner/          校验、差异、op 编译、跨节点依赖
  internal/orchestrator/     调度、跟踪、补偿、恢复
  internal/south/agent/      Agent gRPC Server/Client
  internal/south/device/     预留设备直连接口
  internal/observe/          采集、标准化、观测缓存
  internal/topology/         Overlay、主机位置、物理视图
  internal/store/            etcd Repository
  tests/                     单元、协议契约、故障恢复测试
  lab/                       与 sdn-agent 的多节点联调
  docs/                      协议、操作、部署和测试文档
```

## 3. 请求响应时序图

下图均为 Controller 的拟实现流程。图中的 Agent RPC 名称对应当前已存在的协议；北向资源 RPC 名称是设计提案。

### 3.1 Agent 注册、能力上报与心跳

```text
 Agent             Gateway Server          Node Manager              etcd
   |                     |                       |                      |
   |-- Register -------->|                       |                      |
   | proposed_node_id    |-- 校验身份/转交 ------>|                      |
   | registration_id    |                       |-- 查询注册映射 ----->|
   | instance_id        |                       |<-- 已有身份/不存在 --|
   | endpoint           |                       |                      |
   | capabilities       |                       | 冲突时分配新 node_id |
   |                     |                       |-- 原子保存映射 ----->|
   |                     |                       |<-- 持久化完成 --------|
   |<-- Registered ------|<-- 最终 node_id -------|                      |
   |     node_id         |                       |                      |
   | 本地保存身份        |                       |                      |
   |                     |                       |                      |
   |-- Heartbeat ------->|-- 更新最近心跳 ------>|                      |
   | node_id/instance_id |                       | 校验当前实例         |
   | endpoint/revision   |<-- 更新结果 -----------|                      |
   |<-- Empty / 错误 -----|                       |                      |
```

已有 registration_id 的重试返回原 node_id，不能因响应丢失反复分配新身份。身份映射变更使用原子条件更新；心跳时间等高频瞬态信息可在内存维护，关键状态变化再持久化。Controller 重启后重新确认会话新鲜度，不能仅凭旧在线记录继续调度。

Gateway Client 随后调用 GetCapabilities/GetState 确认节点可达及资源基线。管理心跳超时标记 DOWN 和观测过期；不直接删除业务网络或将其等同于 EVPN 路由撤销。

### 3.2 北向资源编辑、计划预览与任务受理

```text
 User / CLI          North API            Service / Planner           etcd
     |                   |                        |                      |
     |-- BeginChange --->|-- 创建候选 ----------->|-- 保存 Change ------>|
     | base_revision     |                        |<---------------------|
     | request_id        |<-- change_id ----------|                      |
     |<-- change_id -----|                        |                      |
     |                   |                        |                      |
     |-- EditChange ---->|-- 校验资源变更 ------->|                      |
     | tenant/network    |                        |-- 更新 Candidate --->|
     | attachments       |<-- 暂存结果 -----------|<---------------------|
     |<-- 暂存成功 ------|                        |                      |
     |                   |                        |                      |
     |-- PlanChange ---->|-- 生成计划 ----------->|                      |
     |                   |                        | 读取 Desired/Applied |
     |                   |                        | 校验能力、生成 DAG   |
     |<-- 计划预览 ------|<-- 节点 op 与依赖 -----|                      |
     |                   |                        |                      |
     |-- CommitChange -->|-- 再次校验/冻结 ------>|                      |
     | request_id        |                        |-- 原子发布 ---------->|
     |                   |                        | Desired/Allocation   |
     |                   |                        | Plan 引用/Job/Event  |
     |                   |                        | 幂等收据             |
     |                   |                        |<-- 持久化成功 --------|
     |                   |                        | 唤醒 Orchestrator    |
     |<-- job_id --------|<-- ACCEPTED -----------|                      |
     |    ACCEPTED       |                        |                      |
```

PlanChange 可以产生只读节点查询，不下发配置。预览与 Commit 之间资源版本发生变化时，提交应拒绝或要求重新生成计划，不能照旧使用已失效的分配结果。受理响应丢失时，调用方用相同 request_id 和请求内容取回原 Job。

### 3.3 节点事务下发与成功结果归档

```text
 Orchestrator              etcd             Gateway Client             Agent
      |                      |                     |                     |
      |-- 读取 Job/Plan ---->|                     |                     |
      |<---------------------|                     |                     |
      |-- 获取节点状态 --------------------------->|-- GetState -------->|
      |<-- 当前 Agent revision --------------------|<---------------------|
      |                      |                     |                     |
      |-- 保存 Begin 请求 -->|                     |                     |
      |   固定 request_id    |                     |                     |
      |   固定 base_revision |                     |                     |
      |-- Begin ---------------------------------->|-- Begin ----------->|
      |<-- transaction_id -------------------------|<---------------------|
      |-- 保存事务 ID ------>|                     |                     |
      |                      |                     |                     |
      |-- Stage(ops) ----------------------------->|-- Stage ------------>|
      |<-- 暂存成功 -------------------------------|<---------------------|
      |-- 保存 STAGED ------>|                     |                     |
      |                      |                     |                     |
      |  对本批相关节点完成暂存，再按跨节点依赖提交                         |
      |                      |                     |                     |
      |-- 保存 Commit 请求 ->|                     |                     |
      |-- Commit --------------------------------->|-- Commit ---------->|
      |<-- task_id / ACCEPTED ---------------------|<---------------------|
      |-- 保存 task_id ----->|                     |                     |
      |                      |                     |                     |
      |-- WatchTasks(task_id, after_sequence) ---->|-- WatchTasks ------>|
      |<== Agent 任务事件 ========================|<=====================|
      |-- 保存状态/游标 ---->|                     |    本地执行与回滚   |
      |                      |                     |                     |
      |<== SUCCEEDED =============================|<=====================|
      |-- 原子保存 -------->|                     |                     |
      |   NodeJob 成功       |                     |                     |
      |   Applied 基线       |                     |                     |
      |   新 Agent revision  |                     |                     |
      |   事件与游标         |                     |                     |
      |                      |                     |                     |
      |  全部节点确认成功    |                     |                     |
      |-- Job=SUCCEEDED ---->|                     |                     |
```

Stage 只表示节点候选通过当前校验，不会锁住全网资源，也不构成分布式两阶段提交。提交前或排队期间 Agent revision 变化时，按实际结果重新校验和规划。同一节点的后续任务须以最新已确认基线执行，不复用过期 op 计划。

事件先持久化再推进消费游标。重复事件按 node_id、task_id、sequence 去重；订阅断开后按持久化游标重放，必要时使用 GetTask 查询终态。设备配置完成与后续业务验收分别记录。

### 3.4 跨节点部分失败与补偿

```text
 Orchestrator               etcd                 Agent leaf1         Agent leaf2
      |                       |                       |                  |
      |-- Commit ------------------------------------>|                  |
      |<-- task1 / ACCEPTED --------------------------|                  |
      |<== task1 SUCCEEDED ===========================|                  |
      |-- 保存 leaf1 Applied ->|                       |                  |
      |                       |                       |                  |
      |-- Commit ------------------------------------------------------->|
      |<-- task2 / ACCEPTED --------------------------------------------|
      |<== task2 FAILED_ROLLED_BACK =====================================|
      |-- Job=PARTIAL ------->|                       |                  |
      |   停止后续依赖任务    |                       |                  |
      |                       |                       |                  |
      |  若选择且允许补偿：基于变更前基线生成新计划                       |
      |-- 保存补偿计划 ------>|                       |                  |
      |-- Begin/Stage/Commit ------------------------>|                  |
      |<-- 新 compensation_task_id ------------------|                  |
      |<== 补偿结果 ==================================|                  |
      |-- 保存结果/Applied -->|                       |                  |
      |   ROLLED_BACK 或      |                       |                  |
      |   COMPENSATION_FAILED |                       |                  |
```

如果 Leaf2 结果未知，先记录 UNKNOWN 并恢复查询，不立即假定 Leaf2 失败并撤销 Leaf1。若 Agent 返回 ROLLBACK_FAILED，需要保留其未恢复的步骤与实际观测，暂停相关资源变更。Controller 的补偿也是新的单节点事务，可能失败，不能对用户承诺跨节点原子回滚。

### 3.5 Commit 响应丢失与 Controller 重启恢复

```text
 Controller / Recovery        etcd                      Agent
          |                     |                         |
          |-- 保存 Commit 意图 ->|                         |
          |-- Commit(transaction_id, request_id) -------->|
          |                     |                         | 持久化受理
          |                     |                         | 后台继续执行
          |       X 响应丢失 / Controller 重启            |
          |                     |                         |
          |-- 读取原请求 ------>|                         |
          |<-- 原事务与幂等键 --|                         |
          |-- 重试相同 Commit --------------------------->|
          |<-- 原 task_id 与当前状态 ---------------------|
          |-- 保存 task_id ---->|                         |
          |-- GetTask / WatchTasks(after_sequence) ------>|
          |<-- 当前结果 / 持久化事件 ---------------------|
          |-- 恢复任务进度 ---->|                         |
```

已知 task_id 时直接查询/订阅。结果未知期间保留对应资源和节点的配置占用，防止重复创建或使用未经确认的基线继续执行。Agent instance_id 变化时重新核实身份、任务和实际配置；既不能认定所有旧任务丢失，也不能认定旧网络对象必然仍在。

### 3.6 北向任务订阅与拓扑查询

```text
 CLI / 网管            North / Query           Task Store / Topology       Agent
     |                       |                          |                    |
     |-- WatchJob(job_id) -->|-- 读取持久化 Job 事件 -->|                    |
     |<== 全局任务事件 =====|<==========================|                    |
     |                       |                          |                    |
     |-- GetJob(job_id) ---->|-- 查询节点任务汇总 ------>|                    |
     |<-- 全局/各节点结果 --|<--------------------------|                    |
     |                       |                          |                    |
     |-- GetTopology ------>|-- 查询标准化拓扑 -------->|                    |
     |<-- 拓扑 + 来源/时间 --|<--------------------------|                    |
     |                       |                          |                    |
     |                       |   Collector 按周期或显式刷新触发采集          |
     |                       |                          |-- GetState ------->|
     |                       |                          |<-- 原始观测 JSON --|
     |                       |                          | 标准化、更新缓存   |
```

普通拓扑查询读取已有快照并返回新鲜度；显式刷新可以触发采集，但应暴露等待状态和超时，不能将未完成的采集包装成最新拓扑。Controller 重启后可由 Desired/Applied 展示配置关系，实测拓扑则在重新采集后更新，并区分两类数据来源。

## 4. 与现有 Agent 的对接及首版验收

### 4.1 现有接口与需要新增的部分

| 范围 | 当前可用接口/数据 | Controller 设计中的处理 |
| --- | --- | --- |
| 身份与心跳 | Controller.Register、Controller.Heartbeat | 正式实现替换 mock，etcd 保存稳定身份映射 |
| 能力 | GetCapabilities，op/backends/rollback、protocol_version | 首版依据已定义 op schema 与能力校验；细粒度限制后续扩展 |
| 节点配置 | Begin、Stage、Commit、Abort | 直接使用；Abort 只用于未提交候选 |
| 任务 | GetTask、WatchTasks | 持久化 task_id、事件游标与恢复意图 |
| 运行配置 | GetState.running_json 为成功操作日志 | 建立 Controller 自己的规范化 Applied 基线 |
| 实际状态 | GetState.observations 为按来源组织的 JSON | Collector + Normalizer；首版轮询 |
| 北向资源 API | 尚未实现 | 新定义 Controller protobuf、CLI 与资源规则 |
| 全网任务和补偿 | 尚未实现 | 新实现 Planner、Orchestrator 与 Recovery |
| 多实例控制权隔离 | Agent 当前请求没有控制权代次字段 | 首版单写；HA 前扩展协议及校验 |

### 4.2 首版验收场景

1. Leaf/Spine 注册、身份冲突重试、能力查询、心跳超时和重连。
2. 通过 Controller CLI 创建租户、二层网络和接入口，预览各节点计划后 commit。
3. 在现有 Linux + FRR 实验中通过 Controller 下发配置，验证 Type-2/Type-3 和跨网段通信。
4. 重复北向请求和节点 Commit 不重复创建资源；版本冲突有明确结果。
5. Leaf1 成功、Leaf2 失败时准确呈现 PARTIAL；满足条件时用新事务补偿。
6. Commit 响应丢失、订阅断连、Controller/Agent 进程重启后恢复任务，而不盲目重放 op。
7. 节点已有配置的接管流程、人工修改导致的基线偏差、Agent ROLLBACK_FAILED 均有明确阻塞原因。
8. 拓扑数据保留租户上下文、来源、时间与过期状态；管理断线不误报为动态路由已撤销。

对接依据：

- [现有 Agent 设计](../sdn-agent/agent.md)
- [Agent Protobuf 协议](../sdn-agent/api/agent/v1/agent.proto)
- [Agent 操作目录](../sdn-agent/docs/operations.md)
- [Agent 事务与恢复语义](../sdn-agent/docs/transactions.md)
- [Agent 实测验收记录](../sdn-agent/docs/validation.md)

下一步按资源模型、北向协议、节点接入、Planner、任务编排、观测拓扑的顺序细化和实现；实现变更需要同步更新本文与协议契约测试。
