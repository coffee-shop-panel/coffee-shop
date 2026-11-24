# Coffee-shop fork of W-UI

This is The Coffee-shop fork of [AbolfazlTafakori/w-ui](https://github.com/AbolfazlTafakori/w-ui)
(AGPL-3.0, same license as upstream). It carries the fixes and features
built for the Coffee-shop fleet on top of upstream 3.2.0. The `coffee-shop`
branch holds them as separate commits over upstream `main`, so upstream
can still be merged in.

## What differs from upstream

- **fix(web):** creating an interface from the panel works (the form no
  longer sends the `awgMode` field the binary rejects).
- **fix(web):** a reseller can see their customers' subscription links.
- **feat(sub):** a 22nd subscription template, `parseh`.
- **feat(sub):** the Coffee-shop subscription page (the WGPanel-era page)
  served when the `parseh` template is picked, with its own API adapter,
  a one-tap Hiddify import of the AmneziaWG configs as a sing-box group,
  a V2Box tile for customers holding Xray configs, a guide tab, and a
  paged config list.
- **fix(nodes):** a 3x-ui panel's generation is probed (`clients/list`)
  before its version number is trusted — late 2.x panels such as
  2.10.13 carry the new clients API under a 2.x number.
- **fix(reconciler):** simultaneous connections from a single public
  address count as one person while they look like a test burst
  (a group subscription being tested from one phone), and as sharing
  again once more credentials than the limit allows have each run
  without a break for over three minutes.
- **feat(update):** self-update fetches this fork's releases
  (`update.Repo = "ali-coffee/coffee-shop"`), signed with this fork's own
  Ed25519 key.

## Releasing

Builds are made with the version and the signing public key compiled in:

```sh
cd web && npm run build && cd ..
go build -trimpath -o wui-linux-amd64 ./cmd/wui \
  -ldflags "-X main.version=<X.Y.Z> -X github.com/abolfazl/w-ui/internal/update.PublicKey=<base64 public key>"
WUI_SIGNING_KEY=<base64 private key> ./wui-linux-amd64 sign wui-linux-amd64
```

`wui keygen` makes a fresh key pair (it prints the private half once;
whoever holds it can sign updates every panel built with the matching
public key will install — keep it off GitHub and off shared machines).

Publish a GitHub release on this repository tagged `v<X.Y.Z>` with two
assets: `wui-linux-amd64` and the `wui-linux-amd64.sig` the sign step
wrote beside it. Panels running a fork build then offer the update in
the panel and install it after verifying the signature.

A build whose version is `dev` never offers itself an update, by
upstream's design — release builds must carry a real version.
