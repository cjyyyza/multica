# Local release build

When we say "构建" (build), this is the procedure: produce the full artifact set
under `dist/build-YYYYMMDD-HHMMSS/` with verification, modeled on
`dist/build-20261014-171027` (0.6.1) and `dist/build-20261017-090904` (0.6.3).
`dist/` is gitignored; artifacts are never committed.

## Inputs

- Version comes from `git describe --tags --match 'v[0-9]*' --always --dirty`.
  A clean checkout at a release tag yields e.g. `0.6.3`; a dirty or untagged
  snapshot yields the describe string. Record the exact HEAD and version in
  `build-info.json` before starting.
- Docker builds run in WSL (Ubuntu-24.04); Windows has no Docker daemon.
  Invoke as `MSYS_NO_PATHCONV=1 wsl -- bash -c '...'` from Git Bash.
- Go binaries are built with the GoReleaser ldflags:
  `-s -w -X main.version=<v> -X main.commit=<short> -X main.buildCommit=<full> -X main.date=<iso8601>`.

## Artifact set

```
dist/build-<stamp>/
├── build-info.json                  # head, version, tag, platforms, startedAt, dirty
├── BUILD-RECEIPT.json               # artifact sha256/bytes + checks (see below)
├── daemon-windows-x64/              # multica.exe + LICENSE/NOTICE/README.md +
│                                    #   version.txt + daemon-help.txt + daemon-start-help.txt +
│                                    #   go-build-info.txt + SHA256SUMS.txt
├── windows-client/                  # multica-desktop-<v>-windows-x64.exe (+ .blockmap) +
│                                    #   README.md + desktop.json.example
├── docker-server/                   # docker-compose.yml (image tag pinned) + .env.example +
│                                    #   init-env.sh + LICENSE/NOTICE/README.md +
│                                    #   multica-images-linux-amd64.tar.gz + image-manifest.json +
│                                    #   SHA256SUMS.txt
├── multica-server-<v>-docker-linux-amd64.zip   # zipped docker-server/
└── logs: docker-build.log, docker-load.log, docker-images-inspect.json,
    artifact-smoke.spec.ts, artifact-smoke.log, web-login.png,
    bundled-cli-version.txt, docker-cli-version.txt, installer-check/
```

### Build steps

1. **CLI**: `go build` `cmd/multica` (plus `server`/`migrate`/`maintenance` as
   needed) for windows/amd64 into `server/bin/`. The desktop packager rebuilds
   and bundles its own copy (`server/bin/windows-amd64/`).
2. **Desktop client**: `cd apps/desktop && CSC_IDENTITY_AUTO_DISCOVERY=false
   node scripts/package.mjs --win --x64 --publish never`. Version derives from
   `git describe`; output in `apps/desktop/dist/`.
3. **Server image**: build the all-in-one image (`Dockerfile.allinone` — API
   server, migrations, and web frontend in one container) in WSL:
   `docker build -f Dockerfile.allinone -t ghcr.io/cjyyyza/multica-allinone:<tag>
   --build-arg VERSION=<v> --build-arg COMMIT=<short> .`
4. **docker-server package**: `docker save` the all-in-one image and
   `pgvector/pgvector:pg17` into `multica-images-linux-amd64.tar.gz`. The
   packaged `docker-compose.yml` is `docker-compose.selfhost.allinone.yml` with
   the image reference pinned to the built tag; the packaged `.env.example`
   sets `MULTICA_IMAGE_TAG` to that tag and adds `MULTICA_ALLINONE_IMAGE`.
   Extract `manifest.json` from the tar as `image-manifest.json`, then
   `sha256sum` the tar into `SHA256SUMS.txt`.
5. **Zip**: `Compress-Archive docker-server → multica-server-<v>-docker-linux-amd64.zip`.

## Verification (receipt checks)

Run each and record `passed` / `not run` honestly in `BUILD-RECEIPT.json`:

- `backendDockerBuild`, `webDockerBuildAndTypecheck` — from the image build log
  (Next build runs TypeScript; keep the full log as `docker-build.log`).
- `desktopTypecheck` — repo-wide `pnpm typecheck` at the built commit.
- `databaseStartupAndMigrations` — `docker compose up` the packaged compose in a
  scratch project with host ports moved (e.g. 18080/13000) and PostgreSQL
  published via an override for test access; count applied migrations from the
  container log and record as `recordedMigrations`. Tear down with `down -v`.
- `webLoginAndIssueWriteSmoke` — Playwright spec (see
  `dist/build-20261017-090904/artifact-smoke.spec.ts`): `/health` must report
  the built short commit, the login page must render without page errors, and
  `TestApiClient` must log in and create an issue through the built images.
  Copy the spec into `e2e/` (rewriting the fixtures import to `./fixtures`) to
  run it, and export `MULTICA_DEV_VERIFICATION_CODE=` empty so the DB code is
  used rather than any dev-environment override. Screenshot the login page as
  `web-login.png`.
- `dockerOfflineLoad` — untag the local images, `docker load -i` the packaged
  tar, confirm the restored IDs (`docker-load.log`, `docker-images-inspect.json`).
  Never untag `pgvector/pgvector` while the dev environment's postgres
  container references it.
- `artifactIntegrity` — extract `$PLUGINSDIR/app-64.7z` from the installer with
  7-Zip and confirm the installer's `resources/app.asar` and bundled
  `app.asar.unpacked/resources/bin/multica.exe` sha256 match the build tree
  (`installer-check/`).
- `fullTestSuite`, `desktopLiveUi` — record `not run` unless explicitly asked.

## Conventions

- The Windows installer is unsigned (ad-hoc); record `windowsInstallerSigned`.
- Do not commit anything under `dist/`.
- If packaging requires a source fix, commit it separately first (conventional
  commit) and list the files in the receipt's `packagingFixes`.
