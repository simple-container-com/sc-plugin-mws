// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// Package mws holds the provisioning tier of sc-plugin-mws: everything that
// needs a Pulumi context or an MWS SDK. It is a SEPARATE Go module from ../../api
// on purpose — SC core imports the config tier, and this module's dependency on
// the pulumi-mws bridge must not ride along into every SC consumer.
package mws

import (
	"github.com/simple-container-com/api/pkg/clouds/vm"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
)

// mwsUnverified is the reason `mws-vm` is advertised as implemented-but-unverified.
//
// It is quoted VERBATIM in the refusal an operator gets without
// `allowUnverifiedProvider: true`, so it has to be a sentence that tells them
// what to expect rather than a label. Clear it in the same change that records a
// machine booting through SC on MWS — not before, and not in a change that only
// reports a green build.
// It is deliberately precise about HOW FAR the driver has got, because "never
// tried" and "tried and walled on the cloud's side" are different things to an
// operator deciding whether to opt in.
const mwsUnverified = "no machine has ever been provisioned on MWS by this driver. " +
	"A real apply on 2026-11-07 created the network and then hit a hard MWS quota — " +
	"externalAddressCount is 0 in the test project — two resources short of the machine, " +
	"so the flavour grid, the single disk type, where boot images live and " +
	"cloud-init through os.metadata.attributes[\"user-data\"] are all still " +
	"read-only API probing plus vendor example code. See CLOUD-FACTS.md. " +
	"Also unfixed: an MWS stack cannot be destroyed from a machine that did not deploy it. " +
	"The first apply that reaches a booted VM is the measurement"

// VmDriverSpec is the capability row this plugin advertises in Schema().
//
// # Why this is HAND-BUILT rather than read from core's table
//
// It used to be `vm.Drivers[vm.DriverMws].ToProto(vm.DriverMws)`. That was right
// while the driver did not exist and is wrong now, and the reason is the
// ownership rule api #558 (`e8dcd103`, merged 2026-11-06) settled:
//
//	func (i DriverInfo) coreOwned() bool { return i.Implemented && !i.MigratedToPlugin }
//
// Core's `mws-vm` row is a name plus a roadmap item and nothing else, so it is
// not core-owned: registration is allowed and `LookupDriver` /
// `PluginForDriver` let THIS row win. Reading core's row instead would advertise
// `Implemented: false`, which makes `VmConfig.Validate()` refuse `provider:
// mws-vm` with the same "not implemented yet" an operator gets with no plugin
// installed at all — advertising would buy nothing.
//
// The counter-intuitive half: **core's row must stay `Implemented: false`
// forever.** Flipping it to true would make it core-owned again and lock this
// plugin out of its own driver name. So this slice takes no api commit — only a
// pin at or past `e8dcd103`, which is what api release `2026.11.24` is.
//
// # Where each capability came from
//
// All measured 2026-11-06 against the live API with a real service-account key,
// or read off vendor-authored code. Full provenance in ../../CLOUD-FACTS.md; the
// short version per field is inline below, because a wrong capability is branched
// on by shared code and surfaces twenty minutes into a deploy.
//
// Note on what is NOT enforced today: of the five boolean capabilities below,
// only `SecurityGroups` has a behavioural consumer anywhere in core
// (`vm.validateCloud` refuses `ingressPorts` when it is false). `PrivateNetwork`,
// `FloatingIP` and `CloudInitNeedsEgress` currently only round-trip through
// `plugin_spec.go` — they are documentation that core may start branching on, so
// they are stated accurately rather than left zero.
func VmDriverSpec() *scpluginv1.VmDriverSpec {
	info := vm.DriverInfo{
		Implemented: true,
		Unverified:  mwsUnverified,
		RoadmapItem: vm.Drivers[vm.DriverMws].RoadmapItem,
		Capabilities: vm.Capabilities{
			// Confirmed by vendor-authored code that RUNS:
			// `go-sdk@v0.29.0/examples/vm_ssh_test.go` puts a `#cloud-config`
			// in `Os.Metadata.Attributes["user-data"]` and then SSHes in. The
			// acctest fixture sets the same key.
			CloudInit: true,

			// False — and this is about ORDERING, not about whether the payload
			// fetches anything (it does: SC's cloud-init installs docker from
			// get.docker.com, which is why the network is created with
			// `internet_access = true`). The capability means "cloud-init needs
			// a public address AT CREATION TIME, which forces the floating IP to
			// be created before the machine" (Timeweb). MWS binds the external
			// address inline in the VM's own
			// `one_to_one_nat.external.address.ref`, so the ordering is forced
			// by the reference itself and no special-casing is needed.
			CloudInitNeedsEgress: false,

			// Two regions exist (`ru-central1`, `ru-siberia1`), so a region is
			// a real dimension and `region:` is required on a vm resource.
			Regionless: false,

			// 3 — `ru-central1` has a/b/c. `ru-siberia1` has ONE zone, so code
			// that reads this as "3 everywhere" is wrong.
			MultiAZInRegion: 3,

			// `mws_vpc_network` + `mws_vpc_subnet` + `mws_vpc_address`.
			PrivateNetwork: true,

			// `mws_vpc_external_address` is a separate, reservable resource
			// bound to the NIC through `one_to_one_nat`.
			FloatingIP: true,

			// True, with a caveat worth knowing before reading it as AWS-shaped:
			// MWS's `mws_vpc_firewall_rule` is a RULE SCOPED TO A NETWORK with a
			// CIDR destination, closer to GCP/YC firewall rules than to an
			// attachable security group. What the capability gates in core is
			// whether `ingressPorts` may be declared at all, and it may.
			SecurityGroups: true,

			// Upstream models no object-storage bucket and no container
			// registry, so image delivery is ssh-tar or an external registry —
			// see ../../api/init.go for the measurement.
			ContainerRegistry: false,

			// No DNS zone/recordset resource either; a stack on MWS gets its
			// public name from Cloudflare or another registrar plugin. Not to be
			// confused with the per-VM platform-internal name a VM can request.
			DNSInIaC: false,

			// BlockStorageCSI left at zero: `mws_mk8s_cluster` exists and a CSI
			// driver is likely, but nothing in this slice touches Kubernetes and
			// an unmeasured true is worse than an honest unknown.
		},
	}
	return info.ToProto(vm.DriverMws)
}
