// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// Package mws holds the provisioning tier of sc-plugin-mws: everything that
// needs a Pulumi context or an MWS SDK. It is a SEPARATE Go module from ../../api
// on purpose — SC core imports the config tier, and this module's dependency on
// the pulumi-mws bridge must not ride along into every SC consumer.
package mws

import (
	"github.com/pkg/errors"
	"github.com/simple-container-com/api/pkg/clouds/vm"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
)

// ErrVmDriverNotImplemented is what the VmHosts RPC returns until the driver
// lands. A named error rather than an inline one so the plugin's test can assert
// on it without matching a string, and so the message is written once.
var ErrVmDriverNotImplemented = errors.Errorf(
	"vm driver %q is not implemented in this version of sc-plugin-mws: "+
		"the plugin currently owns only the %q provider type (credentials), "+
		"see the roadmap item %q",
	vm.DriverMws, "mws", vm.Drivers[vm.DriverMws].RoadmapItem,
)

// VmDriverSpec is the capability row this plugin will advertise in Schema() when
// the driver lands. Today it mirrors SC core's placeholder row, which means it
// says `Implemented: false` — i.e. advertising it right now would be exactly as
// useful as advertising nothing, because LookupDriver would return this row and
// VmConfig.Validate would refuse `provider: mws-vm` with the same "not
// implemented yet" message an operator gets with no plugin installed.
//
// # The ownership rule, post api #558
//
// api #558 (`e8dcd103`, merged 2026-11-06) made `coreOwned()` the single
// predicate behind "a plugin cannot replace a built-in driver":
//
//	func (i DriverInfo) coreOwned() bool { return i.Implemented && !i.MigratedToPlugin }
//
// `mws-vm` is one of five rows that are a name plus a roadmap item and nothing
// else, so it is no longer treated as a built-in: registration is allowed, and
// `LookupDriver` / `PluginForDriver` let the PLUGIN's row win over the in-tree
// one. Two consequences, and the second is the counter-intuitive one:
//
//   - When the driver lands, this function stops reading core's table and
//     HAND-BUILDS the spec, the way sc-plugin-echo does. The plugin's row is the
//     source of truth for a driver the plugin implements.
//   - Core's `mws-vm` row must stay `Implemented: false` **forever**. Flipping it
//     to true would make it core-owned again and lock this plugin out of its own
//     driver name — #558's own compatibility note says so. So the driver slice
//     needs NO api commit at all; it needs a pin bump, and the release it needs
//     already exists: api `2026.11.24` IS the #558 merge `e8dcd103`. (An earlier
//     revision of this comment had it backwards and told the next author to clear
//     `Implemented` in the api repo. Don't.)
//
// # What to fill in, and what not to guess
//
//   - `Unverified` must be non-empty until a machine has actually been created on
//     MWS. Validate refuses an unverified driver unless the resource opts in with
//     `allowUnverifiedProvider: true`, and quotes the reason verbatim.
//     "Implemented" and "known to work" are different claims.
//   - `Capabilities.CloudInit` is **true**, and `CloudInitNeedsEgress` **false**
//     — settled 2026-11-06, no longer a guess. The key is
//     `os.metadata.attributes["user-data"]`: the vendor's own SDK example
//     (`go-sdk@v0.29.0/examples/vm_ssh_test.go`) passes a `#cloud-config` there
//     and then SSHes into the machine. The payload is self-contained, so nothing
//     is fetched to apply it. Note the sharp edge that comes with it — MWS has
//     **no SSH-key field on the VM at all**, so key injection goes only through
//     the cloud-config, and an operator-supplied `user-data` must be merged with
//     ours rather than overwritten.
//   - `MultiAZInRegion` is **3** (`ru-central1`; `ru-siberia1` has one zone), and
//     `Regionless` is false — both measured. Zero still means "not measured" to
//     Validate, so don't leave them zero now that they are known.
//   - Everything else measured about this cloud — the 40 vm types, the single
//     disk type, where boot images actually live, and the default-DENY ingress
//     that makes an otherwise-successful VM unreachable — is in
//     ../../CLOUD-FACTS.md. Read it before writing VmHosts; most of it was
//     guessed wrong first by analogy to Yandex.
func VmDriverSpec() *scpluginv1.VmDriverSpec {
	return vm.Drivers[vm.DriverMws].ToProto(vm.DriverMws)
}
