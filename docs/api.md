# API

This page describes the HTTP API: creating, editing and deleting links, the
errors it answers with, and the other addresses qisqa serves.

All API requests carry `Authorization: Bearer <token>` with the token of an
account. Bodies are JSON.
The complete reference is served by qisqa itself, without a token:

- `/api/llms.txt`: the whole API in one plain-text file, written for people
  and for language models;
- `/api/openapi.yaml`: the same as an OpenAPI 3.1 document.

The examples below use `https://short.example` as the address of the service.

## Create a link: `POST /api/links`

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/articles/2026/11/a-long-title?ref=mail", "expires_in": "10d"}'
```

```json
{
  "code": "aB3xK",
  "domain": "short.example",
  "short_url": "https://short.example/aB3xK",
  "url": "https://example.com/articles/2026/11/a-long-title?ref=mail",
  "expires_at": "2026-11-23T10:00:00Z",
  "secured": false,
  "password": false,
  "created": true
}
```

Only `url` is required.

| Field | Meaning |
|---|---|
| `url` | The long URL: `http://` or `https://`, at most 2048 bytes. A host with letters outside ASCII, such as `пример.example`, is turned into the ASCII form a browser looks up, `xn--e1afmkfd.example`: that form is checked, stored and returned in `url`, and the rest of the URL is kept as sent. An IPv4 address is written as four numbers, like `192.0.2.1`, never in an IPv6 form. Other spellings that a browser reads as another host, such as an IPv4 address written as one number, are refused, and so is a host name with an empty label, such as `evil.example..`, or with an `xn--` label that is not the ASCII form of a name, such as `xn--evil-.example`. |
| `domain` | The short domain to use, one of the account's. Default: the account's first domain. |
| `expires_in` | Lifetime of the link. Default: `default_ttl`. |
| `expires_after_open` | Makes the link [secured](links.md#secured-links). |
| `password` | Makes the link ask for a [password](links.md#password-links) of 4 to 128 bytes. |
| `format` | Shape of `short_url`: `https`, `https_www`, `bare` or `www`. Default: `short_url_format`. |

The answer is `201` for a new link. The same request sent again within 10
minutes gets the same link back with `200` and `"created": false`, so a
retry does not make a second link; a secured or password link is always
new. A domain the account may not use is refused with `403`. A URL to a
blocked host, or one whose host is no host name or is written in another
spelling, is refused with `400`:

```json
{"error": "url: \"-пример.example\" is not a valid host name"}
```

## Shorten the URLs inside a text: `POST /api/links`

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"text": "Read more: https://example.com/articles/2026/11/a-long-title?ref=mail. Thank you!", "format": "bare"}'
```

```json
{
  "text": "Read more: short.example/aB3xK. Thank you!",
  "links": [
    {
      "code": "aB3xK",
      "domain": "short.example",
      "short_url": "short.example/aB3xK",
      "url": "https://example.com/articles/2026/11/a-long-title?ref=mail",
      "expires_at": "2026-11-23T10:00:00Z",
      "secured": false,
      "password": false,
      "created": true
    }
  ]
}
```

With `"bare_urls": true`, URLs written without a scheme, such as
`example.com/page`, are replaced as well: see
[URLs without a scheme](links.md#urls-without-a-scheme). See
[Text mode](links.md#text-mode) for the other rules.

## Edit a link: `PATCH /api/links/{code}`

```sh
curl -X PATCH "https://short.example/api/links/aB3xK?domain=short.example" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/articles/2026/12/another-title", "expires_in": "3d"}'
```

```json
{
  "code": "aB3xK",
  "domain": "short.example",
  "short_url": "https://short.example/aB3xK",
  "url": "https://example.com/articles/2026/12/another-title",
  "expires_at": "2026-11-16T10:00:00Z",
  "secured": false,
  "password": false,
  "created": false
}
```

Send `url`, `expires_in` or both. `expires_in` counts from now and may also
shorten the lifetime. `?domain=` is optional and defaults to the account's
first domain. A secured or password link cannot be edited. An account edits
and deletes only its own links; another account's link is answered as one
that does not exist.

## Delete a link: `DELETE /api/links/{code}`

```sh
curl -X DELETE "https://short.example/api/links/aB3xK?domain=short.example" \
  -H "Authorization: Bearer $TOKEN"
```

The answer is `204` with no body, and the link stops working at once.

## Errors

Every error is a JSON object with a message that says what to fix:

```json
{"error": "expires_in: invalid duration \"10x\" (examples: 30m, 12h, 10d)"}
```

| Status | Meaning |
|---|---|
| `400` | The request is wrong, or its URL may not be shortened. |
| `401` | The token is missing or wrong, or its account is disabled. |
| `403` | The domain is not one of the account's, or the account has no domain. |
| `404` | No such link, or it belongs to another account. |
| `409` | The link is secured or has a password, and cannot be edited. |
| `429` | This IP is blocked after too many bad requests. |
| `500` | qisqa failed; the reason is in its log. |

## Other addresses

| Address | What it does |
|---|---|
| `GET /{code}` | Redirects to the long URL with `302`, and an alias to its URL. A secured or password link gets a page instead. |
| `POST /{code}` | Sent by the script of that page: answers with the long URL, see [Secured links](links.md#secured-links) and [Password links](links.md#password-links). |
| `GET /` | Redirects to the domain's fallback URL, else shows a small page. |
| `GET /healthz` | Health check, see [Running qisqa](running.md#health-check). |
| `GET /robots.txt` | Tells search engines not to index short links. |
