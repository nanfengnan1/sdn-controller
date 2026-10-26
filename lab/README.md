# 独立端到端实验

此夹具只管理精确标签 `sdn.integration=sdnc-it` 的容器和网络。它创建独立
Controller、Gateway、etcd、Redis、Spine、两个 Leaf、两个 PC 和布线 helper，
不会使用或修改现有 `sevpn-*`、`sdna-e2e-*` 环境。

需要 Linux、Docker、Python 3，以及已构建的相邻 `sdn-agent`、`sdn-web` 项目。
Controller 和 Gateway 的容器可执行文件必须使用 `CGO_ENABLED=0` 构建；UI 使用
`sdn-web/ui/apps/web-ele/dist`。Go/Vue 构建方式见各项目 README。

```sh
cd ~/program/github/sdn-controller
# 初次运行；如果已存在同标签实验，会拒绝覆盖。
python3 lab/run.py

# 只检查 BGP、普通主机 ARP 学习、双向 ping、EVPN 和 L3VNI。
python3 lab/run.py --verify-only

# 源代码回归：Agent/Controller/Gateway race、vet/build、Vue 测试/类型/构建。
# 使用实验独立 etcd 的宿主机回环端口，测试自行使用隔离 key 前缀。
SDNC_TEST_ETCD=http://127.0.0.1:12380 python3 lab/regression.py

# 明确清理本实验标签的容器/网络，然后从全新状态重跑。
python3 lab/run.py --cleanup
python3 lab/run.py
```

`run.py` 通过 HTTP `/api/v1` 执行显式纳管、完整资源候选编辑、预览和提交。
只有 veth 布线与 PC 地址是夹具直接初始化；Spine/Leaf 的 Linux/FRR 配置由
Controller 编译后通过 Agent 执行。主机使用普通 ping 产生本地 ARP/MAC 学习。

本夹具属于旧 `sdnc-it` 实验，默认 Web 地址为 `http://<部署服务器IP>:18080`，将占位符替换为运行本夹具的 Linux 主机管理 IP。源码镜像 `sdn-docker` 使用另一套 28080 入口，见 [Docker 部署](../docker/README.md)。实验随机凭据保存于
`lab/state/tokens.json`（mode 0600）；在 Web 登录框输入其中 `admin` 或 `viewer`
值。凭据不进入测试日志、URL、截图或构建文件。`agent` 凭据供节点注册使用。

HTTP 验证会添加和删除专用临时租户、制造不存在接口引起的事务失败、重启
本实验的 Controller/Gateway。最后恢复示例目标配置并保留可操作实验。
`--verify-only` 也会产生普通 PC 流量，但不进行配置提交或进程重启。

机器可读证据保存在 `artifacts/regression-results.json`、
`artifacts/integration-results.json`；具体 Job、FRR、ping 和拓扑输出同目录保存。
失败尝试单独保留，不把网络可达等同于所有故障场景成功。

浏览器验收脚本位于相邻 `sdn-web/tests/e2e/browser.cjs`，需要在实验完整通过后运行，
它会再提交少量无变化配置以验证 UI 和版本冲突。运行方法见该目录 README。

`lab/state` 只存放本夹具运行数据，有独立 go.mod 防止 Go 测试遍历容器产生的文件。
清理容器不删除历史状态目录；清理状态文件时需要先确认不再被运行容器使用。
