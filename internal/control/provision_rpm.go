package control

// Explicit major versions avoid RHEL minor-release locks selecting nonexistent Docker repositories.
func dockerRPMInstallScript(distro, release string) string {
	repo := "rhel"
	if distro == "centos" {
		repo = "centos"
	}
	return `set -eu
# podman-docker exposes a docker command too; reject it before checking Docker.
for pkg in docker docker-client docker-client-latest docker-common docker-latest docker-latest-logrotate docker-logrotate docker-engine podman podman-docker containerd runc; do
  if rpm -q "$pkg" >/dev/null 2>&1; then
    printf 'Conflicting container runtime requires operator review: %s\n' "$pkg" >&2
    exit 72
  fi
done
if command -v docker >/dev/null 2>&1; then
  docker info >/dev/null
  systemctl enable docker >/dev/null
  exit 0
fi
dnf -y install ca-certificates curl gnupg2 policycoreutils
keydir=$(mktemp -d)
trap 'rm -rf "$keydir"' EXIT HUP INT TERM
curl --proto '=https' --tlsv1.2 -fsSL https://download.docker.com/linux/` + repo + `/gpg -o "$keydir/docker.asc"
fingerprint=$(gpg --batch --homedir "$keydir" --with-colons --show-keys "$keydir/docker.asc" | awk -F: '$1 == "fpr" { print $10; exit }')
[ "$fingerprint" = '060A61C51B558A7F742B77AAC52FEB6B621E9F35' ] || exit 76
install -d -m 0755 /etc/pki/rpm-gpg /etc/yum.repos.d
install -m 0644 "$keydir/docker.asc" /etc/pki/rpm-gpg/ZELA-DOCKER-CE
rpm --import /etc/pki/rpm-gpg/ZELA-DOCKER-CE
cat > /etc/yum.repos.d/docker-ce.repo <<'ZELA_RPM'
[docker-ce-stable]
name=Docker CE Stable
baseurl=https://download.docker.com/linux/` + repo + `/` + release + `/$basearch/stable
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/ZELA-DOCKER-CE
ZELA_RPM
install -d -m 0755 /etc/docker
if [ ! -e /etc/docker/daemon.json ]; then
  printf '%s\n' '{"log-driver":"local","log-opts":{"max-size":"20m","max-file":"3"},"selinux-enabled":true}' > /etc/docker/daemon.json
fi
restorecon -F /etc/pki/rpm-gpg/ZELA-DOCKER-CE /etc/yum.repos.d/docker-ce.repo /etc/docker /etc/docker/daemon.json
dnf -y --refresh --setopt=docker-ce-stable.gpgcheck=1 install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if docker info >/dev/null 2>&1; then exit 0; fi
  sleep 2
done
exit 73
`
}
