#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  printf '%s\n' 'Run this installer with sudo.' >&2
  exit 1
fi

for command in curl sha256sum tar; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    printf 'Required command not found: %s\n' "${command}" >&2
    exit 1
  fi
done

case "$(uname -m)" in
  x86_64 | amd64)
    arch=amd64
    ;;
  aarch64 | arm64)
    arch=arm64
    ;;
  *)
    printf 'Unsupported architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

version=${VERSION:-latest}
if [[ ${version} == latest ]]; then
  release_url=https://github.com/luoye663/mosdns-controller/releases/latest/download
else
  version=${version#v}
  if [[ ! ${version} =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ ]]; then
    printf 'Invalid VERSION: %s\n' "${version}" >&2
    exit 1
  fi
  release_url="https://github.com/luoye663/mosdns-controller/releases/download/v${version}"
fi

archive="mosdns-manager-linux-${arch}.tar.gz"
work_dir=$(mktemp -d)
trap 'rm -rf "${work_dir}"' EXIT

curl -fL --retry 3 -o "${work_dir}/${archive}" "${release_url}/${archive}"
curl -fL --retry 3 -o "${work_dir}/SHA256SUMS" "${release_url}/SHA256SUMS"
(
  cd "${work_dir}"
  sha256sum --check --ignore-missing SHA256SUMS
)
tar -xzf "${work_dir}/${archive}" -C "${work_dir}"
bash "${work_dir}/package/install.sh"
systemctl start mosdns.service mosdns-controller.service

printf '%s\n' 'Services started. Open http://<server-ip>:8080 to create the first administrator.'
