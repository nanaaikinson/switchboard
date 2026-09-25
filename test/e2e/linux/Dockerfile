# An Ubuntu machine that boots systemd, for the Linux end-to-end test. Run it
# privileged; see e2e.sh. Never build or run this on anything but a CI runner
# or a disposable Docker VM.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved libnss-resolve dbus dbus-user-session \
      sudo curl ca-certificates openssl python3 iproute2 libnss3-tools \
 && rm -rf /var/lib/apt/lists/*

# A normal user with passwordless sudo whose systemd user session starts at
# boot (lingering), as it would on a desktop after login.
RUN useradd -m -s /bin/bash dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
 && mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/dev

COPY run.sh /e2e/run.sh
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
