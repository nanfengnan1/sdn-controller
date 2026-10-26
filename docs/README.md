# sdn-controller 文档系统

用户手册与开发手册使用 Sphinx，内容组织和命令入口与 `sdn-agent` 保持一致，阅读样式参考 `route-ovs/docs`。每个项目的构建配置、模板、静态资源和内容均独立维护，构建不依赖其他项目的文档目录。

- [用户指南源文件](user/index.rst)
- [开发指南源文件](developer/index.rst)
- 现有 `.md` 设计记录、协议说明和验收报告保留在此目录。

这里的 Make 命令只生成文档。软件的依赖、编译、安装和运行见[源码安装](user/source-install.rst)，交付镜像、二进制和升级见[版本发布](user/release.rst)，新增资源与接口见[开发扩展流程](developer/extensions.rst)。

## 查看帮助

在 `docs/` 目录运行 `make help`，或者在项目根目录运行：

```bash
make -C docs help
```

帮助不依赖 Sphinx 或 Python；不带目标的 `make -C docs` 仍默认构建 HTML。

## 构建 HTML

Linux/macOS，Python 3.10+、GNU Make。在项目根目录运行：

```bash
python3 -m venv .venv-docs
. .venv-docs/bin/activate
python -m pip install -r docs/requirements.txt
make -C docs html
```

Windows 可使用 Python，省去 Make：

```powershell
py -m venv .venv-docs
.venv-docs\Scripts\python.exe -m pip install -r docs/requirements.txt
.venv-docs\Scripts\python.exe -m sphinx -W --keep-going -n -a -b html docs docs/html
```

打开 `docs/html/index.html`。两类手册分别在 `docs/html/user/index.html` 和 `docs/html/developer/index.html`。发布时复制整个 `html/`，不能只复制单个页面。搜索、样式和脚本均使用本地静态资源。

## 清理

```bash
make -C docs clean
```

Windows 等价命令：

```powershell
py docs/_scripts/clean.py
```

只删除本项目文档构建产物 `docs/html/` 和 `docs/_build/`；不会清除源码、图片、Markdown 文档、应用数据、容器或虚拟环境。脚本拒绝清理指向外部路径的符号链接。

## 编辑规则

新增页面放入 `user/` 或 `developer/`，并加入所属 `index.rst` 的 toctree。页面内链接使用 Sphinx 的 `:doc:` 或 `:ref:`，截图放在 `_static/images/`。代码示例写明运行位置，令牌使用占位符。

`html` 构建默认开启严格引用检查，任何 Sphinx 警告都会使命令失败。此文档系统提供 `help`、`html`、`clean` 三个 Make 目标，不包含 PDF 或版本归档。可覆盖 `SPHINXBUILD`、`SPHINXOPTS`、`PYTHON`；例如 `make -C docs html SPHINXBUILD=../.venv-docs/bin/sphinx-build`。完整发布和静态预览步骤见[版本发布](user/release.rst)。
