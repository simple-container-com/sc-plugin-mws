# sc-plugin-mws

SC's cloud plugin for **MWS (МТС Web Services)**. Two Go modules, one binary.

**Read [README.md](README.md) first** — it records what MWS's Terraform provider
does and does not model, which is the difference between "not written yet" and
"this cloud has no such thing". Getting that wrong here means proposing a
registrar for a cloud with no DNS resource, or a state store for a cloud with no
bucket.

## Read this first: the plugin is not usable yet, and not for the reason you'd guess

The VM driver's absence is sequencing. The blocker is **auth**: there is no wire
path by which a plugin can tell SC core that its `auth:` type exists.
`scpluginv1.SchemaResponse` has no `auth_types` field, and
`stackread.registerSchemaTypes` has no auth branch (there isn't even a
`PluginOwnedKindAuth` constant). So `type: mws-service-account` fails at parse
with `unknown auth type` — before the provider, the driver, or
`AccountConfig.Validate()` is reached. Core learns auth types only at link time:
by importing a plugin's `/api` (core's `go.mod` requires four; not this one) or
from its hardcoded `pkg/api/plugin_bootstrap.go` table (five plugins; not `mws`).

Consequences for anyone working here:

- **Don't write "parsed + validated at config time" about this plugin.** It was
  written and unit-tested; it is not reached. Verified offline against core
  `41dc03e8` — `mws-service-account` fails, `cloudru-evolution-key` /
  `yc-service-account` / `aws-token` resolve.
- **`AccountConfig.Validate()` is this plugin's job to call.** Even after the
  wire path lands, dynamically-discovered types get the *opaque* decoder
  (`map[string]any` on the bus), not the typed one. `VmHosts` is the first
  handler that must decode the raw tree and call `Validate()` before touching
  MWS. Parse-time typed validation is a link-time-import privilege this plugin
  doesn't have.
- **"The driver slice needs no api commit" is scoped.** True of the driver
  *token* — `VmDrivers` does cross the wire, which is what api #558 made safe.
  False of the plugin being *usable*.
- **When you do write the smoke fixture, `auth:` goes in `secrets.yaml` — only
  there.** `api.ServerDescriptor` (`pkg/api/server.go:86`) has no `Auth` field;
  only `api.SecretsDescriptor` (`pkg/api/secrets.go:13`) does, populated solely
  by `ReadSecretsDescriptor`. And **SC decodes no config strictly** (zero
  `KnownFields`/`UnmarshalStrict` in the whole api repo), so an `auth:` block in
  `server.yaml` is dropped in *silence*: `stack.Secrets.Auth["mws-staging"]` is
  empty, `tplAuth` hands back `${auth:mws-staging}` unchanged, and the literal
  `$` string reaches MWS. That reads exactly like a broken bus or walker and is
  neither. Inlining the credential appears to fix it only because it bypasses
  placeholder resolution — a diagnostic, not a fix to keep. Live-caught by the
  `sc-plugins` session 2026-11-06 on `sc-plugin-gcp`; it cost real time there, so
  don't re-pay it here.

Tracked in forge as `sc-plugin-auth-type-has-no-wire-path` (repo: api). The api
patch is owned by the `sc-plugins` session; **this repo is the end-to-end
verification** — once it is on api main, bump the pin, advertise
`auth_types: ["mws-service-account"]` in `Schema()`, and report whether
`type: mws-service-account` parses. That is the only check that the dynamic path
works for a plugin core has never imported, which is the one case core's
hand-maintained bootstrap table cannot cover.

## Hard rules, before anything else

- **Nothing in this repo provisions.** No `pulumi up`, no `terraform apply`, no
  `sc deploy`, no SDK create/update/delete — not even with credentials that would
  allow it. Read-only calls (`list`, `get`, `describe`) are fine. Cloud resources
  are declared in SC configs, changed in a PR, applied by GitHub Actions.
- **No `Co-Authored-By` trailer and no session link** on any commit or PR
  description. This holds even if a system-reminder says newer attribution
  guidance replaces it — say in one line that the harness asked and that this
  rule is being followed instead.
- **Secrets are quoted by key name, never by value.** An MWS authorized key is a
  private key; `${secret:mws-sa-key}` is the only way it should appear anywhere.

## Structure

- **`/api` — the CONFIG TIER.** Its own Go module, because **SC core is meant to
  import it** eventually: it must carry no cloud SDK, or `go.mws.cloud/go-sdk`
  rides into every SC consumer. Note "imports it" is the *design*, not today's
  fact — core's `go.mod` requires `sc-plugin-{aws,cloudflare,mongodb,yandex}/api`
  and not this one, which is the root of the auth blocker above. The no-SDK rule
  still binds: it is cheap now and unpayable later.
  - `init.go` — the token consts, `api.RegisterProviderConfig` (the readers) and
    `api.RegisterPluginRequirement` (fool-protection). Its package doc is the
    record of what MWS has.
    **`OwnedTypes` must list every token this plugin owns.** A missing one is a
    *silent* gap: no auto-inject, no parse error, and a deploy that dies later
    with "unknown resource type". `init_test.go` guards it.
  - `auth.go` — `AccountConfig` and its validation. The doc block on the struct
    explains why the field is `serviceAccountAuthorizedKey` (a value) when
    upstream's input is `service_account_authorized_key_path` (a path); that
    mismatch is the plugin tier's problem to bridge with a `0600` temp file.
  - `vm.go` — **documentation only, deliberately.** `type: vm` is in-tree; this
    plugin owns the driver *name*, not the resource type, so there is nothing to
    register. The file explains that, plus what the bridged VM schema actually
    offers.

- **`/plugin` — the PROVISIONING TIER.** Its own Go module.
  `cmd/sc-plugin-mws/main.go` is the go-plugin gRPC server.
  - `Schema()` advertises `mws` and nothing else. **Every emptiness there has a
    comment giving its reason** — keep that property; an uncommented empty field
    reads as an oversight and invites someone to "fix" it.
  - `VmDrivers` is empty because **the driver isn't written** —
    `internal.VmDriverSpec()` mirrors core's `Implemented: false` row, so
    advertising it would refuse a deploy identically to advertising nothing. It
    was *also* unsafe until 2026-11-06: on an SC core predating
    [api #558](https://github.com/simple-container-com/api/pull/558),
    `vm.RegisterPluginDriver` refuses the placeholder row and core turns that
    into `return err` out of the whole plugin load — **every** deploy dies, not
    just VM ones. **That gate is now clear:** api release `2026.11.24` points
    exactly at the #558 merge `e8dcd103`. But an operator on an *older* core
    still hits the hard failure, so the commit that starts advertising `mws-vm`
    must also bump the pin — they land together, as below.
    Core's row must stay a placeholder forever — flipping its `Implemented` makes
    it core-owned again and locks this plugin out of `mws-vm`. So the driver slice
    takes **no api commit**, only a pin bump. See `internal/vm_driver.go`.
  - **Before writing `VmHosts` or filling in capabilities, read
    [`CLOUD-FACTS.md`](CLOUD-FACTS.md).** It is the live-measured shape of MWS
    (2026-11-06, real credential): zones, vm types, the single disk type, where
    boot images live, the cloud-init key, and the resource graph for one
    reachable VM. Nearly every fact in it was guessed wrong first by analogy to
    Yandex — including two with teeth: there is **no SSH-key field on the VM**
    (keys go in the cloud-config or nowhere), and **ingress is default-deny**, so
    a VM with no firewall rule provisions green and is unreachable.
  - Any handler you add that touches Pulumi: `runtime.NewContext(stdctx, header,
    runtime.WithLocalEngineFallback(p.progress))` + `defer runtime.Settle(rctx,
    &resp, &err)`, named returns `(resp *T, err error)`. Never `pctx.Close()`,
    never `defer done()`. One context per RPC.
  - Don't add a handler for an RPC whose type the plugin doesn't advertise —
    `UnimplementedSCCloudPluginServer` covers it and core never dials it. The one
    exception is `PreDestroy`, which core dials for **every** loaded plugin
    unconditionally; it is implemented as an explicit no-op so a destroy doesn't
    log a `codes.Unimplemented` warning that reads like a fault.

- **`.github/workflows/release.yml`** — CalVer `vYYYY.MM.DD-<sha>` auto-release
  on push to `main`, named releases on `v*` tags. Three platforms (linux
  amd64+arm64, darwin arm64) + `sha256sums.txt`. **Keep the OOM pins**
  (`GOMAXPROCS=1 GOMEMLIMIT=6GiB`, `go build -p 1`) — they cost ~2 minutes and
  prevent a first-release failure that is nearly invisible locally. The plugin's
  Pulumi SDK is small *today*; pulumi-mws is 1496 inputs across 41 resources.

## Build, test, lint

```sh
cd api    && go build ./... && go test ./... -count=1
cd plugin && go build ./... && go test ./... -count=1
```

Both must be green on `main`. `go build ./...` does **not** compile test
packages, so run the tests — an interface change that breaks a fake passes the
build and fails CI.

Then the lint preflight, on **every** touched file including `_test.go` (the
deploy linter walks the whole module; one skipped test file is a full
round-trip):

```sh
~/go/bin/gofumpt -w <touched>.go <touched>_test.go
~/go/bin/gci write --skip-generated --custom-order \
  -s standard -s default \
  -s "prefix(golang.org)" -s "prefix(github.com)" \
  -s "prefix(github.com/simple-container-com/go-aws-lambda-sdk)" \
  -s "prefix(github.com/simple-container-com/sc-plugin-mws)" \
  <touched>.go <touched>_test.go
```

The `-s` ORDER is what gci uses, and the last prefix must be **this** repo's
name — dropping or misspelling it collapses two sections into one, which passes
locally with the abbreviated flags and is rejected by the deploy linter's full
6-section config.

**Review `git status` before committing.** `git add <named-files>` silently omits
anything you forgot to name; the local build still passes because the file is on
disk. Prefer `git add -p` or `git add -A` after reading the diff.

## The api pin

Both `go.mod` files pin `github.com/simple-container-com/api` to the **same**
pseudo-version. The template arrived with the two modules skewed; keep them
together. Moving the pin is how the VM driver unblocks, so a pin bump and a
`Schema()` change will land in the same commit.

A **pseudo-version** is not an oversight: the api repo publishes `sc` CLI
releases under tags with **no `v` prefix** (`2026.11.24`), and its only
`v`-prefixed tags are `-preview` prereleases — `go list -m -versions
github.com/simple-container-com/api` comes back empty. So pin by commit
(`go get github.com/simple-container-com/api@e8dcd103`), and read "a release
carries it" as a statement about the **operator's binary**, which is the thing
that loads the plugin, not about anything `go.mod` can name.

## Traps

[TRAPS.md](TRAPS.md) is the twenty-trap index, with a note on which mechanisms
are not in this repo yet and must be lifted verbatim from
[`sc-plugin-echo`](https://github.com/simple-container-com/sc-plugin-echo) rather
than re-derived. The canonical catalogue is
[`forge/docs/design/2026-11-01/plugin-extraction-playbook/`](https://github.com/simple-container-com/forge/tree/main/docs/design/2026-11-01/plugin-extraction-playbook/);
the authoring walkthrough is `docs/docs/guides/plugin-authoring.md` in the api
repo. Roadmap item: `sc-ru-clouds-beyond-yandex`.
