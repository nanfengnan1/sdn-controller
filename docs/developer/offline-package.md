# sdn-controller 离线镜像包

本包用于 Linux/amd64。需要 Docker、Bash、Python 3、tar、xz 和 sha256sum。
版本、镜像 ID 与配套源码提交以本目录 `manifest.json` 为准。
`docs/html/index.html` 可离线打开；它包含完整用户手册和开发手册。

## 校验和加载

解压前，把下面 PACKAGE 改为下载文件的名称（去掉 `.tar.xz`）：

```bash
PACKAGE=sdn-controller-v1.0.1
sha256sum -c "$PACKAGE.tar.xz.sha256"
tar -xJf "$PACKAGE.tar.xz"
cd "$PACKAGE"
bash load-images.sh
```

外层 tar.xz 含文档，需要先解压。`load-images.sh` 校验包内文件后，对 `images/*.tar` 执行 docker load；它不启动容器。无需手工创建 Docker manifest.json。

以下首次启动命令均在解压目录、同一个 Bash 终端执行，具备 Docker 操作权限。
镜像版本从本包清单读取：

```bash
SDN_IMAGE="$(python3 -c 'import json; print(json.load(open("manifest.json"))["images"][0]["reference"])')"
```

## 首次启动 Controller

镜像包括 Controller、sdnc 和单成员 etcd。下面的 gRPC 仅向宿主机 127.0.0.1:29090 开放。

```bash
set -eu
if [ ! -e controller.conf ]; then cp deploy/controller.conf controller.conf; fi
python3 - <<'PY'
import os, secrets
try:
    fd = os.open('controller.env', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
except FileExistsError:
    print('保留已有 controller.env')
else:
    with os.fdopen(fd, 'w') as stream:
        for key in ('SDN_ADMIN_TOKEN', 'SDN_VIEWER_TOKEN', 'SDN_AGENT_TOKEN'):
            stream.write(key + '=' + secrets.token_hex(24) + '\n')
PY
docker run -d --name sdn-controller-release --restart unless-stopped \
  --env-file controller.env -p 127.0.0.1:29090:19090 \
  --mount "type=bind,src=$PWD/controller.conf,dst=/etc/sdn-controller/controller.conf,readonly" \
  -v sdn-controller-etcd:/etcd-data \
  "$SDN_IMAGE"
docker logs sdn-controller-release
docker exec sdn-controller-release sdnc -addr 127.0.0.1:19090 -insecure nodes
```

等待 etcd 和 Controller 就绪后查询；没有接入 Agent 时节点列表为空是正常的。
Web 使用 controller.env 中的 admin/viewer token；Agent 的 SDN_TOKEN 对应这里的 SDN_AGENT_TOKEN。
跨主机接入时，将端口发布地址调整为可达的宿主机地址，客户端使用宿主机 29090 端口。其他容器里的 127.0.0.1 指向该容器自身。

默认模板启用实验用 insecure 模式，TLS、外部 etcd 与参数说明见 `docs/html/user/configuration-file.html`。
本包不包含 Agent/Web 镜像；完整系统需要另外两个组件包。源码 Compose 的镜像名若为 `sdn-*:alpine`，应显式改为 manifest.json 中的对应发布版本，并配套使用三个项目已验证的配置模板。

## 停止、数据和升级

停止本页创建的容器：

```bash
docker stop sdn-controller-release
```

重新启动使用 `docker start sdn-controller-release`，不要再次随机生成凭据。
配置文件、环境文件及命名卷属于部署方，保留在本机，不要重新打包发布。
升级、备份和回退见 `docs/html/user/release.html`；不要通过删除数据卷完成普通停止或升级。
已有容器与新容器不能同时访问同一数据库目录。

包内 `VERIFICATION.md` 只描述该版本实际完成的验证。Controller/Agent 的持久记录不等于删除容器后自动重建全部 Linux 网络状态，业务接口和网络恢复应单独验证。

