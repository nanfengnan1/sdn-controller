# Controller 资源扩展验证

日期：2026-10-25。此记录仅描述 sdn-controller 源码和文档检查；真实 FRR/Linux 转发、Web 浏览器与 Docker 验收由本轮独立集成报告记录。

新增实现包括：资源物化模型、稳定自然键、增量候选、依赖与能力校验、验证导入、结构化查询、命名 CLI、BD 抑制策略、旧完整配置兼容和独立基础 VRF。

运行命令：

```sh
go test -race ./...
go vet ./...
sphinx-build -W --keep-going -n -a -b html docs docs/html
```

主要新增验证：

| 范围 | 验证内容 |
| --- | --- |
| resources | 自然键不受客户端 ID 影响、重复 CREATE 幂等、不同配置冲突、IPv6 地址/邻居等价文本、optional false/0、不修改输入对象 |
| 目标合并 | sysctl 按 key 合并与重复项拒绝；BD/INTERFACE MTU/MAC 同步；完整配置矛盾属性拒绝 |
| 关系 | 独立 VRF 不要求 BGP/VNI；删除保留业务引用被拒绝；虚拟对象删除同时清理附属接口属性 |
| BD 策略 | 没有 VXLAN 时可保存抑制策略；后加 VXLAN 继承；host 端口不被误配置 |
| 日志与导入 | 折叠删除历史、真实 VerifyResources 失败时不建立 ownership、重复 import receipt、导入后新增 BD 不重复创建 VRF、已受管节点禁止重新导入掩盖漂移 |
| 候选与持久化 | edit revision 冲突、跨次候选编辑补齐依赖、Plan 前校验、增量只下发变化、重启保留目标、受管节点删空后可再次创建 |
| Planner | legacy 基础租户后续追加 L3VNI 不重建 VRF、旧/新所有权冲突、新协议 teardown 顺序 |
| 查询与权限 | 查询 family/version 能力限制；viewer 可读资源和 Query，不能编辑/导入；agent token 不能调用 North |
| CLI | VRF/BD/VXLAN、IPv6、BGP AF/Type-5、OSPFv3 typed payload、布尔 false、查询过滤、参数拼写错误拒绝 |

一次并行 Docker 构建期间，原有公平调度测试的 3 秒墙钟上限触发超时，没有 race 报告；该测试涉及多次真实文件 fsync，已将等待界限改为 20 秒，并保持“无关节点任务必须完成”的断言。随后 Controller race 回归通过。

测试不证明大规模、HA 或所有人工配置漂移都已处理。Controller 仍以有界单快照持久化；持续漂移监控、主机命名空间重建后的自动重放和长期日志生命周期见 [待办](backlog.md)。
