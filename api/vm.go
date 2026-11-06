// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

// This file is documentation, deliberately: the VM driver's config tier needs no
// code, and the reason is the thing worth writing down.
//
// A machine in MWS is a `type: vm` resource with `provider: mws-vm`. `type: vm`
// is IN-TREE — SC core parses it into `*pkg/clouds/vm.VmConfig` and hands it to
// the plugin as JSON on `VmHostsRequest.ConfigJson`. So there is nothing here to
// register with api.RegisterProviderConfig: this plugin owns the DRIVER NAME, not
// the resource type.
//
// Two consequences that are easy to get wrong:
//
//  1. **The driver's capability row is NOT declared here.** It ships inside the
//     plugin binary's Schema() response as `VmDrivers → VmDriverSpec`, read from
//     `vm.Drivers[vm.DriverMws].ToProto(vm.DriverMws)` so the plugin and SC core
//     are quoting the same table rather than two copies of it. Today that row is
//     `Implemented: false` with `RoadmapItem: sc-ru-clouds-beyond-yandex`, which
//     is why `provider: mws-vm` is refused at validate time with "not implemented
//     yet" — the honest state of this repo until the driver lands.
//
//  2. **`DriverMwsVm` still belongs in init.go's OwnedTypes**, even though
//     `type: vm` is in-tree, because fool-protection's walker reaches the
//     `provider:` string through the `api.VmConfigAware` interface
//     (pkg/api/plugin_requirements.go:221) rather than through `res.Type`. Without
//     the OwnedTypes entry, a VM-ONLY stack — which is every first MWS stack,
//     there being no bucket, registry or DNS resource in this cloud to pair it
//     with — would get no `plugins.mws:` auto-inject and fail late.
//
// A WARNING about note (1), because this comment used to say the opposite and
// the opposite is harmful: whoever implements the driver must NOT touch core's
// `mws-vm` row. An earlier version of this paragraph instructed the next author
// to clear its `Implemented: false` in the api repo and ship a paired api
// release. api #558 (`e8dcd103`, merged 2026-11-06) chose the other shape —
// `coreOwned() = Implemented && !MigratedToPlugin` — so a never-implemented
// placeholder row is no longer mistaken for a built-in and a plugin's registered
// row wins on registration, `LookupDriver` and `PluginForDriver`. Under that
// rule, setting `Implemented: true` in core would make the row core-owned again
// and lock THIS plugin out of its own driver name. Core's row stays a
// placeholder permanently; the plugin hand-builds its own spec. So the driver
// slice is NOT playbook trap 8's two-release change — it takes no api commit at
// all, only a pin bump. And the release half is already satisfied: api release
// `2026.11.24` points exactly at `e8dcd103`.
//
// Prior art for the driver itself: sc-plugin-cloudru's `cloudru-evolution-vm` is
// the fleet's first plugin-owned VM driver (stdout-parsed address via
// AddressOutput, docker installed over SSH afterwards, trust-on-first-use because
// the cloud refuses user-data).
//
// What `mws_compute_virtual_machine`'s schema says, as read at tag 0.0.38 — facts,
// so the driver's author starts from these rather than from the cloudru shape:
//
//   - `virtual_machine` (name), `vm_type` (flavour), `zone`, `project`,
//     `service_account`, plus nested `network` / `storage` / `hardware` / `os`.
//   - A public address is `network.network_interfaces[].addresses[].one_to_one_nat`
//     — so a floating IP is modelled, and modelled AT CREATION rather than as a
//     separate attach.
//   - `vpc_firewall_rule` and `vpc_address_group` exist, so ingress is expressible
//     in IaC.
//   - A VM can be given a platform-internal DNS name
//     (`…addresses[].address.spec.dns.name`, with `ptr` and `ttl`). That is NOT a
//     public zone: there is still no zone/recordset resource, so the public name
//     comes from another registrar. Do not mistake one for the other.
//   - **Cloud-init is UNVERIFIED.** There is no `user_data` attribute anywhere in
//     the provider's docs. There is `os.metadata.attributes` (Map of String),
//     which is the slot Yandex's provider uses for `user-data`, so cloud-init is
//     plausible by analogy and nothing more. `vm.Drivers[vm.DriverMws]` therefore
//     leaves `CloudInit` at its zero value, which capabilities.go defines as "not
//     measured yet" — the driver's author must measure it against a real project
//     and fill the row in the change that records the smoke. A guessed `true` here
//     would be branched on by shared code (readiness gate, docker install path,
//     host-key seeding) and would fail twenty minutes into a deploy.
