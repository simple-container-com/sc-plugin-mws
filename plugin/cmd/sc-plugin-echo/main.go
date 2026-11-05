// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

// sc-plugin-echo is the TEMPLATE every SC cloud plugin clones from.
//
// What it does: implements every RPC the SC plugin bus exposes as a
// canned-success stub, so `go build ./...` + `go test ./...` pass against
// the template untouched, and so an author can grep for `echo` and
// replace it piece by piece without having to re-derive the file layout
// or the common scaffolding.
//
// What it does NOT do: provision anything real. The resources registered
// below are pure Pulumi ComponentResources — logical URN groupings with
// no cloud side-effects. An author swaps the body of each RPC for the
// real cloud-driven work; the surrounding structure stays.
//
// # Trap-prevention invariants baked in
//
// This template encodes the invariants the five-plugin extraction wave
// paid for (sc-plugin-aws / cloudflare / cloudru / mongodb / yandex,
// 2026-10-18 → 2026-11-01). See TRAPS.md for the fifteen-trap index and
// forge/docs/design/2026-11-01/plugin-extraction-playbook/ for the
// canonical catalogue. The ones already lived here, do not undo them:
//
//   - Every handler uses `runtime.NewContext(stdctx, header, …)` +
//     `defer runtime.Settle(rctx, &resp, &err)`. The template never calls
//     `pctx.Close()` directly — `Settle` drains outstanding
//     RegisterResource goroutines via `go:linkname` into
//     `(*Context).wait`, then closes the SDK context. (Trap 7, cascade
//     recovery 2026-10-27.)
//
//   - Provision returns `runtime.HarvestExports` output as
//     ProvisionResponse.Outputs. Any `rctx.Pulumi.Export(k, out)` call a
//     real plugin adds crosses the wire with the Secret flag preserved.
//     (Traps 10/11, forge-storage + aws harvest 2026-10-30 / 10-31.)
//
//   - The template demonstrates one `pctx.Export(...)` in Provision so an
//     author sees HarvestExports actually populate Outputs. Delete it and
//     consumers that `uses: [echo-bucket]` get `output "..." not found`.
//
//   - `scCoreRegistrarBridge` below is a commented stub for the
//     cross-plugin registrar bridge pattern (D8, PeerInvoke). It ships
//     the mandatory `ctx.Export(unique-key, urnOut)` forcing function
//     inside each bridge method — without it Pulumi dead-strips the
//     orphan ApplyT when callers discard the ResourceOutput. (Trap 15,
//     forge-notifier 2026-11-01.)
//
//   - See `plugin/internal/registrar.go` for the URN-vs-export-key split
//     comment every operator-nameable resource must honour. (Trap 13,
//     forge-notifier mongodb cluster rename 2026-11-01.)
//
// See `docs/docs/guides/plugin-authoring.md` in the api repo for the
// walkthrough: config tier → provisioning tier → tests → release.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	hclog "github.com/hashicorp/go-hclog"
	hplugin "github.com/hashicorp/go-plugin"
	"github.com/pkg/errors"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/simple-container-com/api/pkg/api"
	"github.com/simple-container-com/api/pkg/api/logger"
	pApi "github.com/simple-container-com/api/pkg/clouds/pulumi/api"
	"github.com/simple-container-com/api/pkg/clouds/vm"
	scPlugin "github.com/simple-container-com/api/pkg/plugin"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
	"github.com/simple-container-com/api/pkg/plugin/runtime"

	echoApi "github.com/simple-container-com/sc-plugin-echo/api"
	echoInternal "github.com/simple-container-com/sc-plugin-echo/plugin/internal"
)

// echoPlugin is the gRPC server impl. Fields follow the same shape every
// real plugin uses: a mutex, cached registrar/handler state carried
// between RPCs within one deploy, and a progress Publisher drained by
// LogStream.
type echoPlugin struct {
	scpluginv1.UnimplementedSCCloudPluginServer

	mu        sync.Mutex
	registrar *echoInternal.Registrar

	// progress is the plugin's side of the LogStream RPC. SC core opens
	// one LogStream per plugin after Schema() returns, and this is where
	// per-RPC narration goes. Drop progress and the plugin is silent for
	// the whole of a multi-step RPC chain — operators see nothing until
	// the deploy either succeeds or fails much later.
	progress *scPlugin.Publisher

	// broker is captured on the Impl by SetBroker when hashicorp/go-plugin
	// hands it to GRPCServer. Required for the SCCore callback stream a
	// composite plugin uses (PeerInvoke to another plugin, or
	// ResolveSecret / ResolveCredential against SC core). Nil until the
	// go-plugin lifecycle has run, so handlers that need it must guard.
	// The echo template keeps this field + SetBroker so a derived plugin
	// does not have to re-derive the BrokerAware shape.
	broker *hplugin.GRPCBroker
}

// SetBroker implements scPlugin.BrokerAware. api/pkg/plugin/goplugin.go's
// scCloudPlugin.GRPCServer type-asserts the Impl against this interface
// and calls SetBroker when it exists; without it, scPlugin.NewPeerClient
// returns "nil broker" and PeerInvoke never dials.
func (p *echoPlugin) SetBroker(b *hplugin.GRPCBroker) { p.broker = b }

// Schema is the first RPC SC core calls. The response advertises every
// token this plugin owns, both at config-parse time (so core routes
// `type: X` to this plugin's RPCs) and at runtime (so core knows which
// Pulumi providers the plugin needs on PATH).
//
// Keep this shape in sync with /api/init.go's OwnedTypes list — the two
// must agree. init.go's reflection test catches gaps in the OwnedTypes
// half; a Schema() that omits a type is caught at the next conformance
// run.
func (p *echoPlugin) Schema(_ context.Context, _ *scpluginv1.SchemaRequest) (*scpluginv1.SchemaResponse, error) {
	return &scpluginv1.SchemaResponse{
		PluginVersion:         "echo-0.0.1",
		ProviderTypes:         []string{echoApi.ProviderType},
		RegistrarTypes:        []string{echoApi.RegistrarTypeEchoDns},
		ResourceTypes:         []string{echoApi.ResourceTypeEchoBucket},
		ComputeProcessorTypes: []string{echoApi.ResourceTypeEchoBucket},
		PulumiPlugins:         []*scpluginv1.PulumiPluginRequirement{
			// Empty for echo — a real cloud lists the Pulumi provider
			// plugin names it imports: `{Package: "cloudflare", Version:
			// "6.17.0"}`, `{Package: "yandex", Version: "1.1.0"}`, etc.
			// Core materialises these in the stack's `plugins` block so
			// `pulumi up` finds them. See
			// github.com/pulumi/pulumi-<name>/sdk for versions.
		},
		VmDrivers: []*scpluginv1.VmDriverSpec{
			// A real VM-driving plugin echoes vm.Drivers[DRIVER].ToProto()
			// so core's capability table and the plugin's agree by reading
			// the same source. The echo driver advertises an in-template
			// stub so the URN contract test can exercise VmDriverSpec
			// round-tripping.
			echoVmDriverSpec(),
		},
	}, nil
}

// echoVmDriverSpec returns the VmDriverSpec for the echo-vm driver. In a
// real plugin this would be `vm.Drivers[vm.Driver<Cloud>].ToProto(name)`
// where the Drivers map entry lives in-tree (the capability facts a
// driver reports — see pkg/clouds/vm/capabilities.go for the shape).
//
// Echo doesn't have an in-tree capabilities entry, so this stub builds
// one by hand. An author replaces this with the ToProto call.
func echoVmDriverSpec() *scpluginv1.VmDriverSpec {
	return &scpluginv1.VmDriverSpec{
		Driver:      echoApi.DriverEchoVm,
		Implemented: true,
		Unverified:  "echo driver is a template stub — it does not actually create any VM. Replace with a real cloud's driver.",
		RoadmapItem: "docs/roadmap/items/sc-plugin-architecture.md",
		Capabilities: &scpluginv1.VmCapabilities{
			CloudInit:      false,
			Regionless:     true,
			SecurityGroups: false,
		},
	}
}

// ProvisionRegistrar initialises the plugin's cached DNS registrar handle
// for this deploy. The handler stores the Registrar on the Impl so later
// NewDnsRecord / ProvisionDomainForEndpoint calls reuse it.
//
// The runtime.NewContext + runtime.Settle pair is NON-OPTIONAL: without
// Settle, pending RegisterResource goroutines are dropped on return and
// SC core plans a destroy of every state entry on the next apply (trap
// 7). WithLocalEngineFallback is what keeps a background ctx.Log.Warn
// (e.g. from pulumi-aws's provider) from panicking when the header
// carries no engine address (Addendum P).
func (p *echoPlugin) ProvisionRegistrar(stdctx context.Context, req *scpluginv1.ProvisionRegistrarRequest) (resp *scpluginv1.ProvisionRegistrarResponse, err error) {
	// A real registrar reads the deploying stack's `baseDnsZone` from
	// req.GetBaseDnsZone() for its zone lookup — NOT its own configured
	// zoneName, which is only a default. Getting this wrong is how a
	// child-stack deploy writes a record into the WRONG zone and the
	// delete-replace half removes a live record (incident 2026-10-28,
	// Addendum R).
	stackZone := req.GetBaseDnsZone()
	configZone := strVal(req.GetConfig()["zoneName"])
	p.progress.Info("provision-registrar", fmt.Sprintf("initialising echo registrar; stack prefers %q (config default %q)", stackZone, configZone))

	rctx, err := runtime.NewContext(stdctx, req.GetHeader(), runtime.WithLocalEngineFallback(p.progress))
	if err != nil {
		return nil, err
	}
	defer runtime.Settle(rctx, &resp, &err)

	r, rerr := echoInternal.NewRegistrar(rctx.Pulumi, stackZone, configZone)
	if rerr != nil {
		p.progress.Error("provision-registrar", "init failed: "+rerr.Error())
		return nil, errors.Wrap(rerr, "init echo registrar")
	}
	p.mu.Lock()
	p.registrar = r
	p.mu.Unlock()
	p.progress.Info("provision-registrar", fmt.Sprintf("ready; main domain is %q", r.MainDomain()))
	return &scpluginv1.ProvisionRegistrarResponse{MainDomain: r.MainDomain()}, nil
}

// NewDnsRecord registers one DNS record against the plugin's cached
// registrar. A composite plugin that proxies DNS to a *peer* plugin uses
// the scCoreRegistrarBridge pattern below instead of calling the local
// registrar — see that stub for the ctx.Export-as-forcing-function
// pattern (trap 15).
func (p *echoPlugin) NewDnsRecord(stdctx context.Context, req *scpluginv1.NewDnsRecordRequest) (resp *scpluginv1.NewDnsRecordResponse, err error) {
	rctx, err := runtime.NewContext(stdctx, req.GetHeader(), runtime.WithLocalEngineFallback(p.progress))
	if err != nil {
		return nil, err
	}
	defer runtime.Settle(rctx, &resp, &err)

	p.mu.Lock()
	r := p.registrar
	p.mu.Unlock()
	if r == nil {
		return nil, errors.New("NewDnsRecord called before ProvisionRegistrar")
	}
	p.progress.Info("dns-record", fmt.Sprintf("creating %s record %q → %q", req.GetRecordType(), req.GetRecordName(), req.GetValueResolved()))
	urnStr, rerr := r.NewRecord(rctx.Pulumi, req.GetRecordName(), req.GetRecordType(), req.GetValueResolved())
	if rerr != nil {
		p.progress.Error("dns-record", fmt.Sprintf("record %q failed: %v", req.GetRecordName(), rerr))
		return nil, rerr
	}
	p.progress.Info("dns-record", fmt.Sprintf("record %q registered (urn=%s)", req.GetRecordName(), urnStr))
	return &scpluginv1.NewDnsRecordResponse{Urn: urnStr}, nil
}

// Provision is the catch-all RPC for a `type: X` resource whose plugin
// advertises X in ResourceTypes. The echo stub registers a logical
// ComponentResource, exports one placeholder output so an operator sees
// HarvestExports fire, and returns the harvested Outputs on the
// response.
//
// A real cloud resource constructs provider resources from
// pulumi-<cloud>, exports every value a `uses:` consumer would want (ARN,
// endpoint, connection string, access key — the last one secret-flagged
// via sdk.ToSecret), and returns the harvested map so SC core can
// re-export onto the parent stack.
//
// Lose the `Outputs:` field on the response and every `uses:` consumer of
// this resource type gets `output "<name>" not found on stack <parent>`
// (trap 10). Lose the Secret flag inside HarvestExports and secret
// outputs land plaintext in the parent stack root (trap 11 — the api
// runtime handles that for you, do not route around it).
func (p *echoPlugin) Provision(stdctx context.Context, req *scpluginv1.ProvisionRequest) (resp *scpluginv1.ProvisionResponse, err error) {
	rctx, err := runtime.NewContext(stdctx, req.GetHeader(), runtime.WithLocalEngineFallback(p.progress))
	if err != nil {
		return nil, err
	}
	defer runtime.Settle(rctx, &resp, &err)

	p.progress.Info("provision", fmt.Sprintf("provisioning echo resource %q (type %q)", req.GetResourceName(), req.GetResourceType()))
	urnStr, perr := echoInternal.ProvisionEcho(rctx.Pulumi, req.GetResourceType(), req.GetResourceName())
	if perr != nil {
		p.progress.Error("provision", perr.Error())
		return nil, perr
	}

	// Export at least one output so HarvestExports has something to carry
	// across the wire. A real plugin exports per-resource outputs under
	// unique keys — see `plugin/internal/registrar.go`'s doc on the
	// URN-vs-export-key split (trap 13): the Pulumi URN name is anchored
	// to stack identity, but export keys should include the
	// operator-visible name a consumer would want to reference. The
	// placeholder below uses the operator's resource name; swap for your
	// cloud's real export shape.
	rctx.Pulumi.Export(fmt.Sprintf("%s-echo-urn", req.GetResourceName()), sdk.String(urnStr))

	outputs, herr := runtime.HarvestExports(stdctx, rctx.Pulumi)
	if herr != nil {
		return nil, errors.Wrap(herr, "harvest provision exports")
	}

	p.progress.Info("provision", fmt.Sprintf("resource %q registered (urn=%s, %d output(s))", req.GetResourceName(), urnStr, len(outputs)))
	return &scpluginv1.ProvisionResponse{Urn: urnStr, Outputs: outputs}, nil
}

// ComputeProcess is called by SC core for each dependency resource a
// consuming stack declares. The echo template demonstrates the
// canonical shape every real plugin's ComputeProcess body should
// wear: open an sdk.Context, decode the parent + input helpers, hand
// a ComputeProcessor to runtime.RunComputeProcess, and let the shared
// library own the drain + marshal sequence.
//
// Why RunComputeProcess, not a hand-rolled collector:
//
//   - The shared runtime.Collector captures sdk.Output chains passed
//     via AddOutput and the drain blocks on them BEFORE marshalling
//     the response. A per-plugin reimplementation that forgot to
//     capture (AddOutput as a no-op) silently discards every
//     ApplyT-wrapped env mutation — trap 16, live-caught 2026-11-02
//     on sc-plugin-aws's S3 processor (agent-native-pdf-rendering
//     f4355798). The shared library's drain is package-private, so a
//     plugin author CANNOT forget by construction.
//
//   - runtime.ParentInfoFromRequest decodes req.ParentInfoJson (JSON of
//     pApi.ParentInfo), the only wire path for parent-stack reference
//     metadata. The decode uses encoding/json; a yaml.Unmarshal would
//     silently land every field empty.
//
//   - runtime.ResourceInputForComputeProcess builds the api.ResourceInput
//     a real driver reads, and swaps the child env for the parent's
//     when ParentInfo.ParentEnv is set — so lookup keys under isolated
//     stacks resolve against `<stack>--staging--<key>` (parent) rather
//     than `<stack>--108b3f--<key>` (child). (Trap 5, forge-storage
//     isolated deploy 2026-11-01.)
//
// What is NOT demonstrated here (not representable on the bus — see
// pkg/plugin/compute.go doc): sdk.Resource dependencies via
// AddDependency, pre/post-processors. Those stay in-plugin and get
// applied against the plugin's own sdk.Context before Build.
func (p *echoPlugin) ComputeProcess(stdctx context.Context, req *scpluginv1.ComputeProcessRequest) (resp *scpluginv1.ComputeProcessResponse, err error) {
	rctx, err := runtime.NewContext(stdctx, req.GetHeader(), runtime.WithLocalEngineFallback(p.progress))
	if err != nil {
		return nil, err
	}
	defer runtime.Settle(rctx, &resp, &err)
	p.progress.Info("compute-process", fmt.Sprintf("resource=%q type=%q", req.GetResourceName(), req.GetResourceType()))

	parent, err := runtime.ParentInfoFromRequest(req)
	if err != nil {
		p.progress.Error("compute-process", "parent decode: "+err.Error())
		return nil, err
	}
	if parent != nil && parent.ParentEnv != "" {
		p.progress.Info("compute-process", fmt.Sprintf("parent env %q (child env %q)", parent.ParentEnv, req.GetHeader().GetEnvironment()))
	}
	input := runtime.ResourceInputForComputeProcess(req, parent, map[string]any{})

	// Drain + marshal are OWNED by RunComputeProcess. A plugin author
	// cannot forget to await captured outputs — toResponse and
	// awaitOutputs are package-private in api/pkg/plugin/runtime.
	resp, _, err = runtime.RunComputeProcess(
		stdctx, rctx,
		api.Stack{Name: req.GetHeader().GetStackName()}, input,
		pApi.ProvisionParams{Log: logger.New(), ParentStack: parent},
		echoInternal.EchoComputeProcessor,
	)
	if err != nil {
		p.progress.Error("compute-process", err.Error())
		return nil, err
	}
	p.progress.Info("compute-process", fmt.Sprintf("emitted %d env var(s) + %d tpl extension(s)", len(resp.GetEnvVars()), len(resp.GetTplExtensions())))
	return resp, nil
}

// PreDestroy is called by SC core before Pulumi's destroy phase, with
// stack outputs + the per-env client descriptor + destroy params all
// packed onto the wire (Phase 1e v2). The echo stub narrates what
// crossed; a real plugin runs cloud-specific cleanup (mongodb drops
// databases named in service-user outputs, etc.).
//
// Deliberately NOT returning a gRPC error even when the hook decides
// not to run — SC core's sidecar is fire-and-forget on the response,
// so an error here would only show up in the plugin's own log and
// never block the destroy.
// ConvertStackConfig is the plugin-side half of SC core's A2 path for
// turning a merged StackConfig* into the plugin's typed Input. SC
// core only dials this when the template type is plugin-owned
// (api.IsPluginOwnedType returns a binding whose PluginName matches
// this plugin). The echo template demonstrates the canonical shape:
// read the InputValue tree, run the plugin's existing /api-side
// ToXxxConfig closure (or an inline conversion), and marshal the
// typed Input back as an InputValue tree.
//
// For a real plugin — sc-plugin-aws / cloudflare / yandex — the body
// delegates to the previously-SC-core-registered converter closures
// (ToStaticSiteConfig, ToPagesConfig, …), reads base_dns_zone / uses
// / depends_on_resources off the resulting typed Input, and returns.
// The ONLY structural change on the plugin side is implementing this
// RPC.
func (p *echoPlugin) ConvertStackConfig(_ context.Context, req *scpluginv1.ConvertStackConfigRequest) (*scpluginv1.ConvertStackConfigResponse, error) {
	p.progress.Info("convert-stack-config", fmt.Sprintf("template=%q stack=%q dir=%q", req.GetTemplateType(), req.GetStackName(), req.GetStackDir()))

	// Minimal response that exercises every field a real plugin
	// would populate. A derived plugin replaces this with its real
	// ToXxxConfig closure's output marshalled via scPlugin.PlainToInputs.
	return &scpluginv1.ConvertStackConfigResponse{
		Input: scPlugin.PlainToInputs(map[string]any{
			"templateType": req.GetTemplateType(),
			"stackName":    req.GetStackName(),
			"stackDir":     req.GetStackDir(),
		}),
		BaseDnsZone: "echo.example.com",
		Uses:        []string{"echo-bucket"},
		// depends_on_resources: zero entries — the echo stub does not
		// synthesise cross-stack dependencies. A real plugin json-marshals
		// each api.StackConfigDependencyResource into the repeated bytes
		// field.
	}, nil
}

func (p *echoPlugin) PreDestroy(ctx context.Context, req *scpluginv1.PreDestroyRequest) (*scpluginv1.PreDestroyResponse, error) {
	if err := echoInternal.PreDestroy(ctx, p.progress, req); err != nil {
		p.progress.Error("pre-destroy", err.Error())
	}
	return &scpluginv1.PreDestroyResponse{}, nil
}

// VmHosts converges a VM resource whose `provider:` names a driver this
// plugin advertises (echo-vm). The echo stub returns one byo-shaped
// host with a TEST-NET address — enough to exercise the plugin bus's
// VmHostsResponse shape without reaching a real cloud.
//
// A real VM-driving plugin registers cloud resources (instance, floating
// IP, deploy key), parses their outputs via plugin.OutputRefTo /
// AddressOutput / PrivateKeyOf, and composes the VmHostBuilder chain.
// See api/pkg/plugin/vmhosts.go for the builder surface.
func (p *echoPlugin) VmHosts(stdctx context.Context, req *scpluginv1.VmHostsRequest) (resp *scpluginv1.VmHostsResponse, err error) {
	p.progress.Info("vm-hosts", fmt.Sprintf("provisioning echo vm %q (driver=%s)", req.GetResourceName(), req.GetDriver()))

	rctx, err := runtime.NewContext(stdctx, req.GetHeader(), runtime.WithLocalEngineFallback(p.progress))
	if err != nil {
		return nil, err
	}
	defer runtime.Settle(rctx, &resp, &err)

	r, verr := echoInternal.VmHosts(rctx.Pulumi, req.GetResourceName())
	if verr != nil {
		p.progress.Error("vm-hosts", fmt.Sprintf("vm %q failed: %v", req.GetResourceName(), verr))
		return nil, verr
	}
	p.progress.Info("vm-hosts", fmt.Sprintf("vm %q ready, %d host(s)", req.GetResourceName(), len(r.GetHosts())))
	return r, nil
}

// LogStream drains the plugin's progress Publisher onto the gRPC stream.
// SC core opens one of these per plugin after Schema() returns. Ends when
// the stream context cancels (SC core teardown) or Serve stops accepting.
func (p *echoPlugin) LogStream(_ *scpluginv1.LogStreamRequest, stream scpluginv1.SCCloudPlugin_LogStreamServer) error {
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

// strVal reads a string input from the plugin bus's typed input map.
// Keep for simple field reads; for a config with nested maps or lists
// use runtime.DecodeInputs[T any](req.GetInputs(), &target) instead —
// it round-trips through scPlugin.InputsToPlain + yaml, which the
// per-plugin scalar-only variants aws/mongodb/cloudru carried pre-cascade
// lost on nested fields (trap 14).
func strVal(v *scpluginv1.InputValue) string {
	if v == nil {
		return ""
	}
	return v.GetStringValue()
}

// scCoreRegistrarBridge is a COMMENTED STUB of the cross-plugin registrar
// bridge pattern a composite plugin needs. The aws plugin delegates
// NewDnsRecord / NewOverrideHeaderRule / ProvisionDomainForEndpoint to
// the operator-configured registrar (typically `cloudflare`) via this
// shape. Enable by uncommenting and wiring into the handler methods.
//
// # Trap 15 — the forcing-function pattern
//
// Every bridge method returns a `*api.ResourceOutput{Ref: urnOut}` where
// `urnOut` is an ApplyT chain containing the PeerInvoke RPC. Callers in
// aws / Lambda / ECS templates discard the ResourceOutput — they only
// check the error. With no downstream consumer of `urnOut`, Pulumi SDK
// DEAD-STRIPS the orphan Output chain and the PeerInvoke never fires.
// The deploy reads green with the DNS record silently never registered.
// Live-caught on forge-notifier run 41339900839 (2026-11-01); fixed in
// sc-plugin-aws v2026.11.01-e377550.
//
// The fix is a `ctx.Export(unique-key, urnOut)` inside each bridge
// method, as a forcing function. Keys must be unique per
// (stack, resource/endpoint/rule) so stacks with multiple records don't
// collide. Any ctx.Export works — the forcing function is the stack-
// output evaluation.
//
/*
import (
	hplugin "github.com/hashicorp/go-plugin"
	pApi "github.com/simple-container-com/api/pkg/clouds/pulumi/api"
)

type scCoreRegistrarBridge struct {
	peer       scPlugin.PeerClient
	pluginName string
}

func (p *echoPlugin) newRegistrarBridge(stdctx context.Context, h *scpluginv1.RequestHeader, peerPluginName string) (*scCoreRegistrarBridge, error) {
	peer, err := scPlugin.NewPeerClient(stdctx, p.broker, h)
	if err != nil {
		return nil, errors.Wrap(err, "dial SCCore for peer bridge")
	}
	return &scCoreRegistrarBridge{peer: peer, pluginName: peerPluginName}, nil
}

func (r *scCoreRegistrarBridge) NewDnsRecord(ctx *sdk.Context, stack api.StackParams, recordName, recordType string, target sdk.StringOutput) (*api.ResourceOutput, error) {
	urnOut := target.ApplyT(func(resolvedTarget string) (string, error) {
		resp, err := r.peer.NewDnsRecord(ctx.Context(), r.pluginName, &scpluginv1.NewDnsRecordRequest{
			Header:        headerFromStack(ctx, stack),
			RecordName:    recordName,
			RecordType:    recordType,
			ValueResolved: resolvedTarget,
		})
		if err != nil { return "", err }
		return resp.GetUrn(), nil
	}).(sdk.StringOutput)

	// TRAP 15 FORCING FUNCTION — DO NOT REMOVE.
	// Without this, callers that discard the ResourceOutput let Pulumi
	// dead-strip the ApplyT and the PeerInvoke RPC never fires. The
	// deploy reads green with no DNS record created.
	ctx.Export(fmt.Sprintf("%s-%s-bridge-record-urn", stack.StackName, recordName), urnOut)

	return &api.ResourceOutput{Ref: urnOut}, nil
}
*/

// Reference used only to anchor the vm package import — a real plugin
// uses vm.Drivers[name].ToProto in Schema(). Prevents an unused-import
// warning while the stub is still a stub.
var _ = vm.ResourceTypeVm

// Reference used only to anchor the api package import for operators who
// grep. A real plugin's ProvisionRegistrar builds an api.RegistrarDescriptor,
// NewDnsRecord an api.DnsRecord, etc.
var _ = api.Credentials{}

func main() {
	impl := &echoPlugin{progress: scPlugin.NewPublisher(0)}
	scPlugin.Serve(scPlugin.ServerConfig{
		Impl: impl,
		Logger: hclog.New(&hclog.LoggerOptions{
			Name:   "sc-plugin-echo",
			Level:  hclog.Warn,
			Output: os.Stderr,
		}),
	})
}
