CLI 与逐步事务
========================================

``sdnc`` 支持命名资源子命令和原始 North Protobuf JSON。全局 flags 放在子命令前；
资源动作的 ``--table-id`` 等参数放在动作之后，详见 :doc:`resources`。
示例在项目根目录执行；Docker 宿主机连接 ``127.0.0.1:29090``，源码直启默认连接
``127.0.0.1:19090``。下文用环境变量固定地址：

.. code-block:: bash

   export SDNC_ADDR=127.0.0.1:29090
   export SDN_ADMIN_TOKEN='<管理员访问令牌>'

查询与便捷命令
----------------------------------------

.. list-table:: 命令
   :header-rows: 1
   :widths: 22 48 30

   * - 子命令
     - 作用
     - 常用参数
   * - ``nodes``
     - 查询节点登记、状态、能力和 revision
     - 无
   * - ``configuration``
     - 查询完整 Desired 与版本
     - 无
   * - ``adopt``
     - 明确纳管节点并确认基线
     - ``-node``、``-revision``
   * - ``plan``
     - 为完整配置创建候选并预览
     - ``-file``、``-request-id``
   * - ``submit``
     - 创建候选、预览、提交并默认等待
     - ``-file``、``-request-id``、``-wait=false``
   * - ``job`` / ``watch``
     - 查询详情 / 订阅任务事件
     - ``-job``
   * - ``topology``
     - 查询拓扑；可触发采集
     - ``-refresh``
   * - ``rpc``
     - 调用任意 North unary RPC
     - ``-method``、``-file``

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure nodes
   ./bin/sdnc -addr "$SDNC_ADDR" -insecure configuration
   ./bin/sdnc -addr "$SDNC_ADDR" -insecure -job '<Job ID>' job
   ./bin/sdnc -addr "$SDNC_ADDR" -insecure -job '<Job ID>' watch

CLI 优先读取 ``SDN_ADMIN_TOKEN``，为空时读取 ``SDN_VIEWER_TOKEN``。
测试只读权限前应清除进程中的 admin 环境变量。RPC JSON 使用 lowerCamelCase 字段；
uint64 版本和 sequence 以十进制字符串输出。

精确控制同一个候选与计划
----------------------------------------

以下文件中的 ID、revision 均取上一步响应，不能直接复制占位符提交。
通过 ``rpc`` 保存原始请求，可在超时后安全重试同一消息。

第一步，读取全局配置：

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure configuration > current.json

将返回的 ``configuration`` 作为编辑基础，记下 ``revision``。
保存 ``begin.json``：

.. code-block:: json

   {"baseRevision": "0", "requestId": "evpn-change-001-begin"}

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure \
     -method BeginChange -file begin.json rpc > change.json

第二步，保存 ``edit.json``。``configuration`` 必须是完整对象，下面仅展示外层结构：

.. code-block:: json

   {
     "changeId": "<change.json 中的 id>",
     "editRevision": "0",
     "configuration": {"nodes": [], "tenants": [], "networks": []}
   }

将三个数组替换为实际目标配置，再执行：

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure \
     -method EditChange -file edit.json rpc > edited.json

第三步，保存 ``change-ref.json`` 并预览：

.. code-block:: json

   {"changeId": "<候选 ID>"}

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure \
     -method PlanChange -file change-ref.json rpc > plan.json

检查 ``nodes[].operations``、``dependsOn``、``expectedAgentRevision`` 与 ``warnings``。
计划中的删除操作和被占用而跳过的节点尤其需要核对。

第四步，保存 ``commit.json``，填写本次预览返回的 plan ID：

.. code-block:: json

   {
     "changeId": "<候选 ID>",
     "requestId": "evpn-change-001-commit",
     "planId": "<plan.json 中的 id>"
   }

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure \
     -method CommitChange -file commit.json rpc > job.json

提交成功返回 Job，Desired revision 已推进，但执行可能尚未完成。
使用 Job ID 查询或订阅，确认每节点最终状态。修改候选后原 plan 失效，需要重新预览。
全局版本冲突时，需要重新读取当前 Desired 并创建新候选。

取消与重试
----------------------------------------

未提交候选可取消：

.. code-block:: bash

   ./bin/sdnc -addr "$SDNC_ADDR" -insecure \
     -method AbortChange -file change-ref.json rpc

已经提交的 Change 不能 Abort，CLI 超时也不会取消后台 Job。
Commit 响应丢失时保留原文件，重新执行同一个 ``CommitChange``，或通过
``GetChange`` 找回 ``jobId``。同一个 request ID 配不同内容会返回 ``AlreadyExists``。

``submit -request-id`` 可以固定便捷流程的请求前缀，但便捷流程每次还会重新读全局版本；
它不能代替保存原始 Begin/Commit 请求的恢复机制。需要可靠自动化时使用上面的逐步 RPC。

结果与错误的详细含义见 :doc:`jobs-topology` 和 :doc:`troubleshooting`。

