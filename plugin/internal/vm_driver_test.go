// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"testing"

	. "github.com/onsi/gomega"
	"github.com/simple-container-com/api/pkg/clouds/vm"

	mwsApi "github.com/simple-container-com/sc-plugin-mws/api"
)

// The two tiers spell the driver name independently: the config tier has a
// literal (it must not import pkg/clouds/vm, which SC core would then pull
// through this plugin's config tier), and the provisioning tier uses the const.
// They are compared nowhere else, and a disagreement is silent: fool-protection
// would auto-inject the plugin for one spelling while the driver table answered
// to the other, so an operator's `provider:` would resolve to "not implemented"
// with the plugin sitting right there, installed.
func TestDriverTokenAgreesWithCore(t *testing.T) {
	RegisterTestingT(t)

	Expect(mwsApi.DriverMwsVm).To(Equal(vm.DriverMws),
		"api/init.go's DriverMwsVm literal must equal pkg/clouds/vm.DriverMws")
}

// CORE's `mws-vm` row must stay a placeholder forever, and this is the test that
// says so — read the failure mode carefully, because it is the opposite of what
// the shape suggests.
//
// Post api #558 (`e8dcd103`) the predicate deciding "a plugin cannot replace a
// built-in" is `Implemented && !MigratedToPlugin`. So a well-meaning change in
// the api repo that fills in core's row — which looks like progress — would make
// `mws-vm` core-owned again and lock THIS plugin out of its own driver name, with
// the symptom being a hard plugin-load failure for every operator. A failure here
// is a REGRESSION IN THE API REPO to go revert, not a green light to delete the
// assertion.
//
// Note `MigratedToPlugin` is checked too: it is the other way the predicate can
// flip, and `Implemented: true` + `MigratedToPlugin: true` would be a legitimate
// core state that this plugin should tolerate. Asserting only on `Implemented`
// would make a correct api change look like a break.
func TestCoreRowMustStayAPlaceholder(t *testing.T) {
	RegisterTestingT(t)

	coreRow, ok := vm.Drivers[vm.DriverMws]
	Expect(ok).To(BeTrue(), "core dropped its mws-vm row entirely; the driver name is now unregistrable")
	Expect(coreRow.Implemented && !coreRow.MigratedToPlugin).To(BeFalse(),
		"core's Drivers[mws-vm] row is now core-owned, which locks this plugin out of mws-vm — see this test's doc and api #558")
	Expect(coreRow.RoadmapItem).NotTo(BeEmpty(),
		"VmDriverSpec reads the roadmap item off core's row; an empty one silently drops it from the spec")
}

// The row this plugin HAND-BUILDS and advertises. Every assertion is a fact a
// deploy branches on, and the two that matter most are the pair at the top:
// `Implemented: true` is what makes `VmConfig.Validate()` accept `provider:
// mws-vm` at all, and the non-empty `Unverified` is what still makes it refuse
// without `allowUnverifiedProvider: true`.
//
// The Unverified assertion is NOT a formality. No machine has ever booted on MWS
// through this driver; every capability below is read-only API probing plus
// vendor example code. Whoever clears that string must be clearing it in the same
// change that records a live boot — if this test is failing because someone
// emptied it to quiet a deploy refusal, the fix is to keep the string.
func TestVmDriverSpecAdvertisesTheMeasuredRow(t *testing.T) {
	RegisterTestingT(t)

	spec := VmDriverSpec()
	Expect(spec.GetDriver()).To(Equal(vm.DriverMws))

	Expect(spec.GetImplemented()).To(BeTrue(),
		"mws-vm is implemented; a false here makes VmConfig.Validate() refuse the provider exactly as if no plugin were installed")
	Expect(spec.GetUnverified()).NotTo(BeEmpty(),
		"no machine has ever been provisioned on MWS by this driver — the Unverified reason is cleared only by a change that records a live boot")
	Expect(spec.GetUnverified()).To(ContainSubstring("CLOUD-FACTS.md"),
		"the refusal is quoted verbatim to the operator, so it must point them somewhere")

	// Read off core's row rather than duplicated, so the two cannot drift.
	Expect(spec.GetRoadmapItem()).To(Equal(vm.Drivers[vm.DriverMws].RoadmapItem))

	caps := spec.GetCapabilities()

	// Vendor-authored code that RUNS: go-sdk@v0.29.0/examples/vm_ssh_test.go puts
	// a #cloud-config in Os.Metadata.Attributes["user-data"] and then SSHes in.
	Expect(caps.GetCloudInit()).To(BeTrue())

	// False because MWS binds the external address INLINE in the VM's own
	// one_to_one_nat.external.address.ref, so nothing has to force the floating IP
	// to exist first. Not a claim that cloud-init needs no egress — it does, which
	// is why the network is created with internet_access = true.
	Expect(caps.GetCloudInitNeedsEgress()).To(BeFalse())

	// Two regions exist (ru-central1, ru-siberia1), so `region:` is required on a
	// vm resource. A true here would let a region-less config through to a
	// provider that needs one.
	Expect(caps.GetRegionless()).To(BeFalse())

	// ru-central1 has a/b/c. ru-siberia1 has ONE zone, so this number is not
	// "3 everywhere" — see mapping.go's zoneRegions.
	Expect(caps.GetMultiAzInRegion()).To(BeEquivalentTo(3))

	Expect(caps.GetPrivateNetwork()).To(BeTrue())
	Expect(caps.GetFloatingIp()).To(BeTrue())

	// The one capability with a behavioural consumer in core today:
	// vm.validateCloud refuses `ingressPorts` when it is false, and this driver's
	// firewall rules are load-bearing because MWS denies ingress by default.
	Expect(caps.GetSecurityGroups()).To(BeTrue())

	// Upstream models no bucket and no container registry, so image delivery is
	// ssh-tar or an external registry, and a stack on MWS gets its public name
	// from Cloudflare or another registrar plugin.
	Expect(caps.GetContainerRegistry()).To(BeFalse())
	Expect(caps.GetDnsInIac()).To(BeFalse())
}
