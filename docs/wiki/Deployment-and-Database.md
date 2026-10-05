# Deployment and Database Guide

[中文](部署与数据库.zh-CN) | English

## Choose a database

| Scenario | Recommendation | Reason |
| --- | --- | --- |
| Local trial or low-frequency single-user use | SQLite | No extra service; the database is one file. |
| Docker production deployment | PostgreSQL 17 | The default Compose file provides the service, persistent volume, and health check. |
| Existing MySQL operations | MySQL 8+ | Reuses existing backups, monitoring, and permission management. |
| Multiple users or long-running service | PostgreSQL / MySQL | Better suited to concurrency, backups, and independent operations. |

All three databases run embedded migrations automatically at application startup. Connection precedence is `DATABASE_URL` > `-db-url` > `-db`. Back up a production database before upgrading it.

## Docker + PostgreSQL (recommended)

1. Copy configuration: `cp .env.example .env`.
2. Edit `.env` and set at least `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `XIANYU_DATA_KEY`, and `XIANYU_ADMIN_PASSWORD`.
3. Generate one long-lived random value for `XIANYU_DATA_KEY`, for example `openssl rand -base64 48`; do not change it during later upgrades.
4. Start: `docker compose up -d`.
5. Inspect: `docker compose ps` and `docker compose logs -f app`.
6. Open `http://server-address:59188`. With `XIANYU_ADMIN_PASSWORD`, Compose creates `admin` automatically on first startup. Other startup methods show the initialization form when the database has no administrator; enter and confirm a password of at least eight characters.

> Security: source and container examples using `-addr :59188` listen on all interfaces. Before initialization there is no pre-shared secret, so the first client reaching the management port can create the administrator. This convenience flow does not restrict by IP. Restrict access at the firewall, security group, or reverse proxy; for public deployments, set `XIANYU_ADMIN_PASSWORD` or initialize with `-init-admin` first.

Compose injects the default connection, so a PostgreSQL URL is not required. Changing `COMPOSE_PROJECT_NAME` changes the new volume prefix; keep the existing `.env` during upgrades to avoid accidentally using an empty new volume.

Automatic renewal in Docker uses the Linux-native browser fingerprint detected by Chromium inside the container when requesting Xianyu. It does not skip silent renewal just because the host is Linux. The result still depends on the Xianyu response and account login state.

The source service defaults to `-addr :59188` and listens on all interfaces. Docker Compose also defaults to `XIANYU_BIND_ADDRESS=0.0.0.0`. Desktop packages explicitly bind `127.0.0.1:59188`. Remote deployments must restrict the management port at the firewall, security group, or reverse proxy; `:59188` in source examples does not mean localhost-only.

## Versions, images, and releases

Git tags are the source of release versions. A release tag must use the `v1.2.3` format and point to a commit already included in `main`.

After a release tag is pushed, `.github/workflows/release.yml` builds desktop packages and Docker images from that tag; it does not reuse artifacts built earlier from `main`. After build, Chromium launch, and `/health` checks pass, the `production-release` Environment still requires manual approval. Only after approval does the workflow create the GitHub Release, upload packages and `SHA256SUMS`, and update release image tags.

The GitHub Release body uses the short SHA and complete commit message (subject and body) of the tagged commit. It does not expand all history between tags. Re-running the same tag updates the body and replaces attachments.

Docker tags:

| Git reference | Image tags | Release? |
| --- | --- | --- |
| `dev` branch | `dev`, `sha-<full-commit>` | No; for daily debugging. |
| `v1.2.3` tag | `v1.2.3`, `1.2.3`, `1.2`, `latest`, `sha-<full-commit>` | Yes. |

The current Docker workflow has a branch-push entry only for `dev`; `main` does not build a branch image but remains the base branch for release tags. `latest` changes only during approved release publication, not on every `dev` commit. Pin production to `v1.2.3`, `1.2.3`, or a SHA tag; Compose uses `latest` by default.

Formal Windows and macOS package releases still require the corresponding signing-certificate Secrets. Without them, those platforms fail at signing. Linux packages do not require desktop signing certificates.

## Standalone packages

Standalone packages and Docker use the same Playwright Chromium source. Chromium, the headless shell, and the Playwright driver are downloaded during build and placed in the package; Debian repository Chromium is not used. Docker CI prefers architecture-specific GitHub Actions runtime caches. The final Docker image uses a fixed Node.js 24 slim runtime, installs only system libraries required by Playwright Chromium, and clears apt indexes and temporary caches. Users do not need to run a browser download command after installation.

| Platform | Installation | Service identifier | Default URL |
| --- | --- | --- | --- |
| Linux amd64/arm64 | Extract and run `./install.sh` as root | `ydisks-xianyu-helper.service` | `http://127.0.0.1:59188` |
| Windows | Run the Inno Setup installer | `YdisksXianyuHelper` | `http://127.0.0.1:59188` |
| macOS arm64/amd64 | Run the package for the matching architecture | `com.ydisks.xianyu-helper.server` | `http://127.0.0.1:59188` |

Windows and macOS packages also install an independent tray/menu-bar controller. It shows checking, starting, healthy, and stopping states; starts, stops, and restarts the backend; opens the log directory; and waits for confirmed shutdown before exiting. During Windows installation, the interactive user receives permission to query, start, and stop this service, so normal tray actions do not repeatedly show UAC. Changing service configuration or deleting the service still requires administrator rights. Linux has no desktop tray; use systemd and `journalctl -u ydisks-xianyu-helper.service`.

Data and log locations are `C:\ProgramData\YdisksXianyuHelper` on Windows, `~/Library/Application Support/YdisksXianyuHelper` and `~/Library/Logs/YdisksXianyuHelper` on macOS, and `/var/lib/ydisks-xianyu-helper` and `/var/log/ydisks-xianyu-helper` on Linux. Select a CPU-matching arm64 or amd64 package on macOS. Install Linux packages on the same architecture as the host; the installer does not use QEMU.

Local macOS packaging must build the frontend and three executables before calling the single packaging script. If runtime files are missing, the script prepares the matching driver, Chromium, and headless shell from the local Playwright cache:

```bash
npm ci --prefix frontend
npm run build --prefix frontend
mkdir -p dist/macos/arm64
go build -trimpath -ldflags='-s -w' -o dist/macos/arm64/xianyu-server ./cmd/server
go build -trimpath -ldflags='-s -w' -o dist/macos/arm64/browser-install ./cmd/browser-install
go build -trimpath -ldflags='-s -w' -o dist/macos/arm64/xianyu-tray ./cmd/tray
packaging/macos/build-pkg.sh 0.0.0-local "$PWD/dist/macos" arm64
```

For Intel macOS, replace `arm64` with `amd64` and prepare the matching x64 runtime. Without a signing identity, the result is an unsigned pkg and must not be represented as a signed distributable.

## Desktop logs and operations

Windows and macOS tray menus can start, stop, or restart the backend, open the management page, and open the log directory. Choosing Exit Tray stops the backend first, preventing an unmanaged background process. The Windows installer configures the service’s minimum control permission once. A normal interactive user can later query, start, and stop `YdisksXianyuHelper` from the tray without another UAC prompt; changing configuration, deleting the service, and controlling other services still require administrator rights.

- Windows: `C:\ProgramData\YdisksXianyuHelper\logs\server.log`
- macOS: `~/Library/Logs/YdisksXianyuHelper/server.log`, with tray output in `tray.log` in the same directory
- Linux: `/var/log/ydisks-xianyu-helper/server.log`; `journalctl -u ydisks-xianyu-helper.service` is also available

For first use on desktop, open the management page and enter and confirm the administrator password on the initialization page. No database path or terminal is required.

## SQLite

SQLite is suitable for development or light single-machine use. The default database path is `data/xianyu_data.db`:

```bash
go run ./cmd/server -db data/xianyu_data.db -addr :59188
```

This listens on all interfaces. For local-only use, change it to `-addr 127.0.0.1:59188`.

You can also specify:

```bash
DATABASE_URL="sqlite://data/xianyu_data.db" ./xianyu-server
```

Stop the service before backing up, or use an SQLite online-backup tool. Do not copy only the main database file while its WAL is being written. Restore the database together with the original `XIANYU_DATA_KEY`.

## MySQL

Create a UTF-8 database and least-privilege account, then provide its URL:

```bash
DATABASE_URL="mysql://user:URL-encoded-password@tcp(db.example:3306)/xianyu" \
  ./xianyu-server -addr :59188
```

The application adds the required MySQL connection parameters. URL-encode `@`, `:`, `/`, and similar characters in passwords. Configure TLS according to the driver and infrastructure requirements; do not put a real password in shell history.

## PostgreSQL (external service)

```bash
DATABASE_URL="postgres://user:URL-encoded-password@db.example:5432/xianyu?sslmode=require" \
  ./xianyu-server -addr :59188
```

Use `sslmode=disable` only on a trusted local network. Public or cross-network deployments should use TLS. The database account should have only the permissions required by this application.

## Initialization, backup, and upgrade

### Web initialization (recommended)

Open the management page after starting the service. When no administrator exists, the first-run page asks for a password. Enter and confirm at least eight characters; the system creates the `admin` account and signs in automatically. You do not need to know the SQLite path or open a terminal.

### CLI initialization (operations fallback)

Use the CLI for headless environments, automation, or resetting the administrator password:

```bash
go run ./cmd/server -init-admin -db data/xianyu_data.db -admin-password 'strong-password'
```

When an administrator already exists, ensure it exists without resetting its password:

```bash
go run ./cmd/server -ensure-admin -admin-password 'strong-password'
```

PostgreSQL backup example:

```bash
docker compose exec -T postgres pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  > ydisks-backup.sql
```

Upgrade order: back up the database and `.env` → pull/build the new image → `docker compose up -d` → inspect logs for successful migrations → sign in and check accounts and automation. If migration fails, stop and restore from backup rather than continuing.
