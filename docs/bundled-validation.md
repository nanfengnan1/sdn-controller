# 内置数据库与外部配置验证记录

记录日期：2026-10-25，时间为 UTC+08:00。

本轮验证通过：内置数据库运行检查 14 项、真实 EVPN / API 检查 18 项、浏览器检查 10 项。完成测试后已清空本项目的测试业务数据并停止八个容器，配置文件与空白持久卷保留。88 个无关容器的身份、启动时间和运行状态未改变。

本报告对应 Controller 内置 etcd、各 Agent 内置 Redis / FRR，以及外部只读挂载 `controller.conf` / `agent.conf` 的版本。此前 [docker-validation.md](docker-validation.md) 保留原独立数据库拓扑的历史结论，不作为本轮版本的验收依据。

## 验证范围

- 默认八个容器：Controller、Gateway、Web、Spine、Leaf1、Leaf2、PC1、PC2。
- 迁移前停止旧数据库并备份，迁移阶段复用原 etcd、Redis、Agent 身份和 FRR 命名卷；验收后测试状态的清理另行记录。
- `controller.conf` / `agent.conf` 描述应用与内部数据库设置，宿主机配置只读挂载，重新启动后加载。
- Web / Gateway 启动不填写 nodeId；网络业务配置使用注册并纳管后的真实 UUID。`agent.conf` 不手工填写 UUID。
- 配置和进程故障验证与完整 EVPN / 浏览器验证分别记录。

## 最终镜像与环境

| 镜像 | 最终短 ID | 本轮实际版本 / 用途 |
| --- | --- | --- |
| `sdn-agent:alpine` | `bff20c0a13bc` | Alpine 3.22.6、FRR 10.2.2、Redis 7.4.11；Spine / Leaf 使用该镜像 |
| `sdn-controller:alpine` | `a822a1ef565e` | Controller 与 etcd 3.6.7，包含残余 writer lease 启动恢复修复 |
| `sdn-web-gateway:alpine` | `310245721107` | Go Gateway |
| `sdn-web-ui:alpine` | `55e62c5c789c` | Vue UI / Nginx |

镜像从源码执行多阶段构建，未变化的阶段复用构建缓存。最终 ID 以 [image-manifest.json](../artifacts/bundled/image-manifest.json) 和 [final-environment.json](../artifacts/bundled/final-environment.json) 为准；数据库、FRR 与 Alpine 版本见 [environment.json](../artifacts/bundled/evpn/environment.json)。宿主内核为 Linux 6.8.0-138-generic。浏览器验证使用 Chrome 155.0.8059.86、Node v24.14.0，见 [浏览器环境记录](../artifacts/bundled/browser/browser-environment.json)。

## 结果清单

| 项目 | 状态 | 证据与说明 |
| --- | --- | --- |
| Agent / Controller 源码与监督逻辑 | 通过 | Go 全包 race / vet、Agent 8 项 Python、Controller 6 项 Python、管理脚本 11 项 Python；共 7 批命令退出码为 0。[汇总](../artifacts/bundled/source-results.json) |
| Gateway 与前端源码 | 通过 | Gateway 全包 race / vet、UI 7 项用例、类型检查，共 4 批命令退出码为 0。[汇总](../artifacts/bundled/web-source-results.json) |
| Controller 残余租约修复回归 | 通过 | 配置真实临时 etcd 后重新运行 Controller 全包 race / vet；覆盖等待失效租约、拒绝抢占活跃写入者、取消等待。[结果](../artifacts/bundled/controller-lease-results.json)、[详细日志](../artifacts/bundled/controller-lease-race.log) |
| 管理与验收 Python 脚本语法 | 通过 | `manage.py`、`test.py`、`test_manage.py` 的 `py_compile` |
| 三项目严格 Sphinx HTML 构建 | 通过 | 三项目分别执行 `make -C docs html`，Sphinx 8.1.3，`-W --keep-going -n -a`，退出码均为 0，无警告 |
| 三项目生成 HTML 内部链接与锚点 | 通过 | Agent 18页/998引用，Controller 21页/1088引用，Web 18页/966引用；本地文件与HTML锚点无失效项，公网外链未检测 |
| 四个源码镜像构建 | 通过 | [构建日志](../artifacts/bundled/final-image-build.log)；Controller 修复后再次[构建](../artifacts/bundled/controller-lease-build.log) |
| 外部配置、监督与持久化运行检查 | 通过 | 最终 14 项 PASS，使用上述 Agent / Controller 镜像。[结果](../artifacts/bundled/runtime-results.json) |
| 旧卷迁移与初始状态确认 | 通过 | 10 个原卷先备份，再复用卷验证 revision=0 的空白初始环境。[备份清单](../artifacts/bundled/backup-manifest.json)、[迁移结果](../artifacts/bundled/migration-clean.json) |
| 八容器、内置服务、只读配置及卷检查 | 通过 | [bundled-services.json](../artifacts/bundled/evpn/bundled-services.json) |
| OSPF / BGP / EVPN、双向流量、事务回滚 | 通过 | 18 项 PASS。[完整结果](../artifacts/bundled/evpn/integration-results.json)、[执行日志](../artifacts/bundled/evpn-test.log) |
| Web 浏览器工作流 | 通过 | 10 项 PASS。[结果](../artifacts/bundled/browser/browser-results.json) |
| 最终清理、停止及无关环境保护 | 通过 | 本项目八容器停止，88 个无关容器未变化。[最终结果](../artifacts/bundled/final-environment.json) |

## 实际检查内容

运行检查使用隔离容器及专属持久卷，验证文件配置可改变 Agent / Controller 监听端口及内部 Redis / etcd 端口；仅配置文件内的令牌可工作，环境令牌覆盖文件，显式 CLI 参数覆盖配置。诊断输出不包含令牌，`-check-config=true` 不启动数据库，无效配置在持久写入前被拒绝。

Agent 容器重建后身份和候选保留；Controller 容器重建后 etcd 候选保留。健康检查跟随实际生效端口。终止 Redis、FRR watchdog 或 etcd 后，相应整个容器退出。Controller 硬终止后重启，可以等待旧 writer lease 失效并读取原候选，最终运行检查 14 项全部通过。

EVPN 实验使用两台 Leaf、一台 Spine / RR 和两台 PC。验证了 BGP EVPN 会话 Established、两个主机的 Type-2 MAC/IP、Type-3 IMET、两台 Leaf 正确识别 L3VNI 50000，以及 L2VNI 10010 / 10020 之间经租户 L3VNI 的双向转发。两次方向测试均为 3 发 3 收、0% 丢包，见 [PC1](../artifacts/bundled/evpn/pc1-ping.txt) 与 [PC2](../artifacts/bundled/evpn/pc2-ping.txt)。

API / 事务检查包括权限隔离、重复 Commit 幂等、SSE 回放、候选版本冲突、资源增删、真实 Agent 配置失败后 MTU 回滚和重新收敛。Controller / Gateway 重启保留配置、身份、Job 和转发；停止 Controller 时 Agent 上报 DOWN，既有双向业务流量仍通过。浏览器检查覆盖登录、节点纳管视图、候选编辑、计划、Commit / Job、重新认证恢复、G6 拓扑、viewer 禁写及错误展示。

## 测试命令

以下为本轮实际执行的检查入口，工作目录为相应源码仓库；运行隔离用例的环境变量、临时端口和清理细节保存在证据脚本中，不在报告中复制凭据。

```bash
# sdn-agent 与 sdn-controller，各自仓库执行
go test -race -count=1 ./...
go vet ./...

# sdn-agent/docker 与 sdn-controller/docker，各自执行
python3 -m unittest -v supervisor_test

# sdn-controller/docker
python3 -m unittest -v test_manage

# sdn-web/gateway
go test -race -count=1 ./...
go vet ./...

# sdn-web/ui
pnpm test
pnpm typecheck

# sdn-controller：真实 etcd 修复回归与独立容器运行验证
python3 artifacts/bundled/lease_startup_check.py
python3 artifacts/bundled/runtime_smoke.py

# sdn-controller：专用空白实验的构建与真实网络验收
python3 docker/manage.py build
python3 docker/manage.py up
python3 docker/manage.py bundled-check
python3 docker/test.py

# sdn-web：已设置对应部署地址、受控凭据与产物目录
node tests/e2e/browser.cjs

# 三项目各自执行
make -C docs html
```

源码检查实际分批命令见 [source_checks.py](../artifacts/bundled/source_checks.py) 与 [web_checks.py](../artifacts/bundled/web_checks.py)。真实 etcd 回归使用 `SDNC_TEST_ETCD` 指向一次性独立实例，执行 `go test -race -v ./...` 与 `go vet ./...` 后移除该实例。该轮真实 etcd 测试不是被条件跳过的模拟结果。

## 本轮发现与修复

旧 etcd 卷中的 writer lease 在 Controller 异常退出后不会立刻消失。原启动方式一旦遇到残余写入锁就退出，监督进程同时关闭 etcd，造成恢复期反复重启。现在只对明确的写入锁占用进行有界等待：默认启动预算中最多等待 25 秒，并为加载快照和健康检查预留时间；不会删除他人锁、撤销活跃租约或重试不确定的存储结果。等待支持取消。真实 etcd 测试验证了自然过期后保留快照、活跃 writer 不被抢占和取消时锁不受损；最终镜像的硬终止 / 重启用例也通过。

配置与监督逻辑同时补充了端口冲突、大小写重复字段、诊断参数布尔值及最后一次参数生效的处理。早期运行脚本有两次测试误判，分别涉及 `pkill` 匹配和 `check-config` 文本断言，修正测试后重新运行；原始记录保留为 [第一次](../artifacts/bundled/runtime-bundled-20261025145626.json) 与 [第二次](../artifacts/bundled/runtime-bundled-20261025150618.json)，不作为当前未解决应用故障。最终结果使用 `bundled-20261025152003` 的 14 项记录。

## 验收后环境

2026-10-25 15:26:53 已核对 `final-environment.json` 的 `passed=true`。初始迁移检查先证明本项目原管理数据为空白 revision=0；验收产生的测试配置随后仅在本项目内清理，再保存新的空白卷并停止服务。[最终空白状态](../artifacts/bundled/final-clean-state.json) 显示 Desired revision=0、无 Job、三个节点均未纳管且 Agent revision=0。

八个 `sdn-docker-*` 容器全部停止。Controller、Gateway、Web 与三个 Agent 的退出码均为 0；两台 PC 的 `sleep` 进程收到 SIGTERM，退出码为 143。宿主配置文件、凭据及空白持久卷保留；临时运行测试容器已清理。88 个无关容器的 ID、StartedAt、Running 三项均与执行前相同。

原始十个卷的备份保存在 `docker/state/backups/before-bundle-20261025-144251`，没有因清理测试数据而删除。后续手测按用户指南执行 `python3 docker/manage.py up`，使用重新生成的 `docker/state/evpn.json`；不能沿用验收阶段旧 UUID。当前 Web 处于停止状态，不是已经开放的在线环境。

## 已知边界

- 内置 etcd 是单成员实例，本轮没有实现或验证 Controller / etcd 高可用。
- Redis / etcd 持久化和身份恢复不等于自动恢复 Linux 数据面；Agent 容器重建后的 Linux VRF / bridge / VXLAN 对象仍不会自动按 Running 重建。
- 本轮验证专用 IPv4 EVPN 实验，不宣称完成任意规模、长期压力、IPv6 全链路、VPP 或硬件数据面验收。Controller 短暂离线时既有流量通过，不等于无限时长的离线保活保证。
- 文档链接检查覆盖本地文件与 HTML 锚点，未检查公网外链可用性。

默认部署步骤见用户手册的“Docker 源码镜像与完整实验”和“controller.conf 与容器内置 etcd”；本记录不重复安装教程。
