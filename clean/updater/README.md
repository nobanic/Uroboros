# Console updates from main

The clean Windows and Linux launchers install a private update controller.
In **System settings → Version & Updates**, choose **Check for updates** and
then **Update from main**. The source is fixed to
`https://github.com/nobanic/Uroboros`, branch `main`. A GitHub Release or version
tag is not required. A normal committed and pushed README change is sufficient
to exercise commit detection; the panel shows the full installation's source
revision independently of its application version.

An existing installation needs one source update and a normal `clean/start`
invocation to install this controller. Subsequent updates use the console.
Docker must remain available and the machine needs internet access for Git and
any uncached build dependencies. Windows uses Docker Desktop Linux containers;
Linux uses a local AMD64 Docker Engine and Compose v2. The Linux launcher uses
the invoking user's UID/GID and the local Docker socket group.

## Update sequence

1. Freeze the exact commit selected by the operator. If main changes before
   acceptance or fetching, ask for another check instead of silently selecting
   a different commit.
2. Export that commit into a private managed source directory. Validate the
   source, the two exact runtime TARs, project/volume ownership, console port
   and PostgreSQL image identity.
3. Build the application, frontend, Arsenal, API Lab, Tor and update service
   under new image tags while the current installation remains available.
4. Require paused/finished operations, no running agent conversations and no
   queued/running discovery, binary, archive or API Lab jobs. Temporarily reject new API writes
   and triggered agent runs during maintenance.
5. Stop application writers. Create and verify a PostgreSQL dump, plus hashed
   archives of application data, the signing keystore, sidecar data, binary CAS
   and socket stores. These contain private installation data.
6. Replace the stack using the existing project, named volumes, credentials
   and console port. An independent worker survives replacement of the update
   controller itself. Check authenticated mode and the exact new source commit.
7. Record the healthy source for future start/status/stop invocations. On a
   failed rollout, restore the snapshot and the captured previous image IDs.
   A build failure does not stop the running installation.

No step removes a persistent Docker volume, runs Docker prune, publishes code,
or changes another Compose project. The original checkout is retained. Future
launcher invocations follow the last healthy managed snapshot while the
checkout still identifies its original seed. A deliberately changed checkout
is treated as an explicit new source selection.

## Private state and access

The source, status and recovery directories are outside the repository:

- Windows: `%LOCALAPPDATA%\Astra\updates\`;
- Linux: `${XDG_STATE_HOME:-$HOME/.local/state}/astra/updates/`;
- an `ASTRA_RUNTIME_DIR` override selects the corresponding private root.

Backups are retained under `updates/backups/`. Keep them private: the database
dump includes API profiles and credentials, and the keystore archive includes
the signing key. They are never added to the Git checkout or release archive.
Only the status subdirectory is mounted read-only in the application.

The controller has Docker-daemon authority through its socket mount. It has no
published host port and accepts only a random installation credential on a
separate control network. The application has an authenticated, fixed update
API; it does not receive the Docker socket. There is no command, repository,
branch or Docker-API passthrough in the update protocol. Protect the Docker
daemon and operator account as part of the installation's administrative boundary.

Public repositories need no GitHub token. While the repository is private,
expand **GitHub access** in the update panel and save a fine-grained token for
this repository with **Contents: read-only**. See the
[GitHub token guide](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens).
Only the configured/not-configured status is returned to the browser. The
credential is stored in private `updates/github-token.json`, outside Git and
the application workspace. Use **Clear token** after making the repository
public if authenticated access is no longer needed. Nothing changes in the
ordinary-commit update workflow.

An optional `UROBOROS_UPDATE_GITHUB_TOKEN=...` in private `runtime.env` is also
supported for unattended setup. A console-saved credential takes precedence;
clearing it explicitly selects public access even if that environment value
remains. This credential is never a model profile or model-facing tool input.

## Recovery boundary

Automatic recovery restores the pre-update database and volume snapshot when
the new stack fails its health check. The UI deliberately does not expose the
standalone binary rollback command for a managed Docker installation.

If Docker or the host is interrupted during replacement, retain `job.lock`,
status, source snapshots, old images and backup directories. Do not delete
volumes or regenerate private runtime credentials. Inspect the last job before
starting another update. A Git rollback alone cannot restore persistent data.
For manual recovery, use the saved `previous-compose.json` and database/volume
archives with the same project and credentials; verify every archive hash before
restoring. Database-image upgrades and changes to existing volume names are
refused and require a separately reviewed migration.
