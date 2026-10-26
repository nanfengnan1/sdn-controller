配置模型与修改方法
========================================

Controller 接收完整 ``Configuration``，包含旧声明式模型的 ``nodes``、``tenants``、
``networks``，以及按节点管理独立资源的 ``nodeResources``。
编辑候选时是整体替换，省略已有资源可能表示删除。修改前先读取当前配置，
在完整副本上修改并保留其他节点的 nodeResources，再预览删除和创建清单。
同一节点不能同时使用两种模型；资源增量编辑和验证导入见 :doc:`resources`。

三类数据
----------------------------------------

.. list-table:: 数据含义
   :header-rows: 1
   :widths: 18 45 37

   * - 数据
     - 保存什么
     - 何时变化
   * - Desired
     - 全网已提交的目标资源
     - ``CommitChange`` 持久化成功
   * - Applied
     - 每节点最后成功的受管 op 基线与 Agent revision
     - 该节点 Task 明确成功
   * - Observed
     - Agent 回读的实际 Linux/FRR 状态
     - 显式拓扑刷新采集

Desired revision、Agent revision、etcd revision 分别表示业务目标版本、
本地成功配置版本、存储版本，不能直接比较数值相等。
Applied 不是持续探测出的实际状态；人工修改 Linux/FRR 不一定导致 Agent revision 变化。
Desired 保存已经提交的意图。Agent 执行失败并成功回滚，不会自动把 Controller 的
Desired 退回旧版本；应结合 Job、Applied 和运行查询判断，再显式提交修正目标。

节点配置 NodeProfile
----------------------------------------

.. list-table:: 主要字段
   :header-rows: 1
   :widths: 30 70

   * - 字段
     - 说明
   * - ``nodeId``
     - 已注册并纳管的 UUID，不能使用 ``leaf1`` 等示例别名
   * - ``name`` / ``role`` / ``fabricId``
     - 展示名称、``leaf`` 或 ``spine``、网络域标识
   * - ``routerId`` / ``asn``
     - IPv4 Router ID 与 BGP ASN
   * - ``vtepIp``
     - Leaf 的 IPv4 VXLAN 本地地址
   * - ``configureSystem``
     - 应用当前 IPv4 实验系统选项，包括关闭 IPv6
   * - ``interfaces``
     - 接口名、MTU、MAC、地址、是否启用 OSPF、是否 passive
   * - ``ospf`` / ``peers``
     - 启用 OSPF；BGP 对端、remoteAs、updateSource 和 rrClient

接口必须已经存在，租户 Bridge/VXLAN 接口除外，它们由对应资源生成。
``interfaces[].addresses`` 和 ``gateways`` 使用带掩码的前缀。
接入口必须出现在本节点 ``interfaces`` 中，且不能同时作为有路由地址的接口。

租户 Tenant
----------------------------------------

``id`` 是 Controller 资源标识，``name`` 是节点上的 VRF 名称。
``fabricId``、``l3vni``、``importRts``、``exportRts`` 描述租户 Overlay。
``placements`` 指定部署到哪些 Leaf，每个位置包含：

* ``nodeId`` 与本地 ``tableId``；路由表号在节点内不得冲突。
* L3-SVI ``bridge``、VXLAN ``vxlan`` 和 ``routerMac``。
* 此节点 EVPN 实例的 ``rd``。

本实验 L3-SVI 不配置 IP；网关地址配置在业务网络的 Bridge 上。
租户 L3 RT 必须显式填写，同一租户的不同节点通常共享导入/导出 RT。

二层网络 Network
----------------------------------------

``id`` 标识网络，``tenantId`` 引用租户，``l2vni`` 标识二层广播域。
``placements`` 指定每个节点的 ``bridge``、``vxlan``、网关 ``mac``、
``gateways``、接入 ``ports`` 和 EVPN ``rd``。
网络只能部署到已经有对应租户 placement 的节点。

以下是完整配置中的租户和一个网络片段，必须与相应的完整 ``nodes`` 合并后提交：

.. code-block:: json

   {
     "tenants": [{
       "id": "tenant-a", "name": "TENANT-A", "fabricId": "lab",
       "l3vni": 50000, "importRts": ["65000:1"], "exportRts": ["65000:1"],
       "placements": [{
         "nodeId": "<leaf1 UUID>", "tableId": 1001,
         "routerMac": "02:00:00:00:ff:11", "bridge": "br50000",
         "vxlan": "vx50000", "rd": "10.255.0.11:50000"
       }]
     }],
     "networks": [{
       "id": "net10", "tenantId": "tenant-a", "l2vni": 10010,
       "importRts": ["65000:10010"], "exportRts": ["65000:10010"],
       "placements": [{
         "nodeId": "<leaf1 UUID>", "bridge": "br10", "vxlan": "vx10010",
         "mac": "02:00:00:00:00:10", "gateways": ["192.168.10.254/24"],
         "ports": ["host10"], "rd": "10.255.0.11:10"
       }]
     }]
   }

完整示例位于 ``examples/evpn.json``；使用 ``examples/render_lab.py`` 将示例节点别名
替换为注册 UUID。Docker 管理脚本已经生成可用于本轮环境的 ``docker/state/evpn.json``。

校验与作用域
----------------------------------------

* VNI 范围为 1–16777215，在同一 fabric 内按资源唯一；同一资源的多个 placement 共用 VNI。
* RD 区分 EVPN 实例；RT 决定导入/导出关系，不能把 RT 一律当作 VNI。
* VNI 大于 65535 时要求显式合法 RD；四字节 ASN 等组合也可能要求显式 RT。
* Linux 接口名最多 15 字符，允许的字符由 Planner 校验；Bridge、VXLAN、VRF 名称不能占用同节点其他资源名称。
* 端口只属于一个普通 Bridge；同一租户不同网络不能配置重叠网段。
* 当前配置条数上限为 64 节点、128 租户、256 网络，实际还受存储容量约束。

如何增加和删除
----------------------------------------

增加租户：保留原完整配置，增加 Tenant 和 placement；若需要接入业务，再增加 Network。
增加接入口：在对应 NodeProfile 保留/增加接口描述，在 Network placement 的 ``ports`` 加入接口。

删除网络：从完整配置移除该 Network，预览撤销网关、端口绑定、EVPN 和 VXLAN/Bridge 操作。
删除租户：先移除或同时移除引用它的网络，再移除 Tenant；保留节点 profile。
Planner 会生成引用约束下的拆除顺序，不需要用户逐条编写 Linux/FRR 命令。

目前明确拒绝的原地修改包括 VXLAN/VRF/L3VNI 身份、受管 ASN/router-id 等。
移动接入口需要分开提交 detach 和 attach。受管节点 profile 整体移除尚不支持。
移除物理接口描述会撤销受管地址/协议引用，但保留接口当前 UP/MTU/MAC 状态。

遇到被任务占用的节点，必须保持该节点的期望操作不变；只修改无关节点的完整配置仍可预览和提交。
详见 :doc:`jobs-topology`。

