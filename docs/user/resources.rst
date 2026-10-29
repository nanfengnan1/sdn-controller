逐节点资源配置与查询
========================================

现在可以单独创建租户、BD、VXLAN、协议实例，再逐项建立关系。
空租户不要求 L3VNI、Router MAC、BGP 或 RT。每项写操作仍经过
Controller 的 Candidate → Plan → Commit，不直接转发设备命令。

以下 ``ctl`` 是宿主机上的便利函数。把地址和 node-id 替换为部署环境的实际值；
node-id 从 ``nodes`` 输出获得。凭据读取 ``SDN_ADMIN_TOKEN``，只读查询也可使用
``SDN_VIEWER_TOKEN``。令牌不要写入配置 JSON 或提交到仓库。

.. code-block:: bash

   export SDNC_ADDR=127.0.0.1:29090
   ctl() { ./bin/sdnc -addr "$SDNC_ADDR" -insecure "$@"; }
   ctl nodes
   export LEAF1_NODE_ID='填入 leaf1 对应的真实 nodeId / UUID'
   ctl -node "$LEAF1_NODE_ID" -revision 0 adopt

全局参数（``-addr``、``-insecure``、``-node`` 等）放在子命令之前；资源参数
（``--table-id``、``--bridge``、``--family`` 等）放在资源动作之后。
布尔参数必须显式填写 ``true`` 或 ``false``。
以下业务示例假设 VTEP 地址、underlay 和引用的物理接口已存在。
若使用 Docker 实验的 ``host10`` 接入口，先运行 ``python3 docker/manage.py lab-up``
启用可选 PC 接线；默认五个核心容器只有 Spine/Leaf 链路。

创建空租户，逐步添加网络
----------------------------------------

.. code-block:: bash

   ctl -node "$LEAF1_NODE_ID" tenant create TENANT-A --table-id 1001
   ctl -node "$LEAF1_NODE_ID" interface set TENANT-A --up true
   ctl -node "$LEAF1_NODE_ID" system set net.ipv6.conf.all.disable_ipv6 --value 0
   ctl -node "$LEAF1_NODE_ID" system set net.ipv6.conf.default.disable_ipv6 --value 0
   ctl -node "$LEAF1_NODE_ID" system set net.ipv6.conf.all.forwarding --value 1
   ctl -node "$LEAF1_NODE_ID" bd create br10 --mtu 1500 --stp false --mac 02:00:00:00:00:10
   ctl -node "$LEAF1_NODE_ID" bd bind-tenant br10 --tenant TENANT-A
   ctl -node "$LEAF1_NODE_ID" interface set br10 --up true --addrgenmode none
   ctl -node "$LEAF1_NODE_ID" bd address-add br10 --prefix 192.168.10.254/24
   ctl -node "$LEAF1_NODE_ID" bd address-add br10 --prefix fe80::ff:fe00:10/64
   ctl -node "$LEAF1_NODE_ID" bd address-add br10 --prefix 2001:db8:10::fe/64
   ctl -node "$LEAF1_NODE_ID" bd port-add br10 --port host10
   ctl -node "$LEAF1_NODE_ID" vxlan create vx10010 --vni 10010 --local 10.255.0.11
   ctl -node "$LEAF1_NODE_ID" vxlan bind vx10010 --bridge br10 --learning false
   ctl -node "$LEAF1_NODE_ID" interface set vx10010 --up true --mtu 1500
   ctl -node "$LEAF1_NODE_ID" bd suppress br10 --enabled true

双栈业务 SVI 必须同时具备全局地址和可用的链路本地地址，才能在主机邻居缓存为空时
主动发送 NS。上述 LL 根据示例 MAC 生成；换用其他节点或同一二层链路时应确认地址唯一，
并通过地址查询确认 DAD 已完成。对已经 UP 且原来设置为 ``none`` 的 SVI，仅改
``addrgenmode eui64`` 不会立即补生成 LL，因此这里显式添加地址，避免重启接口。
只运行 IPv4 时可省略 IPv6 开关和两条 IPv6 地址命令；L3VNI 的无地址 SVI 不需要照搬这些地址。

``bd suppress`` 保存 BD 的 ARP/ND 抑制策略，应用到该 BD 中受管的 VXLAN 端口。
以后新加入的 VXLAN 端口也继承此策略；普通 host 端口不会被一起打开抑制。
没有隧道成员时也可先保存该策略。这是邻居抑制，不是主动 ARP/ND 保活。

只绑定 L3VNI
----------------------------------------

L3VNI 绑定引用已经存在的 VRF、BD 与 VXLAN，命令本身不隐式创建这些对象。
例如先为 TENANT-A 创建专用 L3 BD 和 VXLAN：

.. code-block:: bash

   ctl -node "$LEAF1_NODE_ID" bd create br50000 --mac 02:00:00:00:ff:11 --mtu 1500
   ctl -node "$LEAF1_NODE_ID" bd bind-tenant br50000 --tenant TENANT-A
   ctl -node "$LEAF1_NODE_ID" interface set br50000 --up true --addrgenmode none
   ctl -node "$LEAF1_NODE_ID" vxlan create vx50000 --vni 50000 --local 10.255.0.11
   ctl -node "$LEAF1_NODE_ID" vxlan bind vx50000 --bridge br50000 --learning false
   ctl -node "$LEAF1_NODE_ID" interface set vx50000 --up true --mtu 1500
   ctl -node "$LEAF1_NODE_ID" tenant bind-vni TENANT-A --vni 50000

L3 BD 通常不配业务 IP。L3VNI 与 VXLAN 报文封装的具体关联由 Agent 的 Linux/FRR
适配器配置并读回。删除时先删除协议引用及显式关系，再删除对象，例如：

.. code-block:: bash

   ctl -node "$LEAF1_NODE_ID" tenant unbind-vni TENANT-A
   ctl -node "$LEAF1_NODE_ID" vxlan unbind vx50000
   ctl -node "$LEAF1_NODE_ID" vxlan delete vx50000
   ctl -node "$LEAF1_NODE_ID" bd unbind-tenant br50000
   ctl -node "$LEAF1_NODE_ID" bd delete br50000

删除 VRF/BD/VXLAN 会同时清理同名的受管 ``INTERFACE`` 属性，
但不会隐式删除地址、端口成员、协议配置或业务关系；仍有这些引用会拒绝操作。
``tenant delete TENANT-A`` 只删除租户本身，必须先解除它的引用。
BD 和同名接口同时暴露的 MTU/MAC 是同一链路属性，增量编辑会同步两种目标表示，
后续只修改接口 up/down 不会把 MTU 回退。

一组操作使用一个候选
----------------------------------------

默认每个资源命令独立提交并等待 Job。需要本节点多个操作一起回滚时，使用同一候选：

.. code-block:: bash

   ctl change begin
   # 将响应中的 id 保存为 CHANGE_ID
   ctl -node "$LEAF1_NODE_ID" tenant create TENANT-B --table-id 1002 --change "$CHANGE_ID"
   ctl -node "$LEAF1_NODE_ID" bd create br20 --change "$CHANGE_ID"
   ctl -node "$LEAF1_NODE_ID" bd bind-tenant br20 --tenant TENANT-B --change "$CHANGE_ID"
   ctl change plan "$CHANGE_ID"
   ctl change commit "$CHANGE_ID"

带 ``--change`` 的资源写命令只更新候选，不执行设备配置。候选可逐步补齐依赖，
Plan 和 Commit 校验完整目标。控制器按依赖生成顺序；Agent 执行本节点事务并在失败时回滚。
跨节点仍是既有 STOP/PARTIAL 语义，不承诺分布式原子提交。
Controller Desired 在受理提交时保存目标；Agent 失败后回滚本地配置，
不会自动回退该 Desired。应查看 Job 与运行查询，再显式提交修正目标。
提交超时不代表取消，用 Job ID 查询结果；可以用固定 ``-request-id`` 重试便捷提交。

BGP 与 OSPF
----------------------------------------

.. code-block:: bash

   ctl -node "$LEAF1_NODE_ID" bgp instance create --asn 65000 --router-id 10.255.0.11 --default-ipv4-unicast false
   ctl -node "$LEAF1_NODE_ID" bgp neighbor create 10.255.0.1 --asn 65000 --remote-as 65000 --update-source lo
   ctl -node "$LEAF1_NODE_ID" bgp evpn-peer set 10.255.0.1 --asn 65000 --active true
   ctl -node "$LEAF1_NODE_ID" bgp evpn set --asn 65000 --advertise-all-vni true --advertise-svi-ip true
   ctl -node "$LEAF1_NODE_ID" bgp l2vni set --asn 65000 --vni 10010 --rd 10.255.0.11:10 --import-rts 65000:10010 --export-rts 65000:10010
   ctl -node "$LEAF1_NODE_ID" bgp instance create --asn 65000 --vrf TENANT-A --router-id 10.255.0.11 --default-ipv4-unicast false
   ctl -node "$LEAF1_NODE_ID" bgp tenant set --asn 65000 --vrf TENANT-A --rd 10.255.0.11:50000 --import-rts 65000:1 --export-rts 65000:1
   ctl -node "$LEAF1_NODE_ID" bgp type5 set --asn 65000 --vrf TENANT-A --family ipv4 --enabled true
   ctl -node "$LEAF1_NODE_ID" bgp type5 set --asn 65000 --vrf TENANT-A --family ipv6 --enabled true
   ctl -node "$LEAF1_NODE_ID" bgp redistribute create connected --asn 65000 --vrf TENANT-A --family ipv6

   ctl -node "$LEAF1_NODE_ID" ospf instance create --router-id 10.255.0.11 --version 2
   ctl -node "$LEAF1_NODE_ID" ospf interface set uplink --version 2 --network-type point-to-point
   ctl -node "$LEAF1_NODE_ID" ospf network create 10.0.1.0/30 --area 0 --version 2
   ctl -node "$LEAF1_NODE_ID" ospf instance create --router-id 10.255.0.11 --version 3
   ctl -node "$LEAF1_NODE_ID" ospf interface set uplink --version 3 --area 0 --network-type point-to-point

OSPFv3 通过接口 area 启用，IPv6 内核配置和链路本地地址必须满足 FRR 要求。
这些命令表达配置，不自动保证邻接建立。IPv4/IPv6 BGP 地址族使用
``bgp af-peer set NEIGHBOR --asn ASN --family ipv4|ipv6 --active true``；
网络发布使用 ``bgp network create PREFIX --asn ASN --vrf VRF --family ipv4|ipv6``。
协议对象的 ``delete`` 使用相同身份字段；例如删除地址族网络需要相同 VRF、family 和 prefix。

结构化实际状态查询
----------------------------------------

.. code-block:: bash

   ctl -node "$LEAF1_NODE_ID" resources show
   ctl -node "$LEAF1_NODE_ID" tenant show TENANT-A
   ctl -node "$LEAF1_NODE_ID" bd show br10
   ctl -node "$LEAF1_NODE_ID" bd ports br10
   ctl -node "$LEAF1_NODE_ID" bd fdb br10
   ctl -node "$LEAF1_NODE_ID" bd neighbors br10 --family ipv6
   ctl -node "$LEAF1_NODE_ID" interface show uplink
   ctl -node "$LEAF1_NODE_ID" interface addresses br10
   ctl -node "$LEAF1_NODE_ID" route show --vrf TENANT-A --family ipv6
   ctl -node "$LEAF1_NODE_ID" vxlan show vx10010
   ctl -node "$LEAF1_NODE_ID" bgp summary
   ctl -node "$LEAF1_NODE_ID" bgp routes --vrf TENANT-A --family ipv6
   ctl -node "$LEAF1_NODE_ID" bgp evpn-routes --route-type 2
   ctl -node "$LEAF1_NODE_ID" bgp evpn-routes --route-type 5
   ctl -node "$LEAF1_NODE_ID" bgp vni
   ctl -node "$LEAF1_NODE_ID" ospf neighbors --version 3

``resources show`` 返回持久化 Desired；其余 ``show`` / ``fdb`` / ``routes`` 等查询
由 QueryNode 调用 Agent 结构化查询，包含来源、采集时间、revision、stale 和 error。
Agent 未声明的查询/family/version 返回 Unimplemented，不把“不支持”显示成空表。
EVPN 路由查询使用全局 RIB，不能传 VRF 当成直接过滤器。
分页使用 ``--page-size`` 和响应的 ``nextPageToken``；数据变化会使旧 token 失效，需重新查询。

已有 Agent 配置与旧完整配置
----------------------------------------

``adopt --allow-existing`` 只确认身份和 revision，不能接管已经运行的对象。
已有 Agent 成功事务日志中的对象，必须显式验证导入：

.. code-block:: bash

   ctl -node "$LEAF1_NODE_ID" resources import --revision 3 --request-id import-leaf1-001
   ctl -node "$LEAF1_NODE_ID" resources show

导入会折叠 Running 操作日志中的 create/set/delete/unbind，调用 Agent
VerifyResources 逐对象核验真实 Linux/FRR 状态，再原子保存目标与 Applied 基线；
不执行创建/删除命令。无法验证、未知操作、漂移或未恢复的任务会拒绝导入。
未出现在 Agent 日志中的人工对象不会被接管或删除。

旧 ``nodes/tenants/networks`` 完整 JSON 仍受支持，租户的 ``l3Vni=0`` 可表示独立 VRF。
同一节点不能同时由旧配置和 ``nodeResources`` 管理。旧节点也通过上述导入显式转换，
其余节点的旧配置保留。已经受管的资源节点禁止再次导入来掩盖漂移。

资源动作速查
----------------------------------------

.. list-table:: 写操作
   :header-rows: 1
   :widths: 18 42 40

   * - 对象
     - 动作
     - 主要参数
   * - tenant
     - create / delete / bind-vni / unbind-vni
     - name；创建用 table-id；绑定用 vni
   * - bd
     - create / set / delete
     - name、mtu、mac、stp、ageing-time
   * - bd
     - bind-tenant / unbind-tenant
     - BD name、tenant
   * - bd
     - port-add / port-set / port-delete
     - BD name、port、learning、neigh-suppress
   * - bd / interface
     - address-add / address-delete
     - interface name、prefix（IPv4/IPv6）
   * - bd
     - suppress
     - BD name、enabled
   * - vxlan
     - create / delete / bind / unbind
     - name、vni、local、port、underlay；绑定用 bridge
   * - interface
     - set
     - name、up、mtu、mac、addrgenmode
   * - bgp
     - instance / neighbor / peer-group / af-peer / evpn-peer / evpn / l2vni / tenant / network / redistribute / type5
     - create、set 或 delete；ASN、VRF 与对应数据
   * - ospf
     - instance / network / interface
     - create、set 或 delete；version=2/3、VRF、area 等


VPP 邻居保活
------------

VPP Agent 声明 neighbor-keepalive/v1 能力时，可设置节点级
NEIGHBOR_KEEPALIVE / SET，数据为 neighborKeepalive.enabled（true / false）。
默认关闭；Linux Agent 不支持。不使用 DELETE 关闭。

在 Web“二层网络 BD”选择“邻居保活（节点开关）”，加入候选、预览并提交。
使用已有导入/候选流程，不能绕过节点能力或 revision 检查。
NEIGHBOR_KEEPALIVE_STATUS 查询来自 VPP，支持 IPv4/IPv6，显示策略和每 IP
探测状态、尝试次数、确认时间、发送错误与容量计数。

保活只针对已学习到的本地动态 MAC/IP：单播 ARP Request / IPv6 NS
由 VPP 发送，有效学习续期，三次失败撤销对应 IP；同 MAC 的其他 IP 保留。
不是 ARP/ND 抑制，也不是向远端周期性发送 Type-2。
启用需要配套新版 VPP/Agent 镜像及 neighbor_keepalive_plugin.so；
MAC ageing 为 0 或 >=120 秒，默认 300 秒满足要求。Linux 节点不显示此开关。

VXLAN 外层 VRF：创建与在线修改
--------------------------------

VPP 节点支持为 VXLAN 设置外层路由表，并在创建后修改。
外层 VRF 用于到远端 VTEP 的封装路由及接收隧道匹配；内层租户 VRF 仍由
BD/BVI 与 TENANT 的关系决定。修改外层表不改变 VNI、原生接口编号或 BD 绑定。

使用前先准备同地址族的 VPP 路由表、Linux VRF 控制映射以及到远端 VTEP 的
底层路由。通过 Controller 管理时，非零外层表必须先以 TENANT 资源创建；
这个承载 VRF 可以没有业务 BD 和 L3VNI。表 ID 0 表示 VPP 默认表，不能把
Linux main 的数字 254 填进此字段。253、254、255 和 4294967295 被拒绝。

Web 中选择支持 ``vxlan-encap-vrf/v1`` 的节点，进入“隧道 / VXLAN”：

1. 创建 VXLAN 时可以填写“外层 VRF 表 ID”。
2. 对已有隧道选择“修改 VXLAN 外层 VRF”，填写名称及目标表 ID。
3. 加入候选、预览计划后提交；回到默认表时明确填写 0。
4. 运行查询的“外层 VRF 表 ID”显示 VPP 实际值。

Agent / Controller 资源操作示例：

.. code-block:: json

   {
     "code": "VXLAN",
     "action": "SET",
     "vxlan": {"name": "vx50000", "encapVrfId": 100}
   }

CREATE 使用相同的可选字段，同时提供 local、vni 等创建参数。
SET 只修改 encapVrfId 和可选的 underlay；不接受 local、vni、port 或 learning
的身份修改。已有 underlay 留空表示保留，不表示清除。显式指定的 underlay
必须是 LCP 接口，而且 Linux 与 VPP 两侧均已属于目标外层表。
省略 underlay 时，非零表使用对应 Linux VRF 作为控制镜像的底层设备；
VXLAN 的 bridge master 仍然是原有租户 BD。

此操作不会替用户迁移物理网卡、调整 BGP 管理连接或创建远端 VTEP 路由。
先完成承载网络准备，再修改隧道；切换可能短暂中断通信。要删除旧承载 VRF，
先单独提交所有隧道的切换，再删除 VRF；仍被引用的表不能删除。

需要同时更新 VPP 插件/API/适配器、Agent、Controller 和 Web。
旧节点或 Linux 数据面不显示修改入口；Linux 后端拒绝非零 encapVrfId。
设备配置由适配器持久化，重新加载时仍需先恢复启动配置管理的 Underlay/LCP。

FRR 下一跳解析也需要承载可达性
----------------------------------------

当 BGP EVPN 实例仍在 default VRF，而数据 Underlay 已迁移到 OUTER100，
仅在 VPP 表 100 中存在路由还不够：默认 BGP 实例也必须解析远端 VTEP，
否则 EVPN 路由可能已接收但无 valid/best 标记，进而撤销租户路由和远端 MAC。

可按部署设计向控制面 VRF 泄漏真实承载路由。例如远端 VTEP
192.0.2.2 已经可以通过 OUTER100 中的 uplink 直连访问时，在 FRR 配置：

.. code-block:: text

   ip route 192.0.2.2/32 uplink nexthop-vrf OUTER100

IPv6 对应使用 ipv6 route <VTEP>/128 uplink nexthop-vrf OUTER100。
这是一条经真实 Underlay 出口解析的路由，不能用黑洞或伪造直连路由来让
EVPN 强行选路。使用其他 IGP/BGP 泄漏方案时同样验证 show ip nht / show ipv6 nht。
接口移动、该路由泄漏及其启动恢复由承载网络配置负责，
VXLAN SET 不自动配置它们。当前 Agent 的 STATIC_ROUTE 资源没有 nexthop-vrf 字段；
可在部署时的 FRR 配置文件中维护此项。


Snort IPS
----------------------------

规则、接口绑定、基础文件与恢复说明见 :doc:`ips`。
