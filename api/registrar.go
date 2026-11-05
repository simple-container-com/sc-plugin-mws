// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"github.com/pkg/errors"

	"github.com/simple-container-com/api/pkg/api"
)

// EchoDnsRegistrarConfig is the configuration shape for `type: echo-dns`
// in a server.yaml `registrars:` block. Real cloud registrars carry a
// zone id or name plus credentials; echo keeps just a name because it
// doesn't talk to a cloud.
type EchoDnsRegistrarConfig struct {
	// ZoneName is a cosmetic label — the echo registrar does nothing
	// with it beyond echoing it back in rendered record names. A real
	// cloud registrar MUST read the deploying stack's `baseDnsZone`
	// from `ProvisionRegistrarRequest.BaseDnsZone` and use that for
	// lookups, not this field. (zoneName is a DEFAULT, nothing more.)
	// See the zone-vs-baseDnsZone section of plugin-authoring.md.
	ZoneName string `json:"zoneName" yaml:"zoneName"`

	// Records is the per-zone static records SC renders at provision
	// time, by calling NewRecord once per entry. Real registrars just
	// forward this to the lifted driver.
	Records []api.DnsRecord `json:"dnsRecords,omitempty" yaml:"dnsRecords,omitempty"`

	// Embed the per-cloud account config inline so an operator's
	// `credentials: ${auth:echo}` resolves into Token et al. Real
	// clouds generally embed their *AccountConfig here directly.
	EchoAccountConfig `json:",inline" yaml:",inline"`
}

func (r *EchoDnsRegistrarConfig) ProviderType() string {
	return ProviderType
}

// DnsRecords is what SC core reads to decide which NewRecord calls to
// fan out. The method lives on the config so the plugin bus can call it
// without reflecting on the type.
func (r *EchoDnsRegistrarConfig) DnsRecords() []api.DnsRecord {
	return r.Records
}

// Validate — same philosophy as auth validators: refuse at parse time
// what the first API call would otherwise reject with a cloud-specific
// error message.
func (r *EchoDnsRegistrarConfig) Validate() error {
	if r.ZoneName == "" {
		return errors.Errorf("`zoneName` is required for registrar type %q", RegistrarTypeEchoDns)
	}
	return r.EchoAccountConfig.Validate()
}

func ReadEchoDnsRegistrarConfig(config *api.Config) (api.Config, error) {
	return api.ConvertConfig(config, &EchoDnsRegistrarConfig{})
}

// EchoFunctionTemplateConfig is a stand-in for cloud-specific template
// types (yc-serverless-container, aws-lambda, cloudrun, …). Real clouds
// lift much of the template config from the shared TemplateConfig
// embedded field here.
type EchoFunctionTemplateConfig struct {
	// Timeout is a trivial placeholder knob. A real cloud function
	// template carries runtime, memory, env, concurrency, …
	Timeout int `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	EchoAccountConfig `json:",inline" yaml:",inline"`
}

func (r *EchoFunctionTemplateConfig) ProviderType() string {
	return ProviderType
}

func (r *EchoFunctionTemplateConfig) Validate() error {
	return r.EchoAccountConfig.Validate()
}

func ReadEchoFunctionTemplateConfig(config *api.Config) (api.Config, error) {
	return api.ConvertConfig(config, &EchoFunctionTemplateConfig{})
}
