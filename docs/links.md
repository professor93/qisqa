# Links

This page says how the codes of the links are made and how long to make
them, and how secured links, password links and text mode work.

## How codes work

- A code is a number scrambled with the data folder's `key`. Every domain
  counts the links of each code length 0, 1, 2, and so on. A link's number
  is put through a shuffle that `key` chooses and written with the 62
  letters and digits, `min_length` characters long. Neighbouring numbers
  give unrelated codes, so without `key` nobody can tell which codes are in
  use.
- A code leads straight to its link: its number names the chunk file and
  the place in it. A code that was never handed out is known as such without
  reading any link from disk.
- **A length is used up to a tenth.** Of the 62^length codes of a length,
  qisqa hands out at most a tenth. Then it goes on with codes one character
  longer by itself and logs a warning. Raise `min_length` when you see it.

**Choosing `min_length`.** A random guess finds a code that was handed out
with a chance of *codes handed out ÷ 62^length*, and only a live link opens.
As a length is used up to a tenth, that is at worst 1 guess in 10:

| Length | Codes it hands out | A guess finds a link, with 10,000 links | with 1 million links |
|---|---|---|---|
| 3 | 23,832 | 1 in 24 | (full) |
| 4 | 1,477,633 | 1 in 1,478 | 1 in 15 |
| 5 | 91,613,283 | 1 in 91,613 | 1 in 916 |
| 6 | 5,680,023,558 | 1 in 5.7 million | 1 in 56,800 |
| 7 | 352 billion | 1 in 352 million | 1 in 3.5 million |
| 8 | 21.8 trillion | 1 in 21.8 billion | 1 in 218 million |

The limit on unknown codes slows a guesser down but does not stop one who
has many addresses. So when links lead to something private, choose the
length for the number of links you expect: the length grows by itself only
when a tenth of its codes are handed out, and then a guess finds a link one
time in ten. `min_length` can be changed at any time, and existing links
keep working.

- A code never contains a word from the built-in list of Uzbek, Russian and
  English profanity, nor from `blacklist`. Digits that look like letters
  count as letters: `0` as `o`, `1` as `i`, `3` as `e`, `4` as `a`, `5` as
  `s`, `7` as `t`.
- Codes are case-sensitive: `aB3xK` and `ab3xk` are different links.
- Every domain has its own links. The same code on two domains is two
  different links.
- A domain never hands out a code again, also after its link expired or was
  deleted, or its file was deleted, so an old link does not lead somewhere
  new. Three things undo this: deleting `state.json` or the folder of a
  domain in use, or putting back an older copy of the data folder without
  `qisqa -skip` (see [Data folder](data-folder.md)); and removing a domain
  and adding its host again, which `domain add` does only with `--again`.
  A host added again is a new domain with its own codes: the old links stay
  dead, but a new link there can get a code an old link had. A short link
  still in someone's hands can then lead to a new target. The chance for
  one old link is the new domain's links of that length ÷ 62^length, at
  worst 1 in 10. What to do instead is under `domain remove` in
  [Domains](accounts-and-domains.md#domains).
- An unknown or expired link goes to the domain's fallback URL, else to the
  global `fallback_url`, else to a small 404 page. So does a link of a
  disabled or deleted account, and a link to a blocked host.
- An IP that asks for more than `not_found_limit.per_minute` codes that do
  not exist is blocked for `not_found_limit.block_for` and gets `429` for
  every request. Wrong API tokens and wrong passwords count as well. Expired
  links do not count: those are real people opening an old link.

### Something glued to a link

People and phones glue things to a short link: the full stop of the
sentence, a bracket, a quote, a space, a slash. With `trim_codes` on, the
default, the code ends at the first space, punctuation mark, symbol or
invisible character, so all of these open `aB3xK`:

```
short.example/aB3xK.     short.example/aB3xK .    short.example/aB3xK,
short.example/aB3xK)     short.example/aB3xK"     short.example/aB3xK…
short.example/aB3xK/     short.example/aB3xK.Thanks
```

- The link opens at once, with no redirect to the clean address. Secured
  and password links open the same way.
- `-` and `_` belong to codes and aliases and are no end. Neither is a
  letter of another alphabet: `aB3xKо` with a Cyrillic `о` is not found,
  so nobody is led to another link by a look-alike letter.
- A code that is not found after the cut counts as a code that does not
  exist, as above: otherwise a client could guess codes without limit by
  gluing `.` to each.
- `favicon.ico`, `apple-touch-icon….png` and `browserconfig.xml`, which
  browsers ask for by themselves, are never cut.

## Secured links

A link created with `expires_after_open` is *secured*:

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/articles/2026/11/a-long-title?ref=mail", "expires_after_open": "1m"}'
```

- `"1m"`: the link works for one minute after it is first opened.
  `"0"`: it works exactly once.
- A secured link that nobody opens still expires at `expires_at`.
- Opening it does not redirect. The browser gets a small page whose script
  asks qisqa for the long URL and then goes there. Messengers and other
  programs that only fetch a page to preview it neither use up the link nor
  learn where it leads.
- Every request creates a new code, even for the same URL.
- A secured link cannot be edited. Deleting it kills it at once.
- As for every link, its domain does not hand out its code again; see
  [How codes work](#how-codes-work) for what undoes that.

This is not absolute protection: a scanner that runs a full browser executes
the script like a person's browser does.

## Password links

A link created with `password` opens only for someone who knows the
password:

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/reports/2026/q3.pdf", "password": "plum-river-42"}'
```

- The password is 4 to 128 bytes. qisqa keeps only a salted hash of it
  (PBKDF2-HMAC-SHA256), never the password itself; the answer says
  `"password": true`.
- Opening the link shows a small page with one password field. Its script
  sends `POST /{code}` with `{"password": "..."}`, which is answered with
  `200` and `{"url": "..."}` for the right password, `403` and
  `{"error": "wrong password"}` for a wrong one, and `429` while the link is
  locked. The page and every answer before the right password hold nothing
  of the long URL.
- Five wrong passwords in a row lock the link for five minutes; the right
  password starts the count again. A wrong password also counts against the
  client's IP, as a code that does not exist does. Neither count is written
  to disk.
- No more of a link's passwords are checked at once than could still lock
  it, and no more passwords of all links together than one fewer than the
  CPUs qisqa may use, one at least: with 2 CPUs one at a time, and the other
  CPU is left to the redirects. A password sent while that many are being
  checked gets `429` with `Retry-After: 1`: it is not counted, and a second
  later there is room.
- With `expires_after_open` as well, the link asks for the password first:
  only the right password opens it, so only the right password uses up a
  one-time link.
- Every request creates a new code, even for the same URL. A password link
  cannot be edited. Deleting it kills it at once.

## Text mode

Send `text` instead of `url` and qisqa replaces the URLs inside it:

- Only URLs that start with `http://` or `https://` are replaced, unless the
  request sets `bare_urls`: see
  [URLs without a scheme](#urls-without-a-scheme). Punctuation after a URL
  stays in the text.
- A URL that already points at one of the service's own short domains, with
  or without `www.`, is never replaced: a short link is not shortened again.
- A URL that may not be shortened, or whose short link would not be shorter
  than the URL itself, is left as it is.
- To choose the URLs yourself, list them in `urls`. Then exactly those are
  shortened, and a URL in the list that may not be shortened is an error.
- A URL that occurs several times becomes one link.
- `domain`, `expires_in`, `expires_after_open`, `password` and `format`
  apply to every link of the text.
- Limits: 10 KB of text and 20 links per request.

### URLs without a scheme

With `"bare_urls": true` in the request, a URL written without a scheme,
such as `example.com/page`, is replaced too. Its link leads to `https://`
and the URL as written.

- A word is such a URL when it stands at the start of the text, after a
  space, or after an opening bracket or quote, and its host ends in a domain
  zone that qisqa knows: `example.com`, `www.example.org/a?b=1`,
  `example.com:8080/x`. So `file.txt`, `12.05.2026`, `v1.2` and an e-mail
  address stay as they are. So does a URL with something glued to its
  front: a list mark (`-example.com/page`, `1.example.com/page`) or a word
  of another alphabet.
- `bare_url_zones` in the config says which zones qisqa knows: `top100` (the
  default), `top500` or `all`. The zones are ordered by how many of the
  million most visited sites each one holds: the first 100 hold about 95 %
  of those sites, the first 500 nearly all of them.
- The other rules of text mode hold. A URL that is not longer than its short
  link stays, where the length is that of the URL as written. One of the
  service's own short links is never shortened again. The same URL written
  with `https://` and without a scheme becomes one link.
- Where the space after a full stop is missing, two words can look like a
  host: in `he came.Biz is` the word `came.Biz` ends in the zone `biz`, and
  is replaced if it is longer than its short link. So is the name of a file
  whose ending is a zone, such as `annual-report-2026.pl`. Fewer zones make
  fewer such cases, which is why the default is the first 100.
- With `urls`, `bare_urls` changes nothing: the list names the URLs to
  shorten, each with its scheme.
