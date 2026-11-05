// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

// VmConfigLike exists only to document the shape a VM-driving plugin's
// config implements. Real plugins' VM configs are the shared
// `pkg/clouds/vm.VmConfig` from api — operators write `type: vm` with
// `provider: echo-vm`, SC core parses into `*vm.VmConfig`, and the
// plugin receives it as JSON via `VmHostsRequest.ConfigJson`.
//
// There is nothing to register HERE for the VM driver's config tier:
// `type: vm` is in-tree, parsed by SC core. The plugin only owns the
// *driver* (`echo-vm`), which the plugin bus routes to via VmDrivers in
// Schema() + the VmHosts RPC.
//
// What this file is for: anchoring the fool-protection story. The
// `echo-vm` string in init.go's OwnedTypes list is matched against the
// `provider:` field of a VmConfig via the api.VmConfigAware interface
// (api/pkg/api/plugin_requirements.go) + api PR #495. Without an
// OwnedTypes entry the walker never reaches here and auto-inject
// silently skips VM-only stacks.
//
// Authoring notes for a real cloud:
//   - Carry over the vm.DriverCloudruEvolution-shaped const to your
//     own driver name (e.g. "mws-vm").
//   - Register your driver in `pkg/clouds/vm/capabilities.go`-style
//     Drivers map… NO — that's in-tree. For an out-of-tree plugin, the
//     driver's capabilities row ships inside the plugin's Schema()
//     response as VmDrivers → VmDriverSpec. See the plugin half.
//   - Nothing more needed in /api.
