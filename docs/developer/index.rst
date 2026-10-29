开发指南
========================================

本指南对应当前 Go 实现，说明模块边界、North 协议、节点 op 编译、持久化和验证方法。
新增能力应同时更新协议、Planner、Agent 能力声明与测试，而不是让 Controller 拼接设备命令。

.. toctree::
   :maxdepth: 1

   architecture
   ips
   protocol
   planner
   resources
   extensions
   storage
   testing
   release
   build-docs

建议按 :doc:`architecture` → :doc:`protocol` → :doc:`planner` / :doc:`resources`
→ :doc:`storage` 阅读实现，再按 :doc:`extensions` 完成新能力，按 :doc:`testing` 核对证据。
制作带文档的离线包、发布 tag 和 GitHub 附件见 :doc:`release`；
部署交付及升级兼容性检查见 :doc:`/user/release`。
日常部署和 CLI 操作见 :doc:`../user/index`。

