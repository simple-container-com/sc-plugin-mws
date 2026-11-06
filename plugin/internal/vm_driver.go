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
//     needs NO api commit at all; it needs an api *release* carrying #558 and a
//     pin bump. (An earlier revision of this comment had it backwards and told
//     the next author to clear `Implemented` in the api repo. Don't.)
//
// # What to fill in, and what not to guess
//
//   - `Unverified` must be non-empty until a machine has actually been created on
//     MWS. Validate refuses an unverified driver unless the resource opts in with
//     `allowUnverifiedProvider: true`, and quotes the reason verbatim.
//     "Implemented" and "known to work" are different claims.
//   - `Capabilities.CloudInit` stays at the zero value until measured: MWS's
//     provider documents no `user_data` input anywhere, and
//     `os.metadata.attributes` is a plausible slot by analogy with Yandex and
//     nothing more. Shared code in pkg/clouds/pulumi/vm branches on this field,
//     so a guess here fails twenty minutes into a deploy.
//   - `MultiAZInRegion` likewise — zero means "not measured", and Validate
//     declines to check against it rather than inventing a limit.
func VmDriverSpec() *scpluginv1.VmDriverSpec {
	return vm.Drivers[vm.DriverMws].ToProto(vm.DriverMws)
}
