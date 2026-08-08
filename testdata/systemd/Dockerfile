# Test fixture: Ubuntu 22.04 running real systemd plus sshd, for adapter
# integration tests and for capturing genuine command output as golden files
# (§10). Built locally for the host architecture — unlike the ready-made
# systemd images on Docker Hub, which are amd64-only.
#
# Must be run with:
#   docker run -d --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw
#
# Credentials are hard-coded because this image is disposable and bound to an
# ephemeral port. Never publish it.
# Parameterised so the same fixture covers the distro matrix in §10:
#   docker build --build-arg UBUNTU_VERSION=20.04 -t litedeck-test-systemd-2004 .
ARG UBUNTU_VERSION=22.04
FROM ubuntu:${UBUNTU_VERSION}

ENV DEBIAN_FRONTEND=noninteractive
ENV container=docker

# dbus is not optional here. Root can reach systemd over its private socket
# (/run/systemd/private), so `docker exec systemctl ...` works without it — but
# LiteDeck logs in as an ordinary user, and that path requires the D-Bus system
# bus. Omitting it makes the fixture pass tests that would fail in reality.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus openssh-server sudo iproute2 \
 && rm -rf /var/lib/apt/lists/*

# Strip the units that assume real hardware; a container has no udev, no
# physical filesystems and no console to manage.
RUN find /lib/systemd/system/sysinit.target.wants/ -name 'systemd-tmpfiles-setup*' -delete \
 && rm -f /lib/systemd/system/multi-user.target.wants/* \
          /etc/systemd/system/*.wants/* \
          /lib/systemd/system/local-fs.target.wants/* \
          /lib/systemd/system/sockets.target.wants/*udev* \
          /lib/systemd/system/sockets.target.wants/*initctl* \
          /lib/systemd/system/basic.target.wants/* \
          /lib/systemd/system/anaconda.target.wants/*

# The password is deliberately unlike every other string in the fixture — the
# integration test greps the Command Log for it to prove secrets never reach
# the log, and a password that is also the username and a unit-name prefix
# would make that check match everything and prove nothing.
RUN useradd -m -s /bin/bash litedeck \
 && echo 'litedeck:Tr0ub4dor-fixture-pw' | chpasswd \
 && adduser litedeck sudo \
 && mkdir -p /run/sshd \
 && systemctl enable ssh

# A couple of extra units so the service list has something to show beyond the
# distro defaults, including one that is deliberately left disabled.
RUN printf '%s\n' \
    '[Unit]' \
    'Description=LiteDeck fixture worker' \
    '[Service]' \
    'ExecStart=/bin/sh -c "while true; do sleep 30; done"' \
    '[Install]' \
    'WantedBy=multi-user.target' \
    > /etc/systemd/system/litedeck-worker.service \
 && printf '%s\n' \
    '[Unit]' \
    'Description=LiteDeck fixture idle unit' \
    '[Service]' \
    'Type=oneshot' \
    'ExecStart=/bin/true' \
    '[Install]' \
    'WantedBy=multi-user.target' \
    > /etc/systemd/system/litedeck-idle.service \
 && systemctl enable litedeck-worker

EXPOSE 22
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
