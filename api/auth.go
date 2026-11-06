// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"github.com/simple-container-com/api/pkg/api"
)

// AccountConfig is what a `${auth:<name>}` reference resolves to for MWS.
//
// # The constraint that shapes this struct: upstream takes a PATH, not a value
//
// MWS's Terraform provider accepts exactly two credential inputs, and this was
// read off the source at tag 0.0.38 rather than inferred from the docs
// (internal/provider/config.go:18-36, internal/provider/public/sdk.go:35-52):
//
//	service_account_authorized_key_path   // a FILESYSTEM PATH, env MWS_SERVICE_ACCOUNT_AUTHORIZED_KEY_PATH
//	mws_token                             // a sensitive IAM token,  env MWS_TOKEN
//
// (A third path exists — running inside an MWS Compute VM with a linked service
// account, detected via IMDS — which needs no credential at all and is why the
// provider's own schema marks both of the above optional.)
//
// There is NO inline-JSON credential field. That is the one real difference from
// `sc-plugin-yandex`, whose `serviceAccountKey` takes the authorized-key JSON
// verbatim and hands it straight to the provider. SC carries secrets as VALUES —
// `${secret:mws-sa-key}` interpolates a string, and there is no file on the
// deploying machine to point at — so the plugin tier has to bridge the gap. It
// does so by writing the key to a 0600 temp file under the deploy's own temp dir
// and passing that path to the provider; see the plugin module's provider wiring
// for where the file is created and unlinked.
//
// This is recorded here, in the config tier, because it is the reason the field
// below is named for its CONTENT (`serviceAccountAuthorizedKey`) and not for the
// provider input it eventually feeds (`...Path`). An operator never supplies a
// path, and a field called `path` would invite one that does not exist on the
// runner.
type AccountConfig struct {
	// ServiceAccountAuthorizedKey is the authorized-key JSON verbatim, as issued
	// by `mws iam key create`. Carry it through `${secret:…}` — it contains a
	// private key.
	//
	// It must arrive as a YAML **string** — `${secret:…}` (the normal path) or a
	// `|` block scalar. Never as a nested YAML mapping, even though the value IS
	// a JSON object and pasting it as a block is the obvious thing to try. On the
	// opaque decode path, core's `PluginFieldConfig.CredentialsValue()`
	// (api `pkg/api/read.go:537`) serialises the whole raw auth map to JSON, and
	// its `coerceToStringIfScalarNumber` helper coerces scalars but **passes
	// nested maps through unchanged** — so a mapping here reaches this string
	// field as a JSON object and `json.Unmarshal` fails with "cannot unmarshal
	// object into Go struct field ... of type string".
	//
	// This field makes MWS the fleet's first auth config with a string whose
	// CONTENT is JSON, so it is the first to be able to hit that: the coercer's
	// known case was number→string (api run 41728259367, 2026-11-03), and
	// object→string is the untested sibling. Not a core bug — the shapes core
	// coerces are the ones existing plugins needed — but it is ours to not walk
	// into.
	ServiceAccountAuthorizedKey string `json:"serviceAccountAuthorizedKey,omitempty" yaml:"serviceAccountAuthorizedKey,omitempty"`

	// Token is a short-lived IAM token, the alternative to the authorized key.
	// Useful for a one-off local run against an already-authenticated session;
	// not useful in CI, where the token would expire between provision and
	// deploy. Prefer ServiceAccountAuthorizedKey for anything durable.
	Token string `json:"token,omitempty" yaml:"token,omitempty"`

	// Project is the MWS project (folder) every resource lands in — upstream's
	// `project` provider input (MWS_PROJECT), and what
	// `${auth:<name>.projectId}` interpolates to. Required: upstream treats it
	// as an optional provider default, but a deploy with no project has nowhere
	// to put a machine, and discovering that from the cloud's own 4xx is strictly
	// worse than discovering it at parse time.
	Project string `json:"project" yaml:"project"`

	// Zone is the availability zone (upstream `zone`, MWS_ZONE). Optional —
	// EffectiveZone falls back to DefaultZone. MWS's API has no region dimension
	// above the zone, so there is no `region` field here; that is a property of
	// the cloud and not an omission (compare Capabilities.Regionless, which
	// Cloud.ru Evolution sets for the same reason).
	Zone string `json:"zone,omitempty" yaml:"zone,omitempty"`

	// Endpoint overrides the API host (upstream `endpoint`, MWS_BASE_ENDPOINT).
	// Optional — EffectiveEndpoint falls back to DefaultEndpoint. Present because
	// MWS, like every Russian cloud SC targets, has had more than one API host in
	// its short life, and a hardcoded host is a redeploy away from being wrong.
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`

	// api.Credentials embedded INLINE so an operator can point `${auth:…}` at a
	// whole secret blob rather than filling fields one by one. CredentialsValue
	// below round-trips through api.AuthToString when the blob is empty.
	//
	// `json:",inline"` is legal here and ONLY here: encoding/json honours it on an
	// EMBEDDED field and silently ignores it on a NAMED one, which is playbook
	// trap 6. Do not copy this tag onto a named field.
	api.Credentials `json:",inline" yaml:",inline"`
}

func (r *AccountConfig) ProviderType() string {
	return ProviderType
}

// CredentialsValue is what `${auth:<name>}` resolves to as an opaque string.
// When the operator supplied the credentials as a blob via `${secret:…}`, that
// blob wins verbatim; otherwise SC serialises the whole struct so
// api.ConvertAuth can round-trip it on the plugin side.
func (r *AccountConfig) CredentialsValue() string {
	if r.Credentials.Credentials != "" {
		return r.Credentials.Credentials
	}
	return api.AuthToString(r)
}

// ProjectIdValue is what `${auth:<name>.projectId}` interpolates to.
func (r *AccountConfig) ProjectIdValue() string {
	return r.Project
}

// EffectiveZone resolves the zone, applying upstream's own default rather than
// leaving the provider to do it. Two reasons to resolve it here: the value shows
// up in SC's logs and in the plugin's progress output, and the VM driver needs a
// concrete zone to build a machine's URN-stable name from.
func (r *AccountConfig) EffectiveZone() string {
	if r.Zone != "" {
		return r.Zone
	}
	return DefaultZone
}

// EffectiveEndpoint resolves the API host, same reasoning as EffectiveZone.
func (r *AccountConfig) EffectiveEndpoint() string {
	if r.Endpoint != "" {
		return r.Endpoint
	}
	return DefaultEndpoint
}

// UsesAuthorizedKey reports which of upstream's two credential inputs this
// config will drive. The plugin tier branches on it: true means materialise the
// key to a temp file and set `service_account_authorized_key_path`, false means
// set `mws_token`.
func (r *AccountConfig) UsesAuthorizedKey() bool {
	return r.ServiceAccountAuthorizedKey != ""
}

// Validate refuses, at parse time, a config that cannot authenticate — naming
// the field, because the cloud's own answer to a bad credential is a 401 with no
// indication of which half is missing.
func (r *AccountConfig) Validate() error {
	hasKey := r.ServiceAccountAuthorizedKey != ""
	hasToken := r.Token != ""

	switch {
	case !hasKey && !hasToken:
		return errors.Errorf(
			"one of `serviceAccountAuthorizedKey` or `token` is required for auth type %q "+
				"(MWS accepts a service-account authorized key or a short-lived IAM token)",
			AuthTypeMwsServiceAccount,
		)
	case hasKey && hasToken:
		// Upstream's switch takes the authorized key first and never mentions the
		// token it ignored (public/sdk.go:35). An operator who set both would not
		// learn which one authenticated them, so refuse instead of picking.
		return errors.Errorf(
			"`serviceAccountAuthorizedKey` and `token` are mutually exclusive for auth type %q: "+
				"set exactly one (MWS's provider silently prefers the key and ignores the token)",
			AuthTypeMwsServiceAccount,
		)
	}

	if r.Project == "" {
		return errors.Errorf("`project` is required for auth type %q", AuthTypeMwsServiceAccount)
	}

	if hasKey {
		if err := validateAuthorizedKey(r.ServiceAccountAuthorizedKey, r.Project); err != nil {
			return err
		}
	}

	return nil
}

// authorizedKeyIDPattern is the shape of the `keyId` field inside an MWS
// authorized key, copied from the regexp the SDK itself matches against
// (go.mws.cloud/go-sdk/mws/iam/service_account.go:18). It is not decoration: the
// SDK parses the project and the service-account name OUT of this string, so a
// key whose id does not match authenticates as nobody.
var authorizedKeyIDPattern = regexp.MustCompile(`^projects/([^/]+)/serviceAccounts/([^/]+)/authorizedKeys/(.+)$`)

// authorizedKeyAlgorithm is the only algorithm the SDK accepts (iam.ES256).
const authorizedKeyAlgorithm = "ES256"

// authorizedKeyDoc is the MWS authorized-key document, with upstream's field
// names. They are camelCase and the id is a structured path — NOT the Yandex
// shape (`id` / `service_account_id` / `private_key`), which is the natural wrong
// guess here given how closely the rest of MWS's auth model follows Yandex's.
// Mirrored from the anonymous struct in ServiceAccountAuthorizedKey.UnmarshalJSON
// (go-sdk v0.29.0) rather than importing it: this module must stay SDK-free
// because SC core imports it.
type authorizedKeyDoc struct {
	KeyID      string `json:"keyId"`
	PrivateKey string `json:"privateKey"`
	Algorithm  string `json:"algorithm"`
}

// validateAuthorizedKey checks the key is the JSON document upstream's
// `iam.ServiceAccountAuthorizedKeyFromFile` will parse.
//
// Worth the lines: every failure it prevents happens at the FAR end of a deploy,
// inside the Pulumi provider, as `load service account authorized key file: …`
// against a path SC generated for its own temp copy — so the operator is told
// their temp file is bad rather than their secret, and the message names a file
// that is already unlinked by the time they look. The commonest cause is a
// `${secret:…}` that resolved to a placeholder because the registry entry was
// missing, which is a one-line, obviously-not-JSON string.
func validateAuthorizedKey(key, project string) error {
	trimmed := strings.TrimSpace(key)
	if !strings.HasPrefix(trimmed, "{") {
		return errors.Errorf(
			"`serviceAccountAuthorizedKey` for auth type %q must be the authorized-key JSON document "+
				"(it starts with `{`); got a %d-character value that does not — a `${secret:…}` that "+
				"resolved to a placeholder looks exactly like this",
			AuthTypeMwsServiceAccount, len(trimmed),
		)
	}

	var doc authorizedKeyDoc
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return errors.Wrapf(err, "`serviceAccountAuthorizedKey` for auth type %q is not valid JSON", AuthTypeMwsServiceAccount)
	}
	if doc.PrivateKey == "" {
		return errors.Errorf(
			"`serviceAccountAuthorizedKey` for auth type %q is JSON but has no `privateKey` field — "+
				"this is not an authorized-key document (an API key and a public-key export both look like this)",
			AuthTypeMwsServiceAccount,
		)
	}
	if doc.Algorithm != authorizedKeyAlgorithm {
		return errors.Errorf(
			"`serviceAccountAuthorizedKey` for auth type %q has `algorithm: %q`; MWS's SDK accepts only %q "+
				"and refuses anything else outright",
			AuthTypeMwsServiceAccount, doc.Algorithm, authorizedKeyAlgorithm,
		)
	}

	matches := authorizedKeyIDPattern.FindStringSubmatch(doc.KeyID)
	if matches == nil {
		return errors.Errorf(
			"`serviceAccountAuthorizedKey` for auth type %q has `keyId: %q`, which is not of the form "+
				"`projects/<project>/serviceAccounts/<sa>/authorizedKeys/<id>` — MWS's SDK parses the project "+
				"and service-account name out of this string, so a key with a malformed id authenticates as nobody",
			AuthTypeMwsServiceAccount, doc.KeyID,
		)
	}

	// The key names its own project. A `project:` that disagrees with it is a
	// deploy that authenticates fine and then 403s (or, worse, succeeds against
	// the wrong project) — catching it here costs one string compare and turns a
	// cloud-side permission error into a config error that names both values.
	if keyProject := matches[1]; project != "" && keyProject != project {
		return errors.Errorf(
			"`project` is %q but `serviceAccountAuthorizedKey` belongs to project %q (from its `keyId`); "+
				"the credential cannot act in a project it was not issued for",
			project, keyProject,
		)
	}

	return nil
}

// ProjectFromAuthorizedKey reads the project out of the configured authorized
// key's `keyId`, or "" if there is no key or it is malformed. Exposed because
// the plugin tier logs the resolved project and the two sources should be
// visibly the same one; Validate has already refused a disagreement.
func (r *AccountConfig) ProjectFromAuthorizedKey() string {
	var doc authorizedKeyDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.ServiceAccountAuthorizedKey)), &doc); err != nil {
		return ""
	}
	matches := authorizedKeyIDPattern.FindStringSubmatch(doc.KeyID)
	if matches == nil {
		return ""
	}
	return matches[1]
}

// ReadServiceAccountConfig is the api.ConfigReaderFunc registered in init.go.
func ReadServiceAccountConfig(config *api.Config) (api.Config, error) {
	return api.ConvertConfig(config, &AccountConfig{})
}
