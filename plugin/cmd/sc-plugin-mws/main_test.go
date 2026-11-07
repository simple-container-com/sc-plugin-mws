// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package main

import (
	"context"
	"os"
	"regexp"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/simple-container-com/api/pkg/api"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"

	mwsApi "github.com/simple-container-com/sc-plugin-mws/api"
)

// schema calls the RPC the way core does. Schema takes no state off the plugin
// struct, so a zero value is the honest receiver — if that stops being true this
// test should fail to compile rather than be quietly given a fake.
func schema(t *testing.T) *scpluginv1.SchemaResponse {
	t.Helper()
	resp, err := (&mwsPlugin{}).Schema(context.Background(), &scpluginv1.SchemaRequest{})
	Expect(err).ToNot(HaveOccurred())
	Expect(resp).ToNot(BeNil())
	return resp
}

// Token-by-token, because Schema() is the ONLY way core learns what this plugin
// owns. `api/init_test.go` covers the OwnedTypes half of the contract; nothing
// covered this half until now — main.go's own doc comment said so ("api/init_test.go
// covers the OwnedTypes half"), which is a comment admitting a gap rather than
// closing it.
//
// The two halves fail differently and that is why both need pinning: a token
// missing from OwnedTypes is a SILENT fool-protection gap, a token missing HERE
// is an unroutable type at deploy time.
func TestSchemaAdvertisesEveryOwnedToken(t *testing.T) {
	RegisterTestingT(t)

	resp := schema(t)

	Expect(resp.GetPluginVersion()).ToNot(BeEmpty(),
		"PluginVersion is the only place an operator can see WHICH build their deploy fetched; empty makes a stale cached binary undiagnosable")
	Expect(resp.GetProviderTypes()).To(ConsistOf(mwsApi.ProviderType))
	Expect(resp.GetAuthTypes()).To(ConsistOf(mwsApi.AuthTypeMwsServiceAccount))

	drivers := make([]string, 0, len(resp.GetVmDrivers()))
	for _, d := range resp.GetVmDrivers() {
		drivers = append(drivers, d.GetDriver())
	}
	Expect(drivers).To(ConsistOf(mwsApi.DriverMwsVm))

	// Deliberately empty today, one measured reason each in main.go. Asserting
	// emptiness is not an assertion that they stay empty — it is a tripwire: the
	// change that adds the plugin's first resource type must come here and decide
	// what the token is, rather than discovering at deploy time that core never
	// learned about it.
	Expect(resp.GetResourceTypes()).To(BeEmpty())
	Expect(resp.GetRegistrarTypes()).To(BeEmpty())
	Expect(resp.GetSecretsProviderTypes()).To(BeEmpty())
	Expect(resp.GetStateStoreTypes()).To(BeEmpty())
	Expect(resp.GetComputeProcessorTypes()).To(BeEmpty())
	Expect(resp.GetSafeToReplaceTypes()).To(BeEmpty())

	// There is NO template-types field on SchemaResponse (checked against api
	// 99411193: the message has ProviderTypes, ResourceTypes,
	// ComputeProcessorTypes, RegistrarTypes, StateStoreTypes,
	// SecretsProviderTypes, SafeToReplaceTypes, AuthTypes, PulumiPlugins,
	// VmDrivers — and nothing for templates). So a plugin cannot advertise a
	// `template:` type over the bus at all; sc-plugin-yandex owns two
	// (`yc-serverless-container`, `yc-static-website`) and reaches core only by
	// being link-time imported. This is the same shape of gap `auth_types`
	// closed, and it is the reason nothing here asserts on templates: if MWS
	// ever grows one, the missing wire path is the blocker, not this test.
}

// `AuthTypes` must hold the auth discriminator and NOTHING else.
//
// Core loops the advertised lists in `stackread.registerSchemaTypes` and
// installs a decoder per token per KIND. A token that appears under the wrong
// list gets the wrong kind registered for it, and the last writer wins — so
// listing `mws` or `mws-vm` here would overwrite their real registrations with
// an auth decoder and break the provider or the driver, silently, at parse time.
// The symptom would be a type that resolves to the wrong config struct rather
// than an error naming the mistake.
func TestSchemaAuthTypesIsExactlyTheAuthDiscriminator(t *testing.T) {
	RegisterTestingT(t)

	authTypes := schema(t).GetAuthTypes()

	Expect(authTypes).ToNot(ContainElement(mwsApi.ProviderType),
		"the provider type belongs in ProviderTypes; listing it here registers an auth decoder over it")
	Expect(authTypes).ToNot(ContainElement(mwsApi.DriverMwsVm),
		"the VM driver name belongs in VmDrivers; listing it here registers an auth decoder over it")
}

// Every token Schema() advertises must also be a PluginRequirement OwnedType.
//
// This is the cross-check neither module can do alone: `api/init_test.go` can
// only walk a hand-maintained list in its own package, and nothing in `api` can
// see Schema() (the dependency runs plugin → api, never back). So a token added
// to Schema() and forgotten in OwnedTypes is exactly the silent gap `api/init.go`
// warns about, and this is the only test positioned to catch it.
func TestEverySchemaTokenIsAnOwnedType(t *testing.T) {
	RegisterTestingT(t)

	resp := schema(t)

	advertised := make([]string, 0, 8)
	advertised = append(advertised, resp.GetProviderTypes()...)
	advertised = append(advertised, resp.GetAuthTypes()...)
	advertised = append(advertised, resp.GetResourceTypes()...)
	advertised = append(advertised, resp.GetRegistrarTypes()...)
	advertised = append(advertised, resp.GetSecretsProviderTypes()...)
	advertised = append(advertised, resp.GetStateStoreTypes()...)
	for _, d := range resp.GetVmDrivers() {
		advertised = append(advertised, d.GetDriver())
	}

	Expect(advertised).ToNot(BeEmpty(), "a plugin that advertises nothing cannot be the thing under test")

	for _, token := range advertised {
		req, ok := api.LookupPluginRequirement(token)
		Expect(ok).To(BeTrue(),
			"Schema() advertises %q but api/init.go's OwnedTypes does not list it — fool-protection will silently skip a server.yaml that uses only this token", token)
		Expect(req.PluginName).To(Equal(pluginName),
			"token %q is owned by plugin %q, not %q", token, req.PluginName, pluginName)
	}
}

// An advertised Pulumi-plugin version must equal the version the SDK in go.mod
// actually is, because the two are used at different moments: core PREFETCHES
// the version advertised here, while every resource the driver registers asks
// for the version baked into the SDK it was compiled against. A disagreement
// has core download one plugin and the engine demand another.
//
// `tls` is the half this can check mechanically — it is an ordinary module
// require. It is also the half with a live precedent for getting it wrong:
// sc-plugin-cloudru advertises 5.3.0 while its own go.mod is on 5.5.0, which is
// why main.go carries a "do not copy that number from there" warning.
//
// `mws` is deliberately NOT checked against go.mod: its version is baked into
// the generated SDK's PkgResourceDefaultOpts (`0.0.1-dev`, the generator's
// placeholder) and has no relationship to the module version at all. That
// mismatch is real and is recorded in main.go; a test asserting the two agree
// would be asserting something false.
func TestAdvertisedTlsVersionMatchesGoMod(t *testing.T) {
	RegisterTestingT(t)

	gomod, err := os.ReadFile("../../go.mod")
	Expect(err).ToNot(HaveOccurred(), "this test reads the plugin module's own go.mod; it must run from its package dir")

	m := regexp.MustCompile(`github\.com/pulumi/pulumi-tls/sdk/v5 v(\S+)`).FindSubmatch(gomod)
	Expect(m).ToNot(BeNil(), "pulumi-tls is no longer a require in plugin/go.mod — if the driver stopped minting key pairs, drop the advertisement too")
	wantTLS := string(m[1])

	byPackage := map[string]string{}
	for _, p := range schema(t).GetPulumiPlugins() {
		Expect(p.GetPackage()).ToNot(BeEmpty())
		byPackage[p.GetPackage()] = p.GetVersion()
	}

	Expect(byPackage).To(HaveKeyWithValue("tls", wantTLS),
		"Schema() advertises tls %q but plugin/go.mod is on %q — core prefetches the advertised one, the resources ask for the compiled one",
		byPackage["tls"], wantTLS)

	// The bridge is a third-party provider, so its download URL must be set:
	// core leaves it blank only for providers it can pull from the Pulumi
	// registry, which is geoblocked from RU networks anyway.
	var mws *scpluginv1.PulumiPluginRequirement
	for _, p := range schema(t).GetPulumiPlugins() {
		if p.GetPackage() == "mws" {
			mws = p
		}
	}
	Expect(mws).ToNot(BeNil(), "the mws bridge must be advertised; without it core never materialises pulumi-resource-mws")
	Expect(mws.GetPluginDownloadUrl()).ToNot(BeEmpty(),
		"pulumi-mws is not in the Pulumi registry — a blank download URL sends core to a registry that does not have it and is geoblocked from RU")
}
