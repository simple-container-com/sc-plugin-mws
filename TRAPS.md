# The traps every SC plugin handler must honour

Twenty-one named traps bit the fleet across the five-plugin extraction wave
(sc-plugin-aws / cloudflare / cloudru / mongodb / yandex, 2026-10-18 →
2026-11-01) and the two that followed it (mws + gcp, 2026-11-06). This file is
the index — one line per trap — so an author adding a handler to this repo can
see which safeguards are mandatory before they write one.

**Traps 17–21 are the ones to read first**, and unlike most of the list they
are not about a handler you haven't written yet — they are about whether
anything here runs at all. Trap 20 is about the very first fixture anyone writes
in this repo; trap 21 is about the first handler, and is the one whose window
closes permanently on the first successful apply.

Status, 2026-11-06: **17 is CLEARED** — api #559 merged as `99411193`, this
repo pinned to it and advertising `AuthTypes` in `38ab03c`, so an operator's
`type: mws-service-account` now parses. **18, 19 and 20 are the ones that
stay**: they are the standing price of being an out-of-tree plugin, not bugs
anyone is going to fix for us. 19 and 20 say the same thing in the end —
**core's parse no longer refuses anything on our behalf, so every refusal
happens in this process, at deploy time, after the state-store lock is taken.**
**21 is pinned but not discharged**: `urn_contract_test.go` fixes the ten names
`mws-vm` will claim, and no MWS machine has ever been provisioned, so the first
apply is what turns that snapshot from a choice into a contract.

The canonical catalogue, with root-cause narrative + fix pattern + §4 lint-pass
check for every trap, lives at:

**[forge/docs/design/2026-11-01/plugin-extraction-playbook/](https://github.com/simple-container-com/forge/tree/main/docs/design/2026-11-01/plugin-extraction-playbook/)**

Read it before adding a handler. Every trap below cost at least one live round
trip to produce; several destroyed real resources.

> **Read this first — the state of THIS repo.** `VmHosts` is now live: it
> advertises the `mws-vm` driver, materialises the credential, and registers ten
> resources against `pulumi-mws`. Traps 1, 2, 9 and 21 are therefore about code
> that **is** here, and `plugin/internal/` is the place to read the pattern.
>
> Everything else still is not: no `Provision`, no `ProvisionRegistrar`, no
> `ComputeProcess`, no registrar bridge (see the README for why each is absent).
> For those, the right-hand column describes code you must bring in, verbatim,
> from [`sc-plugin-echo`](https://github.com/simple-container-com/sc-plugin-echo)
> in the change that adds the handler. Do not re-derive these patterns from the
> trap description; the template's wording is the tested one.
>
> And "live" means *written and unit-tested*, not *shipped*: no MWS machine has
> been provisioned by anything, and the `pulumi-mws` provider has no GitHub
> release, so the engine cannot yet download `pulumi-resource-mws` at all.

## The traps, and the mechanism that prevents each

| # | Trap | The mechanism |
|---|---|---|
| 1 | `defer done()` cascade — handler returns, pending RegisterResource dropped, SC core plans destroy of state | Every Pulumi-touching handler uses `runtime.NewContext` + `defer runtime.Settle(rctx, &resp, &err)`, never raw `pctx.Close()`. Settle drains outstanding goroutines via `go:linkname` into `(*Context).wait` before closing. The handler's named returns MUST be `(resp *T, err error)` for Settle to fold them. |
| 2 | DryRun not propagated — plugin thinks apply even on `sc preview`, side-effects fire | `runtime.NewContext` reads `header.GetDryRun()` into `sdk.RunInfo.DryRun`; read it back via `rctx.Pulumi.DryRun()`. |
| 3 | `ProvisionResponse.Outputs` discarded — `uses:` consumers see "output not found" | Return `runtime.HarvestExports(stdctx, rctx.Pulumi)` on the response. Any `rctx.Pulumi.Export(k, out)` is what makes a value readable by a consumer. |
| 4 | Secret flag discarded on harvest — DB passwords, access keys land plaintext in parent state | `runtime.HarvestExports` awaits via `internals.UnsafeAwaitOutput` and copies `.Secret` into `InputValue.Secret`. Do not route around it with a hand-rolled harvest. |
| 5 | Compute-process lookup against the wrong env — isolated stacks (`<wf-id>--svc--child`) build keys against the child env while the parent exported under staging | `runtime.ParentInfoFromRequest` + `runtime.ResourceInputForComputeProcess`; the latter swaps `env = parent.ParentEnv` when non-empty. |
| 6 | `encoding/json` ignores `json:",inline"` on NAMED fields — a whole config subtree lost on the SC-core → plugin round-trip | Honoured only on EMBEDDED fields. [`api/auth.go`](api/auth.go)'s embedded `api.Credentials` is the legal use and says so; never copy that tag onto a named field. |
| 7 | yaml.v3 strict duplicate-key detection — `server.yaml` fails to parse on dup keys | SC-core-side; listed so an author doesn't mis-attribute an operator's parse error to the plugin. |
| 8 | **A plugin change that depends on an `/api` change is a two-release change** — the operator's `sc` is pinned at a version predating the plugin's `/api` module | The `mws-vm` driver needs an api RELEASE carrying [api #558](https://github.com/simple-container-com/api/pull/558) (merged 2026-11-06 as `e8dcd103`). **That release exists: api `2026.11.24` IS `e8dcd103`** (`compare/e8dcd103...2026.11.24` → identical). Two naming traps make it look absent — api release tags carry **no `v` prefix** (`gh release list` column 1 is the *title* `v2026.11.24`, column 3 the *tag*), and an api "release" is the `sc` CLI binary, not a Go module version, so pins stay pseudo-versions moved by commit. Advertising the driver on an *older* core still kills **every** deploy that loads the plugin, so the commit that advertises `mws-vm` must bump the pin in the same breath. Note the shape #558 chose: core's placeholder row stays `Implemented: false` forever and the plugin hand-builds its own spec, so the driver slice takes a pin bump and **no api commit** — this trap is about releases, not commits. **But see trap 17: "no api commit" is true of the driver *token* and false of this plugin being usable.** See the README and the package doc on `plugin/cmd/sc-plugin-mws/main.go`. Also §3b of [upgrading-to-plugins.md](https://github.com/simple-container-com/api/blob/main/docs/docs/guides/upgrading-to-plugins.md). |
| 9 | URN parity across the plugin boundary — a handler EXTRACTED from core must register the same `type:name` pairs it did in-tree, or the first preview against pre-split state destroys and recreates everything | Acceptance test is "preview against pre-split state plans zero changes". **Not applicable to `mws-vm`**: it is greenfield, there is no pre-split state, and nothing anywhere has ever registered an MWS resource. That is what makes trap 21, not this one, the live one here — the names are not being *preserved*, they are being *chosen*, and this is the only moment choosing them is free. |
| 10 | Rename via the stack-name field — an operator-visible knob flows into the Pulumi resource name, state identity shifts, orphan outputs destroyed | Split `pulumiResourceName` from the export key. sc-plugin-echo's `ProvisionEcho` carries the canonical comment. |
| 11 | Cloud-specific serialisation normalisation (API Gateway slash-strip on YC was the first) — a spec round-trip drops significant characters | Document any such gotcha for MWS alongside its config struct in `/api` when found. None measured yet. |
| 12 | Secret-reveal asymmetry — `sc secrets reveal` / `hide` behaviour on fields the plugin owns | Relevant here only if `mws-kms` / `mws-secretmanager` ever become registered secrets providers; read trap 12 before writing `InitSecretsProvider`. |
| 13 | Conflating the Pulumi URN with the export-key prefix — a custom name under `TrimStringMiddle` destroys the entire derived subtree on rename | Non-negotiable the moment this plugin introduces an operator-nameable knob (`bucketName:`, `clusterName:`). The knob goes into export keys and cloud-side names, never into the second argument to `RegisterResource`. |
| 14 | A plugin's local collector drops INCOMING compute env vars — containers come up with `uses:`-sourced env unset | Use `runtime.RunComputeProcess`, whose drain and marshal are package-private so you cannot forget to await captured outputs. A hand-rolled `pApi.ComputeContextCollector` needs TWO pools (incoming from `req.GetComputeEnvVars()`, outgoing from `AddEnvVariableIfNotExist`). For nested config reads use `runtime.DecodeInputs[T]`, not a scalar-only `strVal`. |
| 15 | Cross-plugin registrar bridge Outputs dead-stripped — a PeerInvoke wrapped in `ApplyT` whose caller discards the ResourceOutput is dead-stripped by Pulumi, so the RPC never fires and the deploy reads green | Every bridge method needs a `ctx.Export(unique-key, urnOut)` as a FORCING FUNCTION, with keys unique per (stack, resource). sc-plugin-echo ships the commented `scCoreRegistrarBridge` stub with the Export already inside each method. MWS has no DNS, so a bridge here would be *to* another plugin's registrar, not from one. |
| 16 | A per-plugin collector's `AddOutput` as a no-op silently discards ApplyT-wrapped env mutations | Never hand-roll a collector. `runtime.Collector` + `runtime.NewProvisionCollector(req)` + `runtime.RunComputeProcess(...)`; the drain is package-private so you cannot forget it. `grep -rn 'type capturingCollector' plugin/` must return nothing. |
| 17 | **A plugin's `auth:` type has no wire path** — `type: mws-service-account` dies at PARSE with `unknown auth type`, before the provider, the driver or any validation; the plugin binary is never dialled | **CLEARED 2026-11-06 — api #559 merged as `99411193`, advertised here in `38ab03c`.** `DetectAuthProvider` is one lookup into `providerConfigMapping`, seeded only at link time: core importing a plugin's `/api` (four plugins; not this one) or core's hardcoded `plugin_bootstrap.go` table (five; not `mws`). `SchemaResponse` had no `auth_types` field, so the dynamic-discovery path the bootstrap table's own doc recommends could not register auth. Additive `auth_types` is api PR #559 / `aa504f87`. Verified end-to-end here against a locally-built core + this plugin over the real wire: with `AuthTypes: []string{AuthTypeMwsServiceAccount}` on `Schema()`, `mws-service-account` resolves and `auth["mws"]` decodes to `*api.PluginFieldConfig` (the opaque decoder — core has never linked our `/api`); with that one line removed and nothing else changed, `unknown auth type "mws-service-account"`. Advertisement + pin bump landed as one commit on purpose — split, each half is useless and one is misleading. **What survives:** an out-of-tree auth type **requires an explicit `plugins: mws:` block in the same stack's `server.yaml`** — auto-injection cannot bootstrap it (`DiscoverPluginTypes` runs before `ValidatePluginRequirements`), and omitting it reproduces the pre-fix error verbatim, so that is the first thing to check if you see `unknown auth type` again. Roadmap: `sc-plugin-auth-type-has-no-wire-path`. |
| 18 | **SC decodes no config strictly** — a misplaced or misspelled key is dropped in SILENCE, and `auth:` in `server.yaml` is a no-op | Zero `KnownFields`/`UnmarshalStrict` in the whole api repo. `ServerDescriptor` has no `Auth` field; only `SecretsDescriptor`/**`secrets.yaml`** does. Symptom is `${auth:x}` reaching the cloud literal, which looks exactly like a broken bus or walker and is neither — and *identical* to the real 2026-11-03 decoder regression, so the symptom cannot tell you which you have. Diff key names against struct tags first. Inlining the credential "fixes" it by bypassing placeholder resolution: a diagnostic, never a committed fixture. |
| 19 | **Validation that lived in the in-tree `Read*` path stops running on the opaque path** — and the first symptom is nothing at all | A dynamically-discovered type gets `pluginOwnedWrappingDecoder`, which keeps `Config.Config` as a raw `map[string]any`; parse-time typed validation is a link-time-import privilege. Live in this repo: `AccountConfig.Validate()`'s five refusal paths are written, unit-tested, and **called on no deploy path**. `VmHosts` must call it on raw-tree decode. Verify with a negative test through the *deploy* — a direct call to `Validate()` passes while the function is dead. |
| 20 | **A string field whose CONTENT is JSON breaks if the operator writes it as a YAML mapping** — and on our path it breaks at DEPLOY time, in this process, with core's parse already green | On the opaque path, core's `CredentialsValue()` (api `pkg/api/read.go:537`) marshals the whole raw auth map to JSON; `coerceToStringIfScalarNumber` coerces scalars and **passes nested maps through unchanged**. Measured live 2026-11-06 (api `aa504f87`): `secrets.yaml` parses clean, `auth["mws"]` decodes to `*api.PluginFieldConfig`, no warning, and the nested object crosses the wire verbatim — the refusal lands here at `api.ConvertAuth` as `json: cannot unmarshal object into Go struct field AccountConfig.serviceAccountAuthorizedKey of type string`. A typed (link-imported) plugin would have been refused at parse time with a line number; we trade that for cadence independence, so this decode is the **only** guard. `AccountConfig.ServiceAccountAuthorizedKey` is a `string` holding the authorized-key JSON, which makes MWS the fleet's first config with its own named field of this shape — the coercer's tested case is number→string (api run 41728259367), object→string is the sibling. Fixture rule: `${secret:mws-sa-key}` or a `|` block scalar, never a nested block. Documented on the field in `api/auth.go`; `TestAuthorizedKeyMustArriveAsAString` pins it against the measured bytes. |
| 21 | **URN snapshot pinning for a GREENFIELD handler** — the names a new driver chooses become permanent on its first apply, and a later "tidy-up" rename is a destroy-and-recreate of the machine and its boot disk, with nothing in the diff saying so | Pin every `type:name` in a test that drives the REAL handler under mocks, in the commit that writes it. `plugin/internal/urn_contract_test.go` does this for `mws-vm` (10 names). Two rules the harness itself has to follow, each of which has cost a live round trip: (a) open the context with `pulumi.NewContext` + `RunInfo.Mocks`, **never** `pulumi.RunErr` + `WithMocks` — RunErr's `wait()` silently completes stranded registrations on the test's behalf and hid the whole 2026-10-28 await-returns bug class behind a green suite (Addendum R §R3 of `04-no-destroy-invariant.md`); (b) drive the handler END TO END rather than asserting on a static list, so the test also proves the output graph resolves — a hand-written assertion cannot catch a name that is never reached because an `ApplyT` upstream of it errored. Corollary, measured here: the mock must inject the computed properties the handler blocks on (`metadata.id`, `status.ipAddress`, the tls key halves), because `requireString` turns an absent one into an error and an errored output never reaches `RegisterResource` — so a missing mock field presents as a NAME MISMATCH. A registration that happens in a goroutine with nothing awaiting it is invisible to the mock entirely; a test that registers one resource in isolation (e.g. just the provider) must await its URN by hand via `internals.UnsafeAwaitOutput` or it asserts against an empty capture list and passes. And pin the refusals too: assert `len(captured) == 0` alongside every trap-19 refusal, because a handler that refuses on its fifth resource leaves a half-built network in the operator's project. |

## Checklist before cutting a release

Grep across `plugin/`:

- [ ] `grep -r 'defer done()' plugin/` → nothing (trap 1)
- [ ] `grep -r 'pctx\.Close()' plugin/` → nothing outside a `_test.go` harness (trap 1)
- [ ] `grep -r 'drainContext\|pulumiContextWait' plugin/` → nothing (these live in `api/pkg/plugin/runtime` now)
- [ ] `grep -r 'ProvisionResponse{Urn:' plugin/` → every match also sets `Outputs:` (trap 3)
- [ ] Every bridge method on a `scCoreRegistrar`-shape type exports `urnOut` before returning (trap 15)
- [ ] Every operator-nameable knob is used ONLY for export keys and cloud-side names (trap 13)
- [ ] `Schema()` and `api/init.go`'s `OwnedTypes` agree, and `VmDrivers` is non-empty only if the api pin is at or past `e8dcd103` (shipped as release `2026.11.24`) **and** the advertised spec is hand-built with `Implemented: true` — the advertisement and the pin bump land in the SAME commit (trap 8)
- [ ] `mws-service-account` resolves THROUGH CORE: a throwaway program importing core *and* `pkg/stackread` calls `stackread.ReadServerDescriptor` **then** `api.DetectAuthProvider(&api.AuthDescriptor{Type: "mws-service-account"})` without `unknown auth type`. Three things the harness needs or you will debug the harness instead of the plugin: that read ORDER (it mirrors `provisioner.ReadStacks` — `readServerDescriptor` at `provision.go:106`, `readSecretsDescriptor` at `:132`, same process, same global maps), a blank-import of `pkg/clouds/pulumi` (else the parse dies on `unknown provisioner type "pulumi"`), and an explicit `plugins: mws:` block with `path:` pointing at the local binary. Controls that must resolve: `aws-token`, `yc-service-account`, `cloudru-evolution-key` (trap 17)
- [ ] `AccountConfig.Validate()` is called by a handler on raw-tree decode, with a negative test that asserts the **deploy** refuses a bad config (trap 19)
- [ ] No fixture puts `auth:` in `server.yaml`, and none inlines a credential (trap 18)
- [ ] `serviceAccountAuthorizedKey` is a YAML **string** in every fixture — `${secret:…}` or a `|` block scalar, never a nested mapping (trap 20)
- [ ] `grep -rn 'WithMocks\|pulumi.RunErr' plugin/` → nothing; the harness is `pulumi.NewContext` + `RunInfo.Mocks` (trap 21)
- [ ] Every `type:name` the handlers register is asserted in `urn_contract_test.go`, and a new resource arrives with its assertion in the SAME commit (trap 21)
- [ ] `go test ./... -count=1` green in BOTH modules — `go build ./...` does not compile test packages, so an interface change that breaks a fake passes it

## When you hit a new trap

Add it to the playbook, bump its number, and open a one-line PR against this
file. Anything a grep can catch belongs in the checklist above.
