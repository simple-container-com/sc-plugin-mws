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

// VmDriverSpec still mirrors SC core's placeholder row, and this test pins that
// — but read the failure mode carefully, because it is the opposite of what the
// shape suggests.
//
// Core's `mws-vm` row MUST stay `Implemented: false` permanently. Post api #558
// (`e8dcd103`) the predicate deciding "a plugin cannot replace a built-in" is
// `Implemented && !MigratedToPlugin`, so flipping that row to true would make it
// core-owned again and lock this plugin out of its own driver name. A failure
// here is therefore a REGRESSION IN THE API REPO to go fix, not a green light.
//
// Advertising the driver is a change in THIS repo only: write VmHosts, hand-build
// the spec in VmDriverSpec() with `Implemented: true` + a non-empty `Unverified`
// + measured capabilities, put it in Schema(), and bump the api pin to a release
// carrying #558. At that point this test is replaced by assertions on the
// hand-built row — it is scaffolding for the pre-driver state, not an invariant.
//
// `Capabilities.CloudInit` is asserted separately because it outlives this test:
// a `true` there must come from a live machine, never from the Yandex analogy.
func TestVmDriverSpecIsStillAPlaceholder(t *testing.T) {
	RegisterTestingT(t)

	spec := VmDriverSpec()
	Expect(spec.GetDriver()).To(Equal(vm.DriverMws))
	Expect(spec.GetImplemented()).To(BeFalse(),
		"core's Drivers[mws-vm] row says Implemented: true — that row must stay a placeholder or coreOwned() locks this plugin out of mws-vm; see this test's doc and api #558")
	Expect(spec.GetRoadmapItem()).To(Equal("sc-ru-clouds-beyond-yandex"))
	Expect(spec.GetCapabilities().GetCloudInit()).To(BeFalse(),
		"cloud-init on MWS is unmeasured; a true here must come with a live smoke")
}

// The refusal the VmHosts RPC hands back must name the driver and point
// somewhere. An operator who reaches it has an `mws-vm` resource in a
// server.yaml and a plugin that cannot build it; a bare "not implemented" leaves
// them guessing whether their config or their plugin version is wrong.
func TestVmDriverNotImplementedErrorIsActionable(t *testing.T) {
	RegisterTestingT(t)

	Expect(ErrVmDriverNotImplemented).To(HaveOccurred())
	Expect(ErrVmDriverNotImplemented.Error()).To(ContainSubstring(vm.DriverMws))
	Expect(ErrVmDriverNotImplemented.Error()).To(ContainSubstring("sc-ru-clouds-beyond-yandex"))
}
