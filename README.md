# sc-plugin-mws

Simple Container's cloud plugin for **MWS (МТС Web Services)**.

Status: **scaffolding.** The plugin parses MWS credentials and nothing else yet.
Read the next two sections before assuming a gap is unfinished work — most of
them are facts about the cloud.

## What this plugin owns

| Surface | Token | State |
|---|---|---|
| Provider | `mws` | ✅ advertised |
| Auth | `mws-service-account` | ✅ parsed + validated at config time |
| VM driver | `mws-vm` | ⛔ not advertised — blocked, see below |
| Registrar | — | ✖ MWS models no DNS zone/recordset resource |
| Resources | — | ✖ no object storage, no container registry |
| State store | — | ✖ follows from "no object storage" |
| Secrets provider | — | ❓ `mws-kms` / `mws-secretmanager` unmeasured |

Every ✖ above was read off
[`mws-cloud-platform/terraform-provider-mws`](https://github.com/mws-cloud-platform/terraform-provider-mws)
at tag `0.0.38` (commit `b4f056748a72`) — 41 resources across 15 modules — not
inferred from the docs site. The package doc on [`api/init.go`](api/init.go)
records each one and what follows from it. The short version:

- **No bucket.** `iam/hmacKey` implies an S3-compatible API exists behind the
  platform, but nothing is modelled in IaC, so SC can neither keep Pulumi state
  in MWS nor hand a `uses:`-able bucket to a service.
- **No container registry.** Image delivery on this cloud is `ssh-tar` or an
  external registry, exactly as for `byo`.
- **No standalone DNS zone or recordset.** A stack on MWS gets its public name
  from Cloudflare (or another registrar plugin). Not to be confused with the
  per-VM *internal* name `mws_compute_virtual_machine` can request — that names
  a machine inside MWS Cloud Platform and nobody outside resolves it.
- **`mws-kms` is a maybe, not a no.** The module exists; what is unmeasured is
  whether MWS KMS answers the *AWS* KMS API, which is what the `awskms://`
  gocloud URL `sc-plugin-yandex` relies on actually requires. Naming is not
  compatibility.

## The VM driver, and why it is not advertised yet

`mws-vm` is the next slice. It is **deliberately absent from `Schema()`**, and
the reason is sharp enough to state here rather than only in a code comment:

SC core calls `vm.RegisterPluginDriver` for every driver a plugin advertises,
and a failure there is `return err` out of the **whole pre-engine plugin load**
([`plugin_preengine.go:265`](https://github.com/simple-container-com/api/blob/main/pkg/clouds/pulumi/plugin_preengine.go)),
not a warning. On any SC core older than the release carrying
[api #558](https://github.com/simple-container-com/api/pull/558), `mws-vm` has an
unflagged placeholder row in core's in-tree `Drivers` table, so registration is
refused with *"which SC core provides itself"* — for a driver core does not
provide at all — and **every deploy that loads this plugin dies**, VM or not.
That is the exact shape of the 2026-10-30 `sc-plugin-cloudru` breakage.

So the driver is a two-release change: #558 ships, the api pin moves in both
modules, `internal.VmDriverSpec()` goes into `Schema()`.
`plugin/internal/vm_driver_test.go` already asserts today's placeholder state and
will fail — on purpose, with instructions — the moment core's table changes.

`mws-vm` *is* in `OwnedTypes`, which is correct and not an inconsistency:
fool-protection reads a VM resource's `provider:` through `api.VmConfigAware`
rather than `res.Type`, so a VM-only stack — which is every first MWS stack — is
reached. Without the entry an operator gets no `plugins.mws:` auto-inject and a
late failure instead of an honest "not implemented yet, see
`sc-ru-clouds-beyond-yandex`".

## Credentials

MWS's provider accepts three paths, and the third is why both schema inputs are
optional upstream:

1. `service_account_authorized_key_path` — **a filesystem path**
2. `mws_token` — a short-lived IAM token
3. running **inside an MWS Compute VM** with a linked service account (IMDS)

There is **no inline-JSON credential field**. That is the one real difference
from `sc-plugin-yandex`, whose `serviceAccountKey` hands the JSON straight to the
provider. SC carries secrets as *values* — `${secret:…}` interpolates a string
and there is no file on the deploying machine to point at — so the plugin tier
has to write the key to a `0600` temp file and pass that path. This is why the
config field is named for its **content** (`serviceAccountAuthorizedKey`) and not
for the provider input it feeds; a field called `path` would invite one that does
not exist on the runner.

```yaml
auth:
  mws-staging:
    type: mws-service-account
    config:
      project: my-project
      zone: ru-central1-a            # optional, defaults to ru-central1-a
      serviceAccountAuthorizedKey: ${secret:mws-sa-key}
```

`AccountConfig.Validate()` refuses, at **parse** time: neither credential; both
(upstream's switch silently prefers the key and never mentions the token it
ignored); no `project`; a key that is not JSON (the commonest real failure — a
`${secret:…}` that resolved to a placeholder looks exactly like any other
non-JSON one-liner); a key with no `privateKey`; `algorithm` other than `ES256`;
a malformed `keyId`; and a `project` that disagrees with the project embedded in
the key's `keyId`. Each of those would otherwise surface at the far end of a
deploy as `load service account authorized key file: …` against a temp path SC
generated for its own copy of the secret — telling the operator their temp file
is bad rather than their credential, and naming a file already unlinked by the
time they look.

The authorized-key document is **camelCase with a structured id**
(`keyId` / `privateKey` / `publicKey` / `expirationTime` / `algorithm`, with
`keyId` of the form `projects/<p>/serviceAccounts/<sa>/authorizedKeys/<id>` that
the SDK parses the project and service-account name *out of*). It is **not** the
Yandex shape (`id` / `service_account_id` / `private_key`) — which is the natural
wrong guess, given how closely the rest of MWS's auth model follows Yandex's, and
was in fact the first guess made here. Source of truth:
`go.mws.cloud/go-sdk@v0.29.0/mws/iam/service_account.go`'s `UnmarshalJSON`.

## Repo layout

```
sc-plugin-mws/
├── api/                      ← config tier. Its own Go module; SC core imports
│   │                           it, so it carries NO cloud SDK.
│   ├── init.go               ← tokens + RegisterProviderConfig +
│   │                           RegisterPluginRequirement; the "what MWS has"
│   │                           package doc
│   ├── auth.go               ← AccountConfig, the path-vs-value constraint,
│   │                           authorized-key validation
│   └── vm.go                 ← documentation only: why the VM config tier needs
│                               no code, and what the bridged VM schema offers
│
├── plugin/                   ← provisioning tier. The go-plugin subprocess SC
│   │                           launches.
│   ├── cmd/sc-plugin-mws/
│   │   └── main.go           ← Schema, VmHosts (refusal), PreDestroy, LogStream
│   └── internal/
│       └── vm_driver.go      ← VmDriverSpec() from core's table + the refusal
│
└── .github/workflows/
    └── release.yml           ← CalVer, three-platform build, OOM-safe pins
```

## Pulumi provider

The VM driver will build on
[`simple-container-com/pulumi-mws`](https://github.com/simple-container-com/pulumi-mws),
a static Terraform bridge (pf flavour) over the upstream provider: 41 resources,
1496 inputs, 53 functions, tokens shaped `mws:<service>/<resource>:<Resource>`.
Once `Schema()` advertises the driver it must also advertise the provider in
`PulumiPlugins` **with its download server** — a third-party provider cannot be
resolved by name alone.

## Working on this repo

`go build ./... && go test ./... -count=1` in **both** modules. Before pushing,
run the lint preflight on every touched file including `_test.go` — the deploy
linter walks the whole module and a skipped test file costs a full round-trip:

```sh
~/go/bin/gofumpt -w <touched>.go
~/go/bin/gci write --skip-generated --custom-order \
  -s standard -s default \
  -s "prefix(golang.org)" -s "prefix(github.com)" \
  -s "prefix(github.com/simple-container-com/go-aws-lambda-sdk)" \
  -s "prefix(github.com/simple-container-com/sc-plugin-mws)" \
  <touched>.go
```

[`TRAPS.md`](TRAPS.md) is the fifteen-trap index every plugin handler must
honour; the canonical catalogue is
[`forge/docs/design/2026-11-01/plugin-extraction-playbook/`](https://github.com/simple-container-com/forge/tree/main/docs/design/2026-11-01/plugin-extraction-playbook/).
The authoring walkthrough is
[`docs/docs/guides/plugin-authoring.md`](https://github.com/simple-container-com/api/blob/main/docs/docs/guides/plugin-authoring.md)
in the api repo. This repo was cloned from
[`sc-plugin-echo`](https://github.com/simple-container-com/sc-plugin-echo), which
is where the harness code deleted here (registrar, compute processor, the
`pulumi.NewContext` + `RunInfo.Mocks` URN-contract harness) should be lifted back
from when the driver lands — **not** re-derived, and **not** `pulumi.RunErr`,
which silently completes stranded registrations on the test's behalf and hid the
whole 2026-10-28 await-returns bug class behind a green suite.

## License

MIT. See [LICENSE](LICENSE).
