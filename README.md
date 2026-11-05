# Gutter Server

A self-hosted comic library server (Go + SQLite) and the optional power-user
backend for the [Gutter](https://github.com/rthaines211/gutter) iPad/iPhone
reader. It scans a folder of CBZ/CBR/PDF comics, serves pages and covers, and
syncs reading progress.

## Run

```sh
cp config.example.yaml config.yaml   # set the library path, ports, optional API keys
docker build -t gutter-server .
docker run -v $PWD/config.yaml:/etc/comic-reader/config.yaml \
           -v /path/to/comics:/media/Comics gutter-server
```

Metadata matching uses your own ComicVine API key (`comicvine_api_key` in
`config.yaml`) or Metron credentials; none ship with the server.

## License

[GNU AGPL-3.0](LICENSE).
