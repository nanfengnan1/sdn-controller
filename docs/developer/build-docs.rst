构建与维护文档
========================================

本文档系统参考 ``sdn-agent`` 的用户 / 开发手册组织和 ``route-ovs/docs`` 的阅读样式，
本项目拥有独立的配置、模板、样式和源文件。构建不依赖其他项目的文档目录。
提供 help、html、clean 三个 Make 目标，不生成 PDF 或版本归档。

目录
----------------------------------------

.. code-block:: text

   docs/
   ├── conf.py / Makefile / requirements.txt
   ├── index.rst
   ├── user/               用户指南
   ├── developer/          开发指南
   ├── _static/            样式、脚本和截图
   ├── _templates/         阅读模板
   ├── _scripts/clean.py   受限清理
   ├── *.md               保留的历史说明与验收记录
   └── html/              生成结果

Linux 构建
----------------------------------------

在 docs/ 执行 ``make help``，或在项目根目录执行 ``make -C docs help``，
可以查看命令、依赖准备和输出位置。帮助无需安装 Sphinx 或 Python；默认目标仍为 html。
实际构建需要 Python 3.10+ 和 GNU Make，在项目根目录执行：

.. code-block:: bash

   python3 -m venv .venv-docs
   . .venv-docs/bin/activate
   python -m pip install -r docs/requirements.txt
   make -C docs html

依赖固定为 Sphinx 8.1.3 与 sphinx-rtd-theme 3.1.0。
构建开启警告失败和严格引用检查，成功后打开：

* ``docs/html/index.html``：项目文档首页。
* ``docs/html/user/index.html``：用户指南。
* ``docs/html/developer/index.html``：开发指南。

复制整个 ``html/`` 即可交给静态 Web 服务发布；页面、样式、搜索索引和脚本一起分发。
文档构建不修改 Controller 数据库，也不启动或停止业务容器。
发布归档和本机 HTTP 预览见 :doc:`/user/release`。

Windows 构建
----------------------------------------

不安装 Make 时，在项目根目录 PowerShell 执行：

.. code-block:: powershell

   py -m venv .venv-docs
   .venv-docs\Scripts\python.exe -m pip install -r docs/requirements.txt
   .venv-docs\Scripts\python.exe -m sphinx -W --keep-going -n -a -b html docs docs/html

清理
----------------------------------------

.. code-block:: bash

   make -C docs clean

Windows 等价命令：

.. code-block:: powershell

   py docs/_scripts/clean.py

只删除本项目生成的 ``docs/html`` 与 ``docs/_build``，保留 RST、截图、Markdown、
应用数据、容器和虚拟环境。脚本拒绝清理指向外部目录的符号链接。

编写规则
----------------------------------------

新增页面放入 user 或 developer，并加入对应 index.rst 的 toctree。
内部页面引用使用 Sphinx ``:doc:``，章节引用使用 ``:ref:`` 与显式 label。
截图放 ``_static/images``；不要引用工作区外的绝对图片路径。

教程注明命令执行位置、前置条件、预期结果和故障入口。
示例令牌使用占位符，不引入 docker/state 或其他真实凭据文件。
完整配置与局部片段需要明确区分；状态图区分当前实现和后续设计。

修改后运行 ``make -C docs html``，检查导航、页面内目录、代码复制和窄屏阅读。
需要验证清理时，先确认生成目录中没有需保留的人工内容，再 clean 并重建。
旧 Markdown 报告继续保留，但不把旧验收结果当作新版本的验证。

维护入口
----------------------------------------

* 部署依赖、配置默认值或镜像改变：同步 :doc:`/user/source-install`、
  :doc:`/user/docker-deploy`、:doc:`/user/configuration-file` 和 :doc:`/user/release`。
* 资源、RPC 或权限改变：同步 :doc:`protocol`、:doc:`resources`、
  :doc:`/user/resources` 及根 README；扩展路径见 :doc:`extensions`。
* 存储、任务或恢复改变：同步 :doc:`storage`、:doc:`/user/recovery` 和 :doc:`testing`。

RST 内使用 ``:doc:`` / ``:ref:`` 引用页面，标题下划线覆盖中文显示宽度。
跨项目文档按相邻仓库的源文件路径说明，不假设多个 HTML 站点部署在同一路径。
README 优先链接可直接在 Git 中阅读的源文件，生成的 html 仅作为本地构建后的入口。

