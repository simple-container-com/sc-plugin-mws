// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// Package echo is the CONFIG TIER of the sc-plugin-echo template.
//
// This package is what SC core imports. It contributes a `ProviderType`
// discriminator plus one of each kind of type token a plugin can own —
// auth, registrar, template, resource, VM driver — all echo-shaped
// stubs. An author replicates the pattern for a real cloud and swaps the
// bodies; the file structure, the init() registrations and the
// fool-protection declaration do not change.
//
// Two things authors re-discover from scratch when they skip the
// template, both documented at length in `plugin-authoring.md`:
//
//   - The `init()` here MUST call `api.RegisterPluginRequirement` with
//     every type token this plugin owns. Fool-protection auto-injects a
//     `plugins:` block for operators who upgrade without touching
//     their server.yaml, and this is how SC core knows the mapping.
//   - This package is published as its OWN Go module (api/go.mod).
//     The plugin binary module (../plugin/go.mod) depends on it; SC core
//     also depends on it. Shipping config + provisioning in one module
//     makes every SC core consumer transitively pull every cloud SDK the
//     plugin uses.
//
// See `docs/docs/guides/plugin-authoring.md` in the api repo for the
// walkthrough.
package echo

import (
	"github.com/simple-container-com/api/pkg/api"
)

// ProviderType is the top-level discriminator every echo config reports
// from ProviderType(). It's also the directory generated schemas would
// land in (`docs/schemas/echo/`).
const ProviderType = "echo"

// Owned type tokens. Each is one row the authoring guide swaps for a
// real cloud. The names are deliberately boring — an author greps the
// repo for `echo-` and replaces.
const (
	AuthTypeEchoKey            = "echo-key"
	RegistrarTypeEchoDns       = "echo-dns"
	TemplateTypeEchoFunction   = "echo-function"
	ResourceTypeEchoBucket     = "echo-bucket"
	DriverEchoVm               = "echo-vm"
	StateStorageTypeEchoBucket = "echo-state-bucket"
	SecretsProviderTypeEchoKms = "echo-kms"
)

func init() {
	// Every type that reads config out of the YAML blob SC core parses.
	// For an author's cloud: swap `ReadEchoKeyConfig` for the real
	// `api.ConfigReaderFunc` that unmarshals your account/auth shape.
	api.RegisterProviderConfig(api.ConfigRegisterMap{
		AuthTypeEchoKey:          ReadEchoKeyConfig,
		RegistrarTypeEchoDns:     ReadEchoDnsRegistrarConfig,
		TemplateTypeEchoFunction: ReadEchoFunctionTemplateConfig,
	})

	// Fool-protection: SC core's parse-time validator auto-injects
	// `plugins.echo:` with the latest release from GitHub when it
	// finds an un-declared OwnedTypes token in server.yaml. Without
	// this registration an operator upgrading past the in-tree-drop
	// release of SC would see a hard parse error.
	//
	// OwnedTypes must list EVERY token this plugin owns — missing a
	// token here is a silent gap: an operator who only uses the
	// unlisted token gets no auto-inject and the deploy fails later
	// with "unknown resource type". See api/pkg/api/plugin_requirements.go
	// + Addendum J of 04-no-destroy-invariant.md.
	api.RegisterPluginRequirement(api.PluginRequirement{
		PluginName: "echo",
		OwnedTypes: []string{
			ProviderType,
			AuthTypeEchoKey,
			RegistrarTypeEchoDns,
			TemplateTypeEchoFunction,
			ResourceTypeEchoBucket,
			DriverEchoVm,
			StateStorageTypeEchoBucket,
			SecretsProviderTypeEchoKms,
		},
		DefaultSource: api.PluginSource{
			Type:       "github-release",
			Repository: "simple-container-com/sc-plugin-echo",
		},
	})
}
