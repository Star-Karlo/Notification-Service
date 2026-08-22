# Karlo Notification Service

**HTTP** `:5004` · **gRPC** `:6004` · **Store** MongoDB

The single outbound-messaging boundary: in-app, push, email and WhatsApp, plus
OTP and support tickets.

It consolidates four things the monolith kept apart — the `Notification` model
and its FCM hook (117 creation sites across two services), the chat system with
its own separate hook, `nodemailer` called inline from five controllers, and the
WhatsApp/OTP/ticket service.

**Callers state what happened and who should hear about it.** This service picks
the channels, renders the copy, and delivers. No business code names FCM, SMTP
or WhatsApp.

## Getting started

```bash
cp .env.example .env          # set SERVICE_TOKEN, ACCEPTED_SERVICE_TOKENS
make run
```

Delivery channels are **off unless fully configured**, and `/health` reports
which are live. A half-configured channel that fails at send time is worse than
one that is off, because the failure surfaces as a missing notification rather
than a startup error someone can act on.

## The in-app record is the source of truth

It is written **before** any delivery is attempted, and per-channel outcomes are
recorded on it. In the monolith a Mongo `post('save')` hook drove FCM, so a
delivery failure was invisible and an undeliverable notification was
indistinguishable from one nobody raised.

Delivery never fails the RPC: an order that was approved has been approved,
whether or not the push went out.

## Retries are safe

A caller may pass an `IdempotencyKey`. The claim is written to Redis with
`SET NX` **before** delivery, not after: claiming afterwards leaves a window in
which a retry arriving mid-send sees no claim and delivers a second time, which
is exactly the case retries produce. The sparse-unique `idempotencyKey` on the
notification document is the durable backstop.

When Redis is absent the claim falls back to the Mongo lookup, and if that also
fails **the notification is sent**. A duplicate notification is an annoyance; a
dropped one may mean a driver never learns they were assigned a job. That is the
opposite of how the login rate limiter degrades, and both choices are written
down in `../docs/CACHING.md` §5 so nobody changes one to match the other.

## Also guarded against the legacy database

Same two defences as the master data service, with an `nt_` prefix. The legacy
models own `notifications`, `messages`, `messagegroups` and `messagetemplates`,
all of which still hold live data the monolith reads.


## Layout

```
cmd/server/           entrypoint
internal/
  config/             environment loading; no defaults for security controls
  models/             domain types
  repository/         the only code that talks to the database
  services/           business rules
  handlers/           HTTP
  grpcserver/         gRPC contract implementation
  clients/            outbound gRPC to other services
  routes/             the HTTP surface, one handler per path
  platform/           shared plumbing, vendored (see below)
proto/                gRPC contracts
docs/                 generated OpenAPI document
tests/unit/           no I/O; run always
tests/integration/    real database; build-tagged
```

## Platform documentation

The cross-service documentation — data ownership, business flows, testing and
observability — is **not in this repository**. It describes all four services,
so it lives once in the workspace that holds them side by side, at
`../docs/`, rather than in four drifting copies.

This README covers what is specific to this service.

## About `internal/platform`

This directory is **vendored, not authored here**. It holds the plumbing every
Karlo service shares: RS256 token verification, the structured logger, the
gRPC server and client setup, the query allowlist, and the HTTP response
envelope.

Each service repo carries its own copy so it is fully standalone. The cost is
that a change to shared plumbing — a fix to token verification, say — has to be
applied to all four repos. **`internal/platform/authctx` is security-critical:
a change there must land everywhere.**

The same applies to `proto/`. The contracts are duplicated by design; when one
changes, copy the updated `.proto` into every repo that speaks it and run
`make proto` there. CI fails if the committed bindings do not match the
contracts in the repo, which catches a forgotten regeneration but not a
forgotten copy.

## Commands

| | |
|---|---|
| `make tools` | install buf, the protoc plugins, swag and golangci-lint |
| `make run` | run the service |
| `make test` | unit tests |
| `make test-integration` | integration tests (needs a database; see above) |
| `make lint` | golangci-lint |
| `make proto` | regenerate the gRPC bindings |
| `make swagger` | regenerate the OpenAPI document |
| `make docker` | build the container image |

## API documentation

`make run`, then open **http://localhost:5004/swagger/index.html**.

The browser is served only outside production: the document describes every
endpoint and response shape, which is the reconnaissance an attacker would
otherwise have to guess at. A test asserts it 404s when `ENVIRONMENT=production`.
