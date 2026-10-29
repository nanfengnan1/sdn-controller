Snort IPS 配置与查询
===============================

使用前提
--------

Agent 使用 VPP 集成镜像，开启 ``ips.enabled`` 和内置 Redis，
启动 Snort 后声明 ``snort-ips/v1`` 能力。Controller、Agent 和 Web 应配套升级。
Linux 数据面与旧 Agent 不提供此能力。

Controller 负责候选、版本检查、规划、任务与补偿；
规则生成和 Snort 重载由 Agent 完成。Controller 不连接节点 Redis。

从 Web 操作
-----------

1. 确认节点 RUNNING 且已纳管，进入 **IPS 配置**。
2. 首次使用独立资源时验证并导入节点基线。
3. 新建候选，选择创建规则，填写 SID 和完整单行 Snort 规则。
4. 添加检查接口，使用 VPP 原生三层接口或 BVI 名称，例如 bvi1。
5. 预览、提交，等待任务 SUCCEEDED。
6. 查询 IPS_RULES 与 IPS_STATUS，核对实际加载和接口绑定。

纯 BD 二层转发不经过此检查路径。绑定/解绑需要短暂切换接口 Up/Down；
规则修改采用热重载，不切换接口。

资源与更新
----------

.. list-table::
   :header-rows: 1
   :widths: 23 25 52

   * - 资源
     - 标识
     - 字段
   * - IPS_RULE
     - SID
     - ipsRule.sid、ipsRule.rule
   * - IPS_INTERFACE
     - VPP 原生接口名
     - ipsInterface.name、direction（input/output/inout）

两类资源支持 CREATE/SET/DELETE。删除规则只需要 SID；删除绑定只需要 name。
已有规则修改使用 SET，SID 必须与规则文本一致。以下是一条候选操作，
应通过既有资源编辑和提交接口使用：

.. code-block:: json

   {
     "code": "IPS_RULE",
     "action": "CREATE",
     "ipsRule": {
       "sid": 1000001,
       "rule": "alert icmp any any -> any any (msg:\"ICMP audit\"; sid:1000001; rev:1;)"
     }
   }

alert 只记录，drop 在 inline 路径阻断。SID 对节点内托管规则唯一；
本版本一个节点使用一个 Snort 实例，规则作用于该节点全部 IPS 绑定方向。

状态与错误
----------

IPS_RULES 是 Agent 实际加载的托管规则；Controller 候选或 Desired
不等于设备已生效。IPS_STATUS 显示进程、配置代次、绑定、基础文件路径、
待加载状态和最近错误。Viewer 可以查询，但不能编辑或提交。

错误规则被拒绝并执行恢复；任务页检查失败原因和补偿状态。
旧配置恢复成功不表示原修改成功。补偿失败按已有任务恢复流程处理。

Snort 基础 Lua 由宿主机只读挂载，受控重载命令为
``docker exec <Agent容器> sdn-snort-reload``。它不属于 Controller 候选；
Web 规则修改不会覆盖基础参数。挂载与构建方法见配套 sdn-agent 的
``docs/user/ips.rst``。
