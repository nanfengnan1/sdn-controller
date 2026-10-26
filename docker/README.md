# 源码 Docker 构建、隔离实验与手动测试

三个项目的 Dockerfile 均在各自 `docker/` 中。构建阶段从 Go/Vue 源码生成程序；自有服务运行镜像都基于 Alpine 3.22。每个 Agent 容器运行 Agent + FRR + Redis，Controller 容器运行 Controller + etcd；Web 容器运行独立的 Nginx 和 Go Gateway 两个进程。Redis / etcd 不再部署为独立容器。

## 启动

需要 Linux rootful Docker、Docker Compose 插件和 Python 3；执行用户应有访问 Docker daemon 的权限。默认 Compose 项目名 `sdn-docker`，只管理 `sdn-docker-*` 容器和对应命名卷，不调用其他实验项目的脚本。参考项目 `~/vxlan-s-evpn` 不需要安装，sevpn、sdna-e2e、sdnc-it 环境不是部署依赖。

先把 `sdn-agent`、`sdn-controller`、`sdn-web` 放在同一父目录（示例 `~/program/github/`）；宿主机不需要预装 Go/Node/pnpm，编译发生在 Docker build 阶段。完整的依赖、源码镜像构建、安装、地址及停止流程见 [Docker 部署手册](../docs/html/user/docker-deploy.html)（[源文件](../docs/user/docker-deploy.rst)）。检查依赖：

```sh
docker version
docker compose version
python3 --version
```

```sh
cd ~/program/github/sdn-controller
python3 docker/manage.py init
python3 docker/manage.py build
python3 docker/manage.py up
python3 docker/manage.py status
python3 docker/manage.py bundled-check
docker logs --tail 50 sdn-docker-controller
docker exec sdn-docker-controller sdnc -insecure nodes
```

`build` 实际从源码编译 Agent、Controller、Web 三个自有镜像，`up` 使用已构建镜像。全新安装应看到三个 RUNNING 节点但未纳管、未配置业务；继续下方手动测试。启动过程不需要已有业务配置。

默认常驻 **5 个容器**：controller、web、spine、leaf1、leaf2，前缀均为 `sdn-docker-`。Web 内的 Nginx/Gateway 分别运行，任一进程退出会终止 Web 容器。接线 helper 只临时运行。`bundled-check` 检查这一布局、内置数据库就绪、只读配置挂载和数据库持久卷。

PC1/PC2 是可选 `lab` profile，不属于核心部署。`python3 docker/manage.py lab-up` 启动 5 个核心容器及两台测试 PC，共 7 个；`lab-stop` 停止测试 PC。`up` 会停止正在运行的测试 PC，仅保留核心服务；停止 PC 会移除与其相连的测试 veth。复用旧部署时 `up` 会停止并移除本项目原独立 Gateway 容器，数据库卷与身份卷保留。

只构建 Controller：

```sh
cd ~/program/github
docker build -f sdn-controller/docker/Dockerfile -t sdn-controller:alpine .
```

Dockerfile 专用 ignore 文件将上下文限制在所需源码，排除凭据、运行数据库、编译产物和 node_modules。版本化基础镜像 tag 可通过 build args 替换；严格复现发布应固定 digest。

## 地址、凭据与持久卷

- Web：`http://<部署服务器IP>:28080`，占位符换成 Docker 主机可达管理 IP；本机访问 `http://127.0.0.1:28080`。
- Controller gRPC：宿主机 `127.0.0.1:29090`；管理网内 `controller:19090`。
- 凭据：`docker/state/tokens.json` 的 `admin` / `viewer` 字段用于网页登录；Agent 使用独立 `agent` 凭据。文件和 `docker/.env` 不进入镜像，权限为 0600。
- 每个 Agent 在本容器的 `127.0.0.1:6379` 运行 Redis（AOF always），并持久挂载 Redis、身份与 FRR 配置目录；Controller 在本容器 `127.0.0.1:2379` 运行 etcd 并挂载持久卷。数据库不发布到管理网或宿主机。
- Compose 默认是隔离实验的明文模式。正式远程部署应使用项目已有 TLS 配置。

`init` 不轮换已有凭据，也不会覆盖已有宿主配置文件；`reset` 保留凭据和宿主配置，但重建本项目的数据卷和节点身份。

## 从容器外配置服务

`python3 docker/manage.py init` 根据各镜像的默认配置生成缺失文件：

| 宿主机文件 | 只读挂载目标 | 内容 |
| --- | --- | --- |
| `docker/state/config/controller/controller.conf` | `/etc/sdn-controller/controller.conf` | `controller` 设置与内置 `etcd` 设置 |
| `docker/state/config/spine/agent.conf` | `/etc/sdn-agent/agent.conf` | Spine Agent 与内置 Redis |
| `docker/state/config/leaf1/agent.conf` | `/etc/sdn-agent/agent.conf` | Leaf1 Agent 与内置 Redis |
| `docker/state/config/leaf2/agent.conf` | `/etc/sdn-agent/agent.conf` | Leaf2 Agent 与内置 Redis |

文件扩展名为 `.conf`，内容是严格 JSON；不支持注释、未知字段或错误字段类型。宿主文件可以在启动前编辑，容器中挂载为只读。`init` 再次执行时保留已有内容，因此自定义参数不会被生成器覆盖。更改配置需要重启对应容器，当前不是热加载。

配置优先级是显式 CLI 参数 > 已设置的令牌环境变量 > 配置文件 > 内置默认值。Compose 不再用 CLI 覆盖 controller/endpoint/数据库地址，令牌仍从 `docker/.env` 注入；不要把示例密钥写进镜像。需要用文件配置令牌时，还要移除对应环境覆盖。

Controller 文件的 `etcd.dataDir` 默认为 `/etcd-data`，Agent 文件的 `redis.dataDir` 默认为 `/var/lib/redis`；对应命名卷为原来的 `sdn-docker_etcd-data` 和 `sdn-docker_redis-{spine,leaf1,leaf2}`。不要只改 dataDir 却漏改持久卷挂载。内置数据库只绑定回环地址；调整数据库端口时还要保证应用连接地址与之匹配。

这些是进程启动配置，不是 Web 的租户网络 JSON。Agent 的 nodeId 由首次启动和注册获得并保存；Web 的 `nodes[].nodeId`、`tenants[].placements[].nodeId`、`networks[].placements[].nodeId` 必须引用实际注册 UUID。生成的 `docker/state/evpn.json` 已填好本轮身份。

## 从旧独立数据库容器迁移

先关闭旧的本项目容器，并备份 `etcd-data`、三个 Redis 卷、三个 Agent 身份卷及 FRR 配置卷。当前脚本保留旧卷名并改变挂载归属，不自动导出或转换数据库格式。

`up` 会检查所有目标资源的归属，只移除已停止且属于本项目的旧 `etcd` / `redis-*` 容器，不删除这些卷；若旧数据库容器仍运行则拒绝迁移，防止两个数据库进程同时使用同一卷。未知的同项目容器也会阻止迁移和 reset，需先人工核对。

数据库卷升级不代表 Linux 网络自动恢复。停止或重建 Agent 容器可能使原 veth、VRF、Bridge、VXLAN 消失；旧数据的 revision 必须与实际网络一起核对。不要直接 reset 有业务的环境来绕过恢复问题。

首次启动前可在 `docker/.env` 修改 `SDN_WEB_PORT` / `SDN_GRPC_PORT`；保留令牌行。Web 默认监听 `SDN_WEB_BIND=0.0.0.0`，不是把该值当作浏览器地址。管理脚本使用宿主机 127.0.0.1 探测 Web，因此不要直接把 bind 改成部署 IP；设为 127.0.0.1 会限制为本机访问。Controller 的 29090 只绑定 loopback，外部不能直接访问 `<部署服务器IP>:29090`。`controller:19090`、`leaf1:9090` 等是容器 DNS 名，不需要替换为部署 IP。

## 参考组网

参考 `~/vxlan-s-evpn` 的精简对称 IRB，不运行或依赖原项目脚本。接口、业务地址及 VNI 规划保持一致，另加独立 Docker 管理网络供 Agent/Controller 通信：

```text
              sdn-docker-spine
               swp1    swp2
                |        |
              uplink   uplink
       sdn-docker-leaf1  sdn-docker-leaf2
              host10    host20
                |        |
               eth0     eth0
        sdn-docker-pc1  sdn-docker-pc2
```

目标：Spine 10.255.0.1；Leaf1/2 10.255.0.11/12；Underlay 为 10.0.1.0/30、10.0.2.0/30；PC1/2 为 192.168.10.11/24、192.168.20.12/24。租户 TENANT-A，VRF table 1001，L2VNI 10010/10020，L3VNI 50000，L3 RT 65000:1。业务依靠 EVPN Type-2/Type-3，不配置静态远端 VXLAN/FDB 或 Type-5。

`up` 启动核心服务并建立两对 Spine/Leaf veth；只有 `lab-up` 才增加两对通往 PC 的 veth。辅助接线容器只对校验归属后的新容器 PID 操作，用完即删除。PC 没有 Docker 管理接口，因此业务接口可以保持与参考项目一致的 eth0。完整 `evpn.json` 示例引用 host10/host20，执行该流量实验前需要 `lab-up`。

## 空白环境的手动测试

1. 执行 `python3 docker/manage.py lab-up` 准备本文流量实验；只测试资源创建及查询时使用默认五容器即可。打开 Web，输入 `docker/state/tokens.json` 中的 admin 值。
2. 节点页面应有 3 个 RUNNING、尚未纳管的节点；逐个纳管。
3. 创建候选，粘贴 `docker/state/evpn.json` 的完整配置。该文件已使用本轮真实 node UUID；reset 后需使用新生成文件。
4. 保存候选、预览计划、提交并观察任务。
5. 宿主机执行以下命令配置两台 PC 并产生学习流量：

```sh
cd ~/program/github/sdn-controller
python3 docker/manage.py hosts
docker exec sdn-docker-pc1 ping -c 3 192.168.10.254
docker exec sdn-docker-pc2 ping -c 3 192.168.20.254
docker exec sdn-docker-spine vtysh -c 'show bgp l2vpn evpn'
docker exec sdn-docker-pc1 ping -c 3 192.168.20.12
docker exec sdn-docker-pc2 ping -c 3 192.168.10.11
```

如果选择直接 ip/vtysh 手工配置，可进入 `sdn-docker-*` 容器操作；该方式不会同步 Controller 的受管配置基线，测试两种方式前应 reset，避免混合管理。

## 停止和恢复管理服务

只暂停管理服务、保留 Agent 的业务命名空间：

```sh
docker compose --project-name sdn-docker --env-file docker/.env -f docker/compose.yaml stop web controller
docker compose --project-name sdn-docker --env-file docker/.env -f docker/compose.yaml start controller web
python3 docker/manage.py status
```

若执行整个项目的 Compose `stop` 或重建 Agent 容器，卷会保留，但 Linux 网络命名空间可能丢失。当前没有完整自动重建，不能把再次 up 当成无损恢复。`manage.py` 没有 stop/down 子命令；使用原生 Compose 停止，明确清空时才使用下方 reset。

## 自动验收与清空

```sh
python3 docker/test.py
# 完成浏览器验收后，清空本次环境，恢复为空白服务：
python3 docker/manage.py reset
python3 docker/manage.py clean-check
```

`test.py` 只接受 revision 0、空任务的全新环境，随后会实际配置业务。验证 Web/API、OSPF/BGP/EVPN、双向 ping、资源增删、失败回滚和管理进程重启。浏览器用例在 `sdn-web/tests/e2e/browser.cjs`，支持 `SDN_WEB_URL`、`SDN_TEST_TOKENS` 和 `SDN_TEST_ARTIFACTS` 指定本次环境。

`reset` 删除并重建 **仅本 Compose 项目** 的容器和非 external 数据卷，清除配置、Candidate、Job、Agent 日志、旧身份和 FRR 配置，重新接线并注册节点。保留镜像、凭据、宿主 `.conf` 和验收报告，不删除其他 Docker 项目的资源。结束时 3 个节点未纳管、revision=0、无任务、无租户/Bridge/VXLAN/OSPF/BGP 业务配置，等待手动测试。

命名空间重建会使 veth 消失；当前 Agent 不自动恢复完整 Linux 网络。不要把保留 FRR/数据库卷当作无损重启保证。部分接线缺失时 `wire` 会拒绝覆盖现有接口；使用本项目 reset 重建空环境。

验收产物位于 `artifacts/docker/`，构建日志位于 `artifacts/docker-*-build.log`，最终空白状态为 `artifacts/docker/clean-state.json`。

旧版独立数据库容器的历史结果见 [Docker 验收报告](../docs/docker-validation.md)：该次 17 项网络/API、10 项浏览器及清空后的只读检查通过。内置数据库版本需重新构建并验证；运行时是否启动或停止，以实际服务状态为准。

## 独立资源接口回归环境

维护者可以在不修改上述部署的情况下构建独立测试镜像，使用 `sdn-resource-it` 项目、38080/39090 端口和单独的数据卷：

```sh
python3 docker/resource_lab.py build
python3 docker/resource_lab.py lab-up
python3 docker/resource_lab.py legacy-test
# 仅重建隔离测试项目的空白环境，再测试逐项资源配置：
python3 docker/resource_lab.py lab-reset
python3 docker/resource_lab.py resource-test
# 浏览器验证完成后清理隔离环境：
python3 docker/resource_lab.py down
```

资源回归包含既有 Agent 配置导入、空 VRF、BD/地址/VXLAN/L3VNI 分步配置、双栈 OSPF/BGP、Type-5、真实流量和失败回滚。测试写入业务配置，仅用于独立测试项目。证据写入 `artifacts/resources/`，测试凭据保存在 `docker/state/resource-lab/state/tokens.json`，不要发布这些凭据文件。
