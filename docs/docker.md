# Docker

This page shows how to run qisqa in a container, with the image alone or
with the compose file, and how to put back a copy of its data folder there.

The image holds nothing but the binary, `/qisqa`, on a base without a
shell. It runs as user `65532`, not root, reads its configuration from
`/etc/qisqa/config.yaml` and keeps the data folder in a volume at `/data`.
Relative paths are taken from `/`, so the example's `data_dir: ./data` is
that volume.

```sh
cp config.example.yaml config.yaml
docker build -t qisqa .
docker run -d --name qisqa -p 127.0.0.1:8080:8080 \
  -v "$PWD/config.yaml:/etc/qisqa/config.yaml:ro" -v qisqa-data:/data qisqa
docker exec qisqa /qisqa -config /etc/qisqa/config.yaml domain add short.example
```

[`docker-compose.yml`](../docker-compose.yml) does the same, and starts qisqa
again whenever it stops by itself (`restart: unless-stopped`):

```sh
cp config.example.yaml config.yaml
docker compose up -d --build
docker compose exec app /qisqa -config /etc/qisqa/config.yaml account list
```

To put back a copy of the data folder (see [Data folder](data-folder.md)),
stop the service, put the copy into the volume, and run `-skip` in a
container of its own before you start the service again. The words after
the service name take the place of the image's `-config
/etc/qisqa/config.yaml`, so they give it again:

```sh
docker compose stop app
docker compose run --rm app -config /etc/qisqa/config.yaml -skip 50000
docker compose start app
```

- `-p 127.0.0.1:8080:8080` makes qisqa reachable from this machine only,
  which is where a reverse proxy looks for it. Docker opens a port published
  without an address, as in `-p 8080:8080`, on every network interface, and
  host firewalls such as `ufw` do not stop it.
- User `65532` must be able to read the mounted `config.yaml`.
- Admin commands run inside the container, as in
  `docker exec <container> /qisqa -config /etc/qisqa/config.yaml account list`.
- Keep `data_dir` in `/data`. Anything else is written inside the container
  and is lost with it.
- The service log goes to `docker logs`. With the log files on, set
  `log.dir` to a folder the container's user can write, for example
  `/data/logs` in the volume. The example's `./logs` would be `/logs`, which
  that user cannot create, and qisqa does not start.
