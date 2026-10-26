# 持久化与任务恢复操作

## 数据与实际网络的区别

Controller 已有 etcd / 文件持久化；Agent 已有 Redis / 文件任务日志和本地身份文件。数据库和状态目录应落在持久卷上。Redis 要启用与事务日志要求匹配的 AOF 策略，实验使用 appendonly yes、appendfsync always。宿主机/网络命名空间重建后自动重建 Linux 配置仍未实现，见 [REL-004](backlog.md)。

## Controller 存储异常

etcd 提交回复丢失后，重试同一写入者、旧 revision 和快照的 CAS；若第一次已经提交，失败分支在同一事务中读取快照和写入者，核实相同内容并同步 revision。独立恢复窗口为 3 秒，不沿用已经取消的北向调用上下文。

不能仅凭一次读取旧值就认定第一次提交没有发生，因为原请求仍可能在途。无法核实、发现不同快照或不同写入者时，关闭写入许可，停止 RPC/Worker。修复存储连通性后重启原 Controller，或配置进程管理器对该退出重启，从持久状态恢复。此机制不是多 Controller 自动切主；仍需确保旧实例已退出。File 模式在 rename 后目录同步失败时同样停止写入，不能继续用旧内存覆盖磁盘。

## 无关节点继续配置

活动 Job 占用其全部相关节点；UNKNOWN 不释放这些节点。终态 ROLLBACK_FAILED 隔离失败节点。其他节点可以提交新 Change，但完整 Configuration 必须保留被占用节点的目标操作不变。Plan 明确提示这些节点由原 Job 负责，不会重新下发。

Worker 按创建时间轮流推进已受理 Job，单次 RPC 有超时。当前不是每节点独立执行线程，故障 RPC 仍会增加轮询延迟。涉及同一节点或必须共同更改的依赖仍需等待原任务完成/恢复。

## 重试回滚

先排除后端不可用等故障，获取当前 Job，确认目标节点为 ROLLBACK_FAILED。保存请求到 `retry-rollback.json`：

```json
{
  "jobId": "原Job ID",
  "nodeId": "失败节点 UUID",
  "requestId": "repair-001",
  "expectedJobSequence": "当前Job sequence"
}
```

```sh
bin/sdnc -addr controller:19090 -ca ca.pem -method RetryRollback -file retry-rollback.json rpc
bin/sdnc -addr controller:19090 -ca ca.pem -job '原Job ID' watch
bin/sdnc -addr controller:19090 -ca ca.pem -job '原Job ID' job
```

认证仍使用 `SDN_ADMIN_TOKEN`；viewer 和 Agent 注册凭据无权调用恢复接口。隔离实验可用 `-insecure` 替换 CA 选项。重复请求必须使用同一文件；复用 requestId 修改内容会被拒绝。

流程：持久化恢复意图 → Job RECOVERING → Agent 持久化回滚请求 → 逆序 Undo 和回读 → FAILED_ROLLED_BACK 或 ROLLBACK_FAILED。回复丢失时保留请求 ID，按 UNKNOWN 查询/重试；两端重启后也使用原请求。恢复结果写入原 Job，原失败事件保留。

成功恢复代表回到该节点事务前的配置，不代表原 Job 成功。Job 保持 FAILED/PARTIAL，失败节点变为 FAILED_ROLLED_BACK，之后可重新预览、提交目标配置；不会自动补做原 Job 后续节点。再次失败仍保持节点隔离。

存在旧版本乱序恢复记录、Undo 丢失、基线版本改变等情况时拒绝自动回滚，需要显式核对。不能编辑历史 Task 为成功来绕过检查。新增接口需要同时更新 Controller、Agent；Web 暂无专用恢复按钮，使用上述 CLI。升级正在运行的服务属于独立部署步骤，本次回归不替换现有实验进程。

## 回归

```sh
python3 lab/reliability.py --go /path/to/go --etcd http://127.0.0.1:12379
```

etcd 参数必须是隔离的开发实例。测试在独立随机前缀注入响应故障，不停止数据库；真实 FRR 测试创建临时 network=none 容器、核对所有权标签后清理。不会更改现有 EVPN 实验容器。结果写入 `artifacts/reliability/`。
