# 完整回归与真实 EVPN 验收

> 历史验证记录：下文 IP、路径和运行状态仅对应测试当时的环境。新部署使用自己的部署服务器 IP，源码镜像环境见[部署手册](html/user/docker-deploy.html)。

日期：2026-10-25。最终版本通过 **11 个源码检查阶段、17 项真实网络/API 检查和
10 项真实浏览器检查**。最终实验保持运行，Web 地址为
`http://10.8.126.103:18080`。

机器可读汇总：[validation-summary.json](../artifacts/validation-summary.json)。

## 环境与边界

- 宿主机：10.8.126.103，Linux / Docker；Go 1.27.3。
- 前端构建：Node 24.14.0、固定 Vben Admin v5.7.0 / Element Plus。
- 浏览器：Windows 上 headless Chrome 155.0.8059.86，Node 24.14.0 / playwright-core。
- 实际数据面：Linux 内核与 `quay.io/frrouting/frr:10.0.0`；独立 etcd v3.6.7、Redis 7.4。
- `sdnc-it-*` 十个独立容器：controller、gateway、etcd、redis、spine、leaf1、leaf2、pc1、pc2、helper。
- 管理网与状态目录完全独立。原有 `sevpn-*`、`sdna-e2e-*` 的容器 ID、启动时间与运行状态前后相同。
- 网关/Controller 使用实验显式 insecure 参数；这不是生产 TLS、容量或 HA 验收。

[镜像 digest 与运行环境](../artifacts/integration-environment.json)、
[浏览器环境](../artifacts/browser-environment.json)、
[实际运行二进制 SHA256](../artifacts/binary-sha256.txt)。

## 源码检查

全部阶段 exit code 为 0，详见 [regression-results.json](../artifacts/regression-results.json)。

| 阶段 | 结果与日志 |
| --- | --- |
| 现有 Agent 全部 race 测试 | [通过](../artifacts/agent-race.log) |
| Controller 全部 race 测试 | [通过](../artifacts/controller-race.log) |
| Controller go vet | [通过](../artifacts/controller-vet.log) |
| Controller 构建 | [通过](../artifacts/controller-build.log) |
| Gateway 全部 race 测试 | [通过](../artifacts/gateway-race.log) |
| Gateway go vet | [通过](../artifacts/gateway-vet.log) |
| Gateway 构建 | [通过](../artifacts/gateway-build.log) |
| Vue 单元测试 | [7 项通过](../artifacts/ui-unit.log) |
| Vue 类型检查 | [通过](../artifacts/ui-typecheck.log) |
| Vue 生产构建 | [通过](../artifacts/ui-build.log) |
| 真实 etcd 持久化及第二 writer 拒绝 | [通过](../artifacts/etcd-real.log) |

Controller 当前有 25 个顶层 Go 测试，etcd 用例在单独配置的真实 etcd 检查阶段执行。
覆盖候选/编辑/提交版本、幂等、全节点 Stage 后再 Commit、PARTIAL 与 Applied、
丢失 Commit 响应后的重启恢复、权限变化后仍 UNKNOWN、实例和心跳版本边界、
事件重放、容量预留、资源校验与删除依赖。Gateway 有 7 个顶层 Go 测试。
代码修复后重新执行受影响的 Controller race/vet/build；未改动模块沿用本轮已通过的检查。

## 真实网络与 API：17 项

实验夹具只直接建立 veth 和 PC 地址。Spine/Leaf 的配置路径全部为：

```text
HTTP Gateway -> Controller North -> resource compiler -> Agent gRPC
             -> Linux / FRR adapters -> real kernel and routing daemons
```

1. 三个真实 Agent 注册，经 HTTP 显式纳管。
2. 完整候选编辑、预览、提交，成功配置 Spine 与两个 Leaf。
3. Spine 与两个 Leaf 的 BGP EVPN 会话 Established。
4. PC1/PC2 经 L2VNI 10010、L3VNI 50000、L2VNI 10020 双向通信。
5. Spine 学到两端主机的 Type-2 MAC/IP 和 Type-3 IMET。
6. 两个 Leaf 的 Zebra 将 50000 识别为 L3VNI。
7. 无令牌返回 401；viewer 可读、写入返回 403。
8. 重复相同 Commit 返回原持久 Job。
9. SSE 按序号重放事件，在终态关闭。
10. Controller 从真实 FRR 观测输出 Type-2/3、VTEP 与 host 图关系。
11. 两个候选的基础版本冲突返回 409；无变化计划不重复下发节点。
12. 临时租户/网络创建删除，Linux VRF/BD/VXLAN 和 FRR 引用均被移除。
13. 改 MTU 后操作不存在接口引发真实失败；Agent 回滚 MTU，Controller 记录失败并接受明确修正的目标。
14. 回滚后独立等待双向转发重新收敛，再进入管理进程重启检查。
15. Controller 与 Gateway 重启后，etcd 配置、节点身份、原 Job 和业务转发保持。
16. 既有用户实验容器的身份、启动时间和运行状态未变。
17. 单独停止 Controller，Agent 显示 DOWN，此时两方向 ping 均零丢包；重启后管理状态恢复 RUNNING。

逐项结果：[integration-results.json](../artifacts/integration-results.json)。
证据：[Spine EVPN](../artifacts/spine-evpn.txt)、[Leaf1 VNI](../artifacts/leaf1-vni.txt)、
[正向 ping](../artifacts/pc1-ping.txt)、[反向 ping](../artifacts/pc2-ping.txt)、
[Controller 停止时正向 ping](../artifacts/pc1-controller-down-ping.txt)、
[Controller 停止时反向 ping](../artifacts/pc2-controller-down-ping.txt)、
[最终 Desired](../artifacts/desired-final.json)、[最终节点](../artifacts/nodes-final.json)、
[真实观测拓扑](../artifacts/topology.json)。

MTU 故障实验从故障提交开始到双向转发检查通过记录为 19.2 秒，包含提交、回滚、
协议收敛和探测时间，**不是精确断流时间，也不是无损回滚承诺**。
详见 [rollback-reconvergence.json](../artifacts/rollback-reconvergence.json)。

## 真实浏览器：10 项

浏览器连接实际 `http://10.8.126.103:18080`，未拦截 API 或使用模拟网络数据。

1. 管理员登录、三个已纳管节点、能力详情。
2. TENANT-A 与 L2/L3 网络配置展示。
3. 普通 HTTP 环境生成 UUID，新建/编辑/保存候选并预览真实计划。
4. 另一个提交使预览过期，真实 409 后保留草稿并解锁新候选。
5. 从页面提交，获得 Job 并显示实际 SUCCEEDED。
6. 刷新后令牌不保留；重新认证能恢复查询持久任务。
7. G6 绘制观测拓扑，页面显示真实主机 EVPN 路由。
8. viewer 页面隐藏写操作；伪造 HTTP 写请求仍被后端 403 拒绝。
9. 不存在的 Job 显示真实错误。
10. 上述导航和操作没有未捕获的 pageerror。

[browser-results.json](../artifacts/browser-results.json)、
[浏览器控制台记录](../artifacts/browser-console.json)。控制台唯一 error 是故意触发的
409 版本冲突；HTTP 403/404 也通过独立断言验证，没有未捕获的页面异常。

截图：[节点](../artifacts/ui-nodes.png)、[租户与网络](../artifacts/ui-desired.png)、
[配置计划](../artifacts/ui-plan.png)、[无变化任务](../artifacts/ui-job.png)、
[三节点真实配置任务](../artifacts/ui-provision-job.png)、
[网络拓扑](../artifacts/ui-topology.png)、[只读用户](../artifacts/ui-viewer.png)。

拓扑保留各来源节点的路由观测，同一 MAC/IP 可能出现多条带来源的图记录，图节点数
不等于物理设备数。最终任务列表保留一个故障注入产生的预期 FAILED Job；后续明确修正
的目标已成功提交，当前业务网络正常，失败历史没有被删除或伪装成成功。

## 本轮发现与修正

- Gateway 初始产物动态链接 glibc，无法在 FRR Alpine 镜像启动。构建已改为
  `CGO_ENABLED=0`，在同镜像实际执行并重跑完整实验。
- 原删除计划先删除租户 BGP，再解绑 L3VNI；真实 FRR 拒绝并触发 Agent
  ROLLBACK_FAILED。Controller 已改为显式拆除依赖，增加顺序回归。
  失败记录保留于 [attempt2 Agent Task](../artifacts/attempt2-agent-task.json)；没有修改历史日志解锁，
  修复后使用全新状态实验验证完整删除。
- 夹具原先把 MTU 回滚后的协议收敛与管理重启混在一起，产生一次即时 ping 失败。
  已分离为有界收敛检查和独立 Controller 停止测试；没有把配置回滚等同于立即转发恢复。
- 首轮浏览器定位 `spine` 同时匹配名称和角色，修改测试定位后全套通过，无产品代码变更。

此前尝试记录：[attempt1](../artifacts/integration-attempt1-results.json)、
[attempt2](../artifacts/integration-attempt2-results.json)、
[attempt3](../artifacts/integration-attempt3-results.json)、
[browser attempt1](../artifacts/browser-attempt1-results.json)。最终结论只依据最后通过的版本。

## 复现与当前限制

运行、清理和重复验收见 [lab/README.md](../lab/README.md)；浏览器命令见
[Web E2E README](../../sdn-web/tests/e2e/README.md)。
实验凭据在 `lab/state/tokens.json`，由用户在登录框输入 admin/viewer 值，未写入构建或报告。

这次验证覆盖单写入 Controller、小规模 IPv4 EVPN 和桌面 Chromium。
Controller 仍有 1 MiB 快照上限和默认 STOP/PARTIAL 策略；跨节点自动补偿、HA、
生产身份体系、IPv6 全流程、VPP/交换机、FPM/LLDP、MAC/ARP 主动保活和容量压测未在本轮实现或验证。
