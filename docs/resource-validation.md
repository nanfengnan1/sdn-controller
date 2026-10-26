# 独立资源与五核心容器端到端验证

日期：2026-10-25，时间为 UTC+08:00。状态：**本轮验收与部署已完成**。

四组端到端检查共 57 条记录通过：原网络 / API 18 条、原浏览器 10 条、新资源网络 / API / CLI 22 条、真实资源浏览器 7 条。模拟浏览器组件另有 8 项通过，正式部署后另有 3 项只读检查通过。独立验收环境已清理，原 `sdn-docker` 部署现为五个健康核心容器，可在 [http://10.8.126.103:28080](http://10.8.126.103:28080) 手动测试。实际汇总见 [validation-ready.json](../artifacts/resources/validation-ready.json)。

本报告汇总 Agent、Controller、Web 联合验收。Controller 源码层面的测试另见 [controller-resource-validation.md](controller-resource-validation.md)，Web 源码与组件测试另见相邻 `sdn-web/docs/resource-validation.md`；它们不等同于真实 Linux / FRR 网络验证。上一轮 [bundled-validation.md](bundled-validation.md) 保留当时八容器部署的历史结论，不重写为本轮结果。

## 本轮功能与部署边界

- Agent 协议提供 26 类配置 op 与 19 类 Query；以节点实际能力声明为准，配置使用 typed Protobuf，查询返回来源、采集时间、revision、stale 和错误。
- Controller `NodeResources` 管理节点独立资源，支持候选增量编辑、依赖计划、提交、验证导入、结构化查询与命名 CLI。Controller Desired 保存提交意图，Agent 回滚不等于 Desired 回退。
- Web 资源表单按 Agent 操作，显示租户、BD、端口、VXLAN 与三层地址关系；完整 JSON 仍作为高级入口。
- 默认部署五个核心容器：Controller（内置 etcd）、Web（Nginx 与 Gateway）、Spine / Leaf1 / Leaf2（各自内置 Redis / FRR）。启用 `lab` profile 后增加两台 PC，总计七个；helper 仅用于临时接线。
- 仅暂停管理面时停止 `web controller`，保留三个 Agent 的业务网络命名空间。停止 / 重建 Agent 后不会自动全量恢复 Linux 网络对象。
- 网络业务对象中的 nodeId 使用已注册 UUID；Web / Gateway 启动不填写 nodeId，`agent.conf` 不手工分配 UUID，容器名 `leaf1` 不能替代 UUID。

本轮验收使用独立 `sdn-resource-it` 项目，Web 为 38080，宿主机 Controller gRPC 为 39090；凭据、镜像标签、容器、卷与配置目录和原 `sdn-docker` 部署分开。验收完成并清理独立环境后，才按下文记录升级原 28080 部署。

## 最终镜像

| 组件 | 验收与最终部署使用的短 ID | 主要内容 |
| --- | --- | --- |
| Agent | `f97e9e96a3e9` | 26 类 op、19 类 Query，包含稳定分页指纹修复；内置 Redis / FRR |
| Controller | `c93ee5df515b` | 独立资源、验证导入、命名 CLI；内置 etcd |
| Web | `f361a0c2e8ae` | Nginx 与 Gateway 合并，资源表单及数字控件状态修复 |

完整 SHA 记录在 [验收清单](../artifacts/resources/validation-ready.json) 与 [最终部署结果](../artifacts/resources/deployment-final.json)，两者一致。验收环境使用 Alpine 3.22.6、FRR 10.2.2、Redis 7.4.11、etcd 3.6.7，具体采集记录见 [environment.json](../artifacts/resources/environment.json)。

## 当前结果

| 范围 | 当前状态 | 证据 / 判断依据 |
| --- | --- | --- |
| 原声明式 EVPN / API 功能回归 | 已通过，18 项 PASS | [integration-results.json](../artifacts/resources/integration-results.json)、[执行日志](../artifacts/resources/legacy-test.log) |
| 原浏览器工作流回归 | 已通过，10 项 PASS | [legacy-browser/browser-results.json](../artifacts/resources/legacy-browser/browser-results.json) |
| 新资源 API / CLI 完整 suite | 修复后全新基线重跑通过，22 条检查记录 PASS | 含重启后的协议 / 流量复核；[resource-results.json](../artifacts/resources/resource-results.json)、[最终执行日志](../artifacts/resources/resource-test.log) |
| IPv6 冷启动专项复核 | 已通过专项验证 | 显式补充业务 SVI link-local 后，清除两端主机邻居并直接跨 Leaf 测试，两方向均 4/4、首包成功，抓到 NS / NA；[诊断](../../sdn-agent/artifacts/ipv6-cold-start/diagnosis.md) |
| 资源 CRUD / MTU / 清理 | 已在完整 suite 通过 | 虚拟对象及附属接口清理、后续 link-up 保留 MTU；早期续跑结果保留作诊断历史 |
| INTERFACES 连续分页 | 稳定指纹修复后通过 | 完整 suite 实际 HTTP 请求连续读取两页；[查询快照](../artifacts/resources/query-INTERFACES.json) |
| Web 资源组件 | 已通过，8 项 PASS | 包括数字控件从禁用转为启用后的可访问性；[组件结果](../../sdn-web/artifacts/components/resource-component-results.json)，不是整网表单验收 |
| 新资源浏览器表单与层级显示 | 最终 Web 镜像实际复测通过，7 项 PASS | 三笔实际事务、关系树、查询来源与错误、viewer 权限、无 pageerror；[真实结果](../artifacts/resources/resource-browser/resource-browser-results.json)、[租户关系树截图](../artifacts/resources/resource-browser/resources-tenant-tree.png) |
| 命名 CLI 的真实节点验证 | 已通过专项并纳入完整 suite | 单命令 VRF 提交与显式多操作候选 / Plan / Commit / 删除；[resource-cli-results.json](../artifacts/resources/resource-cli-results.json) |
| 三项目严格 Sphinx 构建 | 已通过 | `make -C docs html`，`-W --keep-going -n -a`，三个项目退出码均为 0，无警告 |
| 三项目 HTML 与报告证据链接 | 已通过 | Agent 21页、Controller 23页、Web 19页，本地文件与锚点无失效；未检查公网外链 |
| 独立验收环境清理 | 已通过 | `resource_lab.py down` 完成，97 个原容器的身份、启动时间与运行状态保护检查通过；[清理日志](../artifacts/resources/lab-cleanup.log) |
| 原部署升级、身份与环境保护 | 已通过 | 五核心健康、原三节点 UUID 与空白目标保留，89 个其他容器未改变；[最终部署结果](../artifacts/resources/deployment-final.json)、[恢复执行日志](../artifacts/resources/deployment-resume.log) |
| 部署后只读验证 | 已通过，3 项 PASS | 原三节点 RUNNING、未纳管且 revision=0，每节点 26 op / 19 Query；空 Desired / Job；Web 资源页可用且无 pageerror。[结果](../artifacts/resources/deployment-smoke-results.json)、[部署后截图](../artifacts/resources/deployed-resource-workspace.png) |

回滚测试会主动提交不能成功的操作，因此个别 Job 为 FAILED 属于用例预期。整体验收结果以 suite 最终汇总、断言和进程退出码为准，不要求所有测试 Job 都为 SUCCEEDED，也不能只凭若干 PASS 推断全套完成。

## 本轮诊断与修复进展

首轮新资源 suite 已通过逐项创建、引用检查、真实缺失端口回滚，以及 OSPFv3 邻接和双栈查询，随后在 IPv6 冷启动跨 Leaf 流量验证超时。诊断确认 IPv4 示例沿用的业务 SVI `addrgenmode none` 使 br10 / br20 没有 link-local 地址；本实验 Linux 6.8 的邻居发现无法为转发流量发送所需 NS。抓包在修复前没有观察到发往本地主机的 NS，不能把该失败归为远端 VXLAN 邻居抑制或缺少 Type-5 路由。

Controller 随后显式配置 br10 的 `fe80::ff:fe00:10/64` 和 br20 的 `fe80::ff:fe00:20/64`，不 flap 接口。DAD 完成后清除两端主机邻居，直接跨 Leaf 访问，两方向各 4/4 且首包成功；没有先让主机 ping 网关，也没有静态邻居预热。证据包括 [IPv6 冷启动诊断](../../sdn-agent/artifacts/ipv6-cold-start/diagnosis.md)、[PC 流量](../../sdn-agent/artifacts/ipv6-cold-start/pc1-to-pc2-after.log) 和 [接入口 NS / NA 抓包](../../sdn-agent/artifacts/ipv6-cold-start/leaf2-host20-after.log)。`resource_test.py` 已增加显式 link-local 前置；不配置业务 IP 的 L3VNI SVI br50000 保持原方式，IPv4 默认实验不因此改变。

早期后半段独立续跑确认 VRF / BD / 地址 / VXLAN / 端口 CRUD 清理以及 MTU 更新不会被之后的接口 up 覆盖，但 INTERFACES 分页暴露另一个问题：raw 详情中的运行计时改变指纹，使下一页连续返回 `409 Aborted`。该问题已修正为稳定指纹，并在重新构建 Agent 后从全新基线运行完整资源 suite。22 条记录全部 PASS，包含显式 link-local、清空主机邻居后的双向 IPv6、BGP IPv4 / IPv6 单播、OSPFv2 / v3、Type-5、连续两页 INTERFACES、CRUD、回滚与修正 Desired、命名 CLI 及管理面重启。

此结论来自完整重跑，不把早期诊断 / continue 等同于整套成功。首次失败与续跑失败保留在 [首轮结果](../artifacts/resources/attempt1-resource-results.json)、[首轮日志](../artifacts/resources/attempt1-resource-test.log) 和 [续跑历史](../artifacts/resources/attempt1-resource-continuation-results.json)。最终持久目标见 [resource-final-desired.json](../artifacts/resources/resource-final-desired.json)，实际协议与接口查询记录位于同目录的 `query-*.json`。

真实 Web 表单测试还发现 Element Plus InputNumber 的禁用状态切换问题：新建候选后原生输入已启用，但 `aria-disabled` 仍为禁用。Web 按有效 disabled 状态重建数字组件并保留表单模型，新增回归后组件检查由 7 项增加为 8 项且全部通过，没有通过强制填写跳过可访问性检查。最终 Web 镜像的真实资源表单复测 7 项全部通过；空 VRF、后续 BD / 地址、再绑定 L3VNI 分三笔事务成功，已有 VRF 没有被重复创建。早期浏览器失败记录保留在同一目录带时间戳的 `resource-browser-results-*.json`，没有改写为成功。

验收结束时 Controller 中有 26 个持久 Job，快照为 371337 字节，低于当前 1 MiB 上限；这是本轮有限规模验证的数据量，不代表任意规模或长期使用没有容量限制。

## 验收入口

隔离环境与网络测试由 `sdn-controller/docker/resource_lab.py` 管理，资源用例由 `docker/resource_test.py` 执行；管理脚本的 `legacy-test` 分支复用原 EVPN / API 回归。以下为专属验收入口，会改变隔离环境，不能替换为原手测环境直接执行：

```bash
cd ~/program/github/sdn-controller
python3 docker/resource_lab.py build
python3 docker/resource_lab.py lab-up
python3 docker/resource_lab.py legacy-test
# 完成原功能回归后，由验收执行方重建专属空白基线
python3 docker/resource_lab.py lab-reset
python3 docker/resource_lab.py resource-test
```

浏览器用例必须显式设置该隔离环境的 URL、受控凭据文件和产物目录。报告不包含访问令牌；首次启动、日常操作与资源 CLI 示例分别参见用户指南的 Docker 部署和逐节点资源配置页。

## 清理与最终部署状态

18:26:55 的 [validation-ready.json](../artifacts/resources/validation-ready.json) 确认四组验收汇总通过，随后移除 `sdn-resource-it` 的专属容器与卷。该阶段检查了 97 个原容器的 ID、StartedAt、Running，均保持不变。

原 `sdn-docker` 环境确认空白后，停止属于本项目的旧八个容器，备份原十个数据卷、私有配置和容器启动参数，再升级为五个核心容器。旧独立 Gateway 与两台默认 PC 移除，Web 内合并 Nginx / Gateway；原三个 Agent UUID、令牌和持久卷保留。升级阶段的无关保护集合不包含本次授权升级的旧八容器，因此该阶段为 89 个其他容器未改变，与验收阶段的 97 个不是同一统计集合。

备份目录为 `docker/state/backups/resources-20261025-182805`。备份归档最初由 root 创建，设置权限时触发 `PermissionError`；处理限定为修复备份文件属主，并重新核验原停止容器 ID 后恢复升级流程，没有把这次中断当作成功。最终恢复日志与 18:31:52 的 [deployment-final.json](../artifacts/resources/deployment-final.json) 均为 PASS。备份中的凭据和原始启动参数属于私有资料，本报告不链接或复制其内容。

目前仅以下五个核心容器运行且健康：`sdn-docker-controller`、`sdn-docker-web`、`sdn-docker-spine`、`sdn-docker-leaf1`、`sdn-docker-leaf2`。原三节点在线、未纳管，Agent revision=0，Desired revision=0、无 Job，供用户从空白目标开始测试；这与隔离验收环境结束时的 26 个 Job 分属不同环境。

部署后只读浏览器检查确认 28080 的新资源工作台各分类可见、没有页面错误。通用安装指南继续使用 `<部署服务器IP>` 占位，以上实际 URL 仅记录本次部署地址。默认五核心环境不包含测试 PC；验证主机转发时按用户指南显式启用 `lab-up`。

## 已知限制

- Linux 网络命名空间重建后，Agent 不会自动依据 Running 全量重建 VRF、Bridge、VXLAN 等对象；数据库持久化不等于数据面已恢复。
- Controller Desired 是已提交意图。Agent 执行失败并回滚本地配置后，Desired 不自动回退；需要查看 Job、Applied 和实际查询，再显式提交修正目标。
- 事务回滚限定于节点，跨节点保留 STOP / PARTIAL 语义。内置单成员 etcd 不提供高可用。
- 57 条记录包含重启后的重复协议 / 转发核验，计数不是 57 种不同能力。模拟组件、源码测试和部署后只读检查单独列出，不混入端到端总数。
- 本轮没有声明大规模长期运行、跨节点原子事务、VPP 或硬件数据面的验收；Controller 单快照和历史 Job 的容量上限仍需管理。
