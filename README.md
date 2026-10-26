# sdn-controller

`sdn-controller` 是用 Go 编写的**全网配置与任务协调服务**。它接收 Web Gateway、CLI 或自动化客户端的配置请求，保存期望配置，将变更编译成各节点的执行计划，再通过 gRPC 交给 `sdn-agent` 管理本地数据面。当前 Agent 提供 Linux 和 FRR 适配器；Controller 负责全局模型、版本冲突、任务推进和持久恢复。

北向使用版本化 Protobuf，Web 和 CLI 共享同一接口，关闭浏览器不会取消已受理任务。项目采用单写入存储；多节点提交可以部分成功，当前没有跨节点原子事务或自动多实例接管。扩展数据面由 Agent 实现统一操作和能力，Controller 按能力生成计划，开发入口见[扩展流程](docs/developer/extensions.rst)。

从这里部署软件：[源码编译、安装与启动](docs/user/source-install.rst) · [源码镜像与 Docker 部署](docs/user/docker-deploy.rst) · [版本发布、交付与升级](docs/user/release.rst)。

默认 Controller 镜像现已集成 etcd；各 Agent 镜像各自集成 Redis / FRR。`controller.conf` 是严格 JSON，可通过 `-config` 加载，也可从容器外只读挂载覆盖 `/etc/sdn-controller/controller.conf`，其中同时配置 Controller 和内置 etcd。详见 [配置文件与挂载教程](docs/user/configuration-file.rst)。CLI 参数覆盖文件，现有 token 环境变量覆盖文件中的 token；不传 `-config` 的原生启动方式保持兼容。

完整文档：[用户手册源码](docs/user/index.rst) · [开发手册源码](docs/developer/index.rst)。先运行 `make -C docs help` 查看帮助，安装依赖后执行 `make -C docs html`，打开 `docs/html/index.html`。`help` / `html` / `clean` 只管理手册，**不是编译或安装软件**；独立依赖见[文档构建说明](docs/README.md)。

源码 Alpine 镜像、独立 Compose 环境、参考 VXLAN-S-EVPN 的组网和手动测试步骤见 [docker/README.md](docker/README.md)。Dockerfile 位于 `docker/Dockerfile`；新环境 Web 使用 28080，不影响旧实验的 18080。

各阶段的历史镜像、资源及可靠性验收见[验证记录索引](docs/developer/testing.rst)。记录仅对应其注明的版本和环境；更新文档不表示重新部署或重新完成真实网络验收。

## 当前实现

- 节点注册、UUID 冲突分配、实例心跳校验、INIT/RUNNING/DOWN 和显式纳管。
- 完整结构化配置：NodeProfile（接口、地址、系统选项、OSPF、BGP/RR）、Tenant（VRF/L3VNI）、Network（BD/L2VNI、网关和接入口）。
- 逐节点 `nodeResources`：独立 VRF、BD、VXLAN、地址与成员、静态路由、L3VNI 绑定、BD ARP/ND 抑制策略；BGP IPv4/IPv6/EVPN Type-5 与 OSPFv2/v3。命名 CLI 与 Web 共享候选和计划，见[逐资源操作](docs/user/resources.rst)。
- `GetNodeResources` / `EditNodeResources` / `ImportNodeResources` / `QueryNode`：现有 Agent 日志经折叠及真实状态验证后显式导入，查询支持结构化数据与能力检查。详细[模型和事务](docs/developer/resources.rst)。
- BeginChange → EditChange → PlanChange → CommitChange；候选编辑版本和全局版本校验，持久化幂等收据。
- 编译 Agent typed op，所有节点先 Stage，再按确定性节点顺序 Commit；每个节点的步骤显式依赖。
- 任务、事件重放、结果查询、成功 Applied 基线、未知 Commit 结果查询/重试和 Controller 重启恢复。
- 按节点保留任务执行权；互不重叠的 Job 轮流推进。支持持久化 `RetryRollback`，仅在 Agent 验证 Undo 后解除该节点的回滚屏障。
- etcd 或开发 File 持久化；默认 STOP/PARTIAL 跨节点失败策略。Agent 本地失败回滚由 Agent 执行。
- 原始观测缓存，FRR 10 EVPN Type-2/Type-3 解析，配置拓扑和带来源的 VTEP/MAC/IP 关系。未知或模糊网络映射保留路由上下文。
- admin/viewer/agent 三种独立 token，在 Controller 侧进行 RPC 授权。

## 从源码编译和安装

需要 Linux 和 Go 1.25+。当前源码工作区需保留相邻 `sdn-agent` 项目，仅依赖其公开生成协议，未引用其 internal 包。正式发布独立协议模块是后续工作。

```sh
cd ~/program/github/sdn-controller
go version # 必须为 1.25 或更高版本
go mod download
mkdir -p bin
CGO_ENABLED=0 go build -o bin/sdn-controller ./cmd/sdn-controller
CGO_ENABLED=0 go build -o bin/sdnc ./cmd/sdnc
sudo install -m 0755 bin/sdn-controller /usr/local/bin/sdn-controller
sudo install -m 0755 bin/sdnc /usr/local/bin/sdnc
sdn-controller -h
sdnc -h
```

项目没有 `make install`，上面显式安装两个程序。也可用 `make build` 代替两条 go build。Go 的安装、非 root 运行、配置文件位置、持久存储和 systemd 示例见 [源码安装教程](docs/user/source-install.rst)。生成后的静态程序运行时不需要 Go。

`api/controller/v1/*.pb.go` 已提交，不要求用户安装 protoc 才能构建。协议变化时重新生成：

```sh
protoc -I. -I../sdn-agent --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/controller/v1/controller.proto
```

## 启动

通过环境设置三个不同且至少 16 字节的随机凭据。`SDN_AGENT_TOKEN` 必须与各 Agent 的 `SDN_TOKEN` 一致；Web Gateway 透传用户 admin/viewer token，不使用 Agent token。

下列令牌生成只在新部署时执行；将值保存到专用的权限 0600 环境文件，重启加载相同值。完整的“只在文件不存在时生成”示例和 systemd EnvironmentFile 用法见源码安装教程，不要每次启动都轮换令牌。

```sh
export SDN_ADMIN_TOKEN="$(openssl rand -hex 24)"
export SDN_VIEWER_TOKEN="$(openssl rand -hex 24)"
export SDN_AGENT_TOKEN="$(openssl rand -hex 24)"

# 隔离实验：etcd 必须已启动，且使用此 Controller 独占的 key 前缀。
./bin/sdn-controller -listen :19090 -etcd 'http://<etcd服务器IP>:2379' \
  -etcd-prefix /sdn/lab/controller -insecure

# 开发用文件存储（二选一）
./bin/sdn-controller -listen :19090 -state-file ./controller-state.pb -insecure
```

正式 TLS 传输使用 `-tls-cert`、`-tls-key` 和出站 `-agent-ca`，CLI 使用 `-ca`。不指定 TLS 时必须显式 `-insecure`。etcd 示例使用隔离网络；当前启动参数尚未提供 etcd 客户端证书、账号和密码管理。

Agent 启动时设置 `-controller controller:19090 -endpoint leaf1:9090`。Controller 必须可以解析并连接此 endpoint；Docker 实验应将 Controller 和 Agent 放在同一个管理网络。首版不会自动接管已有的其他 Controller/Agent 实例。

上面的 `controller` / `leaf1` 是容器网络 DNS 名。源码分机部署应改为 `-controller '<部署服务器IP>:19090' -endpoint '<Agent节点管理IP>:9090'`。Controller 默认 gRPC 19090；Docker 只将它发布到宿主机 `127.0.0.1:29090`。完整 Docker Web 地址是 `http://<部署服务器IP>:28080`，请替换为实际部署主机管理 IP；不要替换 loopback 或 Docker DNS 名。

前台 Controller 用 Ctrl+C 停止。systemd 安装及 `systemctl start/stop`、运行检查、TLS 参数见 [源码安装教程](docs/user/source-install.rst)。停止 Controller 不会删除数据库或主动清除 Agent 网络。

## 配置实验网络

所有 CLI flags 放在子命令前。输出采用 protojson：字段使用 lowerCamelCase，uint64 编码为十进制字符串。

下面在 Controller 所在主机执行；源码部署使用 19090，若连接本项目 Docker 映射，将 SDNC_ADDR 改为 127.0.0.1:29090。

```sh
export SDNC_ADDR=127.0.0.1:19090
./bin/sdnc -addr "$SDNC_ADDR" -insecure nodes > nodes.json

# 对每个节点执行。expected revision 可由 nodes 的 agentRevision 查看。
./bin/sdnc -addr "$SDNC_ADDR" -insecure -node NODE_UUID -revision 0 adopt

# 通过登记 endpoint 中的 spine/leaf1/leaf2 将示例别名替换为真实 UUID。
python3 examples/render_lab.py nodes.json > evpn.json
./bin/sdnc -addr "$SDNC_ADDR" -insecure -file evpn.json plan
./bin/sdnc -addr "$SDNC_ADDR" -insecure -file evpn.json submit

./bin/sdnc -addr "$SDNC_ADDR" -insecure -refresh topology
```

`examples/evpn.json` 描述一个 Spine RR、两个 Leaf、TENANT-A、L3VNI 50000 和 L2VNI 10010/10020。它配置节点网络与 FRR；Docker veth 布线和 PC 地址由实验夹具负责。通过普通主机流量产生 ARP/MAC 学习，再独立验证 BGP 会话、Type-2/Type-3 和跨网段 ping。

显式纳管表示允许后续管理指定资源，不会自动导入或删除节点已有配置。Agent Running revision=0 也不能证明 Linux/FRR 没有人工配置。`-allow-existing` 只确认接受已有 Agent revision，不能将人工配置转换为 Controller 的受管基线。受管节点不能通过重复纳管掩盖版本漂移。

## Web / SDK 对接

公共包：`sdn-controller/api/controller/v1`，服务名 `sdn.controller.v1.North`。

| RPC | 语义 |
| --- | --- |
| ListNodes / GetNode | 登记、角色、能力、管理状态、Agent revision |
| AdoptNode | 显式纳管并确认 Agent 基础 revision |
| GetConfiguration | 当前完整 Desired 和 revision |
| GetNodeResources / QueryNode | 节点受管目标 / 限时转发到 Agent 的结构化实际状态查询 |
| EditNodeResources / ImportNodeResources | 候选内编辑节点资源 / 回读验证后显式导入已有 Agent 日志 |
| BeginChange | 从指定 base_revision 建立候选；request_id 幂等 |
| EditChange | 用完整 Configuration 替换候选；校验 edit_revision |
| PlanChange | 校验和预览确定性计划；返回 plan_id |
| CommitChange | 复核版本和计划，原子发布 Desired 与 Job；受理不等于执行成功 |
| AbortChange | 取消尚未提交的候选 |
| GetChange | 候选状态与最终 job_id |
| ListJobs / GetJob | 任务查询；列表省略大计划和事件，详情返回完整记录 |
| WatchJob | 按 after_sequence 重放 JobEvent；终态后关闭流 |
| RetryRollback | 对指定失败节点持久化恢复请求，重试原 Agent Undo |
| GetTopology | 缓存查询；refresh=true 进行有界主动采集 |

Gateway 将 HTTP bearer 原样传递给 Controller；Controller 根据令牌识别角色，不信任请求携带的角色字符串。Viewer 可查询（含刷新观测），不能纳管、编辑、计划或提交。Agent token 仅可 Register/Heartbeat。

要精确控制事务，可用 CLI 调用任意 North unary RPC：

```sh
./bin/sdnc -insecure -method BeginChange -file begin.json rpc
./bin/sdnc -insecure -method CommitChange -file commit.json rpc
./bin/sdnc -insecure -job JOB_UUID watch
./bin/sdnc -insecure -job JOB_UUID job
```

超时后不要重新生成 request_id；重试原始 CommitChange 的相同消息可找回原 Job。`sdnc submit` 是交互便利命令；生产自动化应保存完整 Begin/Commit 请求和 task/job ID 后使用 RPC。CLI 总超时不会取消已受理任务。

## 持久化和故障语义

首版 repository 使用 **单个 Protobuf 数据库快照**，etcd 的 `PREFIX/snapshot` 原子 CAS，`PREFIX/writer` 租约用于排除同时启动的写入者。File 模式使用 flock、fsync 和 rename。etcd 主存储和 Agent Redis 之间不建立主从复制。

Controller 本身提供 gRPC，不保存 Web 的 HTTP 响应缓存。Desired / Job 查询读取已持久化后发布的内存数据库；`QueryNode` 每次校验能力并转发 Agent；`GetTopology(refresh=false)` 读取进程内的观测缓存，重启后需重新采集。持久幂等收据记录请求哈希与结果 ID，用于找回同一 Change / Job，不是第一次响应内容的缓存。字段和数据归属见[存储设计](docs/developer/storage.rst)。

- 快照上限 1 MiB；新写入预留至少 256 KiB，并投影在途任务最终 Applied 的大小。空间不足返回 ResourceExhausted；历史、幂等收据不自动删除。
- 最多登记 64 节点，配置最多 128 租户和 256 网络。实际容量还受 1 MiB 约束。首版适用于实验和小规模验证。
- 每任务最多保留最新 128 个事件；过期或超前游标返回 OutOfRange，客户端重新 GetJob 并从当前 sequence 订阅。任务和幂等结果仍保留。
- 心跳更新快照，未采用设计中的分对象 key 空间。大规模部署前应迁移 Repository 的布局和历史归档策略。
- Commit 意图先持久化再调用 Agent。之后任何 RPC 错误均保留 UNKNOWN，直到查询到明确 Task 结果；不会把超时当作回滚成功。
- etcd 写入响应丢失时，在独立的有界恢复窗口中重试相同 CAS 并同时读取快照和写入者身份；核实成功后同步本地 revision。无法核实或发现不同写入者/不同数据时停止服务，由运维或进程管理器重新启动并加载持久状态，不盲目覆盖。
- 默认失败后停止后续节点。部分成功返回 PARTIAL，保留实际 Applied；用户可基于当前 Desired 新建 Change 重试尚未成功的节点。
- **没有跨节点自动补偿 API，也没有分布式原子事务。** 回退已成功配置需要用户提交明确的新目标配置。
- UNKNOWN 保留原 Job 的节点执行权；ROLLBACK_FAILED 隔离失败节点。保留这些节点期望配置不变时，可提交无关节点的变更。新计划只对本次未被占用的节点负责，不能把其成功理解为其他旧 Job 已完成。调度仍是单 Worker 轮询，每次 RPC 有超时，不是无限并行执行。
- 回滚失败可通过 North `RetryRollback` 重试原 Undo；失败记录不删除，不强制解锁，也不自动继续原正向计划。成功恢复后重新预览、提交明确的新目标。CLI 和限制见 [恢复操作](docs/recovery.md)。
- Lease 丢失立即停止 Controller 服务和 worker；这不等于 Agent 端 fencing。禁止自动多实例故障接管，必须确认旧写入进程已退出。

持久化现状、主机重建后的恢复缺口和后续任务统一记录在 [待解决问题](docs/backlog.md)。可靠性回归命令见 [恢复操作](docs/recovery.md)。

## 当前支持边界

支持添加/删除租户、网络、placement、网关地址和端口绑定；配置差异转换为 Agent op。支持接口显式属性、BGP peer 与 EVPN RD/RT 等更新。VXLAN/VRF/L3VNI 身份、受管 ASN/router-id 等原地变化明确拒绝，需要规划显式拆除/重建。节点 profile 移除也暂不支持。移除 profile 中物理接口描述会移除受管地址/协议引用，保留物理接口的 UP/MTU/MAC 状态。

旧 NodeProfile 模型的 configureSystem 针对 IPv4 EVPN 实验（包括禁用 IPv6），不能直接用于 IPv6 目标。逐节点 nodeResources 已暴露 IPv6 地址/路由、BGP IPv6、OSPFv3 和 EVPN Type-5；具体范围、前置条件和验证边界见[逐资源操作](docs/user/resources.rst)。静态 MAC/邻居保活、交换机/VPP 适配器、FPM/ZAPI、LLDP 物理拓扑、租户级用户权限及自动 reconciler 尚未实现。Controller 使用 Agent GetTask 轮询跟踪进度，未使用 Agent WatchTasks 消费；北向 WatchJob 从 Controller 持久化事件重放。

观测缓存可重建，Controller 重启后需要 refresh。单次刷新总上限 20 秒、每节点 5 秒、最多并发 4 个采集；并行刷新被拒绝。采集失败保留旧数据并标 stale。拓扑查询最多返回 2000 条 EVPN 路由，原始观测响应总预算 512 KiB；被省略数据明确警示。实际物理连接不能根据 Type-2/Type-3 推导。

## 验证

```sh
go test -race ./...
go vet ./...
# 真实 etcd 独立集成检查；普通测试不要求 Docker。
SDNC_TEST_ETCD=http://127.0.0.1:12379 go test -race -v ./internal/repository
```

模拟 Agent 用例覆盖真实 gRPC 调用、幂等、跨节点失败、丢失 Commit 响应、权限变化后的未知结果、重启恢复和事件重放。模拟协议测试不等于验证转发；完整集成测试另行使用独立 Docker 网络和真实 Linux/FRR。

历史 [完整验收记录](docs/validation.md) 记录 11 个源码检查阶段、17 项真实网络/API 检查、10 项浏览器检查通过。该记录对应旧 `sdnc-it` 实验；复现与清理命令见 [独立实验说明](lab/README.md)，其 Web 使用 `http://<部署服务器IP>:18080`，凭据在 `lab/state/tokens.json`。本项目源码镜像 Compose 使用 `http://<部署服务器IP>:28080` 和 `docker/state/tokens.json`，两套环境不要混用。
