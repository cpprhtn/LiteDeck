# Test fixture: a real Docker daemon plus sshd, for container-view integration
# tests (§4.5).
#
# Docker-in-Docker rather than mounting the host's socket: the test creates and
# destroys containers, and it must not be able to touch anything the developer
# running it cares about. Isolation is worth the extra fixture.
#
# Must be run with --privileged (dind needs it to set up its own networking and
# storage driver).
#
# Credentials are hard-coded because this image is disposable and bound to an
# ephemeral port. Never publish it.
FROM docker:28-dind

RUN apk add --no-cache openssh-server openssh-sftp-server shadow \
 && ssh-keygen -A \
 && adduser -D -s /bin/sh litedeck \
 && echo 'litedeck:Tr0ub4dor-fixture-pw' | chpasswd \
 # The docker group already exists in this image; membership is what lets an
 # ordinary user reach the daemon socket without sudo — which is the
 # configuration LiteDeck expects and advises.
 && addgroup litedeck docker

RUN sed -i \
    -e 's|^#*PasswordAuthentication.*|PasswordAuthentication yes|' \
    -e 's|^#*PermitRootLogin.*|PermitRootLogin no|' \
    -e 's|^#*Subsystem.*sftp.*|Subsystem sftp /usr/lib/ssh/sftp-server|' \
    /etc/ssh/sshd_config

# dockerd is PID 1 via the base image's entrypoint; sshd has to be started
# alongside it. The wrapper keeps the entrypoint contract intact.
COPY entrypoint.sh /usr/local/bin/litedeck-entrypoint.sh
RUN chmod +x /usr/local/bin/litedeck-entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/litedeck-entrypoint.sh"]
