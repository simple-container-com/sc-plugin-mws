# sc-plugin-echo

Template every SC cloud plugin clones from. The point of this repo is
NOT to deploy anything — the resources below register as logical Pulumi
ComponentResources with no cloud side-effects. The point is to carry the
scaffolding and conventions every plugin needs, so a new cloud plugin is
a `gh repo create --template` and a grep-replace of `echo` away.

## Structure

- `/api` — the CONFIG TIER. Separate Go module (SC core imports it).
  Pattern per `init.go`:
  - A `const ProviderType` plus a set of per-token constants
    (AuthType*, RegistrarType*, TemplateType*, ResourceType*,
    Driver*, StateStorageType*, SecretsProviderType*).
  - `init()` calls `api.RegisterProviderConfig(...)` to wire the config
    readers and `api.RegisterPluginRequirement(...)` so SC core's
    fool-protection auto-inject can find this plugin by any owned
    token.
  - **OwnedTypes must list every token this plugin owns.** Missing one
    is a silent gap — an operator who only uses the un-listed token
    gets no auto-inject and the deploy fails later with
    "unknown resource type". `api/init_test.go` guards this.

- `/plugin` — the PROVISIONING TIER. Separate Go module.
  `cmd/sc-plugin-echo/main.go` is the go-plugin gRPC server. Pattern:
  - `Schema()` echoes every owned token into the proto, plus
    `PulumiPlugins` (the SDKs this plugin links against) and
    `VmDrivers` (the vm.Drivers table rows this plugin owns).
  - Every handler opens ONE Pulumi context per RPC via
    `runtime.NewContext(stdctx, header, runtime.WithLocalEngineFallback(p.progress))`
    and defers `runtime.Settle(rctx, &resp, &err)`. The handler's named
    return values must be `(resp *T, err error)` for Settle to fold them.
    Reusing a context across RPCs produces resources that cannot be
    addressed from the right monitor; skipping Settle lets Pulumi's
    teardown drop pending RegisterResource goroutines (trap 1).
  - `runtime.HarvestExports(stdctx, rctx.Pulumi)` is how Provision gets
    its `Outputs` field. Any `rctx.Pulumi.Export(k, out)` call is what
    makes a `uses:` consumer able to read the value. Lose this and
    `uses:` returns "output not found" (traps 3 + 4).
  - `scCoreRegistrarBridge` is a commented stub showing the cross-plugin
    bridge pattern. The `ctx.Export(unique-key, urnOut)` inside each
    bridge method is a FORCING FUNCTION — remove it and Pulumi
    dead-strips the orphan ApplyT when callers discard the
    ResourceOutput, and the PeerInvoke never fires (trap 15).
  - `plugin/internal/registrar.go`'s `ProvisionEcho` carries the
    URN-vs-export-key split comment. When a derived plugin introduces an
    operator-nameable knob (`clusterName:`, `bucketName:`), the knob
    goes into export keys and cloud-side names but NEVER into the second
    argument to `RegisterResource` (trap 13).

- `/plugin/internal/urn_contract_test.go` — the gate that locks the
  URN name contract. Uses `pulumi.NewContext` + `RunInfo.Mocks`,
  **not** `pulumi.RunErr`, so the assertion lands on the handler's
  return rather than on the harness's RPC drain (which would silently
  complete stranded registrations — Addendum R §R3 of
  `04-no-destroy-invariant.md`, incident 2026-10-28).

- `.github/workflows/release.yml` — CalVer `vYYYY.MM.DD-<sha>` auto-release
  on push to `main`, or named releases on `v*` tags. Three-platform
  build (linux amd64+arm64, darwin arm64) + `sha256sums.txt`.
  **Carries the OOM-safe pins** (`GOMAXPROCS=1 GOMEMLIMIT=6GiB`,
  `go build -p 1`). Keep them even for a plugin whose Pulumi SDK is
  small today.

## Running tests

```
cd api && go test ./...
cd plugin && go test ./...
```

Both must stay green on `main`.

## The traps this template already prevents

See [TRAPS.md](TRAPS.md) — fifteen named traps from the five-plugin
extraction wave, with the mechanism this template encodes against each.
The grep-checklist in that file is the minimum pre-release lint pass.

## Writing a new plugin from this template

See `docs/docs/guides/plugin-authoring.md` in the api repo for the full
walkthrough. Short version in the README.
