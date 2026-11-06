// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// Package mws is the CONFIG TIER for MWS (МТС Web Services).
//
// This package is what SC core imports: it contributes the type tokens an
// operator may write in `server.yaml`, the readers that parse them, and the
// fool-protection declaration that maps every one of those tokens back to this
// plugin. It deliberately carries NO cloud SDK — the binary module next door
// (../plugin) does, and keeping them apart is why SC core can depend on this
// without pulling `go.mws.cloud/go-sdk` into every consumer.
//
// # What MWS has, and what this plugin therefore owns
//
// MWS's Terraform provider (mws-cloud-platform/terraform-provider-mws,
// Apache-2.0, bridged into Pulumi as simple-container-com/pulumi-mws) models 41
// resources across 15 modules: certmanager, compute, gpt, iam, kms, mclickhouse,
// metastore, mk8s, mkafka, mpostgres, nlb, queue, resmanager, secretmanager, vpc.
//
// Three absences shape this file, and all three were measured against tag 0.0.38
// (commit b4f056748a72) rather than assumed:
//
//   - **No object storage / bucket resource.** `iam/hmacKey` implies an S3-compatible
//     API exists behind the platform, but no bucket is modelled in IaC. So there is
//     no `mws-state-bucket` state-store token and no `mws-bucket` resource token —
//     SC cannot keep its Pulumi state or a `uses:`-able bucket in MWS through
//     configs, and claiming otherwise would fail at provision time.
//   - **No container registry resource.** Image delivery on this cloud is therefore
//     `ssh-tar` or an external registry, exactly as for `byo`. See vm-matrix.md §7.
//   - **No DNS zone / recordset resource.** So no `mws-dns` registrar: a stack on
//     MWS gets its public name from Cloudflare (or another registrar plugin), not
//     from MWS. The echo template's registrar files were deleted rather than
//     renamed for this reason — a registrar that cannot write a record is worse
//     than no registrar, because fool-protection would offer it.
//     Not to be confused with the per-VM internal name
//     `mws_compute_virtual_machine` can request
//     (`network.network_interfaces[].addresses[].address.spec.dns`, with `name`,
//     `ptr` and `ttl`): that names a machine inside MWS Cloud Platform and is not
//     a zone anyone outside it resolves, so it cannot carry a stack's public
//     hostname and does not make a registrar possible.
//
// What is left, and what this package registers, is the credential plus the VM
// driver: a machine in MWS is a `type: vm` resource (in-tree, parsed by SC core)
// with `provider: mws-vm`, and everything above the cloud API — cloud-init,
// readiness, compose, k3s — is the cloud-independent code in pkg/clouds/vm and
// pkg/clouds/pulumi/vm. `mws-kms` and `mws-secretmanager` are plausible future
// secrets-provider tokens (the modules exist) but are unregistered until someone
// checks whether MWS KMS answers the AWS KMS API the way Yandex's does — the
// `awskms://` gocloud URL that `sc-plugin-yandex` relies on is an API-compatibility
// claim, not a naming one.
//
// See forge/docs/design/2026-10-13/sc-russia-market-readiness/vm-matrix.md and the
// roadmap item `sc-ru-clouds-beyond-yandex`.
package mws

import (
	"github.com/simple-container-com/api/pkg/api"
)

// ProviderType is the top-level discriminator every MWS config reports from
// ProviderType(). It is also the directory generated schemas land in
// (`docs/schemas/mws/`).
const ProviderType = "mws"

const (
	// AuthTypeMwsServiceAccount is the one credential type MWS has. Both shapes
	// upstream accepts (a service-account authorized key, or a short-lived IAM
	// token) live on the same config struct rather than in two auth types,
	// because they address the SAME cloud with the same project and zone — unlike
	// Cloud.ru's two, which are two clouds behind one brand.
	AuthTypeMwsServiceAccount = "mws-service-account"

	// DriverMwsVm is the `provider:` value of a `type: vm` resource that lands in
	// MWS. It MUST equal `vm.DriverMws` in SC core — the plugin tier reads
	// `vm.Drivers[vm.DriverMws].ToProto(vm.DriverMws)` for its Schema() row, and
	// this literal is what fool-protection matches against an operator's
	// `provider:`. Spelled as a literal here so the config tier does not have to
	// import pkg/clouds/vm; the plugin tier's Schema() is where the two meet, and
	// init_test.go asserts they agree.
	DriverMwsVm = "mws-vm"
)

// DefaultEndpoint is MWS's public API host, and the default upstream's provider
// applies when `endpoint` is unset (`MWS_BASE_ENDPOINT`). Stated here so
// EffectiveEndpoint can fill it in rather than sending an empty string the
// provider would have to default for us — a config whose resolved values are
// visible is one an operator can debug.
const DefaultEndpoint = "https://api.mwsapis.ru"

// DefaultZone is upstream's default for `zone` (`MWS_ZONE`). MWS's zone names
// follow Yandex's `ru-central1-*` convention, which is a coincidence of regional
// naming and not an API relationship.
//
// Measured against the live API 2026-11-06 (it was an assumption before that):
// this is a real zone, `status.ready.state: OK`. MWS has four — `ru-central1-a`,
// `ru-central1-b`, `ru-central1-c` and `ru-siberia1-a` — across two regions. The
// full measured list, and the three different spellings a zone/type/image id
// takes depending on whether you are talking to the API, Terraform or the Go
// SDK, are in ../CLOUD-FACTS.md.
const DefaultZone = "ru-central1-a"

func init() {
	// Every type that reads config out of the YAML blob SC core parses. One
	// entry, because MWS has one credential shape and (today) no plugin-owned
	// template, resource or registrar type — see the package doc for which
	// absences are upstream's and which are ours.
	api.RegisterProviderConfig(api.ConfigRegisterMap{
		AuthTypeMwsServiceAccount: ReadServiceAccountConfig,
	})

	// Fool-protection: SC core's parse-time validator auto-injects a
	// `plugins.mws:` block with the latest release from GitHub when it finds an
	// un-declared owned type in server.yaml. Without this registration an
	// operator writing `mws-service-account` would get a hard parse error.
	//
	// OwnedTypes must list EVERY token this plugin owns — a missing token is a
	// SILENT gap, not an error: an operator who only uses the unlisted token gets
	// no auto-inject and the deploy fails much later with "unknown resource type".
	//
	// `DriverMwsVm` earns its place here even though `type: vm` itself is in-tree:
	// SC core's walker reads the driver off a VM resource through the
	// api.VmConfigAware interface (pkg/api/plugin_requirements.go:221), so a
	// VM-ONLY stack — which is every first MWS stack — is reached. That was not
	// true when sc-plugin-cloudru was written, whose init.go still carries the
	// "documentation-only until a follow-up extends the walker" caveat.
	api.RegisterPluginRequirement(api.PluginRequirement{
		PluginName: "mws",
		OwnedTypes: []string{
			ProviderType,
			AuthTypeMwsServiceAccount,
			DriverMwsVm,
		},
		DefaultSource: api.PluginSource{
			Type:       "github-release",
			Repository: "simple-container-com/sc-plugin-mws",
		},
	})
}
