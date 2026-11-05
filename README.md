# sc-plugin-echo

The template every SC cloud plugin clones from.

This repo is a **working** minimum-viable SC plugin:

- `go build ./...` + `go test ./...` pass against it untouched.
- Every RPC class on the SC plugin bus is implemented as a canned-success stub
  so you can grep for `echo`, replace it piece by piece, and run tests
  continuously as you swap in real cloud logic.
- Every handler uses `api/pkg/plugin/runtime`'s `NewContext` + `Settle` +
  `HarvestExports` — the shared scaffolding that encodes the fifteen named
  traps the first five plugin extractions paid for (2026-10-18 → 2026-11-01).
  See [`TRAPS.md`](TRAPS.md) for the index + checklist, and
  [`forge/docs/design/2026-11-01/plugin-extraction-playbook/`](https://github.com/simple-container-com/forge/tree/main/docs/design/2026-11-01/plugin-extraction-playbook/)
  for the canonical catalogue.
- The release workflow in `.github/workflows/release.yml` carries the memory-
  pin that most SC plugin repos learned the hard way on their first release
  run (`GOMAXPROCS=1 GOMEMLIMIT=6GiB`, `go build -p 1`) — keep it even for a
  cloud whose Pulumi SDK is small today.

## Writing a plugin from this template

See the full walkthrough at
[`docs/docs/guides/plugin-authoring.md`](https://github.com/simple-container-com/api/blob/main/docs/docs/guides/plugin-authoring.md)
in the api repo. Short version:

1. **Clone**: `gh repo create <org>/sc-plugin-<cloud> --template simple-container-com/sc-plugin-echo`.
2. **Rename**: `grep -rl echo | xargs sed -i 's/echo/<cloud>/g'` plus module
   renames in both `go.mod` files.
3. **Author the config tier** in `api/` — swap `EchoAccountConfig` for your
   cloud's auth, `EchoDnsRegistrarConfig` for the per-zone shape, add resource
   types. Keep `init.go`'s `RegisterPluginRequirement` call and update its
   `OwnedTypes` list.
4. **Author the provisioning tier** in `plugin/internal/` — the three
   RPC bodies here register logical ComponentResources with no cloud side-
   effects. Swap each body for calls against `pulumi-<cloud>` or a REST
   client. Add the Pulumi provider you now import to Schema's
   `PulumiPlugins`.
5. **Test** using the `pulumi.NewContext` + `RunInfo.Mocks` pattern in
   `plugin/internal/urn_contract_test.go` — NOT `pulumi.RunErr`, which
   silently completes stranded registrations on tests' behalf and hides
   the 2026-10-28 await-returns bug class (Addendum R of
   `04-no-destroy-invariant.md`).
6. **Release**: push to `main` → auto-release with CalVer. Three-platform
   binaries + `sha256sums.txt`.
7. **Pin from an operator's `server.yaml`**:
   ```yaml
   plugins:
     <cloud>:
       source:
         type: github-release
         repository: <org>/sc-plugin-<cloud>
         version: v2026.MM.DD-<sha>
   ```
   or let fool-protection auto-resolve latest.

## Repo layout

```
sc-plugin-echo/
├── api/                     ← config tier. Published as its own Go module;
│                              SC core imports this.
│   ├── init.go              ← RegisterProviderConfig + RegisterPluginRequirement
│   ├── auth.go              ← one AuthType with ReadConfig + Validate
│   └── registrar.go         ← DnsRegistrarConfig + FunctionTemplateConfig stubs
│
├── plugin/                  ← provisioning tier. Compiled to the binary SC
│                              launches as a go-plugin subprocess.
│   ├── cmd/sc-plugin-echo/
│   │   └── main.go          ← go-plugin server, Schema, every RPC
│   └── internal/
│       ├── registrar.go     ← echo DNS registrar (ComponentResource stub)
│       ├── vm_hosts.go      ← echo VmHosts (byo-shaped host at TEST-NET)
│       └── urn_contract_test.go
│
└── .github/workflows/
    └── release.yml          ← CalVer, three-platform build, OOM-safe pins
```

## Prior art

Five live SC plugin repos were the sources this template was lifted from:

- [`sc-plugin-cloudflare`](https://github.com/simple-container-com/sc-plugin-cloudflare) —
  simplest registrar-only plugin, cleanest `main.go` scaffolding.
- [`sc-plugin-yandex`](https://github.com/simple-container-com/sc-plugin-yandex) —
  full provider+resource+state-store surface, origin of the release-workflow
  OOM pins AND the `settle()`/`finishPulumiContext()` pattern now packaged
  in `api/pkg/plugin/runtime`.
- [`sc-plugin-cloudru`](https://github.com/simple-container-com/sc-plugin-cloudru) —
  dual-mode binary (plugin + lifted cobra CLI), used for clouds with no
  Pulumi provider at all.
- [`sc-plugin-mongodb`](https://github.com/simple-container-com/sc-plugin-mongodb) —
  first ComputeProcess-heavy plugin + `PreDestroy` with a database-drop
  hook. Also the origin of the URN-vs-export-key split (trap 13).
- [`sc-plugin-aws`](https://github.com/simple-container-com/sc-plugin-aws) —
  the biggest surface (provider + state-store + secrets-provider + three
  templates + five resources). First plugin to use `BrokerAware` →
  `PeerInvoke` to delegate DNS to cloudflare without a Go-module import.
  Carries the collector-split (trap 14) and ApplyT-forcing-function
  (trap 15) fixes.

## License

MIT. See [LICENSE](LICENSE).
