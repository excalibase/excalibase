# NATS auth callout — server configuration and load behaviour

The control plane is the delegated authorization server for the platform's
NATS bus: `internal/natsauth.Responder` answers `$SYS.REQ.USER.AUTH`,
verifies the presented credential and mints a user JWT whose permissions
come from the principal matrix.

## Required server configuration

The NATS server config we ship **must** set an explicit authorization
timeout:

```
authorization {
  timeout: 5
  auth_callout {
    issuer: <account public key>
    auth_users: [ auth-callout ]
    account: AUTH
  }
}
```

The server's default is 2 seconds. When the callout does not answer inside
that window the server fails the connection with `Authorization Violation` —
a legitimate client is refused for a reason that has nothing to do with its
credential. A verification is a bcrypt comparison at the default cost: ~46 ms
on an idle core, and an order of magnitude slower on a CPU-limited pod or a
loaded CI runner. Five seconds is the value `natsauth.ServerAuthTimeout`
encodes; the responder's own admission budget is derived from it, so the two
must stay in step. The chart change lives in the service repo.

Keep a single verification well under two seconds regardless. Past that the
server pings inside the client's connect handshake and nats.go fails the dial
with `expected 'PONG', got 'PING'`, which no amount of server-side timeout
will fix.

## Responder concurrency

The NATS client delivers async subscription messages on one goroutine per
subscription, so verifying inline made every connection queue behind one
bcrypt — measured at ~5 connections/second end to end. The responder now
dispatches each request onto a bounded pool (`internal/natsauth/dispatch.go`):

- **Pool size**: `2 × GOMAXPROCS`, clamped to `[8, 64]`. Verification is
  CPU-bound, so the processor budget is the right base; the ceiling stops a
  large node from spawning a pool that only adds scheduler pressure.
  `WithWorkers` overrides it.
- **Queue bound**: `8 × workers` admitted requests (running plus waiting).
  `WithQueueCapacity` overrides it.
- **Backpressure fails closed.** A request that cannot be admitted, or that
  does not reach a worker within 75% of `ServerAuthTimeout`, is answered with
  an explicit denial. It is never allowed through unverified, and it is never
  left to expire silently — the client learns immediately.
- **Panic containment.** A panic inside one request is recovered, logged and
  answered with a denial. It cannot take the subscription goroutine down.
- **Shutdown drains.** `Responder.Close` unsubscribes and then waits for the
  verifications already accepted; `Responder.InFlight` reports what is still
  running.

Measured on a 24-core host: one verification costs 46 ms serially, and the
parallel benchmark reports 4.76 ms/op — about 210 verifications/second. On a
2-CPU pod the same arithmetic gives roughly 43/second, and roughly 21/second
on a single CPU. A whole-platform reconnect storm is tens of connections, so
it drains inside the 5 s budget even on the smallest pod.

**No credential cache.** Concurrency alone provides that headroom, and a
cache would add a revocation window: a rotated or revoked credential would
keep working until the entry expired. If throughput ever becomes the binding
constraint, the cheaper lever is bcrypt cost, and any cache added later must
be invalidated on the rotation path rather than relying on a TTL.

## Platform client dial options

Every platform client dials with `natsauth.ClientOptions`, which layers the
principal's credential and inbox prefix on top of `natsauth.BaseOptions`:
`RetryOnFailedConnect(true)`, `MaxReconnects(-1)`, `IgnoreAuthErrorAbort()`
and handlers that log each state change and set the
`excalibase_nats_connected{client=...}` gauge.

The defaults are actively wrong here. Without `RetryOnFailedConnect` a
publisher built before the bus is up fails construction; with a finite
`MaxReconnects` it gives up on a restarting bus; and nats.go stops
reconnecting after two consecutive authorization errors, so one late callout
takes a service off the bus for the life of the process unless
`IgnoreAuthErrorAbort` is set.

Publishers never drop an event silently: `PgDogNotifier` and
`PolicyChangePublisher` expose `Connected()`, and an event that cannot be
published increments `excalibase_nats_publish_dropped_total{publisher=...}`.
