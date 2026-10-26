# Alpine 源码镜像与独立 Docker 环境验收

> 历史验证记录：下文 IP、路径和最终环境状态均为测试当时的证据，不是部署默认值。新部署按[部署手册](html/user/docker-deploy.html)填写实际部署服务器 IP。

日期：2026-10-25。服务器：10.8.126.103。参考组网：`/home/alexan/vxlan-s-evpn`。

## 交付与当前状态

- 三个项目均有 `docker/Dockerfile`、专用构建上下文排除规则和使用文档。
- 从源码构建四个 Linux/amd64 镜像：`sdn-agent:alpine`、`sdn-controller:alpine`、`sdn-web-gateway:alpine`、`sdn-web-ui:alpine`。没有复制宿主机预编译二进制或 node_modules/dist 代替源码构建。
- 自有运行镜像基于 Alpine 3.22；实测 Agent 为 Alpine 3.22.6、FRR 10.2.2，包含 frr-reload.py。Web UI/Gateway 是独立容器，Controller/Gateway/UI 使用非 root 运行用户。
- 独立 Compose 项目 `sdn-docker`。Web 为 `http://10.8.126.103:28080`，Controller 宿主机接口为 `127.0.0.1:29090`。
- 全部测试结束后，已删除并重建本项目的容器和数据卷，重新接线，保留空白服务供手动测试。镜像、凭据和测试证据保留。
- 原有 88 个容器（含停止的容器）的 ID、启动时间和运行状态与开始前一致；未运行原项目 lab.sh，也未修改其配置或容器。

## 镜像与组网

| 镜像 | 构建 | 运行 |
|---|---|---|
| Agent | Go 源码、多阶段构建 | Alpine + apk FRR/frr-pythontools + Agent；独立身份/FRR 卷 |
| Controller | Go 源码、多阶段构建 | Alpine + Controller；独立 etcd 持久卷 |
| Gateway | Go 源码、多阶段构建 | Alpine + HTTP/gRPC Gateway |
| UI | Node 24 + pnpm 10.33.4，锁定依赖、单测、类型检查和 Vite 构建 | Alpine + Nginx，反向代理 Gateway，支持 SSE |

数据库采用独立 etcd 容器和每节点独立 Redis 容器（AOF always）。测试采用参考项目的四对 veth、Spine/Leaf/PC 接口名和对称 IRB 地址规划；PC 无管理接口，业务口保持 eth0。管理网络使用本项目单独的 Docker bridge。

镜像 ID/大小见 [image-manifest.txt](../artifacts/docker/image-manifest.txt)。构建日志：[Agent](../artifacts/docker-agent-build.log)、[Controller](../artifacts/docker-controller-build.log)、[Gateway](../artifacts/docker-gateway-build.log)、[UI](../artifacts/docker-ui-build.log)。实测系统版本见 [environment.json](../artifacts/docker/environment.json)。

## 测试结果

**17 项网络/API 检查、10 项真实浏览器检查全部通过；清空后的 API、网络状态与浏览器只读检查通过。**

网络/API 覆盖：

- 三个真实 Agent 注册和显式纳管；Nginx → Gateway → Controller → Agent 完成 Linux/FRR 配置。
- Spine 两个 BGP EVPN 会话建立；主机 Type-2 MAC/IP、Type-3 IMET 学习；两台 Leaf 识别 L3VNI 50000。
- PC1/PC2 跨 Leaf 双向通信，最终记录各 3/3 收包、0% 丢包；Controller 停止期间双向流量也为 0% 丢包。
- 管理员/只读权限、未认证拒绝、Commit 幂等、SSE 有序重放、拓扑观测和配置版本冲突。
- 临时租户/Bridge/VXLAN 创建和完整删除；真实接口故障导致 MTU 回滚，随后显式修正并重新收敛。
- Controller/Gateway 重启后配置、节点身份和任务仍在；管理断开时 Agent 报 DOWN，恢复后 RUNNING。

MTU 故障提交到重新通过转发检查共记录 12.49 秒，包含执行、回滚、收敛与探测，不是精确断流时间，不代表无损回滚。

浏览器使用 Chromium 155，覆盖登录、节点能力、期望配置、JSON 候选编辑/预览/提交、409 冲突恢复、任务/SSE、刷新重新认证、G6 拓扑、只读权限和不存在任务的报错。没有未捕获页面异常；控制台中的 409 来自有意制造的冲突检查。

首次浏览器验收发现 Nginx 使用 `$host` 丢失外部端口，导致 Gateway same-origin 检查拒绝配置提交。已改为转发 `$http_host`，重建并更新本项目 UI 容器后，重跑全部 10 项浏览器检查通过。保留 [首次失败记录](../artifacts/docker/browser-attempt1-results.json)，没有绕过 Origin 校验。

完整结果：[网络/API](../artifacts/docker/integration-results.json)、[浏览器](../artifacts/docker/browser-results.json)、[控制台](../artifacts/docker/browser-console.json)、[PC1 ping](../artifacts/docker/pc1-ping.txt)、[PC2 ping](../artifacts/docker/pc2-ping.txt)。

## 清空后的验收

`python3 docker/manage.py reset` 校验资源所属项目后，只清理本项目的容器及命名卷，再启动空白服务并创建四对 veth。当前：

- 3 个节点 RUNNING、未纳管，Agent revision=0。
- Controller desired revision=0，nodes/tenants/networks 目标配置为空，Job 为空。
- Agent 没有租户 VRF、业务 Bridge/VXLAN、OSPF/BGP 配置和业务 IP；PC eth0 无地址。
- Web、Gateway、Controller、Agent、数据库运行正常；临时接线 helper 已删除。
- 凭据不变；新节点 UUID 已写入新的 `docker/state/evpn.json`，手动测试请使用该文件。

证据：[clean-state.json](../artifacts/docker/clean-state.json)、[只读浏览器结果](../artifacts/docker/manual-ready-browser.json)、[空白节点页](../artifacts/docker/manual-ready-nodes.png)、[空白配置页](../artifacts/docker/manual-ready-desired.png)、[空任务页](../artifacts/docker/manual-ready-jobs.png)、[其他容器前后快照](../artifacts/docker/unrelated-after.json)。

## 复现和手动操作

详见 [docker/README.md](../docker/README.md)。主要命令：

```sh
cd ~/program/github/sdn-controller
python3 docker/manage.py init
python3 docker/manage.py build
python3 docker/manage.py up
python3 docker/test.py
# 浏览器测试完成后，恢复空白服务
python3 docker/manage.py reset
python3 docker/manage.py clean-check
```

网页登录凭据在 `docker/state/tokens.json` 的 admin/viewer 字段。本报告不包含 token。打开 Web 纳管节点后，可将 `docker/state/evpn.json` 粘贴到候选编辑器，预览和提交；PC 配置由显式 `python3 docker/manage.py hosts` 完成。

这次验证是单机隔离 IPv4 EVPN 实验，不覆盖 HA、断电恢复、规模性能、所有 FRR 版本或 IPv6。命名空间重建后自动恢复全部 Linux 配置仍属于待办；不能直接重启 Leaf 容器并假定业务已恢复。
