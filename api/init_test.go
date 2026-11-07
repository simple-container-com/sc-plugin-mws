// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/simple-container-com/api/pkg/api"
)

// init() must register every type token this plugin owns as a PluginRequirement
// OwnedType, so SC core's fool-protection auto-injects the `plugins.mws:` block
// when it meets the token in a server.yaml. A gap here is the one SILENT failure
// in the whole plugin surface: an operator who uses only the un-listed token
// gets no auto-inject, no parse error, and a deploy that dies much later with
// "unknown resource type".
func TestInitRegistersEveryOwnedType(t *testing.T) {
	RegisterTestingT(t)

	req, ok := api.LookupPluginRequirement(ProviderType)
	Expect(ok).To(BeTrue(), "ProviderType %q must appear in RegisterPluginRequirement's OwnedTypes", ProviderType)
	Expect(req.PluginName).To(Equal("mws"),
		"PluginName must match the `pluginName` const in plugin/cmd/sc-plugin-mws/main.go — core keys the release, the auto-injected block and the driver table by it")
	Expect(req.DefaultSource.Type).To(Equal("github-release"))
	Expect(req.DefaultSource.Repository).To(Equal("simple-container-com/sc-plugin-mws"))

	for _, token := range []string{
		ProviderType,
		AuthTypeMwsServiceAccount,
		// DriverMwsVm is reached through api.VmConfigAware, not res.Type — a
		// VM-ONLY stack (every first MWS stack) depends on this entry.
		DriverMwsVm,
	} {
		_, ok := api.LookupPluginRequirement(token)
		Expect(ok).To(BeTrue(),
			"token %q is not in OwnedTypes — fool-protection will silently skip server.yamls that only use this type", token)
	}
}

// The auth type must be parseable by the reader init() registered against it.
// This is the config tier's whole job; a reader that does not round-trip means
// `${auth:…}` resolves to a zero-value struct and the deploy authenticates as
// nobody.
func TestReadServiceAccountConfigRoundTrips(t *testing.T) {
	RegisterTestingT(t)

	cfg, err := ReadServiceAccountConfig(&api.Config{Config: &AccountConfig{
		Token:   "t-abc",
		Project: "p-1",
		Zone:    "ru-central1-b",
	}})
	Expect(err).ToNot(HaveOccurred())

	acc, ok := cfg.Config.(*AccountConfig)
	Expect(ok).To(BeTrue(), "reader must yield *AccountConfig")
	Expect(acc.ProviderType()).To(Equal(ProviderType))
	Expect(acc.ProjectIdValue()).To(Equal("p-1"))
	Expect(acc.EffectiveZone()).To(Equal("ru-central1-b"))
	Expect(acc.EffectiveEndpoint()).To(Equal(DefaultEndpoint), "an unset endpoint must resolve to the default, not to \"\"")
	Expect(acc.UsesAuthorizedKey()).To(BeFalse())
}

func TestEffectiveZoneDefaults(t *testing.T) {
	RegisterTestingT(t)

	Expect((&AccountConfig{}).EffectiveZone()).To(Equal(DefaultZone))
	Expect((&AccountConfig{Zone: "ru-central1-c"}).EffectiveZone()).To(Equal("ru-central1-c"))
}

// validKey builds a minimal authorized-key document in MWS's own shape. The
// field names are camelCase and `keyId` is a structured path — NOT the Yandex
// shape (`id` / `service_account_id` / `private_key`), which is the natural wrong
// guess given how closely the rest of MWS's auth model follows Yandex's. See
// go.mws.cloud/go-sdk/mws/iam/service_account.go's UnmarshalJSON.
func validKey(project string) string {
	b, err := json.Marshal(map[string]string{
		"keyId":      "projects/" + project + "/serviceAccounts/sa-deployer/authorizedKeys/key-1",
		"privateKey": "MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg",
		"publicKey":  "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE",
		"algorithm":  "ES256",
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Each case here is a deploy that would otherwise fail at the FAR end, inside
// the Pulumi provider, as `load service account authorized key file: …` against
// a path SC generated for its own temp copy of the secret — so the operator is
// told their temp file is bad rather than their credential, and the message
// names a file that is already unlinked by the time they look.
func TestAccountConfigValidate(t *testing.T) {
	RegisterTestingT(t)

	for _, tc := range []struct {
		name   string
		cfg    AccountConfig
		expect string // substring; "" means the config must validate
	}{
		{
			name:   "neither credential",
			cfg:    AccountConfig{Project: "p-1"},
			expect: "one of `serviceAccountAuthorizedKey` or `token` is required",
		},
		{
			// Upstream's switch takes the key first and never mentions the token it
			// ignored (internal/provider/public/sdk.go:35), so an operator who set
			// both would not learn which one authenticated them.
			name:   "both credentials",
			cfg:    AccountConfig{Project: "p-1", Token: "t", ServiceAccountAuthorizedKey: validKey("p-1")},
			expect: "mutually exclusive",
		},
		{
			name:   "no project",
			cfg:    AccountConfig{Token: "t"},
			expect: "`project` is required",
		},
		{
			// The commonest real failure: a `${secret:…}` that resolved to a
			// placeholder because the registry entry was missing. It looks exactly
			// like any other non-JSON one-liner.
			name:   "key is not JSON",
			cfg:    AccountConfig{Project: "p-1", ServiceAccountAuthorizedKey: "${secret:mws-sa-key}"},
			expect: "must be the authorized-key JSON document",
		},
		{
			name:   "key is JSON but not an authorized key",
			cfg:    AccountConfig{Project: "p-1", ServiceAccountAuthorizedKey: `{"accessKeyId":"x","secretAccessKey":"y"}`},
			expect: "has no `privateKey` field",
		},
		{
			name:   "wrong algorithm",
			cfg:    AccountConfig{Project: "p-1", ServiceAccountAuthorizedKey: `{"keyId":"projects/p-1/serviceAccounts/sa/authorizedKeys/k","privateKey":"x","algorithm":"RS256"}`},
			expect: "MWS's SDK accepts only",
		},
		{
			// The SDK parses the project and service-account name OUT of keyId, so a
			// malformed id authenticates as nobody.
			name:   "malformed keyId",
			cfg:    AccountConfig{Project: "p-1", ServiceAccountAuthorizedKey: `{"keyId":"key-1","privateKey":"x","algorithm":"ES256"}`},
			expect: "which is not of the form",
		},
		{
			// Authenticates fine, then 403s against the wrong project — or, worse,
			// succeeds against it.
			name:   "project disagrees with the key",
			cfg:    AccountConfig{Project: "p-2", ServiceAccountAuthorizedKey: validKey("p-1")},
			expect: `belongs to project "p-1"`,
		},
		{
			name: "valid key",
			cfg:  AccountConfig{Project: "p-1", ServiceAccountAuthorizedKey: validKey("p-1")},
		},
		{
			name: "valid token",
			cfg:  AccountConfig{Project: "p-1", Token: "t-abc"},
		},
	} {
		err := tc.cfg.Validate()
		if tc.expect == "" {
			Expect(err).ToNot(HaveOccurred(), "case %q must validate", tc.name)
			continue
		}
		Expect(err).To(HaveOccurred(), "case %q must be refused", tc.name)
		Expect(err.Error()).To(ContainSubstring(tc.expect), "case %q", tc.name)
	}
}

func TestProjectFromAuthorizedKey(t *testing.T) {
	RegisterTestingT(t)

	Expect((&AccountConfig{ServiceAccountAuthorizedKey: validKey("p-9")}).ProjectFromAuthorizedKey()).To(Equal("p-9"))
	// No key, or a key that is not a key: "" rather than a panic — this is a
	// logging helper, and Validate has already refused anything malformed.
	Expect((&AccountConfig{}).ProjectFromAuthorizedKey()).To(BeEmpty())
	Expect((&AccountConfig{ServiceAccountAuthorizedKey: "not json"}).ProjectFromAuthorizedKey()).To(BeEmpty())
}

// An operator may hand the whole credential over as one blob via `${secret:…}`
// instead of filling fields; when they do, that blob must cross to the plugin
// verbatim rather than being re-serialised from a struct that never saw it.
func TestCredentialsValuePrefersTheBlob(t *testing.T) {
	RegisterTestingT(t)

	blob := &AccountConfig{Project: "p-1", Token: "t"}
	blob.Credentials.Credentials = `{"raw":"blob"}`
	Expect(blob.CredentialsValue()).To(Equal(`{"raw":"blob"}`))

	plain := &AccountConfig{Project: "p-1", Token: "t-abc"}
	Expect(plain.CredentialsValue()).To(ContainSubstring("t-abc"))
}

// TRAP 20. `ServiceAccountAuthorizedKey` is a `string` whose CONTENT is a JSON
// document, which makes it the first field in the fleet able to hit the
// container-shape case of core's coercer. On the opaque decode path core
// serialises the whole raw auth map to JSON via
// `PluginFieldConfig.CredentialsValue()` (api `pkg/api/read.go:537`), running
// each value through `coerceToStringIfScalarNumber` — which coerces scalars but
// passes nested maps through UNCHANGED. So an operator who pastes the key file
// as a YAML mapping (the intuitive thing, since the value really is a JSON
// object) sends an object where this field is a string.
//
// Both forms are asserted here rather than just the good one: the failing form
// must fail, and it must fail naming the field, because the message is the only
// thing the operator sees. The string form is what `${secret:…}` and a `|` block
// scalar both produce.
func TestAuthorizedKeyMustArriveAsAString(t *testing.T) {
	RegisterTestingT(t)

	const key = `{"id":"k1","privateKey":"-----BEGIN PRIVATE KEY-----"}`

	// The two shapes an operator can legitimately write — `${secret:…}` and a
	// `|` block scalar — both reach us as a JSON string. These must decode.
	stringForm, err := json.Marshal(map[string]any{
		"project":                     "p-1",
		"serviceAccountAuthorizedKey": key,
	})
	Expect(err).NotTo(HaveOccurred())

	var ok AccountConfig
	Expect(json.Unmarshal(stringForm, &ok)).To(Succeed())
	Expect(ok.ServiceAccountAuthorizedKey).To(Equal(key))

	// A nested YAML mapping arrives as a JSON object. This must be refused, and
	// the error must name the field so the operator can find it.
	//
	// These bytes are MEASURED, not hand-built: they are byte-for-byte what
	// core's `PluginFieldConfig.CredentialsValue()` emitted for a secrets.yaml
	// whose `serviceAccountAuthorizedKey` was written as a nested mapping,
	// captured 2026-11-06 against api aa504f87 with this plugin loaded over the
	// real gRPC wire. Do not regenerate this with `json.Marshal(map[string]any)`
	// — that would assert only that encoding/json rejects an object where a
	// string is wanted (never in doubt) while assuming our model of core's
	// `coerceToStringIfScalarNumber` is right, which is the actual thing under
	// test. The coercer walks nested maps and passes them through UNCHANGED, so
	// nothing upstream of this Unmarshal refuses the mapping form: core's parse
	// of secrets.yaml succeeds, the value crosses the wire intact, and this
	// decode is the first and only guard. That makes the failure DEPLOY-time and
	// in-process-here, not parse-time in core — see the playbook's trap 20.
	const measuredMappingForm = `{"project":"smoke-project","serviceAccountAuthorizedKey":{"id":"not-a-real-key-id","key_algorithm":"RSA_2048","private_key":"not-a-real-key","service_account_id":"not-real"}}`

	var bad AccountConfig
	err = json.Unmarshal([]byte(measuredMappingForm), &bad)
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("serviceAccountAuthorizedKey"))
}
