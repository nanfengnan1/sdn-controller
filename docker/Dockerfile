ARG GO_IMAGE=golang:1.25-alpine3.22
ARG ALPINE_IMAGE=alpine:3.22
ARG ETCD_IMAGE=quay.io/coreos/etcd:v3.6.7
FROM ${ETCD_IMAGE} AS etcd
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY sdn-agent/go.mod sdn-agent/go.sum ./sdn-agent/
COPY sdn-agent/api ./sdn-agent/api
COPY sdn-controller/go.mod sdn-controller/go.sum ./sdn-controller/
WORKDIR /src/sdn-controller
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY sdn-controller/api ./api
COPY sdn-controller/internal ./internal
COPY sdn-controller/pkg ./pkg
COPY sdn-controller/cmd ./cmd
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/sdn-controller ./cmd/sdn-controller && \
    CGO_ENABLED=0 go build -trimpath -o /out/sdnc ./cmd/sdnc
FROM ${ALPINE_IMAGE}
RUN apk add --no-cache ca-certificates tini python3 && \
    mkdir -p /var/lib/sdn-controller /etc/sdn-controller /run/sdn-controller /etcd-data && \
    chmod 700 /run/sdn-controller /etcd-data
COPY --from=build /out/ /usr/local/bin/
COPY --from=etcd /usr/local/bin/etcd /usr/local/bin/etcdctl /usr/local/bin/
COPY sdn-controller/docker/controller.conf /etc/sdn-controller/controller.conf
COPY sdn-controller/docker/supervisor.py /usr/local/lib/sdn-controller/supervisor.py
EXPOSE 19090
VOLUME ["/etcd-data"]
HEALTHCHECK --interval=10s --timeout=10s --start-period=45s --retries=3 CMD ["python3", "/usr/local/lib/sdn-controller/supervisor.py", "--healthcheck"]
ENTRYPOINT ["/sbin/tini", "--", "python3", "/usr/local/lib/sdn-controller/supervisor.py"]
CMD ["-config", "/etc/sdn-controller/controller.conf"]
