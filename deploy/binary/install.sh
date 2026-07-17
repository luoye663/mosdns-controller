#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  printf '%s\n' 'Run this installer with sudo.' >&2
  exit 1
fi

package_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

getent group mosdns-manager >/dev/null || groupadd --system mosdns-manager
id -u mosdns >/dev/null 2>&1 || useradd --system --gid mosdns-manager --home-dir /var/lib/mosdns --shell /usr/sbin/nologin mosdns
id -u mosdns-controller >/dev/null 2>&1 || useradd --system --gid mosdns-manager --home-dir /var/lib/mosdns-controller --shell /usr/sbin/nologin mosdns-controller

install -d -o root -g mosdns-manager -m 0750 /etc/mosdns-manager /etc/mosdns-manager/mosdns /etc/mosdns-manager/mosdns/rules
install -d -o mosdns -g mosdns-manager -m 0750 /var/lib/mosdns
install -d -o mosdns-controller -g mosdns-manager -m 0750 /var/lib/mosdns-controller
install -m 0755 "${package_dir}/bin/mosdns" /usr/local/bin/mosdns
install -m 0755 "${package_dir}/bin/controller" /usr/local/bin/controller
install -o root -g mosdns-manager -m 0640 "${package_dir}/etc/mosdns/config.yaml" /etc/mosdns-manager/mosdns/config.yaml
install -o root -g mosdns-manager -m 0640 "${package_dir}/etc/mosdns/rules/geosite_cn.txt" /etc/mosdns-manager/mosdns/rules/geosite_cn.txt
install -o root -g mosdns-manager -m 0640 "${package_dir}/etc/controller/config.yaml" /etc/mosdns-manager/controller.yaml
install -m 0644 "${package_dir}/systemd/mosdns.service" /etc/systemd/system/mosdns.service
install -m 0644 "${package_dir}/systemd/mosdns-controller.service" /etc/systemd/system/mosdns-controller.service

if [[ ! -f /etc/mosdns-manager/mosdns_control_token ]]; then
  umask 077
  openssl rand -hex 32 > /etc/mosdns-manager/mosdns_control_token
fi
chown root:mosdns-manager /etc/mosdns-manager/mosdns_control_token
chmod 0440 /etc/mosdns-manager/mosdns_control_token

systemctl daemon-reload
systemctl enable mosdns.service mosdns-controller.service
printf '%s\n' 'Installed. Set <REMOTE_DOH_URL> in /etc/mosdns-manager/mosdns/config.yaml, then start both services.'
