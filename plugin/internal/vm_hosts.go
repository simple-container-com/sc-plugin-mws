// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"fmt"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/simple-container-com/api/pkg/clouds/vm"
	scPlugin "github.com/simple-container-com/api/pkg/plugin"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
)

// VmHosts is the echo stub for the VmHosts RPC. Returns one byo-shaped
// host at a TEST-NET address — enough to exercise the response shape
// without a real cloud.
//
// A real VM-driving plugin:
//
//  1. Reads req.ConfigJson into pkg/clouds/vm.VmConfig.
//  2. Converts credentials via api.ConvertAuth to the cloud's auth config.
//  3. Registers cloud resources (instance, floating IP, deploy key).
//  4. Parses outputs through plugin.OutputRefTo / AddressOutput / PrivateKeyOf.
//  5. Builds the VmHostBuilder chain and calls BuildVmHosts.
//
// See api/pkg/plugin/vmhosts.go for the builder surface and
// pkg/clouds/pulumi/vm/plugin_authoring_test.go for the end-to-end
// pattern. Cloud.ru Evolution's VM driver in sc-plugin-cloudru is the
// first live example (stdout-parsed address via AddressOutput, dockered
// over SSH, trust-on-first-use).
func VmHosts(pctx *sdk.Context, resourceName string) (*scpluginv1.VmHostsResponse, error) {
	hostName := fmt.Sprintf("%s-host-0", resourceName)
	host := scPlugin.NewVmHost(pctx.Context(), hostName).
		User("sc").
		Port(vm.DefaultSshPort).
		Role(string(vm.RoleCompose)).
		// TEST-NET-1 reserved address (RFC 5737) — guaranteed non-routable,
		// safe to echo in a stub without ever hitting a real machine.
		Address("192.0.2.1").
		// Real plugins mint a deploy key via tls.NewPrivateKey and reference
		// it with PrivateKeyOf(key, "privateKeyOpenssh"). Echo passes a
		// literal placeholder — any downstream SSH would fail, but the
		// template's job is to show the shape, not provision.
		PrivateKey("-----BEGIN OPENSSH PRIVATE KEY-----\nreplace-with-a-real-tls.PrivateKey.privateKeyOpenssh-ref\n-----END OPENSSH PRIVATE KEY-----").
		// Echo cannot pin a host key — no cloud, no first boot, no identity
		// to seed. Real drivers whose cloud accepts user-data use HostKeyOf
		// against a tls.PrivateKey; drivers whose cloud refuses user-data
		// (Cloud.ru Evolution) use TrustOnFirstUse like this one.
		TrustOnFirstUse()
	return scPlugin.BuildVmHosts(host)
}
