#!/bin/bash
set -e

if [ ! -d "dist" ]; then
  echo "Error: dist directory not found"
  exit 1
fi

cd dist
for os_arch in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do
  os="${os_arch%-*}"
  arch="${os_arch#*-}"
  exe=""
  if [ "$os" = "windows" ]; then
    exe=".exe"
  fi

  if [ ! -f "blockemulator-consensusnode-${os}-${arch}${exe}" ] || \
     [ ! -f "blockemulator-supervisor-${os}-${arch}${exe}" ]; then
    echo "Error: binaries for ${os_arch} not found"
    exit 1
  fi

  mkdir "${os_arch}"
  cp "blockemulator-consensusnode-${os}-${arch}${exe}" "${os_arch}/consensusnode${exe}"
  cp "blockemulator-supervisor-${os}-${arch}${exe}" "${os_arch}/supervisor${exe}"
  if [ "$os" = "windows" ]; then
    (cd "${os_arch}" && zip "../agentemulator-${os_arch}.zip" "consensusnode${exe}" "supervisor${exe}")
  else
    tar -czf "agentemulator-${os_arch}.tar.gz" -C "${os_arch}" consensusnode supervisor
  fi
  rm -rf "${os_arch}"
  rm -f "blockemulator-consensusnode-${os}-${arch}${exe}" \
        "blockemulator-supervisor-${os}-${arch}${exe}"
done
