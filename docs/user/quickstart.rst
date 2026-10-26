第一次 EVPN 实验
========================================

本教程使用 ``sdn-docker`` 隔离环境，要求服务已按 :doc:`deployment` 启动，
并处于未纳管、revision 为 0 的空白状态。当前环境若已有配置，请先查看状态，
不要直接把首次部署示例覆盖到其他目标配置上。

本教程包含两台测试 PC，在五个核心容器基础上先启用可选实验环境：

.. code-block:: bash

   python3 docker/manage.py lab-up

这会增加两台 PC 和 Leaf 接入口，容器总数为七个。

实验目标
----------------------------------------

.. code-block:: text

                     spine
                 swp1     swp2
                   |       |
                uplink   uplink
                 leaf1    leaf2
                 host10   host20
                   |       |
                 eth0     eth0
                  pc1      pc2

.. list-table:: 地址与租户规划
   :header-rows: 1
   :widths: 25 38 37

   * - 对象
     - Leaf1 一侧
     - Leaf2 一侧
   * - VTEP / BGP router-id
     - ``10.255.0.11``
     - ``10.255.0.12``
   * - Underlay
     - ``10.0.1.0/30``
     - ``10.0.2.0/30``
   * - L2VNI / 网关
     - ``10010`` / ``192.168.10.254/24``
     - ``10020`` / ``192.168.20.254/24``
   * - PC
     - ``192.168.10.11/24``
     - ``192.168.20.12/24``
   * - 租户
     - ``TENANT-A``，VRF table ``1001``
     - 同一租户
   * - L3VNI / L3 RT
     - ``50000`` / ``65000:1``
     - 同一 L3VNI 与 RT

Spine 是 ``10.255.0.1``，AS 为 ``65000``，承担 EVPN Route Reflector。
本实验使用 Type-2 / Type-3，不依赖静态远端 VXLAN/FDB 或 Type-5。

通过 Web 提交
----------------------------------------

1. 打开 ``http://<部署服务器IP>:28080``，将占位符替换为 Docker 部署主机的可达管理 IP，使用本环境的 admin 令牌登录。
2. 节点页面应出现三个 ``RUNNING`` 节点，逐个执行纳管。
3. 在配置页面创建候选，将 ``docker/state/evpn.json`` 的完整内容粘贴到编辑器。
4. 保存候选、预览计划，核对三台节点、VNI、接口名和操作顺序。
5. 提交后进入任务页面，等待 ``SUCCEEDED``。此时只代表配置事务完成。

如果刚执行过 ``reset``，必须使用重新生成的示例文件；旧文件中的 node UUID 已失效。
Web 的逐页操作和界面说明位于相邻项目 ``sdn-web/docs/html/user/index.html``。

通过 CLI 提交
----------------------------------------

这是 Web 流程的另一种入口，两种方式任选其一。下面在宿主机执行，要求已构建
``bin/sdnc``。将令牌占位符换为本环境 admin 值：

.. code-block:: bash

   cd ~/program/github/sdn-controller
   export SDN_ADMIN_TOKEN='<管理员访问令牌>'
   ./bin/sdnc -addr 127.0.0.1:29090 -insecure nodes

记下每个节点的 ``nodeId`` 与 ``agentRevision``，逐个执行：

.. code-block:: bash

   ./bin/sdnc -addr 127.0.0.1:29090 -insecure \
     -node '<节点 UUID>' -revision 0 adopt

然后预览和提交完整配置：

.. code-block:: bash

   ./bin/sdnc -addr 127.0.0.1:29090 -insecure \
     -file docker/state/evpn.json plan
   ./bin/sdnc -addr 127.0.0.1:29090 -insecure \
     -file docker/state/evpn.json -request-id first-evpn submit

``plan`` 会创建并保存一个候选和预览；``submit`` 会重新创建自己的候选与计划，
不是提交上一个 ``plan`` 的 plan ID。需要确认同一份计划再提交时，使用
:doc:`cli` 中的逐步 RPC 流程。``submit`` 默认等待任务结束，总超时默认五分钟。

验证数据面
----------------------------------------

在宿主机配置 PC 并触发 ARP/MAC 学习：

.. code-block:: bash

   python3 docker/manage.py hosts
   docker exec sdn-docker-pc1 ping -c 3 192.168.10.254
   docker exec sdn-docker-pc2 ping -c 3 192.168.20.254
   docker exec sdn-docker-spine vtysh -c 'show bgp l2vpn evpn'
   docker exec sdn-docker-pc1 ping -c 3 192.168.20.12
   docker exec sdn-docker-pc2 ping -c 3 192.168.10.11

预期看到两个 Leaf 的业务 L2VNI Type-3 路由，以及主机流量学习后的 MAC/IP Type-2。
确认 OSPF 和 BGP 会话已收敛后，两台 PC 跨网段互 ping 应成功。
Type-2 不出现时，先确认 PC、接入口和网关通信，再检查本地 FDB/邻居表。

刷新 Controller 观测：

.. code-block:: bash

   ./bin/sdnc -addr 127.0.0.1:29090 -insecure -refresh topology

返回数据带 ``sourceNode``、RD、NLRI 和 RT 等来源信息。
Type-2/Type-3 构成 Overlay 关系，不代表 Controller 已探测出物理网线。

结束实验
----------------------------------------

保留当前环境即可继续手动测试。只有确定要清空本项目实验时才执行：

.. code-block:: bash

   python3 docker/manage.py reset
   python3 docker/manage.py clean-check

``reset`` 会删除并重建本 Compose 项目的数据卷、节点身份和业务配置，
保留镜像、凭据与验收报告。其他 Docker 项目不在其管理范围。
它与 ``make -C docs clean`` 完全不同：后者仅清理生成的文档 HTML。

