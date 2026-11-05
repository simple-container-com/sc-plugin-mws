// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/simple-container-com/api/pkg/api"
)

// init() must register every type token it owns both as a config reader
// (so SC core can parse the YAML) and as a PluginRequirement OwnedType
// (so fool-protection auto-inject reaches it). Mismatches are the one
// silent gap the template exists to prevent — operators who upgrade and
// use only the un-listed token get no auto-inject and a late deploy
// error "unknown resource type".
func TestInitRegistersEveryOwnedType(t *testing.T) {
	RegisterTestingT(t)

	req, ok := api.LookupPluginRequirement(ProviderType)
	Expect(ok).To(BeTrue(), "ProviderType %q must appear in RegisterPluginRequirement's OwnedTypes", ProviderType)
	Expect(req.PluginName).To(Equal("echo"))
	Expect(req.DefaultSource.Type).To(Equal("github-release"))
	Expect(req.DefaultSource.Repository).To(Equal("simple-container-com/sc-plugin-echo"))

	// Every token the template exports must be lookup-able — a token
	// added to the const block above but missed from OwnedTypes would
	// fail here with a message naming the specific gap.
	for _, token := range []string{
		AuthTypeEchoKey,
		RegistrarTypeEchoDns,
		TemplateTypeEchoFunction,
		ResourceTypeEchoBucket,
		DriverEchoVm,
		StateStorageTypeEchoBucket,
		SecretsProviderTypeEchoKms,
	} {
		_, ok := api.LookupPluginRequirement(token)
		Expect(ok).To(BeTrue(), "token %q is not in OwnedTypes — fool-protection will silently skip server.yamls that only use this type", token)
	}
}

// Auth validation surfaces a clear, named error. Real plugins add tests
// for every per-cloud credential field they refuse.
func TestEchoAccountConfigValidate(t *testing.T) {
	RegisterTestingT(t)

	err := (&EchoAccountConfig{}).Validate()
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("`token` is required"))

	err = (&EchoAccountConfig{Token: "xyz"}).Validate()
	Expect(err).ToNot(HaveOccurred())
}
