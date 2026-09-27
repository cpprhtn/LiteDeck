# Test fixture: an SSH git remote, for the settings-sync integration test (§10.2).
#
# The same shape as a user's own server used as a sync repository: sshd, git, and
# a bare repository in the account's home. Key authentication only — that is what
# a deploy key is, and the password path is not what this exercises.
#
# The authorized_keys file is written by the test at run time, so no key is baked
# into the image. Built locally, bound to an ephemeral port, destroyed with the
# run. It must never be published.
FROM alpine:3.20

RUN apk add --no-cache openssh-server git \
 && ssh-keygen -A \
 && adduser -D -s /bin/sh git \
 && passwd -u git \
 && mkdir -p /home/git/.ssh \
 && chown git:git /home/git/.ssh \
 && chmod 700 /home/git/.ssh \
 && su git -c 'git init --bare -b main /home/git/sync.git'

RUN sed -i \
    -e 's|^#*PasswordAuthentication.*|PasswordAuthentication no|' \
    -e 's|^#*PubkeyAuthentication.*|PubkeyAuthentication yes|' \
    -e 's|^#*PermitRootLogin.*|PermitRootLogin no|' \
    /etc/ssh/sshd_config

EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
