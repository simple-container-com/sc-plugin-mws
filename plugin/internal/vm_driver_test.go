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

// VmDriverSpec reads SC core's table, so this test states what that table says
// TODAY and will fail the moment it changes. That is the point: the two things
// it asserts are both preconditions for Schema() advertising the driver.
//
//   - `Implemented: false` means SC core still carries `mws-vm` as a PLACEHOLDER
//     row. Advertising a placeholder is refused by vm.RegisterPluginDriver on any
//     core older than the release carrying api #558, and that refusal is
//     `return err` out of the whole plugin load — every deploy, not just VM ones.
//   - An empty `Capabilities.CloudInit` means nobody has measured whether MWS
//     accepts user-data. Shared VM code branches on it.
//
// So when this test fails because core now says `Implemented: true` with real
// capabilities, that is the signal to add VmDriverSpec() to Schema() and bump the
// api pin in both modules — not a signal to update the expectations here.
func TestVmDriverSpecIsStillAPlaceholder(t *testing.T) {
	RegisterTestingT(t)

	spec := VmDriverSpec()
	Expect(spec.GetDriver()).To(Equal(vm.DriverMws))
	Expect(spec.GetImplemented()).To(BeFalse(),
		"core's Drivers[mws-vm] row is implemented now — see this test's doc: advertise VmDriverSpec() in Schema() and pin the api release carrying #558")
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
