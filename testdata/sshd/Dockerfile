# Test fixture: a throwaway OpenSSH server for sshcore integration tests (§10).
# Credentials are hard-coded on purpose — this image is built locally, bound to
# an ephemeral port, and destroyed when the test run ends. It must never be
# published or used for anything but tests.
FROM alpine:3.20

RUN apk add --no-cache openssh-server openssh-sftp-server \
 && ssh-keygen -A \
 && adduser -D -s /bin/sh litedeck \
 && echo 'litedeck:litedeck' | chpasswd

RUN sed -i \
    -e 's|^#*PasswordAuthentication.*|PasswordAuthentication yes|' \
    -e 's|^#*PermitRootLogin.*|PermitRootLogin no|' \
    -e 's|^#*Subsystem.*sftp.*|Subsystem sftp /usr/lib/ssh/sftp-server|' \
    /etc/ssh/sshd_config

EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
