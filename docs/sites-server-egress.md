# Server sites: outbound requests (EXC-661)

Every `fetch()` a server site's code makes goes through the outbound Worker
(`edge/sites-outbound`), bound on the dispatch namespace by
`edge/sites-router/deploy.sh --outbound`. Publishing server code is refused
while the router has no outbound Worker bound.

What the outbound Worker does:

* only `http`/`https` on ports 80 and 443;
* refuses private, local and reserved addresses, and every host listed in
  `PLATFORM_HOSTS`;
* lets the code reach its own project's API paths only, without the
  platform's `x-excalibase-*` headers or forwarding headers;
* drops a `Host` header the code set, and any `cf` options;
* follows redirects itself, checking every hop, and drops credentials on a
  cross-origin hop.

Per Cloudflare's outbound Workers docs, a bound outbound Worker also blocks
`connect()`, but it does not see fetches from Durable Objects or mTLS
certificate bindings. A site Worker can hold only text bindings and
`ASSETS`, so it gets neither.

## Preconditions before turning on server sites in prod

The fence matches names and IP literals. It cannot see what a name resolves
to, so a customer domain that points at our origin IP gets through. The
Worker can't close that gap, so the origin has to:

1. **HAProxy's default backend denies unknown hosts.** A request for a Host
   we don't serve gets a deny, never a Studio or API backend.
2. **Our zones use authenticated origin pulls or mTLS.** The origin accepts
   only Cloudflare's client certificate for our zones, so a request that
   arrives through a customer's zone is refused.
3. **DB edge ports aren't reachable from the internet.** The MetalLB DB
   ports stay closed to the public internet (the fence also allows only 80
   and 443).
4. **Origin IPs go in `PLATFORM_HOSTS`.** Every origin IPv4 and IPv6 address
   is listed in `edge/sites-outbound/deploy.sh --platform-hosts`, so a
   literal IP is refused.
