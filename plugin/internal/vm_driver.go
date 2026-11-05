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

// VmDriverSpec is the capability row this plugin WILL advertise in Schema() once
// api PR #558 has shipped in a release — see the package doc on
// ../cmd/sc-plugin-mws/main.go for why advertising it before then takes down
// every deploy that loads the plugin.
//
// It reads SC core's own `Drivers` table rather than restating the capabilities
// here, which is the rule for every real plugin (sc-plugin-echo hand-builds a
// spec only because `echo-vm` has no in-tree row). Two things follow from that
// and are easy to miss:
//
//   - The row is the SOURCE OF TRUTH for what the driver can do, and it lives in
//     the api repo. Clearing `Implemented: false` and filling `Capabilities` is an
//     /api change that must ship in the SAME release wave as the driver — a
//     plugin change depending on an /api change is a two-release change (playbook
//     trap 8).
//   - `Capabilities.CloudInit` is currently the zero value because nobody has
//     measured it: MWS's provider documents no `user_data` input anywhere, and
//     `os.metadata.attributes` is a plausible slot by analogy with Yandex and
//     nothing more. Shared code in pkg/clouds/pulumi/vm branches on this field,
//     so a guess here fails twenty minutes into a deploy. Measure, then fill.
func VmDriverSpec() *scpluginv1.VmDriverSpec {
	return vm.Drivers[vm.DriverMws].ToProto(vm.DriverMws)
}
