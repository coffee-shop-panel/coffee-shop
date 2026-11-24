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
  (`update.Repo = "coffee-shop-panel/coffee-shop"`), signed with this fork's own
  Ed25519 key.
- **fix(api):** a machine (node) token reaches only the node protocol
  (v3.2.5 audit) — the six `/api/node/*` routes, the probe, the node
  list and the managed update — not every owner-gated route.
- **feat(v3.3.0):** an owner-only activity log (every operator change
  and sign-in, persisted and browsable in the panel); the coffee
  subscription page shows each node's real reachability and a renew
  banner when a plan is nearly out of time or data; a per-customer
  connection check (plan, devices, nodes, last handshakes); an
  "expiring soon" customer filter; and moving every customer of one
  WireGuard tunnel onto another in one action.
- **feat(v3.4.0):** per-operator Telegram sales bots. Every operator
  (owner, admin, reseller) can run their own shop bot under their own
  BotFather token, set up on a dedicated «بات فروش» page: their plans,
  their card number and/or ZarinPal merchant, their orders and sales
  stats. A buyer purchases inside the bot -- card-to-card with a
  receipt photo the seller approves in Telegram, or ZarinPal with
  gateway verification and a callback on the subscription listener --
  and a paid order becomes a customer under the seller's own scope
  (their assigned tunnels, their client ceiling), or renews the
  buyer's existing customer. The activity store moved from
  internal/api into internal/service, where the architecture tests
  want it.
- **fix(audit, v3.2.5):** the same-address exemption compares hosts,
  not ports; traffic resets respect the caller's scope; hand-down
  checks the client limit; a node's note survives its list toggle;
  3x-ui clients are keyed per inbound; the coffee page downloads
  AmneziaWG configs instead of building broken `wireguard://` links;
  the Interfaces page has exactly two tabs (WireGuard / the 3x-ui
  panel) and the client form gives a panel's inbounds as one switch.

- `feat(v3.5.0)` the shop splits in two: at «خرید اشتراک» the buyer first
  picks **WireGuard** or **V2ray**; plans carry a section, an order
  snapshots it, and fulfillment delivers only that section's tunnels —
  a later purchase of the other section attaches to the same customer
  instead of creating a second one. Delivery and «اشتراک من» are
  per-section too (the V2ray side hands over the `?format=base64` links
  bundle for V2Box/v2rayNG). Card receipts are also stored in the
  panel (`shop_receipts`), so the seller reviews and approves them
  from the orders list; a buyers list and an announce composer (one
  buyer or broadcast, sent through the seller's own bot) round out
  the shop page.

- `feat(v3.6.0)` a second bot per operator: the **management bot**
  («بات مدیریت»), with its own BotFather token and its own panel page,
  separate from the sales bot. Only the operator's own numeric
  Telegram ID may use it — everyone else is refused and nothing else
  ever happens for strangers. Under the operator's own scope it lists
  and searches their customers and opens a detail screen per customer:
  subscription link, cut off / restore, extend days, add volume,
  reset usage, delete (the destructive two behind a confirm). A
  step-by-step wizard creates a customer on the operator's own
  tunnels, and a stats screen totals their book against their
  customer cap.

- v3.6.2 — the management bot's add-customer wizard no longer lists
  every tunnel as a toggle (five rows all named wg0, then eighteen
  Sanaei inbounds). Its second step is now the same section choice the
  sales bot and the panel's client form use: WireGuard, V2ray, or
  both — one tap gives the customer every tunnel of that kind the
  operator may give, and the summary names the section and its count
  instead of the tunnels.

- v3.7.0 — the sales bot grew a seller panel (Mirza-style) in the
  seller's own chat: /start now opens a menu — pending orders (with
  the waiting count on the button; open one to approve/reject through
  the same decision path as the receipt's buttons, or retry a paid
  delivery), my buyers (standing, expiry, usage, the subscription
  link to re-send, and a direct message box), sales stats, the plan
  list, and announcements (broadcast with a confirm step, or one
  buyer). The panel's buttons are gated to the seller's chat alone.

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
