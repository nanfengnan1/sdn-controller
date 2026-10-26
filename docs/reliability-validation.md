# 持久化与任务恢复修复验收

> 历史验证记录：环境地址与路径保留原始测试信息，不作为部署默认值。新部署参照[源码安装手册](html/user/source-install.html)或[Docker 部署手册](html/user/docker-deploy.html)。

日期：2026-10-25。对应 [REL-001 / REL-002 / REL-003](backlog.md)。

## 结论

本轮 11 个检查阶段全部通过：Agent、Controller、Web Gateway 各自的 Go race 测试、vet、build，以及独立 FRR 测试程序构建和真实回滚测试。

| 测试组 | 顶层用例结果 | 说明 |
|---|---|---|
| Agent `go test -race -count=1 -json ./...` | 16 通过、1 条件跳过 | 跳过的真实 FRR 用例随后在独立容器运行通过 |
| Controller `go test -race -count=1 -json ./...` | 32 通过、无跳过 | 配置真实开发 etcd endpoint，未跳过数据库用例 |
| Gateway `go test -race -count=1 -json ./...` | 7 通过、无跳过 | 验证公共协议新增字段/RPC 的编译和现有 API 兼容 |
| 独立 FRR | 1 通过 | 实际 vtysh / frr-reload.py / running-config 回读 |

机器结果：[summary.json](../artifacts/reliability/summary.json)、[阶段明细](../artifacts/reliability/results.json)。原始日志：[Agent](../artifacts/reliability/agent-race.log)、[Controller](../artifacts/reliability/controller-race.log)、[Gateway](../artifacts/reliability/gateway-race.log)、[真实 FRR](../artifacts/reliability/real-frr-rollback.log)。

## 覆盖的故障与行为

- 真实 etcd 在随机独立 key 前缀执行事务，在客户端响应边界注入：发送前失败、已提交后丢响应、已提交后调用方取消。确认恢复后的下一次写入仍成功。
- 持续不可达时不无限重试；无法核实结果就停止写入。不同快照或不同写入者不会被旧内存覆盖。调用前已取消的请求不发送，也不误停正常写入者。
- 一个节点的 Job pending/UNKNOWN 时，无关节点的 Job 可完成；占用节点修改或移除被拒绝。
- 回滚失败只隔离失败节点。Controller 持久化恢复意图，丢失恢复响应并重启后继续使用原请求，恢复只执行一次；不自动重放正向操作。
- Agent 检查 sequence、Running revision、Undo、后端和逆序依赖；不安全的旧版乱序恢复日志被拒绝。失败事件保留，恢复后可执行新的显式事务。
- 新增 North RetryRollback 仅管理员可调用，viewer/Agent 注册凭据不能调用。
- 真实 FRR 测试创建 BGP/EVPN 配置，在配置回读后注入失败，并令首次 Undo 暂时失败。确认 ROLLBACK_FAILED 时实际 BGP 配置仍在；持久化恢复请求后重建 Agent 对象，重新执行原 Undo，最终回读 BGP 配置已撤回且 Running revision 未增加。

真实 FRR 故障通过测试 Driver 包装器注入；FRR 正向配置、恢复命令和回读是真实执行。这里验证暂时性 Undo 失败后的恢复，不声称所有 FRR 版本或任意失败位置均已覆盖。

## 复现与隔离

```sh
cd ~/program/github/sdn-controller
python3 lab/reliability.py \
  --go /home/alexan/.cache/sdn-agent-tools/go/bin/go \
  --etcd http://127.0.0.1:12379
```

环境：10.8.126.103，Go 1.27.3，开发 etcd，FRR 镜像 `quay.io/frrouting/frr:10.0.0`。临时 FRR 容器使用 network=none、唯一名称和 `sdn.reliability=rollback-test` 标签；完成后核对标签并清理。未修改或重启现有 sdnc-it、sevpn、sdna-e2e 实验服务，也未部署新的二进制覆盖运行进程。

## 未覆盖或尚未解决

- 本轮未重跑旧报告的完整 EVPN 转发/浏览器套件；原验收记录仅代表当时版本。
- 未验证真实断电、磁盘控制器缓存故障、目录 fsync 失败注入；Controller File 的此类错误处理已改为停止写入，不能把普通文件重启测试当作断电认证。
- Agent Redis 存储故障矩阵、宿主机/网络命名空间重建后的配置重建、配置漂移、日志容量和归档仍在待办。
- 回滚重试不解决缺失 Undo、Running 基线变化或不安全旧日志；不提供强制解锁。Web 尚无恢复按钮。
- 新版本为单 Controller 写入实例；节点隔离不等同于 Agent 端管理代次隔离或 HA 自动接管。

操作方式见 [恢复说明](recovery.md)。
