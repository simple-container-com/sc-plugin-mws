// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"github.com/pkg/errors"

	"github.com/simple-container-com/api/pkg/api"
)

// EchoAccountConfig is the credential blob a `${auth:<name>}` reference
// resolves to for the echo cloud. Replace the fields with your cloud's —
// typical shapes are a key id + secret (like Cloud.ru Evolution) or a
// service-account JSON (like Yandex) or an API token (like Cloudflare).
type EchoAccountConfig struct {
	// Token is the one credential the echo cloud understands. Real
	// clouds generally need more fields (project id, region, …).
	Token string `json:"token" yaml:"token"`

	// api.Credentials embedded inline: operators point `${auth:…}` at a
	// secret blob rather than filling fields verbatim. CredentialsValue
	// below round-trips through api.AuthToString when the blob is empty.
	api.Credentials `json:",inline" yaml:",inline"`
}

func (r *EchoAccountConfig) ProviderType() string {
	return ProviderType
}

// CredentialsValue is what `${auth:<name>}` resolves to as an opaque
// string. When the operator supplied the credentials via `${secret:…}`,
// that blob wins verbatim; otherwise SC serialises the whole struct so
// api.ConvertAuth can round-trip it on the plugin side.
func (r *EchoAccountConfig) CredentialsValue() string {
	if r.Credentials.Credentials != "" {
		return r.Credentials.Credentials
	}
	return api.AuthToString(r)
}

// ProjectIdValue is what `${auth:<name>.projectId}` interpolates to —
// most real clouds carry a project id or folder id here. Echo has no
// such notion, so this returns the k8s-style "n/a" marker.
func (r *EchoAccountConfig) ProjectIdValue() string {
	return "n/a"
}

// Validate refuses a config that cannot authenticate, naming the field.
// A real cloud catches here what the first API call would otherwise
// surface as a 401 or 403 with no clue which half is missing.
func (r *EchoAccountConfig) Validate() error {
	if r.Token == "" {
		return errors.Errorf("`token` is required for auth type %q", AuthTypeEchoKey)
	}
	return nil
}

// ReadEchoKeyConfig is the api.ConfigReaderFunc registered in init.go.
// It unmarshals the operator's YAML into the typed struct. All plugin
// config readers follow this one-line pattern.
func ReadEchoKeyConfig(config *api.Config) (api.Config, error) {
	return api.ConvertConfig(config, &EchoAccountConfig{})
}
