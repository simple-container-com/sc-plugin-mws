# The traps this template already prevents

Fifteen named traps bit the fleet across the five-plugin extraction wave
(sc-plugin-aws / cloudflare / cloudru / mongodb / yandex, 2026-10-18 →
2026-11-01). This file is the index — one line per trap, so an author
can see at a glance which safeguards are already baked into the template
and which still need deliberate thought as the stub becomes real code.

The canonical catalogue, with root-cause narrative + fix pattern + §4
lint-pass check for every trap, lives at:

**[forge/docs/design/2026-11-01/plugin-extraction-playbook/](https://github.com/simple-container-com/forge/tree/main/docs/design/2026-11-01/plugin-extraction-playbook/)**

Read it before you start a new extraction (gcp, k8s, selectel, vkcs,
mws, timeweb) — every trap below cost at least one live round trip to
produce, several destroyed real resources.

## Already encoded in this template — don't undo

| # | Trap | How the template handles it |
|---|---|---|
| 1 | `defer done()` cascade — handler returns, pending RegisterResource dropped, SC core plans destroy of state | Every handler in `main.go` uses `runtime.NewContext` + `defer runtime.Settle(rctx, &resp, &err)` instead of raw `pctx.Close()`. Settle drains outstanding goroutines via `go:linkname` into `(*Context).wait` before closing. |
| 2 | DryRun not propagated — plugin thinks apply even on `sc preview`, side-effects fire | `runtime.NewContext` reads `header.GetDryRun()` into `sdk.RunInfo.DryRun`. Your handler can read `rctx.Pulumi.DryRun()`. |
| 3 | `ProvisionResponse.Outputs` discarded — `uses:` consumers see "output not found" | `Provision()` in `main.go` returns `runtime.HarvestExports(...)` on the response. Delete the Export line and the harvest proves it: no output = no consumer bind. |
| 4 | Secret flag discarded on harvest — DB passwords, access keys land plaintext in parent state | `runtime.HarvestExports` awaits `sdk.Output` via `internals.UnsafeAwaitOutput` and copies `.Secret` into `InputValue.Secret`. Do not route around it. |
| 5 | Compute-process lookup against the wrong env — isolated stacks (`<wf-id>--svc--child`) build keys against child env, parent exports under staging | `ComputeProcess()` in `main.go` calls `runtime.ParentInfoFromRequest` + `runtime.ResourceInputForComputeProcess`. The latter swaps `env = parent.ParentEnv` when non-empty. |
| 6 | `encoding/json` ignores `json:",inline"` on NAMED fields — StackConfigStatic.Site subtree lost on SC-core → plugin round-trip | api-side fix (`StackConfigStatic` now embeds `StaticSiteConfig`). Nothing to do in the plugin; just don't reach back for `i.Site.X`. |
| 7 | yaml.v3 strict duplicate-key detection — `.sc/stacks/*/server.yaml` fails to parse on dup keys | SC-core-side; not a plugin concern. Mentioned so an author doesn't mis-attribute an operator's parse error to the plugin. |
| 8 | SC core staleness on `/api` package changes — operator's `sc` pinned at a version that pre-dates the plugin's `/api` module → unknown config keys | §3b in [upgrading-to-plugins.md](https://github.com/simple-container-com/api/blob/main/docs/docs/guides/upgrading-to-plugins.md). Document the SC CLI floor your plugin's `/api` module needs. |
| 9 | URN parity across the plugin boundary — the extraction's acceptance test is "preview against pre-split state plans zero changes" | `plugin/internal/urn_contract_test.go` is already set up; add one assertion per Pulumi `type:name` your plugin registers. See its `withTrapFreeContext` helper — do NOT replace with `pulumi.RunErr`. |
| 10 | DB rename via the stack-name field — operator-visible knob flows into the Pulumi resource name, state identity shifts, orphan outputs destroyed | `ProvisionEcho` in `plugin/internal/registrar.go` carries a long comment on the URN-vs-export-key split. Follow the pattern. |
| 11 | API Gateway slash-strip (YC-specific but pattern-general) — spec serialisation drops significant characters | Cloud-specific; if your target cloud has a known normalisation gotcha, document it alongside its config struct in `/api`. |
| 12 | Secret-reveal asymmetry — `sc secrets reveal` / `hide` behaviour on config fields the plugin owns | Operator concern unless your `/api` registers a secrets provider; if it does, read trap 12 before writing `InitSecretsProvider`. |
| 13 | Conflating Pulumi URN with export-key prefix — custom cluster name under `TrimStringMiddle` destroys the entire derived subtree on rename | `plugin/internal/registrar.go`'s `ProvisionEcho` comment names the pattern (split `pulumiResourceName` + `exportClusterName`). Non-negotiable when you introduce an operator-nameable knob. |
| 14 | Plugin's local collector drops incoming compute env vars — Lambda/ECS/Cloud Run containers come up with `uses:`-sourced env unset | If you build a template-type driver, your `pApi.ComputeContextCollector` must have TWO pools (incoming from `req.GetComputeEnvVars()`, outgoing from `AddEnvVariableIfNotExist`). `main.go`'s `strVal` doc points at `runtime.DecodeInputs` for nested configs. |
| 15 | Cross-plugin registrar bridge Outputs dead-stripped — PeerInvoke wrapped in `ApplyT` whose caller discards the ResourceOutput → Pulumi dead-strips the orphan Output → RPC never fires | `scCoreRegistrarBridge` commented stub in `main.go` ships the mandatory `ctx.Export(unique-key, urnOut)` forcing function inside each bridge method. Uncomment, wire, do NOT remove the Export line. |

## Checklist before you cut the first release

Grep across the plugin you're building:

- [ ] `grep -r 'defer done()' plugin/` → nothing (trap 1)
- [ ] `grep -r 'pctx\.Close()' plugin/` → nothing outside of the `_test.go` harness (trap 1 — the real path uses `runtime.Settle`)
- [ ] `grep -r 'drainContext\|pulumiContextWait' plugin/` → nothing (all live in `api/pkg/plugin/runtime` now)
- [ ] `grep -r 'ProvisionResponse{Urn:' plugin/` → every match includes `Outputs:` (trap 3)
- [ ] Every bridge method on a `scCoreRegistrar`-shape type has a `ctx.Export(..., urnOut)` before `return` (trap 15)
- [ ] Every operator-nameable knob is used ONLY for export keys and cloud-side names, never as the second arg to `RegisterResource` (trap 13)
- [ ] Every template-driver collector exposes `EnvVariables()` / `SecretEnvVariables()` that read from a pool seeded by `req.GetComputeEnvVars()` (trap 14)

## When you hit a new trap

Add it to the playbook, bump its number, open a one-line PR against this
file. The template is also where the lint check should land — anything a
grep can catch belongs in the checklist above.
