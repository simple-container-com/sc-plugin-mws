# sc-plugin-mws

SC's cloud plugin for **MWS (МТС Web Services)**. Two Go modules, one binary.

**Read [README.md](README.md) first** — it records what MWS's Terraform provider
does and does not model, which is the difference between "not written yet" and
"this cloud has no such thing". Getting that wrong here means proposing a
registrar for a cloud with no DNS resource, or a state store for a cloud with no
bucket.

## Read this first: auth is cleared; what it leaves behind is yours to call

The plugin's one hard blocker is gone. It was **auth**: there was no wire path by
which a plugin could tell SC core that its `auth:` type exists, so
`type: mws-service-account` died at parse with `unknown auth type` — before the
provider, the driver, or `AccountConfig.Validate()` was reached. Core learned auth
types only at link time: by importing a plugin's `/api` (core's `go.mod` requires
four; not this one) or from its hardcoded `pkg/api/plugin_bootstrap.go` table
(five plugins; not `mws`).

**api #559 fixed it, merged as `99411193`** — additive `repeated string
auth_types = 11` on `SchemaResponse`, a `PluginOwnedKindAuth` constant, and one
loop in `stackread.registerSchemaTypes`. This repo pins that commit and
advertises the type in `Schema()` as of `38ab03c`; the pin bump and the
advertisement are deliberately **one** commit, because on an older core the proto
field does not exist and either half alone is useless. Verified end-to-end
against a locally-built core with this plugin over the real gRPC wire —
`auth["mws"]` decodes to `*api.PluginFieldConfig`, and a second binary differing
only in that one line still gets `unknown auth type`, which is what makes the
green run mean something rather than merely not-fail.

Consequences for anyone working here:

- **`AccountConfig.Validate()` is this plugin's job to call — and as of the
  `mws-vm` slice it IS called**, at `plugin/internal/vm_hosts.go:160`, on the raw
  tree `VmHosts` decodes, before anything touches MWS. Dynamically-discovered
  types get the *opaque* decoder (`map[string]any` on the bus), not the typed one,
  so core performs **no** parse-time validation of the auth tree; parse-time typed
  validation remains a link-time-import privilege this plugin doesn't have. Any
  future handler has the same obligation — the call is per-handler, not global.
- **Don't write "parsed + validated at config time" about this plugin.** The
  validation runs at DEPLOY time, in this subprocess. Until the `mws-vm` slice it
  ran nowhere at all despite being fully unit-tested, which is trap 19's whole
  point: a direct unit test on a validator passes just as happily when nothing
  calls it. If you add a handler, add a negative test that refuses through the
  *handler*, not through `Validate()`.
- **Every refusal is ours, at deploy time, after the state-store lock.** Because
  the decoder is opaque there is no string-typed field for core to reject a nested
  mapping against: a `serviceAccountAuthorizedKey` written as a YAML object parses
  clean, warns nothing, and crosses the wire verbatim — the refusal lands here in
  `api.ConvertAuth` (`json: cannot unmarshal object into Go struct field
  AccountConfig.serviceAccountAuthorizedKey of type string`) in a subprocess, mid
  deploy. Measured 2026-11-06; see TRAPS 20.
- **An operator must declare an explicit `plugins: mws:` block** in the same
  stack's `server.yaml`. `DiscoverPluginTypes` reads `descriptor.Plugins` *before*
  `ReadServerConfigs`, and `ValidatePluginRequirements` injects specs inside it —
  so auto-injection runs after type discovery and cannot bootstrap this. Omitting
  the block reproduces the pre-fix `unknown auth type "mws-service-account"`
  byte-identically, which reads exactly like "the patch didn't land".
- **"The driver slice needs no api commit" is scoped.** True of the driver
  *token* — `VmDrivers` does cross the wire, which is what api #558 made safe.
- **`auth:` goes in `secrets.yaml` — only there.**
  `api.ServerDescriptor` (`pkg/api/server.go:86`) has no `Auth` field; only
  `api.SecretsDescriptor` (`pkg/api/secrets.go:13`) does, populated solely by
  `ReadSecretsDescriptor`. And **SC decodes no config strictly** (zero
  `KnownFields`/`UnmarshalStrict` in the whole api repo), so an `auth:` block in
  `server.yaml` is dropped in *silence*: `stack.Secrets.Auth["mws-staging"]` is
  empty, `tplAuth` hands back `${auth:mws-staging}` unchanged, and the literal
  `$` string reaches MWS. That reads exactly like a broken bus or walker and is
  neither. Inlining the credential appears to fix it only because it bypasses
  placeholder resolution — a diagnostic, not a fix to keep. Live-caught by the
  `sc-plugins` session 2026-11-06 on `sc-plugin-gcp`; it cost real time there, so
  don't re-pay it here.

Tracked in forge as `sc-plugin-auth-type-has-no-wire-path` (repo: api). The api
patch was owned by the `sc-plugins` session; this repo was the end-to-end
verification, and it is the only check that the dynamic path works for a plugin
core has never imported — the one case core's hand-maintained bootstrap table
cannot cover.

The `mws-vm` driver is written, advertised and unit-tested. **The remaining work
is a live deploy**, and it is blocked on something this repo cannot decide for
itself: `pulumi-mws` has no GitHub release, so the engine has nothing to download
the provider binary from (and it cannot be built on the box this was written on —
it needs Go 1.27). Cutting a release is a distribution decision; propose it to the
operator with the alternative and wait. Until a machine boots,
`VmDriverSpec().Unverified` stays non-empty and CLOUD-FACTS' open list is the
honest inventory.

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
  - `Schema()` advertises the `mws` provider, `mws-service-account`, the
    `mws-vm` driver and two `PulumiPlugins`. **Every remaining emptiness there
    has a comment giving its reason** — keep that property; an uncommented empty
    field reads as an oversight and invites someone to "fix" it.
  - `VmDrivers` carries **one hand-built spec**, `internal.VmDriverSpec()`, with
    `Implemented: true` and the capabilities measured in CLOUD-FACTS. Do not
    "simplify" it back to `vm.Drivers[vm.DriverMws].ToProto(...)`: core's row is
    an unflagged placeholder (`Implemented: false`), and advertising that would
    refuse a deploy identically to advertising nothing.
    **Core's row must stay a placeholder forever** — flipping its `Implemented`
    without `MigratedToPlugin` makes it core-owned again under
    `coreOwned() = Implemented && !MigratedToPlugin` and locks this plugin out of
    its own driver name. `TestCoreRowMustStayAPlaceholder` watches that, on both
    fields (`Implemented && MigratedToPlugin` is a legitimate core state, so a
    test reading only `Implemented` would report a correct api change as a break).
    So the driver slice took **no api commit**, only a pin bump.
  - **The advertisement carries a hard core-version floor.** On an SC core
    predating [api #558](https://github.com/simple-container-com/api/pull/558),
    `vm.RegisterPluginDriver` refuses the placeholder row and core turns that into
    `return err` out of the whole pre-engine plugin load — **every** deploy dies,
    not just VM ones. The minimum is `e8dcd103`, shipped as api release
    `2026.11.24`. Both `go.mod` files are pinned at or past it, and that pin and
    the `Schema()` advertisement landed in the same commit *because* either half
    alone is useless or dangerous. Never relax one without the other.
  - **`PulumiPlugins` values are read off the generated SDK, not chosen.**
    `{mws, 0.0.1-dev, github://api.github.com/simple-container-com/pulumi-mws}`
    is exactly what `pulumi-mws`'s `PkgResourceDefaultOpts` bakes into every
    resource; `pulumiVersion.go`'s empty `SdkVersion` is dead code. The field is
    `PluginDownloadUrl` — `PulumiPluginRequirement` has no `Server` field. `tls`
    is `5.5.0` per `go.mod`; **do not copy sc-plugin-cloudru's advertised
    `5.3.0`**, which disagrees with cloudru's own module.
    Caveat that outranks all of the above: **`pulumi-mws` has no release**, so the
    engine cannot fetch `pulumi-resource-mws` yet. Cutting one is a distribution
    decision — propose it, don't do it.
  - **Before adding a handler or changing capabilities, read
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
