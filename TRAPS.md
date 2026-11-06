# The traps every SC plugin handler must honour

Fifteen named traps bit the fleet across the five-plugin extraction wave
(sc-plugin-aws / cloudflare / cloudru / mongodb / yandex, 2026-10-18 →
2026-11-01). This file is the index — one line per trap — so an author adding a
handler to this repo can see which safeguards are mandatory before they write
one.

The canonical catalogue, with root-cause narrative + fix pattern + §4 lint-pass
check for every trap, lives at:

**[forge/docs/design/2026-11-01/plugin-extraction-playbook/](https://github.com/simple-container-com/forge/tree/main/docs/design/2026-11-01/plugin-extraction-playbook/)**

Read it before adding a handler. Every trap below cost at least one live round
trip to produce; several destroyed real resources.

> **Read this first — the state of THIS repo.** sc-plugin-mws is scaffolding: it
> has no `Provision`, no `ProvisionRegistrar`, no `ComputeProcess`, no registrar
> bridge, and no live `VmHosts` (see the README for why each is absent). So the
> right-hand column below mostly describes code that is **not here yet** — it is
> what you must bring in, verbatim, from
> [`sc-plugin-echo`](https://github.com/simple-container-com/sc-plugin-echo) in
> the change that adds the handler. Do not re-derive these patterns from the
> trap description; the template's wording is the tested one.

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
| 8 | **A plugin change that depends on an `/api` change is a two-release change** — the operator's `sc` is pinned at a version predating the plugin's `/api` module | Live in this repo: the `mws-vm` driver needs an api RELEASE carrying [api #558](https://github.com/simple-container-com/api/pull/558) (merged 2026-11-06 as `e8dcd103`, *after* `v2026.11.23` was cut — so no release has it, and the pin can't move yet). Advertising it on an older core kills *every* deploy that loads the plugin, not just VM ones. Note the shape #558 chose: core's placeholder row stays `Implemented: false` forever and the plugin hand-builds its own spec, so the driver slice takes a pin bump and **no api commit** — this trap is about releases, not commits. See the README and the package doc on `plugin/cmd/sc-plugin-mws/main.go`. Also §3b of [upgrading-to-plugins.md](https://github.com/simple-container-com/api/blob/main/docs/docs/guides/upgrading-to-plugins.md). |
| 9 | URN parity across the plugin boundary — the acceptance test for any extraction is "preview against pre-split state plans zero changes" | Lift `plugin/internal/urn_contract_test.go` back from sc-plugin-echo with the first resource this plugin registers, and add one assertion per Pulumi `type:name`. Use its `withTrapFreeContext` (`pulumi.NewContext` + `RunInfo.Mocks`) — **not** `pulumi.RunErr`, whose `wait()` silently completes stranded registrations on the test's behalf and hid the whole 2026-10-28 await-returns bug class behind a green suite (Addendum R §R3 of `04-no-destroy-invariant.md`). |
| 10 | Rename via the stack-name field — an operator-visible knob flows into the Pulumi resource name, state identity shifts, orphan outputs destroyed | Split `pulumiResourceName` from the export key. sc-plugin-echo's `ProvisionEcho` carries the canonical comment. |
| 11 | Cloud-specific serialisation normalisation (API Gateway slash-strip on YC was the first) — a spec round-trip drops significant characters | Document any such gotcha for MWS alongside its config struct in `/api` when found. None measured yet. |
| 12 | Secret-reveal asymmetry — `sc secrets reveal` / `hide` behaviour on fields the plugin owns | Relevant here only if `mws-kms` / `mws-secretmanager` ever become registered secrets providers; read trap 12 before writing `InitSecretsProvider`. |
| 13 | Conflating the Pulumi URN with the export-key prefix — a custom name under `TrimStringMiddle` destroys the entire derived subtree on rename | Non-negotiable the moment this plugin introduces an operator-nameable knob (`bucketName:`, `clusterName:`). The knob goes into export keys and cloud-side names, never into the second argument to `RegisterResource`. |
| 14 | A plugin's local collector drops INCOMING compute env vars — containers come up with `uses:`-sourced env unset | Use `runtime.RunComputeProcess`, whose drain and marshal are package-private so you cannot forget to await captured outputs. A hand-rolled `pApi.ComputeContextCollector` needs TWO pools (incoming from `req.GetComputeEnvVars()`, outgoing from `AddEnvVariableIfNotExist`). For nested config reads use `runtime.DecodeInputs[T]`, not a scalar-only `strVal`. |
| 15 | Cross-plugin registrar bridge Outputs dead-stripped — a PeerInvoke wrapped in `ApplyT` whose caller discards the ResourceOutput is dead-stripped by Pulumi, so the RPC never fires and the deploy reads green | Every bridge method needs a `ctx.Export(unique-key, urnOut)` as a FORCING FUNCTION, with keys unique per (stack, resource). sc-plugin-echo ships the commented `scCoreRegistrarBridge` stub with the Export already inside each method. MWS has no DNS, so a bridge here would be *to* another plugin's registrar, not from one. |

## Checklist before cutting a release

Grep across `plugin/`:

- [ ] `grep -r 'defer done()' plugin/` → nothing (trap 1)
- [ ] `grep -r 'pctx\.Close()' plugin/` → nothing outside a `_test.go` harness (trap 1)
- [ ] `grep -r 'drainContext\|pulumiContextWait' plugin/` → nothing (these live in `api/pkg/plugin/runtime` now)
- [ ] `grep -r 'ProvisionResponse{Urn:' plugin/` → every match also sets `Outputs:` (trap 3)
- [ ] Every bridge method on a `scCoreRegistrar`-shape type exports `urnOut` before returning (trap 15)
- [ ] Every operator-nameable knob is used ONLY for export keys and cloud-side names (trap 13)
- [ ] `Schema()` and `api/init.go`'s `OwnedTypes` agree, and `VmDrivers` is non-empty only if the api pin is a release carrying #558 **and** the advertised spec is hand-built with `Implemented: true` (trap 8)
- [ ] `go test ./... -count=1` green in BOTH modules — `go build ./...` does not compile test packages, so an interface change that breaks a fake passes it

## When you hit a new trap

Add it to the playbook, bump its number, and open a one-line PR against this
file. Anything a grep can catch belongs in the checklist above.
