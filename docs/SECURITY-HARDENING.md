# Security configuration

Password hashes use Argon2id with independent random salts (64 MiB, two passes,
one lane). Existing fixed-salt hashes remain compatible and migrate after the
next correct password verification. Password resets invalidate existing sessions.
No password reset is required for this upgrade.

Password login accepts at most 16 KiB, applies a ten-second body read deadline,
allows ten attempts per source per minute and 120 attempts globally per minute,
and admits at most two concurrent password checks. IPv6 addresses are grouped
by /64. A limited request returns HTTP 429 and a Retry-After header.

Only explicitly trusted reverse proxies may supply forwarded client IP and
scheme headers. Native loopback proxies are trusted by default. For a proxy
reaching a container through a Docker gateway, configure the **actual gateway
address**, for example:

```text
KOMARI_TRUSTED_PROXIES=127.0.0.1,::1,172.22.0.1
```

The variable accepts comma-separated IP addresses or CIDRs. An explicit empty
value trusts no proxies. Invalid entries and catch-all /0 networks are rejected.
Do not list client networks or broad shared container networks as trusted proxies.
The edge proxy must overwrite forwarded headers from untrusted clients.

When a reverse proxy terminates HTTPS, expose the application port only to that
proxy (for a proxy on the same host, publish `127.0.0.1:25774:25774`). Agents and
browsers should use the HTTPS site address. This preserves the normal agent,
terminal and file-manager routes while preventing direct access around the edge.
