# Short codes

Every link has a short code. The code is either generated for you or given
by you as `slug` when the link is created. It is the last part of the short
URL, built from base_url: `https://sl.example/x7Kp2` has the code `x7Kp2`.

## Generated codes

By default a code is code_length characters long, 5, drawn from letters and
digits. The look-alike characters `0`, `O`, `1`, `l`, and `I` are left out,
so a code typed from a screenshot or read aloud is less likely to be
mistaken for another one. Change code_length in config/defaults.yaml if 5
is too short or too long; shorter codes run out sooner as links pile up.

To create a link from the command line:

```sh
shortlink links add https://example.com/a/long/path
```

## Custom codes

Give a code with the `--slug` flag:

```sh
shortlink links add https://example.com/a/long/path --slug launch
```

or with the `slug` field when creating a link through the API (see
docs/api.md). A slug must be 3 to 32 characters and may contain letters,
digits, and hyphens.

If the slug is already in use, the request fails with 409 Conflict. Pick
another slug, or delete the existing link first:

```sh
shortlink links delete launch
```

The codes `api`, `admin`, `healthz`, and `metrics` are reserved and cannot
be used as slugs.

## Target rules

The target of a link is a URL, with these rules:

- It must use the http or https scheme.
- It must be no longer than max_url_length, 2048 bytes by default.
- Its domain must not be listed in blocked_domains.

A target that breaks a rule is refused when the link is created. A blocked
domain gets 400 with the error `domain blocked`. Set blocked_domains in
config/defaults.yaml to refuse whole domains you never want shortened.

## Why a target cannot be changed

A link's target is fixed at creation. There is no update operation, so a
short code that has been printed, mailed, or posted always points where it
pointed when it was made. That keeps a shared short URL stable: nobody can
retarget it later and send existing readers somewhere else.

To point a code somewhere new, delete the link and create it again with the
same slug:

```sh
shortlink links delete launch
shortlink links add https://example.com/new/path --slug launch
```

This only works while the code is free, so do the two steps close together.
Before deleting, check redirect_status in config/defaults.yaml. With 301
the old target may be cached by browsers that have already visited the
link, and those browsers keep going there even after the link is deleted or
recreated; see docs/redirects.md. With the default 302, browsers pick up
the new target on their next visit.
