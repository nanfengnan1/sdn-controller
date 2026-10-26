资源编译与节点执行计划
========================================

``internal/planner`` 是纯资源编译与校验层，不执行 Linux 命令，也不调用 vtysh。
Controller 将业务目标编译为统一 Agent Operation；具体后端实现属于 sdn-agent。

Compile 与 Build
----------------------------------------

.. code-block:: text

   Configuration(nodes, tenants, networks, node_resources)
                 |
        Compile：作用域和引用校验
                 |
        每节点完整 target_operations
                 |
        Build：检查节点执行权与 Adopt/Revision/能力
                 |
        delta(Applied.operations, target_operations)
                 |
        创建/修改 + 按引用排序的拆除 + depends_on
                 |
        NodePlan：expected_agent_revision + operations + target_operations
                 |
        Plan：Spine优先、节点ID确定顺序、确定性内容哈希

Compile 校验 fabric 内 VNI、Router ID/VTEP 地址、接口名和所有权、
节点路由表、RD、RT、租户网络引用、端口归属和网段冲突。
Build 用 Applied 比较差异，而不是只比较上一个 Desired；因此部分失败后可以只重试尚未成功的目标。
逐节点对象的自然键、部分编辑、删除依赖、BD 抑制策略及现有配置转换见 :doc:`resources`。

目标如何分到后端
----------------------------------------

.. list-table:: 资源与 Agent 操作
   :header-rows: 1
   :widths: 27 45 28

   * - 资源意图
     - 生成的主要 op
     - 实际后端职责
   * - 系统、接口、地址
     - NODE_NETWORK、INTERFACE、ADDRESS
     - Linux sysctl / ip
   * - 独立静态路由
     - STATIC_ROUTE
     - Linux 路由表；nodeResources 支持 IPv4 / IPv6
   * - 租户与 Bridge
     - TENANT、BD、BD_TENANT、BD_PORT
     - Linux VRF/Bridge；TENANT 也涉及 FRR 对应对象
   * - VXLAN
     - VXLAN、BD_PORT、INTERFACE
     - Linux VXLAN；旧业务模型关闭学习并开启邻居抑制，nodeResources 按字段及 BD 策略配置
   * - 租户 L3VNI
     - TENANT_L3VNI
     - FRR VRF 与 L3VNI 绑定
   * - OSPFv2 / OSPFv3
     - OSPF_INSTANCE、OSPF_INTERFACE、OSPF_NETWORK、OSPF6_INSTANCE、OSPF6_INTERFACE
     - FRR ospfd / ospf6d；旧业务模型的 Underlay 使用 OSPFv2
   * - BGP 与 RR
     - BGP_INSTANCE、BGP_PEER_GROUP、BGP_NEIGHBOR、BGP_EVPN_PEER
     - FRR BGP/EVPN AF、RR client
   * - BGP IPv4 / IPv6 地址族
     - BGP_AF_PEER、BGP_NETWORK、BGP_REDISTRIBUTE
     - FRR 地址族邻居、network 与协议重分发
   * - EVPN 业务
     - BGP_EVPN_GLOBAL、EVPN_L2VNI、EVPN_TENANT、EVPN_TYPE5
     - FRR 通告开关、RD/RT 与 Type-5 前缀通告

上表同时覆盖旧 NodeProfile / Tenant / Network 模型与逐节点 nodeResources。
后者已经暴露静态路由、OSPFv3、BGP IPv4/IPv6 和 Type-5；旧业务模型仍有 IPv4 假设，
不是所有操作都由两种入口生成。新增 Agent 枚举不会自动成为 Controller 可管理资源，
仍需补充模型、自然键、校验、差异和测试。

创建与拆除顺序
----------------------------------------

Compiler 为目标操作赋稳定的资源 ID，创建关系覆盖 VRF、Bridge、绑定、VXLAN、地址和协议。
当前按明确顺序生成依赖，可保守串行执行；不进行后端并行性能调度。
差异按稳定资源 ID 查找旧值，允许的修改生成新的 typed op。

删除顺序根据引用关系单独计算，不能只反转所有创建 op：

.. code-block:: text

   撤销 EVPN 配置引用
         |
   解绑租户 L3VNI
         |
   删除租户 BGP 实例
         |
   解绑端口 / 撤销网关地址 / 解绑 Bridge 与租户
         |
   删除 VXLAN / Bridge
         |
   删除 VRF

具体拆除顺序以 ``orderRemovals`` 生成的 DAG 为准；共享对象和未删除资源必须继续存在。
已修复的关键约束是 EVPN_TENANT 和 TENANT_L3VNI 撤销先于租户 BGP 删除。

身份类字段变更、端口移动和节点 profile 移除有明确拒绝分支。
不要为了让 diff 通过而自动拆除所有资源再重建，否则会改变任务和中断语义。

节点执行权
----------------------------------------

``reservations.go`` 从活动 Job 和失败回滚记录计算被占用节点。
保留节点的新目标必须等于原 Plan 的 target_operations；相同则从新 Plan 排除并发出 warning，
不同则拒绝。终态 ROLLBACK_FAILED 仅保留有问题的节点。

对未被占用节点检查纳管、当前实例确认、Agent revision 与 Applied 一致，
并验证每个所需 op 在能力列表声明 rollback 支持。空差异不加入 NodePlan。

扩展一种能力
----------------------------------------

完整源码映射、RPC / 查询 / 存储扩展与验证步骤见 :doc:`extensions`。

1. 在公共协议定义资源字段与语义，区分未设置和明确的零值/false。
2. 完成 Agent typed op、相应后端 Validate/PrepareUndo/Apply/Verify/Undo 与能力声明。
3. 在 Compile 增加资源校验和稳定操作 ID，定义节点内名称与 VNI 等作用域。
4. 在 delta 中定义允许原地更新、显式重建或拒绝的情况，并加入拆除引用。
5. 测试创建、更新、删除、重复输入、引用冲突、缺能力、重启和失败恢复。
6. 补充用户配置说明与 API 文档；需要 Web 编辑能力时更新独立 Gateway/UI。

VPP 和交换机后端应首先适配 Agent 的统一操作，Controller 根据能力规划。
不能在 Planner 中硬编码某个设备 shell 命令作为兼容分支。

