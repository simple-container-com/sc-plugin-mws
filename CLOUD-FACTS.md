# MWS, as measured

Everything here was read off the **live MWS API** on **2026-11-06** with a real
service-account authorized key (project `project-smecsia`), using the vendor SDK
`go.mws.cloud/go-sdk@v0.29.0`, plus the vendor's own generated docs and example
code at `go.mws.cloud/terraform-provider-mws@0.0.38` (`b4f0567`).

It exists because the `mws-vm` driver is not written yet, and almost every fact
below was *guessed wrong first* by analogy to Yandex. Read it before you fill in
`VmDriverSpec()`'s capabilities or write a single Pulumi resource.

**Provenance matters here.** Where a claim is measured against the live API it
says so. Where it comes from vendor code it names the file. Nothing in this file
is inference — if it isn't measured or vendor-authored, it is in
[What is still unmeasured](#what-is-still-unmeasured) instead.

> One source is NOT reliable: `acctest/resources/compute/testdata/*.tf`. Its
> inline disk spec writes `diskType = "nbs-pl2"` — camelCase, bare id — which
> contradicts the generated schema (`disk_type`, and prefixed elsewhere). Trust
> `docs/resources/*.md` (generated from the schema) and the SDK examples.

## Regions and zones — measured (`rm/sdk.ListZones`, `ListRegions`)

| Zone | Region | Ready |
|---|---|---|
| `ru-central1-a` | `ru-central1` (Москва) | `OK` |
| `ru-central1-b` | `ru-central1` | `OK` |
| `ru-central1-c` | `ru-central1` | `OK` |
| `ru-siberia1-a` | `ru-siberia1` (Новосибирск) | `OK` |

Two regions, four zones. So:

- **`DefaultZone = "ru-central1-a"` in `api/init.go` is a real zone**, not just a
  plausible-looking string. That was an assumption until this probe.
- `Capabilities.Regionless` is **false** — there are genuinely two regions.
- `Capabilities.MultiAZInRegion` is **3** (`ru-central1`). Note `ru-siberia1` has
  only one zone, so code that assumes 3 everywhere is wrong.

`metadata.id` comes back as `zones/ru-central1-a` and the region reference as
`regions/ru-central1`; the bare id is the last segment.

## VM types — measured, 40 of them (`compute/sdk.ListVmTypes`)

Three families, all named `<family>-<vcpu>-<memGB>`:

- **`base-*`** (16): `base-2-4 base-2-8 base-2-16 base-4-8 base-4-16 base-4-32
  base-8-16 base-8-32 base-8-64 base-16-32 base-16-64 base-16-128 base-24-48
  base-24-96 base-32-64 base-32-128`
- **`gen-*`** (22): the same grid plus `gen-12-24 gen-12-48 gen-12-96
  gen-24-192 gen-48-96 gen-48-192`
- **`gpu-a100-*`** (2): `gpu-a100-1-12-128`, `gpu-a100-2-24-256` — here the
  pattern is `gpu-a100-<gpus>-<vcpu>-<memGB>`, so a parser written against the
  two-number families will mis-read these.

vCPU runs 2–48, memory 4–192 GB. The list is a single page (`nextPageToken` nil
at `pageSize: 200`). The vendor example uses `gen-2-8`, which is the sane default
for a smoke.

## Disk types — measured: there is exactly ONE (`compute/sdk.ListDiskTypes`)

```json
{"id":"compute/diskTypes/nbs-pl2","name":"nbs-pl2",
 "spec":{"zones":["zones/ru-central1-a","zones/ru-central1-b",
                  "zones/ru-central1-c","zones/ru-siberia1-a"],
         "limits":{"iops":{"base":1000,"max":10000},
                   "minDiskSize":"1 GB","maxDiskSize":"16 TB"}}}
```

`nbs-pl2`, available in all four zones, 1 GB – 16 TB, IOPS 1000 base / 10000 max.
So a `diskType:` knob in the config has exactly one legal value today — don't
build an abstraction over it, and don't make the operator supply it.

## Boot images — the catalogue is in per-distro system projects

**This is the fact that most needs writing down.** `ListImages` against your own
project returns **0**, and the obvious guesses (`public`, `marketplace`,
`standard-images`, `images`, `mws-public`) all return
`403 PERMISSION_DENIED … rm/projects/<name> is forbidden`. Images live in
**system projects named `mws-<os>`** — documented in one line at
`docs/resources/compute_virtual_machine.md:308` and confirmed live:

| Project | Images | Newest (2026-11-06) | Family |
|---|---|---|---|
| `mws-ubuntu` | 9 | `mws-ubuntu-2404-lts-v20260916` | `mws-ubuntu-2404-lts` |
| | | `mws-ubuntu-2204-lts-v20260916` | `mws-ubuntu-2204-lts` |
| `mws-debian` | 9 | `mws-debian-12-v20260916` | `mws-debian-12-lts` |
| | | `mws-debian-11-v20260916` | `mws-debian-11-lts` |
| `mws-centos` | 2 | `mws-centos-stream-9-v20260916` | `mws-centos-stream-9` |

Two things to not get wrong:

- **Family ≠ name prefix for Debian.** The image is `mws-debian-12-v20260916` but
  its family is `mws-debian-12-**lts**`. Ubuntu's agree; Debian's don't. Deriving
  one from the other by string surgery breaks on Debian.
- **Only the 2026-09-16 batch declares `spec.regions`** (`[ru-central1,
  ru-siberia1]`). Older images carry no region list, so a regional copy may not
  exist in `ru-siberia1` for them. Pin to a dated image you have checked, or read
  `spec.regions` before placing a VM outside `ru-central1`.

Reference form: the full path `compute/projects/mws-ubuntu/images/<name>` (that
is `metadata.id`); the SDK takes the pair via
`computeref.NewMustImageRef("mws-ubuntu", "<name>")`.

There is also a `mws_compute_image` **resource** that creates an image from
`source.external_url` (e.g.
`https://storage.mwsapis.ru/mws-ubuntu/mws-ubuntu-2204-lts-v20250529.334787.raw`).
You do **not** need it to boot a VM — reading the system project is enough — and
a driver that creates one per stack would be copying a multi-GB image needlessly.

## cloud-init — CONFIRMED, via `os.metadata.attributes["user-data"]`

This was flagged UNVERIFIED everywhere in this repo until 2026-11-06, with
`metadata.attributes` described as a Yandex analogy. It is now confirmed by
**vendor-authored code**, which is as good as a live smoke for the question "does
this key exist and does cloud-init consume it":

`go-sdk@v0.29.0/examples/vm_ssh_test.go` (`Example_sshIntoVirtualMachine`)
generates an ed25519 key pair, builds a `#cloud-config` with
`users: / ssh_authorized_keys:`, passes it as
`Os.Metadata.Attributes["user-data"]`, and then **SSHes into the machine and runs
a command**. `acctest/.../virtual_machine.tf` sets the same `user-data` key.

Consequences for `Capabilities`:

- `CloudInit` → **true**.
- `CloudInitNeedsEgress` → **false**. The cloud-config in the vendor path is
  self-contained (an inline authorized key); nothing is fetched over the network
  to apply it.

And one consequence for the driver that is easy to miss:

> **There is no SSH-key field on the VM resource.** Not on `mws_compute_virtual_machine`,
> not on the disk, not on the image. Key injection happens *only* through the
> cloud-config. So `VmHosts` must synthesise `users:`/`ssh_authorized_keys:` into
> the `user-data` attribute — and if an operator also supplies their own
> `user-data`, the two have to be merged, not overwritten. There is likewise no
> vendor-guaranteed default login user: the example's `ubuntu` user exists
> *because the cloud-config creates it*.

## Ingress is default-DENY, and the rule depends on a VM output

From the vendor example's own comment: *"By default, the network firewall blocks
all incoming traffic to the virtual machine, so such a rule is required to
connect to the virtual machine over SSH."*

A VM created without a firewall rule provisions **successfully and is
unreachable** — the "green deploy is not a running container" failure class, on a
new cloud. The rule shape that is known to work (`createFirewallRuleAllowSSH`):

| Field | Value |
|---|---|
| scope | per-**network** (`UpsertFirewallRuleRequest{Network, FirewallRule}`) |
| `direction` | `INGRESS` |
| `priority` | `1000` (legal range **1000–64535**, lower = higher priority) |
| `action` | `ALLOW` |
| `source.spec.cidrs` | `["0.0.0.0/0"]` |
| `destination.spec.cidrs` | the VM's **internal** address `/32` |
| `protoPorts` | `["tcp:22"]` |

The destination is the internal address, not the reserved external one — again
the vendor's words: *"The firewall evaluates packets after the one-to-one NAT
translation."* So the example creates the rule **after** waiting for the VM to be
assigned an internal IP. In Pulumi that is an `ApplyT` on the VM's address
output, which means ordering is handled but the machine is briefly unreachable
after it reports ready.

Firewall rules here are **rules scoped to a network**, not attachable groups —
closer to GCP/YC firewall rules than to AWS security groups. `Capabilities.SecurityGroups`
should be read with that in mind (there is a separate `vpc_address_group`
resource, which is an address grouping, not a rule set).

Untested but worth trying first in the driver: using the **subnet CIDR** as the
destination instead of the VM's `/32`, which would remove the dependency on a VM
output entirely. The vendor path uses the `/32`; the subnet form is unverified.

## The resource graph for one reachable VM

From `docs/resources/compute_virtual_machine.md`'s example and the SDK example,
which agree:

```
mws_vpc_network                                  network
  └─ mws_vpc_subnet                              cidr, e.g. 192.168.0.0/16
       └─ mws_vpc_address                        the NIC's internal address
mws_vpc_external_address                         reserved public IP
mws_compute_disk            (optional — see below)
mws_compute_virtual_machine
  ├─ vm_type         = "vmTypes/gen-2-8"
  ├─ zone
  ├─ hardware        = { power = "ON", graceful_shutdown_timeout = "1m 30s" }
  ├─ os.metadata.attributes["user-data"] = <#cloud-config>
  ├─ storage.disks[] = [{ name="boot", boot=true, disk = { spec | ref } }]
  └─ network.network_interfaces[].addresses[]
       ├─ address      = { ref | spec }
       └─ one_to_one_nat.external.address.ref = <external address>
mws_vpc_firewall_rule                            or the VM is unreachable
```

- **The boot disk can be inline.** `storage.disks[].disk` is a
  `spec`-or-`ref` union: `spec` creates the disk as part of the VM (fewer
  resources, fewer URNs, one less thing to orphan), `ref` points at a standalone
  `mws_compute_disk`. The vendor SDK example uses `spec`; the docs example uses
  `ref`. Prefer `spec` for the boot disk and `ref` for data disks you want to
  outlive the VM.
- **Power is explicit.** `hardware.power = "ON"`. A VM created without it is not
  obviously started.

## Reference formats — three different conventions, by layer

The single most error-prone surface here. Same underlying id, three spellings:

| Layer | Form | Example |
|---|---|---|
| API / `metadata.id` | full path | `compute/vmTypes/gen-2-8`, `compute/projects/mws-ubuntu/images/mws-ubuntu-2404-lts-v20260916`, `zones/ru-central1-a` |
| Terraform / Pulumi attribute | **prefixed short path** | `vm_type = "vmTypes/gen-2-8"`, `disk_type = "diskTypes/nbs-pl2"` |
| Go SDK | **bare id** via typed ref ctor | `computeref.NewMustVmTypeRef("gen-2-8")`, `NewMustDiskTypeRef("nbs-pl2")` |

Two things the table above does not make obvious, both measured while writing the
driver and both of which cost a wrong first draft:

- **A `Ref` serialises SHORT; an `ID` serialises FULL.** Whether you write
  `vmTypes/gen-2-8` or `compute/vmTypes/gen-2-8` is decided by which *field* you
  are filling, not by which value you happen to hold. `vm_type` is a ref → short.
  A cross-resource `.metadata.id` is an id → full.
- **`zone` is the BARE id** — `ru-central1-a`, not `zones/ru-central1-a`. It is
  the one place in the schema that breaks the ref convention, which is exactly why
  it is easy to prefix out of habit.
- **There is no id→name lookup for a network.** `mws_vpc_subnet.network` takes the
  network's *name*, and nothing upstream resolves a network id to one. That is why
  the `mws-vm` driver refuses SC's `network:` block (which carries an `id`) rather
  than half-supporting it — see the refusal in `plugin/internal/vm_hosts.go`.

Cross-resource references in Terraform use **`.metadata.id`** (the full path) —
except `mws_vpc_subnet.network`, which takes the network's `.network` *name*
field. That asymmetry is in the vendor's own example and is not a typo.

Sizes are strings with a unit: `"10GB"` and `"10 GB"` both parse (the vendor uses
both); the unit set is `B KB MB GB TB PB EB ZB YB RB QB`, default `B`. The parser
lowercases its input and tolerates the space, so `"10gb"` is the same value.

**The units are BINARY despite the decimal-SI spelling.** Measured in the
vendor's own parser — `go-sdk@v0.29.0/pkg/apimodels/units/bytesize/unit.go` has
`gb = 1 << 30` and `tb = 1 << 40`, i.e. `"10GB"` is 10 GiB = 10 737 418 240
bytes, not 10 000 000 000. There is no `GiB` spelling at all; `KB` is 1024.

This closes a question the earlier drafts of this file left open, and the
direction matters: reading `GB` as decimal understates every size by ~7% at the
GB scale and ~10% at TB, so a 16 TB-ceiling check written against decimal maths
would refuse disks the cloud accepts. The plugin's own constants are therefore
written as binary shifts (`defaultDiskBytes = 30 << 30`, `maxDiskBytes = 16 <<
40`) and `parseByteSize` accepts the `Gi`/`GiB` spellings an SC operator is used
to as synonyms for the `G`/`GB` MWS actually wants — see
`plugin/internal/mapping.go`.

## What is still unmeasured

Not guesses to fill in — open questions, each needing a live apply:

- **A real VM has never been provisioned by this plugin or by me.** Everything
  above is read-only probing plus vendor code. `Unverified` in `VmDriverSpec()`
  must stay non-empty until a VM boots through SC and `Capabilities` are
  re-checked against it.
- Whether the **subnet CIDR** works as a firewall destination (above).
- Whether `os.metadata.attributes` has a **size limit** on `user-data`. Clouds
  usually cap this (YC: 256 KB); nothing in the schema or docs states one, and SC
  cloud-init payloads are not small.
- **Quota** on this account: `ListVirtualMachines`, `ListNetworks` and the
  project's own `ListImages` were all **0**, so nothing has ever been created
  here and no quota has been exercised.
- `api.mwsapis.ru` from a **GitHub Actions runner** — measured only from the
  operator's line (401/envoy/HTTP-2 in ~85 ms, `188.93.55.134`, no geoblock).
- **`vpc_firewall_rule.spec.ports` syntax.** The driver emits one rule per port
  (`web-fw-tcp-22`, `web-fw-tcp-10250`) because a RANGE spelling is unmeasured —
  whether it is `"22-80"`, a list, or a `from`/`to` pair. One-rule-per-port is
  correct either way; it is only wasteful if ranges turn out to work.
- **Protocol CASE.** The driver writes `"tcp"` lowercase. The schema is a string
  with no documented enum, so `"TCP"` may or may not be the accepted spelling, and
  a case mismatch would be refused at apply rather than at parse.
- **The cloud-side object-name length and charset limit.** `mwsObjectName` in
  `plugin/internal/mapping.go` truncates defensively and keeps the role suffix
  (`-net`, `-subnet`, `-addr`, `-ext-addr`) on the *right* so four names stay
  distinct after truncation, but the actual ceiling is a guess — nothing in the
  schema or the vendor docs states one. A long stack name plus a long env is where
  this will first bite.
- **Whether SC's RENDERED cloud-config is one the guest accepts.** Not the same
  question as the section above, which is settled: the key exists and the guest
  consumes it, proven by the vendor's own `vm_ssh_test.go`. What is open is the
  *document* — `vm.RenderCloudInit` had **zero prior deploy-path callers anywhere
  in the fleet** before this driver, so SC has never had any cloud, on any
  provider, apply its output. A malformed `#cloud-config` fails silently from the
  outside: the machine boots, the apply goes green, and the only evidence is on
  the guest. On the first deploy, read `/var/log/cloud-init-output.log` before
  trusting the green check.

## Reproducing the probe

Read-only, via the vendor SDK. The credential is a **file path**
(`MWS_SERVICE_ACCOUNT_AUTHORIZED_KEY_PATH`), not inline JSON — see
`api/auth.go`.

```bash
export MWS_SERVICE_ACCOUNT_AUTHORIZED_KEY_PATH=/path/to/authorized-key.json
export MWS_PROJECT=<your-project>
# then: rm/sdk.ListZones|ListRegions, compute/sdk.ListVmTypes|ListDiskTypes,
#       compute/sdk.ListImages{Project: "mws-ubuntu"}
```

`mws.Load(ctx)` reads the environment and needs nothing else. Pipe any output
through a redactor — the key document and `v1.*` tokens must never reach a log.
