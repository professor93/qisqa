# Behind nginx

This page shows how to put nginx in front of qisqa, so that it handles
HTTPS and passes the client's address on.

qisqa speaks plain HTTP, so put a proxy that handles HTTPS in front of it.
With [nginx](https://github.com/nginx/nginx) on the same machine, two
settings in `config.yaml`:

```yaml
listen: "127.0.0.1:8080"         # only nginx can reach qisqa
not_found_limit:
  client_ip_header: X-Real-IP    # the header nginx puts the client address in
```

and one file for nginx, for example `/etc/nginx/conf.d/qisqa.conf`:

```nginx
upstream qisqa {
    server 127.0.0.1:8080;
    keepalive 32;    # reuse the connections to qisqa
}

server {
    listen 80;
    server_name short.example www.short.example;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name short.example www.short.example;

    ssl_certificate     /etc/letsencrypt/live/short.example/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/short.example/privkey.pem;

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;                # qisqa finds the domain by it
    proxy_set_header X-Real-IP $remote_addr;    # replaces whatever the client sent

    location / {
        proxy_pass http://qisqa;
    }

    # Optional: only these addresses may use the API.
    # location /api/ {
    #     allow 203.0.113.10;
    #     deny  all;
    #     proxy_pass http://qisqa;
    # }
}
```

- List every short domain of `domain list`, with and without `www.`, in
  both `server_name` lines. The certificate must cover all of them.
- Without `client_ip_header` every request seems to come from nginx, and one
  client that guesses codes gets everyone blocked.
- Set `client_ip_header` only when qisqa can be reached through the proxy
  alone, as with `listen: "127.0.0.1:8080"`. Anyone who can reach qisqa
  directly can write that header themselves.
