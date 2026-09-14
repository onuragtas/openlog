# Debian 12 with systemd as PID 1 for the self-update end-to-end scenario (run.sh). Run with
# --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock.
# PHP-FPM 8.2 (+ cgi-fcgi to send requests to it) for the PHP agent fleet installation steps.
FROM debian:12
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus procps ca-certificates php8.2-cli php8.2-fpm libfcgi-bin && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && \
    systemctl mask getty.target console-getty.service systemd-logind.service systemd-remount-fs.service \
      systemd-udevd.service systemd-udev-trigger.service systemd-firstboot.service
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
