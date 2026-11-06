// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// sc-plugin-mws is SC's cloud plugin for MWS (МТС Web Services).
//
// # What this binary advertises today, and why it is so small
//
// Schema() advertises ONE thing: the `mws` provider type, so an operator can
// write an `mws-service-account` auth block in server.yaml and have SC parse it.
// Everything else is empty, and each emptiness is a measured fact about the cloud
// rather than unfinished work — see the package doc on ../../api/init.go, which
// records what MWS's Terraform provider does and does not model (no bucket, no
// container registry, no standalone DNS zone). A plugin that advertised a
// registrar or a state store it cannot back would be offered to operators by
// fool-protection and then fail at provision time.
//
// The VM driver (`mws-vm`) is the next slice and is NOT advertised yet. The
// reason is a hard one, worth stating where the next author will be standing:
// SC core calls vm.RegisterPluginDriver for every driver in VmDrivers, and a
// failure there is `return err` out of the whole pre-engine plugin load
// (pkg/clouds/pulumi/plugin_preengine.go:265) — not a warning. On any SC core
// older than the release carrying api PR #558, `mws-vm` has an unflagged
// placeholder row in the in-tree Drivers table, so registration is refused with
// "which SC core provides itself" and EVERY deploy that loads this plugin dies.
// That is the exact shape of the 2026-10-30 sc-plugin-cloudru breakage.
//
// #558 MERGED 2026-11-06 (api e8dcd103), but the newest api release — v2026.11.23,
// cut 2026-11-05, which is also what both go.mod files pin — predates the merge.
// So every RELEASED core still refuses, and the hazard is live until the next
// api release.
//
// And when that release lands, it still does not by itself make advertising
// worthwhile, which is the easy thing to get wrong here. VmDriverSpec() reads
// core's own Drivers row, and that row says Implemented: false — so core would
// merge in a spec that makes VmConfig.Validate() refuse `provider: mws-vm`
// exactly as it already does with nothing advertised (pkg/clouds/vm/vm.go:427).
// Advertising buys something only when this plugin declares its OWN spec with
// Implemented: true and measured capabilities, i.e. when the driver actually
// exists. At that point VmDriverSpec() stops reading core's table and
// hand-builds the row the way sc-plugin-echo does.
//
// So the order is: api release carrying #558 → write the driver → hand-build the
// spec, advertise it, and bump the api pin, all in one change — and note that
// that change needs no api COMMIT. #558 deliberately left core's `mws-vm` row a
// placeholder; flipping its `Implemented` would lock this plugin back out. See
// ../../internal/vm_driver.go for the ownership rule.
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
//     (trap 7). No handler here touches Pulumi yet, which is why `runtime` is
//     not imported — the VM driver will be the first, and must.
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
// covers the OwnedTypes half.
func (p *mwsPlugin) Schema(_ context.Context, _ *scpluginv1.SchemaRequest) (*scpluginv1.SchemaResponse, error) {
	return &scpluginv1.SchemaResponse{
		PluginVersion: version,
		ProviderTypes: []string{mwsApi.ProviderType},

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
		//   VmDrivers             — two gates, both still shut; see the package
		//                           doc. (a) no api RELEASE carries #558 yet, so
		//                           a released core still refuses the placeholder
		//                           row and fails the whole plugin load; (b) the
		//                           driver isn't written, and a spec read from
		//                           core's Implemented:false row refuses a deploy
		//                           identically to advertising nothing. This is
		//                           the one entry that is sequencing rather than
		//                           absence.
		PulumiPlugins: []*scpluginv1.PulumiPluginRequirement{
			// Empty until the VM driver lands, at which point this becomes
			// `{Package: "mws", Version: "0.1.0", Server:
			// "github://api.github.com/simple-container-com/pulumi-mws"}` — the
			// bridge is a third-party provider, so core needs the download server
			// as well as the name or `pulumi up` cannot resolve it.
		},
	}, nil
}

// VmHosts converges a VM resource whose `provider:` names a driver this plugin
// advertises. It is unreachable today — SC core only wires the VmHosts sidecar
// when Schema() returns a non-empty VmDrivers list — and returns an explicit
// refusal rather than a stub host, so that if the wiring ever reaches it the
// operator is told the truth instead of being handed an address nothing listens
// on. (sc-plugin-echo's template returns a TEST-NET host here; that is right for
// a template and wrong for a real cloud.)
//
// The implementation, when it lands: read req.ConfigJson into
// pkg/clouds/vm.VmConfig; convert credentials via api.ConvertAuth into
// mwsApi.AccountConfig and materialise the authorized key to a 0600 temp file
// (see the doc on AccountConfig for why a path and not a value); register the
// pulumi-mws resources (compute.VirtualMachine, the one_to_one_nat address, a
// vpc.FirewallRule, a tls deploy key); parse outputs through
// plugin.OutputRefTo / AddressOutput / PrivateKeyOf; compose the VmHostBuilder
// chain and return BuildVmHosts. api/pkg/plugin/vmhosts.go is the builder
// surface; pkg/clouds/pulumi/vm/plugin_authoring_test.go is the end-to-end
// pattern; sc-plugin-cloudru's `cloudru-evolution-vm` is the first live example.
// And this handler is where the runtime.NewContext + runtime.Settle pair becomes
// mandatory.
func (p *mwsPlugin) VmHosts(_ context.Context, req *scpluginv1.VmHostsRequest) (*scpluginv1.VmHostsResponse, error) {
	p.progress.Error("vm-hosts", fmt.Sprintf("refusing vm %q: driver %q is not implemented in this plugin version", req.GetResourceName(), req.GetDriver()))
	return nil, mwsInternal.ErrVmDriverNotImplemented
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
