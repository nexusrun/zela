# Install a cluster manager with one script

The installer sets up Zela's manager, PostgreSQL database, CLI, worker binaries,
and systemd services on one Linux server. Services start immediately and after
reboot. Add worker servers through the dashboard once installation finishes.

## What you need

- A fresh systemd server: Ubuntu 24.04/26.04, Debian 12/13, RHEL, Rocky Linux, or AlmaLinux 9/10, or CentOS Stream 9/10.
- On RHEL, an active subscription or cloud-provided access to BaseOS/AppStream repositories. The installer does not register subscriptions.
- An Intel/AMD 64-bit (`amd64`) or ARM64 server.
- Root or sudo access and internet access to your distribution repositories.
- The Zela source checkout, or a published release archive.

This creates a single manager with a local database. It does not create a
multi-manager HA setup or an NFS server. See [HA](../deploy/HA.md) and
[shared storage](shared-storage.md) when you need those features.

## Install now, without a published release

Run this on your Linux manager server. Install Git, Make, Python 3, and the exact
Go version listed in `.go-version` first. The build also needs access to download
Go dependencies.

```sh
git clone https://github.com/nexusrun/zela.git
cd zela
make install
```

If you already have the project checked out, run `make install` from that folder.
Use the branch containing the installer until it is merged into the default branch.
The command builds the checked-out source, creates a temporary local archive, and
runs its installer with sudo. It installs PostgreSQL and starts the manager just
like the release installer. No GitHub release or tag is required.

The local build is labeled `v0.0.0-local`; it includes the source commit in
`BUILD.json`. This is an installation from source, not a published project release.
To build on another computer and copy the archive to the server, use
`make release VERSION=v0.0.0-local` instead.

## Install from a published release

Use this path only after a maintainer has published the matching release assets.
The version below is an example, not a claim that it has been released.

Download the Linux archive for your CPU and `SHA256SUMS` from the repository's
GitHub **Releases** page. Copy them to the manager server. Replace the example
version below with an actual published release. Draft releases are not public.

```sh
sha256sum --check --ignore-missing SHA256SUMS
# Check that your archive reports OK before extracting it.
tar -xzf zela-v0.2.0-beta.1-linux-amd64.tar.gz
cd zela-v0.2.0-beta.1-linux-amd64
bash install.sh --bundle-dir "$PWD" --plan
sudo bash install.sh --bundle-dir "$PWD"
```

The installer bundled with the release has the same checksum protection as the
binaries. `--bundle-dir` trusts the files you supply, so verify the archive first.

If you already have a trusted copy of `install.sh`, it can download and verify
the release itself:

```sh
sudo bash install.sh --version v0.2.0-beta.1
```

From a source checkout, the same script is at `scripts/install.sh`. For development
before a release is published, build an archive with `make release VERSION=v0.0.0-local`,
then extract and install it as above. See [release builds](releases.md).

## Open the dashboard

When installation succeeds, it prints **Zela is ready**. On your own computer,
open a terminal and replace the SSH username and manager IP:

```sh
ssh -L 8080:127.0.0.1:8080 USER@MANAGER_IP
```

Keep that connection open and visit **http://127.0.0.1:8080** in your browser.
On the manager server, read the private sign-in token:

```sh
sudo cat /var/lib/zela/admin-token
```

Paste the token into Zela. Open **Workers** to connect your Linux worker servers.
The [browser guide](console-guide.md#connect-your-first-worker) walks through this.
Docker is installed on workers through provisioning; the manager itself does not
need Docker. Initial networking configuration happens in the dashboard.

## Defaults and installed files

| Component | Default |
| --- | --- |
| Dashboard | `127.0.0.1:8080`, accessible through an SSH tunnel |
| Manager and CLI | `/usr/local/bin/zela-server`, `/usr/local/bin/zela` |
| Worker binaries | `/opt/zela/workers`, AMD64 and ARM64 |
| Service account | `zela`, without an interactive login shell |
| Manager state and credentials | `/var/lib/zela`, accessible only to the service user and root |
| Environment | `/etc/zela/zela-server.env`, root-only |
| systemd service | `/etc/systemd/system/zela-server.service` |
| Database | Dedicated PostgreSQL cluster `zela`, database and role `zela` |
| Database connection | Unix socket `/var/run/postgresql`, socket port `5433`, peer authentication |
| Database data | Debian/Ubuntu: `/var/lib/postgresql/<major>/zela`; RHEL family: `/var/lib/pgsql/zela` |
| Database service | Debian/Ubuntu: `postgresql@<major>-zela`; RHEL family: `postgresql-zela` |

The database accepts the operating-system `zela` account through the local socket.
It has no TCP listener and needs no stored password. The installer uses the
PostgreSQL version installed by the distribution. On Debian/Ubuntu, package installation may also
create the distribution's default `main` cluster; Zela does not use it or change
existing clusters. An existing `zela` cluster or use of port 5433 prevents a new
installation, so review that conflict before proceeding.

## RHEL, Rocky Linux, AlmaLinux, and CentOS Stream

Use the same installation command on versions 9 and 10, including minor releases
such as 9.6. CentOS support covers CentOS Stream 9 and 10 (`ID=centos`).
CentOS Linux 7/8, CentOS Stream 8, and Fedora are not supported by this installer.
CentOS Stream uses its public BaseOS and AppStream repositories and does not
require a RHEL subscription. See the [CentOS Stream 10 release notes](https://www.centos.org/centos10/)
for OS requirements; Zela supports its AMD64 and ARM64 architectures.

The installer selects `dnf` and installs the distribution's PostgreSQL server,
OpenSSH clients, and SELinux tools. It preserves your selected PostgreSQL module
stream and does not enable third-party repositories. The package choices follow
the [distribution PostgreSQL guidance](https://www.postgresql.org/download/linux/redhat/).

A separate `postgresql-zela.service` runs the database in `/var/lib/pgsql/zela`.
The installer does not initialize or modify the distribution's default database
at `/var/lib/pgsql/data`. Zela's database uses peer authentication and a local
socket, with TCP listening disabled. Connection settings live in the
installer-owned `/var/lib/pgsql/zela/zela.conf`; repeat installs restore these
local-only defaults. Other PostgreSQL configuration remains in `postgresql.conf`.

SELinux stays in its existing mode. The installer restores standard file labels
for binaries, database files, sockets, configuration, and systemd units. It does
not disable enforcement, add broad policy exceptions, or change firewall rules.
If a host has custom SELinux policy, inspect denials with your administrator.

```sh
sudo systemctl status postgresql-zela zela-server
sudo journalctl -u postgresql-zela -u zela-server -n 100
getenforce
```

Database major-version changes are refused on repeat installations. Back up and
migrate the database before switching PostgreSQL streams or upgrading the OS.

## Public HTTPS access

For access without an SSH tunnel, configure an HTTPS reverse proxy on the manager
that forwards to `127.0.0.1:8080`, or configure Zela's native TLS using a systemd
drop-in. See [TLS](security.md#tls) and
[Linux service configuration](installation.md#option-a-systemd-recommended-for-production).
Only expose the dashboard after HTTPS is ready. The installer does not change
firewall rules, issue certificates, or open port 8080 publicly.

## Repeat installs, upgrades, and recovery

The installer recognizes installations it created using
`/etc/zela/installer-pg-version`. Repeating it preserves the database, admin token,
vault key, environment file, and service unit. It replaces the supplied binaries
and restarts the manager, so schedule a maintenance window. Back up PostgreSQL and
`/var/lib/zela` before upgrading, and review release migration notes. Automatic
rollback of database migrations is not provided.

Existing manual or Compose installations are not adopted. Follow the
[manual installation guide](installation.md) for those installations. If this
installer fails partway through, fix the reported cause and repeat the command.
It retains data rather than deleting it on failure. On Debian/Ubuntu, repeating
the installer also repairs the local-only PostgreSQL listen setting, including
clusters affected by the earlier quoted-empty-value startup bug. Preserve its marker file;
do not delete state directories to retry.

```sh
sudo systemctl status zela-server
sudo journalctl -u zela-server -n 100
curl -fsS http://127.0.0.1:8080/api/ready
```

The startup check expects the default localhost URL. If you later customize the
service to native HTTPS or a different port, verify your endpoint manually after
rerunning. A failed health check does not roll back binaries.

Stop the manager with `sudo systemctl disable --now zela-server`. This retains all
data and leaves PostgreSQL running. Follow [uninstallation](installation.md#8-uninstall)
for deliberate removal, and [backup guidance](operations.md#backups-and-restore)
before removing anything.

## Validation coverage

Local tests cover argument validation, platform selection, service configuration,
wrong-architecture bundles, checksum failures, and unsafe archive links. RHEL-family
tests also cover package selection, service dependencies, simulated initialization,
data preservation on repeat runs, and database major-version mismatch rejection. CI also
installs on an Ubuntu 24.04 systemd runner and reruns the installer to check that
the database, token, and vault key survive. Actual RHEL-family hosts (including CentOS Stream 9/10), SELinux
enforcement, Debian, Ubuntu 26.04, and ARM64 still need live acceptance testing.
The simulated RHEL tests do not replace those checks.
