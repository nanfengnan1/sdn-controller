扩展资源、接口与存储
============================================================

本页面向第一次接手项目的开发者：先确定改变哪一层，再按真实请求链路修改和验证。
组件及请求时序见 :doc:`architecture`，协议见 :doc:`protocol`，
资源物化见 :doc:`resources`，差异和执行计划见 :doc:`planner`。
根目录 agent.md 是更完整的目标设计，不能据此假定相应代码已经存在。

项目与代码边界
------------------------------------------------------------

.. list-table:: 扩展入口
   :header-rows: 1
   :widths: 26 43 31

   * - 需求
     - 主要实现位置
     - 协作边界
   * - 新资源或字段
     - api/controller/v1/controller.proto、internal/resources、internal/planner
     - Agent Operation / 能力声明；CLI / Web 编辑和查询
   * - 新北向接口
     - controller.proto、internal/controller、pkg/transport
     - Gateway 只翻译 HTTP 与公开 North RPC
   * - 新查询
     - internal/controller/resources.go 的 QueryNode
     - Agent 实现 QueryKind、查询能力和结构化读回
   * - 新持久化实现
     - internal/repository、cmd/sdn-controller、internal/config
     - 遵守 Save / Lost 和单写入恢复语义
   * - 新启动配置
     - internal/config、cmd/sdn-controller、docker/supervisor.py
     - Go 解释配置，Python 消费无令牌运行计划
   * - 新数据面
     - 相邻 sdn-agent 的 internal/adapters 和启动注册
     - Controller 根据统一能力规划，不能直接执行设备命令

``cmd/`` 是可执行入口，``api/`` 是跨项目公开协议，``pkg/transport`` 是公开传输辅助包，
``internal/`` 包含本模块私有实现，受 Go 导入规则保护。不要为了共享实现而让其他项目
引用 Controller 或 Agent 的 internal 包；稳定公共合同应放在 api 或明确设计的公共包。
当前 Agent 协议通过相邻源码 replace 引入，并未发布为独立协议模块。

加入一种配置资源
------------------------------------------------------------

以“给节点增加可独立管理的资源”为例，不必为已有 Operation 再创建一套命令执行器。

1. **写清目标及身份。** 确定节点 / VRF / 地址族作用域、自然键、默认值、字段 presence、
   CREATE / SET / DELETE 或 BIND / UNBIND 的语义，以及前置引用和可回滚条件。
   区分物化目标与操作历史；Desired 中保留当前对象，不追加删除命令作为目标。
2. **核对 Agent 合同。** 已有 typed Operation 时复用其公开 payload；新操作先在 Agent
   补齐校验、Undo、Apply、Verify、能力和测试。新字段不得复用旧 Protobuf 编号。
   只有 Controller 需要的业务抽象才加入 North 模型，普通构建使用提交的生成文件。
3. **实现物化与校验。** 在 ``internal/resources/resources.go`` 核对 payloads、Key、
   canonical / canonicalAction、allowed、merge、Validate、rank / Sort 和 Compile。
   处理同键重复、optional false / 0、地址规范化、关系引用和 BridgePolicy 等逻辑属性。
   客户端自选 operation.id 或 depends_on 不能取得其他资源的所有权。
4. **实现计划差异。** 在 ``internal/planner/planner.go`` 的 Compile / Build / delta /
   orderRemovals 定义稳定目标、允许原地改变的字段、需显式拆除重建的身份和反向引用。
   Build 与 Applied 比较，验证纳管、实例、revision、操作能力和 rollback 支持。
   计划必须可确定性重建；同目标不能因 map 遍历顺序不同产生不同 plan_id。
5. **接入候选链路。** ``EditNodeResources`` 更新候选，PlanChange 预览，CommitChange
   原子发布 Desired 与 Job；沿用 Worker 的持久意图和 Agent 事务。
   不在编辑候选或生成计划时调用设备写操作，不跳过 revision 或节点执行权检查。
6. **覆盖导入和恢复。** 扩展 Replay 历史折叠和 VerifyResources 所需支持。
   不可表达或无法验证的旧历史应明确拒绝导入，不能仅凭名称相同纳管人工配置。
   检查旧 Applied、在途 Job 和失败 Undo 在升级后的含义。
7. **接入使用入口。** 在 ``cmd/sdnc/resources.go`` 增加命名命令、参数与 protojson 编码测试；
   若需要浏览器入口，通知 sdn-web 同步 Gateway 和资源表单。
   更新 :doc:`/user/resources`、:doc:`resources`、能力边界及 README。

现有静态路由、OSPFv3、BGP 地址族和 EVPN Type-5 已通过 nodeResources 暴露，
可阅读它们的自然键与测试作为实例。旧 NodeProfile / Tenant / Network 业务模型的 IPv4
假设不能直接当作逐节点资源模型的限制，反过来也不能声称每个新能力已映射到旧模型。

新增 RPC 或查询
------------------------------------------------------------

* 在 controller.proto 明确请求、响应、权限、幂等、超时和错误码，按 :doc:`protocol` 重新生成代码。
* 在 ``internal/controller`` 实现 handler。持久修改走现有 update 路径：克隆、校验、容量检查、
  Save 成功后发布内存。新增查询不能把数据库内部可变对象暴露给调用方。
* 在 ``pkg/transport/transport.go`` 评估 authorized。viewer 查询使用显式白名单；
  admin 默认允许 North RPC，因此新增敏感方法时必须主动核对授权策略并测试。
* 为重复提交定义稳定 request_id、请求哈希与持久结果 ID；编辑冲突使用 revision。
  收据返回相同业务结果身份，不等于保存第一次 HTTP 响应字节。
* QueryNode 当前先检查节点 RUNNING、QueryKind / family / OSPF version 能力，再限时调用
  Agent.Query，并核对响应 node_id / kind。它不缓存查询结果；新增 QueryKind 需要 Agent 支持。
  分页、过滤、返回上限和数据来源应由公开协议说明，不能提供任意 shell 查询入口。
* 新的 North unary RPC 可通过 sdnc rpc 调用；专用命令仍需自己的参数和交互文档。
  Web HTTP 路由、角色行为和错误映射由相邻项目更新，不在 Controller 中加入 Web 状态。

接口测试至少验证 admin / viewer / agent 权限、无效字段、相同幂等键不同内容、版本冲突、
RPC 取消与不确定结果。若改变任务提交或恢复，加入重启和响应丢失用例，而非只测成功返回。

扩展存储与启动配置
------------------------------------------------------------

Repository Store 合同位于 ``internal/repository/repository.go``：

.. code-block:: go

   type Store interface {
       Load(context.Context) ([]byte, error)
       Save(context.Context, []byte) error
       Lost() <-chan struct{}
       Close() error
   }

实现新的 Store 需要保证单写所有权、原子发布、明确的持久成功边界、响应丢失后的核实，
以及无法安全继续时关闭 Lost。不能把内存修改成功当作持久保存成功。
接入点在 cmd/sdn-controller 与 internal/config；兼容性还包括启动时旧快照加载、
schema_version、容量预留和未终态任务。详细不变量见 :doc:`storage`。

若改成分对象数据库布局，需重新设计跨 Desired / Job / Receipt / Applied 的发布原子性，
提供版本迁移与故障恢复证据，不是简单替换 Save 内部写法。
目前没有公开的热插拔存储或数据面插件加载器。

新增配置项时同步 Go 默认值、严格 JSON 校验、明确 CLI 覆盖和 token 环境规则。
内置 etcd 的服务参数通过 -runtime-plan 交给监督进程，不在 Python 再实现一遍配置语义。
同时维护 docker/controller.conf 模板、启动预检、健康检查、停止预算和配置测试，
文档见 :doc:`/user/configuration-file`。

验证与交付检查
------------------------------------------------------------

.. list-table:: 修改对应的验证
   :header-rows: 1
   :widths: 34 66

   * - 修改位置
     - 重点测试
   * - internal/resources
     - 自然身份、重复编辑、缺失与显式零值、历史折叠、引用与属性冲突
   * - internal/planner
     - 新建 / 更新 / 删除、确定性、依赖顺序、Applied 差异、缺能力、节点屏障
   * - internal/controller
     - gRPC 合同、权限、候选与全局版本、导入二次校验、幂等、未知结果及重启
   * - internal/repository
     - 多写互斥、CAS 冲突、响应丢失、Lost、持久结果和再次加载
   * - cmd/sdnc / docker
     - 参数编码 / 拒绝、配置预检、无令牌诊断和监督生命周期

先执行相关包测试，再按 :doc:`testing` 运行所需整体验证。
公开协议变化后编译三个消费者：Agent、Controller、sdn-web/gateway。
``make test`` / ``make vet`` / ``make build`` 和严格 HTML 构建是基础检查，
模拟 Agent 成功不能代替 Linux / FRR 的实际读回、协议收敛与转发测试。
真实实验使用专属环境，保留版本、命令、结果、跳过项和限制，再按 :doc:`/user/release` 交付。
