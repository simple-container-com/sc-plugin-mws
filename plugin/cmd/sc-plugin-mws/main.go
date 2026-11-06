// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// sc-plugin-mws is SC's cloud plugin for MWS (МТС Web Services).
//
// # What this binary advertises, and what the emptiness means
//
// Schema() advertises the `mws` provider type and auth type (so an operator can
// write an `mws-service-account` block in server.yaml), the `mws-vm` VM driver,
// and the two Pulumi provider plugins the driver needs. Everything else is
// empty, and each emptiness is a measured fact about the cloud rather than
// unfinished work — see the package doc on ../../api/init.go, which records what
// MWS's Terraform provider does and does not model (no bucket, no container
// registry, no standalone DNS zone). A plugin that advertised a registrar or a
// state store it cannot back would be offered to operators by fool-protection
// and then fail at provision time.
//
// # The core-version floor, and why it is not negotiable
//
// SC core calls vm.RegisterPluginDriver for every driver in VmDrivers, and a
// failure there is `return err` out of the whole pre-engine plugin load
// (pkg/clouds/pulumi/plugin_preengine.go:265) — not a warning. On any SC core
// older than the release carrying api PR #558, `mws-vm` has an unflagged
// placeholder row in the in-tree Drivers table, so registration is refused with
// "which SC core provides itself" and EVERY deploy that loads this plugin dies —
// not just the ones using a VM. That is the exact shape of the 2026-10-30
// sc-plugin-cloudru breakage, and it is why advertising `mws-vm` and raising the
// api pin had to land as one commit.
//
// #558 merged 2026-11-06 (api e8dcd103) and api release 2026.11.24, published
// 26 minutes later, points at exactly that commit — verified with
// `gh api repos/simple-container-com/api/compare/e8dcd103...2026.11.24` →
// "status":"identical". So **2026.11.24 is this plugin's minimum core version**.
// Two notes for whoever re-checks: the api release tags carry no `v` prefix (the
// `v2026.11.24` in `gh release list` is the title; `gh release view v2026.11.24`
// 404s), and that release is the CLI binary, not a Go module version — the only
// v-tags in the api repo are `-preview` prereleases, so both go.mod files stay on
// a pseudo-version and the pin moves by commit (`go get …/api@e8dcd103`).
//
// # What is NOT yet true, despite a green build
//
// No machine has ever been provisioned on MWS by this driver. Every capability
// in ../../internal/vm_driver.go is read-only API probing plus vendor example
// code, and `mws-vm` is advertised with a non-empty `Unverified` reason, so a
// deploy refuses without `allowUnverifiedProvider: true`. Clear that reason in
// the change that records a boot, not in one that reports a compile.
//
// The other half is distribution: there is no pulumi-mws RELEASE, so the engine
// cannot download `pulumi-resource-mws` even though PulumiPlugins names it
// correctly below. A release pipeline is a distribution decision and needs the
// operator's explicit yes.
//
// # Trap-prevention invariants inherited from the template
//
// From sc-plugin-echo and the fifteen-trap index in TRAPS.md (see
// forge/docs/design/2026-11-01/plugin-extraction-playbook/ for the catalogue).
// The ones that bind even a plugin this small:
//
//   - Any handler that touches Pulumi MUST use `runtime.NewContext(stdctx,
//     header, runtime.WithLocalEngineFallback(p.progress))` plus
//     `defer runtime.Settle(rctx, &resp, &err)`. Never `pctx.Close()`, never
//     `defer done()`. Settle drains outstanding RegisterResource goroutines;
//     without it SC core plans a destroy of every state entry on the next apply
//     (trap 7). VmHosts below is this plugin's one such handler.
//   - A `Provision` that returns outputs returns `runtime.HarvestExports`'s map
//     (traps 10/11), and a `ComputeProcess` goes through
//     `runtime.RunComputeProcess` rather than a per-plugin collector (trap 16).
//   - LogStream is not optional cosmetics: it is the only way an operator sees
//     anything during a multi-minute RPC chain.
//
// See `docs/docs/guides/plugin-authoring.md` in the api repo for the walkthrough:
// config tier → provisioning tier → tests → release.
package main

import (
	"context"
	"fmt"
	"os"

	hclog "github.com/hashicorp/go-hclog"
	hplugin "github.com/hashicorp/go-plugin"
	scPlugin "github.com/simple-container-com/api/pkg/plugin"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
	"github.com/simple-container-com/api/pkg/plugin/runtime"

	mwsApi "github.com/simple-container-com/sc-plugin-mws/api"
	mwsInternal "github.com/simple-container-com/sc-plugin-mws/plugin/internal"
)

// pluginName is this plugin's name on the bus. It MUST equal the PluginName in
// ../../api/init.go's RegisterPluginRequirement: SC core keys the fetched
// release, the auto-injected `plugins.<name>:` block and the per-plugin driver
// table by this string, and a disagreement between the two tiers is a plugin
// that downloads fine and then owns nothing. api/init_test.go asserts it.
const pluginName = "mws"

// version is stamped by the release workflow's
// `-ldflags "-X main.version=<CalVer>"` and reported as SchemaResponse's
// PluginVersion, which SC core logs as `pre-engine loaded plugin "mws" v<...>`.
// That log line is the only place an operator can see WHICH build of the plugin
// their deploy actually fetched, so it must not be a hardcoded constant: the
// github-release fetcher resolves "latest" by default, and a stale cached binary
// reporting a fresh version number is a debugging dead end. Default "dev"
// identifies a local `go build`.
var version = "dev"

// mwsPlugin is the gRPC server impl.
type mwsPlugin struct {
	scpluginv1.UnimplementedSCCloudPluginServer

	// progress is the plugin's side of the LogStream RPC. SC core opens one
	// LogStream per plugin after Schema() returns, and this is where per-RPC
	// narration goes.
	progress *scPlugin.Publisher

	// broker is captured by SetBroker when hashicorp/go-plugin hands it to
	// GRPCServer. Needed only for the SCCore callback stream (PeerInvoke to
	// another plugin, ResolveSecret / ResolveCredential against core), which this
	// plugin does not use yet — kept because it is nil until the go-plugin
	// lifecycle has run, and a derived plugin that adds a registrar bridge later
	// should not have to re-derive the BrokerAware shape.
	broker *hplugin.GRPCBroker
}

// SetBroker implements scPlugin.BrokerAware. api/pkg/plugin/goplugin.go's
// scCloudPlugin.GRPCServer type-asserts the Impl against this interface and
// calls SetBroker when it exists; without it, scPlugin.NewPeerClient returns
// "nil broker" and PeerInvoke never dials.
func (p *mwsPlugin) SetBroker(b *hplugin.GRPCBroker) { p.broker = b }

// Schema is the first RPC SC core calls. It advertises every token this plugin
// owns, so core can route config parsing and know which Pulumi provider plugins
// to materialise in the stack.
//
// Keep in sync with ../../api/init.go's OwnedTypes. The two halves fail
// differently: a token missing from OwnedTypes is a SILENT fool-protection gap,
// a token missing here is an unroutable type at deploy time. api/init_test.go
// covers the OwnedTypes half; main_test.go covers THIS half and, in
// TestEverySchemaTokenIsAnOwnedType, the cross-check between them — which only
// this module can run, since the dependency goes plugin → api and never back.
func (p *mwsPlugin) Schema(_ context.Context, _ *scpluginv1.SchemaRequest) (*scpluginv1.SchemaResponse, error) {
	return &scpluginv1.SchemaResponse{
		PluginVersion: version,
		ProviderTypes: []string{mwsApi.ProviderType},

		// The one line that makes this plugin usable at all. Until api #559
		// (merged as 99411193, 2026-11-06) `SchemaResponse` had no
		// `auth_types` field, so `type: mws-service-account` in a secrets.yaml
		// died at PARSE with `unknown auth type` — before the provider, before
		// any driver, before `AccountConfig.Validate()`, and before this binary
		// was ever dialled. `api.DetectAuthProvider` (api `pkg/api/read.go:183`)
		// is one lookup into `providerConfigMapping`, which core seeds either by
		// link-time-importing a plugin's `/api` (four plugins; never this one)
		// or from its hardcoded `plugin_bootstrap.go` table (five; never `mws`).
		// This is the third seam — `stackread.registerSchemaTypes` loops these
		// values and installs the OPAQUE decoder for each, which is why the
		// go.mod pin and this line had to land in one commit: on an older core
		// the field does not exist, and advertising nothing leaves the plugin
		// unusable while bumping alone changes nothing.
		//
		// Verified end-to-end 2026-11-06 against a locally-built core: with this
		// line, `auth["mws"]` decodes to `*api.PluginFieldConfig`; with it
		// removed and nothing else changed, `unknown auth type
		// "mws-service-account"`. Two consequences worth knowing — an operator
		// must declare an explicit `plugins: mws:` block (auto-injection runs
		// after type discovery, so it cannot bootstrap this), and because the
		// decoder is opaque, core performs NO parse-time validation of the auth
		// tree: every refusal is ours, at deploy time, in this process. See
		// TRAPS.md 17 / 19 / 20.
		AuthTypes: []string{mwsApi.AuthTypeMwsServiceAccount},

		// Deliberately empty, one measured reason each (../../api/init.go):
		//   RegistrarTypes        — MWS models no DNS zone/recordset resource.
		//   ResourceTypes         — no bucket, no registry; nothing to own yet.
		//   ComputeProcessorTypes — follows ResourceTypes: nothing to inject into
		//                           a consuming stack's env.
		//   StateStoreTypes       — no object storage, so SC's Pulumi state cannot
		//                           live in MWS.
		//   SecretsProviderTypes  — `mws-kms` / `mws-secretmanager` are plausible
		//                           (the modules exist upstream) but unregistered
		//                           until someone checks whether MWS KMS answers
		//                           the AWS KMS API, which is what the
		//                           `awskms://` gocloud URL sc-plugin-yandex uses
		//                           actually requires.
		PulumiPlugins: []*scpluginv1.PulumiPluginRequirement{
			// pulumi-mws, the static bridge over MWS's Terraform provider. Both
			// values are READ OFF THE GENERATED SDK rather than chosen, because the
			// SDK is what every resource in internal/vm_hosts.go actually requests:
			// `sdk/go/mws/internal/pulumiUtilities.go`'s PkgResourceDefaultOpts bakes
			// `PluginDownloadURL("github://api.github.com/simple-container-com/pulumi-mws")`
			// and `Version("0.0.1-dev")` into every single one. A number invented
			// here that disagrees with that would have core prefetch one version
			// while the resources ask for another.
			//
			// `0.0.1-dev` is the generator's placeholder — it appears because the SDK
			// was generated without a version stamp (`pulumiVersion.go` has
			// `SdkVersion = semver.Version{}`). It is honest about the state of
			// things rather than tidy: there is no pulumi-mws RELEASE yet, so the
			// engine cannot fetch `pulumi-resource-mws` at all. When the release
			// pipeline is approved and lands, the SDK must be regenerated with a real
			// version and this line moved with it — the two are one change.
			//
			// PluginDownloadUrl is the field's real name; there is no `Server` field
			// on this message. It is non-empty precisely because this is a
			// third-party provider: for an official one core leaves it blank and
			// pulls from the Pulumi registry, which is geoblocked from RU networks
			// anyway (see pulumi-mws's README).
			{
				Package:           "mws",
				Version:           "0.0.1-dev",
				PluginDownloadUrl: "github://api.github.com/simple-container-com/pulumi-mws",
			},
			// pulumi-tls — the VM driver mints two ED25519 key pairs (the deploy key
			// and the machine's own host key) as tls.PrivateKey. Matches this module's
			// go.mod; sc-plugin-cloudru advertises 5.3.0 while its go.mod is also on
			// 5.5.0, so do not copy that number from there.
			{Package: "tls", Version: "5.5.0"},
		},
		VmDrivers: []*scpluginv1.VmDriverSpec{
			// Advertise `mws-vm` so core routes a `type: vm` / `provider: mws-vm`
			// resource to this plugin's VmHosts RPC.
			//
			// Unlike sc-plugin-cloudru — which passes
			// `vm.Drivers[...].ToProto(...)` straight through — this spec is
			// HAND-BUILT in the plugin. Core's `mws-vm` row is a deliberate
			// `Implemented: false` placeholder, and it has to stay one: api #558's
			// ownership rule is `coreOwned() = Implemented && !MigratedToPlugin`,
			// so flipping core's row would make core the owner and lock this
			// plugin out of its own driver name. See ../../internal/vm_driver.go.
			mwsInternal.VmDriverSpec(),
		},
	}, nil
}

// VmHosts converges a VM resource whose `provider:` names a driver this plugin
// advertises, i.e. `mws-vm`. The work is in internal/vm_hosts.go; this is the
// transport shell, and it is deliberately identical in shape to
// sc-plugin-cloudru's.
//
// The three lines that are not boilerplate:
//
//   - `runtime.NewContext(ctx, req.GetHeader(), …)` builds the Pulumi context
//     from the header SC core sent. `WithLocalEngineFallback(p.progress)` is what
//     lets the handler run under a local engine when no core-side monitor is on
//     the other end — which is how the contract tests drive it.
//   - `defer runtime.Settle(rctx, &resp, &err)` is MANDATORY and must take the
//     ADDRESSES of the named returns, which is the only reason this signature
//     names them. Settle drains RegisterResource goroutines that are still in
//     flight when the handler returns; without it the engine never learns about
//     those resources and SC core plans a DESTROY of every one of them on the
//     next apply (trap 7). `pctx.Close()` is not a substitute and must not be
//     used.
//   - the progress lines bracket a multi-minute RPC. Six cloud resources plus a
//     cloud-init boot is long enough that silence reads as a hang, and LogStream
//     is the operator's only window into it.
func (p *mwsPlugin) VmHosts(ctx context.Context, req *scpluginv1.VmHostsRequest) (resp *scpluginv1.VmHostsResponse, err error) {
	p.progress.Info("vm-hosts", fmt.Sprintf("provisioning mws-vm %q (driver=%s)", req.GetResourceName(), req.GetDriver()))

	rctx, err := runtime.NewContext(ctx, req.GetHeader(), runtime.WithLocalEngineFallback(p.progress))
	if err != nil {
		return nil, err
	}
	defer runtime.Settle(rctx, &resp, &err)

	resp, err = mwsInternal.VmHosts(rctx.Pulumi, req)
	if err != nil {
		p.progress.Error("vm-hosts", fmt.Sprintf("vm %q failed: %v", req.GetResourceName(), err))
		return nil, err
	}

	p.progress.Info("vm-hosts", fmt.Sprintf("vm %q ready, %d host(s)", req.GetResourceName(), len(resp.GetHosts())))
	return resp, nil
}

// PreDestroy is dialled by SC core before Pulumi's destroy phase for EVERY
// loaded plugin, unconditionally — there is no type keying and no opt-out
// (pkg/clouds/pulumi/plugin_preengine.go:275). Implemented as an explicit no-op
// rather than left to UnimplementedSCCloudPluginServer so a destroy does not log
// a codes.Unimplemented warning that reads like a fault. MWS has nothing to
// clean up outside Pulumi's own state; a cloud that does (mongodb drops
// databases named in service-user outputs) puts that work here.
func (p *mwsPlugin) PreDestroy(_ context.Context, _ *scpluginv1.PreDestroyRequest) (*scpluginv1.PreDestroyResponse, error) {
	return &scpluginv1.PreDestroyResponse{}, nil
}

// LogStream drains the plugin's progress Publisher onto the gRPC stream. SC core
// opens one of these per plugin after Schema() returns. Ends when the stream
// context cancels (SC core teardown) or Serve stops accepting.
func (p *mwsPlugin) LogStream(_ *scpluginv1.LogStreamRequest, stream scpluginv1.SCCloudPlugin_LogStreamServer) error {
	ctx := stream.Context()
	for {
		batch, ok := p.progress.Drain()
		if !ok {
			return nil
		}
		for _, ev := range batch {
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func main() {
	// AFTER Serve returns, never in the VmHosts handler. The MWS provider reads
	// the materialised authorized-key FILE during the engine's apply, which
	// happens after the RPC that wrote it has already returned — a `defer` in the
	// handler deletes the credential before the provider opens it. See
	// internal/credential.go's header.
	defer mwsInternal.CleanupCredentials()

	impl := &mwsPlugin{progress: scPlugin.NewPublisher(0)}
	scPlugin.Serve(scPlugin.ServerConfig{
		Impl: impl,
		Logger: hclog.New(&hclog.LoggerOptions{
			Name:   "sc-plugin-" + pluginName,
			Level:  hclog.Warn,
			Output: os.Stderr,
		}),
	})
}
