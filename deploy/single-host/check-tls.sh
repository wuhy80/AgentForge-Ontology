#!/usr/bin/env bash
set -Eeuo pipefail

certificate=${1:?certificate path required}
private_key=${2:?private key path required}
host=${3:?public hostname or IP required}
verify_name=(-verify_hostname "$host")
[[ "$host" != *:* && ! "$host" =~ ^[0-9.]+$ ]] || verify_name=(-verify_ip "$host")
openssl x509 -in "$certificate" -checkend 86400 -noout
openssl verify -purpose sslserver "${verify_name[@]}" -untrusted "$certificate" "$certificate"
certificate_public=$(openssl x509 -in "$certificate" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum)
private_public=$(openssl pkey -in "$private_key" -passin pass: -pubout -outform DER | sha256sum)
[[ "$certificate_public" == "$private_public" ]] || { echo 'HTTPS证书与私钥不匹配' >&2; exit 1; }
