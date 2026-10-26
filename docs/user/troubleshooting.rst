故障排查
========================================

先保存原 request ID、Change ID、Job ID、node ID 和完整错误，再决定是否重试。
不要通过清空数据库、改任务状态或重复纳管来消除报错。

.. list-table:: 常见现象
   :header-rows: 1
   :widths: 26 34 40

   * - 现象
     - 可能原因
     - 下一步
   * - Web 令牌无效 / ``Unauthenticated``
     - 令牌错误、环境混用、未设置客户端凭据
     - 检查本环境 tokens.json 的 admin/viewer；Agent 令牌不能登录 North
   * - ``PermissionDenied``
     - 使用 viewer 或 agent 令牌调用写接口
     - 核实调用角色；CLI 优先使用 SDN_ADMIN_TOKEN
   * - 节点不出现
     - Agent 未注册、Controller 地址错误或凭据不匹配
     - 检查 Agent 日志、SDN_TOKEN 与 SDN_AGENT_TOKEN、管理网络
   * - 节点 DOWN / 无法纳管
     - 心跳超时，或 Controller 无法回连 endpoint
     - 区分注册方向和 Controller 到 Agent 的反向 RPC 连通性
   * - ``desired revision conflict``
     - 其他提交已推进全局版本
     - 获取最新配置，重新合并目标并创建候选
   * - ``candidate edit revision conflict``
     - 同一候选已被另一次编辑更新
     - GetChange，基于最新候选版本重做修改
   * - ``plan changed``
     - 候选、节点基线或能力等改变
     - 重新预览并核对计划后提交
   * - ``agent revision drift``
     - Agent 基线与 Controller Applied 不一致
     - 查询 Agent 任务及实际配置，排查外部写入或日志丢失
   * - ``reserved by an active job``
     - 原 Job 未完成或节点回滚失败
     - 查询旧 Job；修复原问题，或只修改无关节点
   * - ``AlreadyExists``
     - 同 request ID 被用于不同请求内容
     - 重试恢复时使用原文件；新业务请求使用新 ID
   * - Job UNKNOWN
     - Commit 可能已受理但响应丢失
     - 保留任务与请求 ID，恢复连接，继续查询；见 :doc:`recovery`
   * - ``ResourceExhausted``
     - 数据库容量、节点上限或重复拓扑刷新
     - 按错误文本区分容量限制与刷新互斥，不进行无界重试
   * - ``writer lease`` 或存储结果不确定
     - 已有写入者、etcd 断连或不确定提交
     - 确认旧进程与存储状态，从持久状态重启

任务成功但 ping 失败
----------------------------------------

按以下顺序检查，避免直接把应用事务失败和网络收敛混为一谈：

1. PC 地址、默认路由和网关 ping；本实验需要显式执行 ``docker/manage.py hosts``。
2. 接口 UP、MTU 和 veth 两端是否存在；容器重建后 veth 不一定还在。
3. Underlay OSPF 邻居和 VTEP loopback 路由。
4. BGP EVPN 邻居和 Type-3；FRR 是否将 50000 正确识别为租户 L3VNI。
5. 主机发流量后检查 FDB、ARP 和 Type-2，再看远端 VRF 路由与邻居。

在宿主机只读查询示例：

.. code-block:: bash

   docker exec sdn-docker-leaf1 ip -d link show
   docker exec sdn-docker-leaf1 ip route show table 1001
   docker exec sdn-docker-leaf1 bridge fdb show
   docker exec sdn-docker-leaf1 ip neigh show
   docker exec sdn-docker-leaf1 vtysh -c 'show ip ospf neighbor'
   docker exec sdn-docker-leaf1 vtysh -c 'show bgp l2vpn evpn summary'
   docker exec sdn-docker-leaf1 vtysh -c 'show evpn vni'
   docker exec sdn-docker-spine vtysh -c 'show bgp l2vpn evpn'

拓扑显示旧信息
----------------------------------------

查看 ``collectedUnixMs``、``stale``、``error`` 和 ``warnings``，再显式刷新。
Controller 重启后缓存为空，首次查询只显示配置关系是正常的。
FRR 输出格式不受支持时保留原始观测并提示解析失败；不能把空解析结果认定为路由已撤销。
Type-2/Type-3 也不能直接推断 Spine 与 Leaf 的物理接线。

可保留的排查材料
----------------------------------------

记录 CLI 的 Job 详情、相关 Agent Task、拓扑刷新结果和对应时间的容器日志。
不要在共享报告中包含 Bearer token、tokens.json 或环境文件内容。
现有实验验收证据位于 ``artifacts/docker/``，历史可靠性证据位于
``artifacts/reliability/``；运行新测试前确认其是否会修改当前业务配置。

