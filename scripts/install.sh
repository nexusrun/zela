#!/usr/bin/env bash
# Install a single Zela manager on Ubuntu/Debian or RHEL-family Linux. No worker hosts are changed.
set -euo pipefail

fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'HELP'
Usage: sudo bash install.sh --version v0.2.0-beta.1
       sudo bash install.sh --bundle-dir /path/to/extracted-release
Options:
  --version VERSION   Download this release from nexusrun/zela and verify SHA-256.
  --bundle-dir DIR    Install a trusted, previously verified release directory.
  --plan              Print the installation plan without changing this computer.
  --help              Show this help.
Supports Ubuntu 24.04/26.04, Debian 12/13, RHEL/Rocky/AlmaLinux 9/10, and CentOS Stream 9/10.
Requires systemd and AMD64/ARM64; RHEL needs enabled distribution repositories.
The dashboard listens on 127.0.0.1:8080. PostgreSQL uses a local Unix socket.
HELP
}
parse_args() {
  version='' bundle='' plan=false
  while (($#)); do
    case "$1" in
      --version|--bundle-dir)
        if (($# < 2)) || [[ -z "$2" || "$2" == --* ]]; then fail "$1 requires a value"; fi
        if [[ "$1" == --version ]]; then version=$2; else bundle=$2; fi
        shift 2 ;;
      --plan) plan=true; shift ;;
      --help|-h) usage; exit 0 ;;
      *) fail "Unknown option: $1" ;;
    esac
  done
  [[ -z "$version" || -z "$bundle" ]] || fail 'Choose --version or --bundle-dir, not both'
  [[ -n "$version" || -n "$bundle" ]] || fail 'Specify --version or --bundle-dir'
  [[ -z "$version" || "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]] || fail 'Invalid release version'
}
platform_arch() {
  case "$1" in x86_64|amd64) printf amd64 ;; aarch64|arm64) printf arm64 ;; *) fail "Unsupported architecture: $1" ;; esac
}
supported_os() {
  case "$1:$2" in ubuntu:24.04|ubuntu:26.04|debian:12|debian:13) return 0 ;; esac
  case "$1:${2%%.*}" in rhel:9|rhel:10|rocky:9|rocky:10|almalinux:9|almalinux:10|centos:9|centos:10) return 0 ;; *) return 1 ;; esac
}
os_family() {
  case "$1" in ubuntu|debian) printf debian ;; rhel|rocky|almalinux|centos) printf rpm ;; *) fail "Unsupported OS: $1" ;; esac
}
install_packages() {
  if [[ "$family" == debian ]]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y ca-certificates curl openssh-client python3 postgresql postgresql-client
  else
    # Use distribution packages and the administrator's selected stream, never reset modules.
    dnf install -y ca-certificates openssh-clients python3 postgresql-server postgresql policycoreutils
    if ! command -v curl >/dev/null; then dnf install -y curl; fi
  fi
}
rpm_pg_major() {
  /usr/bin/postgres --version | sed -nE 's/.* ([0-9]+)\..*/\1/p'
}
write_rpm_database_unit() {
  cat <<'UNIT'
[Unit]
Description=Zela local PostgreSQL database
After=network.target

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/usr/bin/postgres -D /var/lib/pgsql/zela
RuntimeDirectory=postgresql
RuntimeDirectoryMode=0755
RuntimeDirectoryPreserve=yes
KillSignal=SIGINT
TimeoutStopSec=120
Restart=on-failure
RestartSec=5
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
}
prepare_database() {
  if [[ "$family" == debian ]]; then
    pg_service="postgresql@${pg_version}-zela.service"
    if [[ ! -d /etc/postgresql/$pg_version/zela ]]; then
      pg_createcluster "$pg_version" zela --port 5433 --start-conf auto --locale C.UTF-8 \
        -- --auth-local=peer --auth-host=scram-sha-256
    fi
    # pg_conftool quotes values itself. Passing literal quotes produces a bogus hostname.
    # Apply on retries too, repairing clusters created by the previous installer.
    pg_conftool "$pg_version" zela set listen_addresses ''
  else
    pg_service=postgresql-zela.service
    if [[ ! -f /var/lib/pgsql/zela/PG_VERSION ]]; then
      [[ ! -e /var/lib/pgsql/zela ]] || fail 'Incomplete PostgreSQL data directory; inspect it before retrying'
      install -d -o postgres -g postgres -m 0700 /var/lib/pgsql/zela
      runuser -u postgres -- /usr/bin/initdb -D /var/lib/pgsql/zela --encoding=UTF8 --locale=C \
        --auth-local=peer --auth-host=scram-sha-256
    fi
    [[ $(cat /var/lib/pgsql/zela/PG_VERSION) == "$pg_version" ]] || fail 'PostgreSQL data major version mismatch; migrate the database before upgrading'
    # Separate installer-owned configuration is written atomically, including on retries.
    cat > /var/lib/pgsql/zela/zela.conf.next <<'PGCONF'
listen_addresses = ''
port = 5433
unix_socket_directories = '/var/run/postgresql'
PGCONF
    chown postgres:postgres /var/lib/pgsql/zela/zela.conf.next
    chmod 0600 /var/lib/pgsql/zela/zela.conf.next
    mv -f /var/lib/pgsql/zela/zela.conf.next /var/lib/pgsql/zela/zela.conf
    if ! grep -qxF "include = 'zela.conf'" /var/lib/pgsql/zela/postgresql.conf; then
      printf "\ninclude = 'zela.conf'\n" >> /var/lib/pgsql/zela/postgresql.conf
    fi
    if [[ ! -f /etc/systemd/system/postgresql-zela.service ]]; then
      write_rpm_database_unit > /etc/systemd/system/postgresql-zela.service
      chmod 0644 /etc/systemd/system/postgresql-zela.service
    fi
    # Default distribution labels cover /var/lib/pgsql and /run/postgresql.
    # Keep SELinux enforcing; do not install broad allow rules or change booleans.
    install -d -o postgres -g postgres -m 0755 /run/postgresql
    restorecon -RF /var/lib/pgsql/zela /run/postgresql /etc/systemd/system/postgresql-zela.service
    systemctl daemon-reload
  fi
  if ! systemctl enable --now "$pg_service"; then
    journalctl -u "$pg_service" --no-pager -n 60 >&2 || true
    fail "PostgreSQL service failed to start: $pg_service"
  fi
  for attempt in $(seq 1 30); do
    if runuser -u postgres -- pg_isready -q -h /var/run/postgresql -p 5433; then return; fi
    if (( attempt < 30 )); then sleep 1; fi
  done
  fail "PostgreSQL did not become ready; inspect journalctl -u $pg_service"
}
write_unit() {
  cat <<UNIT
[Unit]
Description=Zela cluster manager
Wants=network-online.target
After=network-online.target ${pg_service}
Requires=${pg_service}

[Service]
User=zela
Group=zela
EnvironmentFile=/etc/zela/zela-server.env
ExecStart=/usr/local/bin/zela-server --listen 127.0.0.1:8080 --data /var/lib/zela --worker-binaries /opt/zela/workers
StateDirectory=zela
StateDirectoryMode=0700
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
}
main() {
  parse_args "$@"
  if "$plan"; then
    cat <<PLAN
Install from: ${version:-$bundle}
- Install distribution PostgreSQL, OpenSSH client, curl, Python 3, and CA certificates.
- Create service account zela and a dedicated PostgreSQL cluster named zela (socket port 5433).
- Use Unix peer authentication; no database password or public database listener.
- Install manager/CLI in /usr/local/bin and both worker architectures in /opt/zela/workers.
- Store private configuration in /etc/zela and manager state in /var/lib/zela.
- Enable PostgreSQL and zela-server systemd services and wait for /api/ready.
- Preserve credentials and database on repeat runs of this installer.
- Listen on 127.0.0.1:8080; connect workers afterward through the dashboard.
PLAN
    return
  fi
  [[ $(uname -s) == Linux ]] || fail 'Run this installer on a Linux manager server'
  [[ $EUID == 0 ]] || fail 'Run with sudo or as root'
  [[ -d /run/systemd/system ]] || fail 'A running systemd host is required'
  # OS-provided file, never source a file from the downloaded release.
  # shellcheck source=/dev/null
  . /etc/os-release
  supported_os "$ID" "$VERSION_ID" || fail "Unsupported OS: $ID $VERSION_ID"
  family=$(os_family "$ID")
  arch=$(platform_arch "$(uname -m)")
  command -v flock >/dev/null || fail 'Install util-linux (flock) first'
  exec 9>/run/lock/zela-install.lock
  flock -n 9 || fail 'Another Zela installation is running'
  if [[ ! -f /etc/zela/installer-pg-version ]]; then
    for path in /etc/zela /var/lib/zela /opt/zela /usr/local/bin/zela-server /usr/local/bin/zela /etc/systemd/system/zela-server.service /var/lib/pgsql/zela /etc/systemd/system/postgresql-zela.service; do
      [[ ! -e "$path" ]] || fail "Existing installation at $path; use the manual upgrade guide"
    done
    ! getent passwd zela >/dev/null || fail 'Existing zela account; use the manual installation guide'
    ! getent group zela >/dev/null || fail 'Existing zela group; use the manual installation guide'
  fi
  install_packages
  temp=$(mktemp -d)
  trap 'rm -rf -- "$temp"' EXIT
  if [[ -n "$version" ]]; then
    archive="zela-${version}-linux-${arch}.tar.gz"
    base="https://github.com/nexusrun/zela/releases/download/${version}"
    curl --proto '=https' --tlsv1.2 -fL --retry 3 "$base/$archive" -o "$temp/$archive"
    curl --proto '=https' --tlsv1.2 -fL --retry 3 "$base/SHA256SUMS" -o "$temp/SHA256SUMS"
    # Extract only regular files with exact expected names, never archive links or paths.
    python3 - "$temp" "$archive" <<'PY'
import hashlib, pathlib, sys, tarfile
root, name = pathlib.Path(sys.argv[1]), sys.argv[2]
entries = [line.split() for line in (root/'SHA256SUMS').read_text().splitlines()]
matching = [v[0] for v in entries if len(v) == 2 and v[1] == name]
if len(matching) != 1 or hashlib.sha256((root/name).read_bytes()).hexdigest() != matching[0]:
    sys.exit('Release checksum verification failed')
prefix = name.removesuffix('.tar.gz')
with tarfile.open(root/name) as tar:
    for path in ('BUILD.json', 'bin/zela-server', 'bin/zela', 'bin/zela-worker-linux-amd64', 'bin/zela-worker-linux-arm64'):
        member = tar.getmember(prefix+'/'+path)
        if not member.isfile():
            sys.exit('Release contains a non-regular file: '+path)
        target = root/prefix/path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(tar.extractfile(member).read())
PY
    bundle="$temp/${archive%.tar.gz}"
  fi
  # Check metadata and binary CPU before changing services or creating the database.
  python3 - "$bundle" "$arch" "$version" <<'PY'
import json, pathlib, re, sys
root, arch, version = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
meta = json.loads((root/'BUILD.json').read_text())
if meta.get('os') != 'linux' or meta.get('arch') != arch:
    sys.exit('Release is for a different operating system or CPU')
if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?', meta.get('version', '')):
    sys.exit('Invalid release metadata version')
if version and meta.get('version') != version:
    sys.exit('Release metadata version mismatch')
for name, cpu in [('zela-server',arch),('zela',arch),('zela-worker-linux-amd64','amd64'),('zela-worker-linux-arm64','arm64')]:
    path = root/'bin'/name
    header = path.read_bytes()[:20]
    if path.is_symlink() or len(header) < 20 or header[:6] != b'\x7fELF\x02\x01' or int.from_bytes(header[18:20], 'little') != {'amd64':62,'arm64':183}[cpu]:
        sys.exit('Invalid Linux binary: '+name)
PY
  if [[ -f /etc/zela/installer-pg-version ]]; then
    pg_version=$(cat /etc/zela/installer-pg-version)
    [[ "$pg_version" =~ ^[0-9]+$ ]] || fail 'Invalid recorded PostgreSQL version'
    if [[ "$family" == debian ]]; then
      [[ -x /usr/lib/postgresql/$pg_version/bin/postgres ]] || fail 'Recorded PostgreSQL version is unavailable'
    else
      [[ $(rpm_pg_major) == "$pg_version" ]] || fail 'PostgreSQL major version changed; migrate the database before upgrading'
    fi
  else
    if [[ "$family" == debian ]]; then
      pg_version=$(find /usr/lib/postgresql -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -n | tail -1)
      if pg_lsclusters --no-header | awk '$2 == "zela" || $3 == 5433 {found=1} END {exit !found}'; then
        fail 'A PostgreSQL cluster named zela or socket port 5433 already exists'
      fi
    else
      pg_version=$(rpm_pg_major)
      [[ ! -e /var/run/postgresql/.s.PGSQL.5433 && ! -e /var/run/postgresql/.s.PGSQL.5433.lock ]] || fail 'PostgreSQL socket port 5433 is already in use'
    fi
    [[ "$pg_version" =~ ^[0-9]+$ ]] || fail 'Cannot find PostgreSQL server'
    install -d -m 0700 /etc/zela
    printf '%s\n' "$pg_version" > /etc/zela/installer-pg-version
  fi
  if ! id zela >/dev/null 2>&1; then
    useradd --system --user-group --home-dir /var/lib/zela --no-create-home --shell /usr/sbin/nologin zela
  fi
  install -d -o zela -g zela -m 0700 /var/lib/zela
  install -d -m 0755 /opt/zela/workers
  prepare_database
  runuser -u postgres -- psql -X -h /var/run/postgresql -p 5433 -d postgres -v ON_ERROR_STOP=1 <<'SQL'
SELECT 'CREATE ROLE zela LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'zela') \gexec
SELECT 'CREATE DATABASE zela OWNER zela'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'zela') \gexec
SQL
  # Prove the application user can authenticate before starting the manager.
  runuser -u zela -- psql -X -h /var/run/postgresql -p 5433 -d zela -v ON_ERROR_STOP=1 -c 'SELECT 1' >/dev/null
  if [[ ! -f /etc/zela/zela-server.env ]]; then
    printf '%s\n' 'ZELA_DATABASE_URL=postgres://zela@/zela?host=/var/run/postgresql&port=5433&sslmode=disable' > /etc/zela/zela-server.env
    chmod 0600 /etc/zela/zela-server.env
  fi
  # Atomic replacement avoids truncating a running executable on repeated installs.
  for name in zela-server zela; do
    install -m 0755 "$bundle/bin/$name" "/usr/local/bin/$name.next"
    mv -f "/usr/local/bin/$name.next" "/usr/local/bin/$name"
  done
  for cpu in amd64 arm64; do
    install -m 0755 "$bundle/bin/zela-worker-linux-$cpu" "/opt/zela/workers/zela-worker-linux-$cpu.next"
    mv -f "/opt/zela/workers/zela-worker-linux-$cpu.next" "/opt/zela/workers/zela-worker-linux-$cpu"
  done
  if [[ ! -f /etc/systemd/system/zela-server.service ]]; then
    write_unit > /etc/systemd/system/zela-server.service
    chmod 0644 /etc/systemd/system/zela-server.service
  fi
  if [[ "$family" == rpm ]]; then
    restorecon -RF /usr/local/bin/zela-server /usr/local/bin/zela /opt/zela /etc/zela /var/lib/zela /etc/systemd/system/zela-server.service
  fi
  systemctl daemon-reload
  systemctl enable zela-server
  systemctl restart zela-server
  for attempt in $(seq 1 60); do
    if systemctl is-active --quiet zela-server && curl -fsS --max-time 2 http://127.0.0.1:8080/api/ready >/dev/null; then
      printf '\nZela is ready at http://127.0.0.1:8080\n'
      printf 'Read your sign-in token: sudo cat /var/lib/zela/admin-token\n'
      printf 'From your computer: ssh -L 8080:127.0.0.1:8080 USER@MANAGER_IP\n'
      printf 'Then open http://127.0.0.1:8080 and add workers in the dashboard.\n'
      return
    fi
    if (( attempt < 60 )); then sleep 2; fi
  done
  fail 'Manager did not become ready. Inspect: journalctl -u zela-server -n 100. Data was retained; fix the error and rerun.'
}
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
