ARG ALPINE_VERSION=3.22
FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache \
        buildkit \
        ca-certificates \
        cni-plugins \
        containerd \
        e2fsprogs \
        iproute2 \
        iptables \
        linux-virt \
        nerdctl \
        runc \
        util-linux

COPY rootfs/ /
COPY wendy-runtime-guest-proxy /usr/local/libexec/wendy-runtime-guest-proxy

RUN chmod 0755 /init /usr/local/libexec/wendy-runtime-guest-proxy \
    && mkdir -p /etc/buildkit /etc/containerd /etc/cni/net.d \
        /run/buildkit /run/containerd /var/lib/wendy-runtime

ENTRYPOINT ["/init"]
