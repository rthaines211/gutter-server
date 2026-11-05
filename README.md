# Gutter Server

A self-hosted comic library server (Go + SQLite) for the
[Gutter](https://github.com/rthaines211/gutter) iPad/iPhone reader. It scans a
comics folder laid out as one folder per series, CBZ/CBR files inside
(`<comics>/<Series>/*.cbz`), serves pages and covers, and syncs reading
progress. Loose files at the top level, deeper subfolders and PDFs are skipped.

## Quick start

1. Download [`compose.yaml`](compose.yaml).
2. Edit it: point the comics path (`/path/to/your/comics`) at the folder that
   holds one folder per series, with CBZ/CBR files inside, and set `GUTTER_PASSWORD`
   (the server refuses to start with the placeholder `change-me`).
3. Start it:

   ```sh
   docker compose up -d
   ```

4. Open `http://<host>:8081`. Sign in with any username and your password. The
   first scan of an empty library starts on its own, and this page shows its
   progress. The admin console is at `/_admin/`.
5. In Gutter, add the server address (`http://<host>:8081`) and enter the
   password when asked.

Your library is mounted read-only, so Gutter never changes your files.

## Docs

- [Configuration reference](docs/CONFIG.md)
- [Remote access](docs/REMOTE-ACCESS.md)

## License

[GNU AGPL-3.0](LICENSE).
