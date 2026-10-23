# Automated checks and releases

Every pull request and push to `main` runs GitHub Actions checks. CI builds Zela,
checks Go and JavaScript, runs the race-enabled Go and frontend suites, and enables
the PostgreSQL, Docker gateway, and manager-process HA tests using disposable
services. It also builds Linux release archives twice and compares every byte.

Real two-worker NFS, network partition, and storage outage checks still require
the separate [shared-storage acceptance environment](shared-storage.md). A green
CI run does not complete those release gates.

## Download and verify

When a maintainer publishes a release, download the archive for your Linux server
from the repository's **Releases** page: `amd64` for Intel/AMD or `arm64` for ARM.
Download `SHA256SUMS` alongside it. In that directory, run:

```sh
sha256sum --check --ignore-missing SHA256SUMS
```

The archive should report `OK`. Extract it and open the resulting directory.
The `bin/` folder contains the manager, CLI, native worker, and worker binaries for
both supported architectures. From that directory, you can try the dashboard:

```sh
./bin/zela-server --demo
```

Follow the [installation guide](installation.md) for a live installation. Retain
both architecture-specific worker binaries when using SSH provisioning. The
manager defaults to looking for them in `./bin`; set `--worker-binaries` if moved.

## Maintainer release steps

1. Merge the reviewed changes and confirm CI passes.
2. Create and push a version tag, such as `v0.2.0-beta.1`.
3. The Release workflow reruns CI, builds the archives, and creates a **draft**
   GitHub release with checksums and generated notes.
4. Review the notes, document compatibility and remaining live-test limitations,
   mark beta versions as prereleases in GitHub, then publish when ready.

Existing releases are not overwritten. If draft creation fails because the tag
already has a release, inspect that release before making changes.

## Reproduce a release

Use the tagged source, the exact Go version in `.go-version`, Python 3, and Git.
The script needs an empty output directory and downloads Go modules if necessary.

```sh
python3 scripts/release.py --version v0.2.0-beta.1 --output /tmp/zela-release-a
python3 scripts/release.py --version v0.2.0-beta.1 --output /tmp/zela-release-b
diff -r /tmp/zela-release-a /tmp/zela-release-b
```

No diff output means both builds match. Compare `SHA256SUMS` against the published
release to verify it too. Use a clean checkout at the exact tag: modified source
changes the result. Each archive includes `BUILD.json` with version, source commit,
Go version, OS, and architecture. Builds disable CGO and embedded VCS metadata,
strip local paths, and normalize archive ordering, ownership, and timestamps.
CI verifies repeat builds on Linux; cross-host reproducibility is not separately
certified. Update `.go-version` deliberately when upgrading the build toolchain.

Repository administrators can require the `checks` CI job in branch protection.
Adding workflow files alone does not enable branch protection settings.
