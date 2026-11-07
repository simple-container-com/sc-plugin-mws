// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/simple-container-com/api/pkg/api"
	"github.com/simple-container-com/api/pkg/clouds/vm"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
)

// Trap 21 — URN snapshot pinning. The Pulumi state's URN embeds the resource
// NAMES below, so they are a wire contract with every stack that has ever
// deployed: a rename is a replace+create, which on this driver means destroying a
// machine and its boot disk. `mws-vm` has no live stacks yet, which makes this the
// cheapest moment to pin the names and the only moment at which changing one is
// free.
//
// It is not only a naming test. Because it drives the real handler end to end
// under mocks, it also proves that the whole output graph RESOLVES — cloud-init
// renders from three tls outputs, both addresses' `status.ipAddress` awaits
// complete, and the NIC's two `ref` fields resolve — which is the half that a
// hand-written spec assertion cannot reach.
//
// Why `pulumi.NewContext` + `RunInfo.Mocks` and NOT the shorter
// `pulumi.RunErr(..., WithMocks)`, copied verbatim from sc-plugin-gcp's twin:
// `RunErr` ends in the context's `wait()`, which drains the engine's RPC queue —
// any registration stranded on an unresolved input is **silently completed by the
// harness itself on the test's behalf**. That hid the whole 2026-10-28
// await-returns bug class behind a green suite. Opening the context by hand lands
// the assertion on the handler's RETURN instead of the harness's drain: a handler
// that returned before awaiting a registration would not have called the mock, and
// the URN assertion fails — which is the shape we want.

type captureMocks struct {
	mu       sync.Mutex
	captured []pulumi.MockResourceArgs
}

// NewResource echoes the inputs back and ADDS the computed properties this
// driver reads.
//
// Not optional padding: every injected field is one the handler blocks on. MWS's
// bridged schema marks them optional, `requireString` turns an absent one into an
// error, and an errored output never reaches RegisterResource — so without these
// the firewall rules and the host address would simply never register and the
// failure would read as a naming mismatch rather than a missing mock. The values
// are shaped like the real ones (an IP is an IP) because the handler does string
// work on them: it appends `/32` to the internal address.
func (m *captureMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.captured = append(m.captured, args)
	m.mu.Unlock()

	outs := resource.PropertyMap{}
	for k, v := range args.Inputs {
		outs[k] = v
	}

	metadataWithID := func(id string) resource.PropertyValue {
		return resource.NewObjectProperty(resource.PropertyMap{
			"id": resource.NewStringProperty(id),
		})
	}
	statusWithIP := func(ip string) resource.PropertyValue {
		return resource.NewObjectProperty(resource.PropertyMap{
			"ipAddress": resource.NewStringProperty(ip),
		})
	}

	switch args.TypeToken {
	case "tls:index/privateKey:PrivateKey":
		// Shaped like real openssh output. RenderCloudInit refuses an empty deploy
		// key and refuses exactly one half of a host key, so all three are needed.
		outs["publicKeyOpenssh"] = resource.NewStringProperty("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI" + args.Name + "\n")
		outs["privateKeyOpenssh"] = resource.NewStringProperty(
			"-----BEGIN OPENSSH PRIVATE KEY-----\n" + args.Name + "\n-----END OPENSSH PRIVATE KEY-----\n",
		)
	case "mws:vpc/subnet:Subnet":
		outs["metadata"] = metadataWithID("subnet-id")
	case "mws:vpc/address:Address":
		outs["metadata"] = metadataWithID("internal-address-id")
		outs["status"] = statusWithIP("192.168.0.10")
	case "mws:vpc/externalAddress:ExternalAddress":
		outs["metadata"] = metadataWithID("external-address-id")
		outs["status"] = statusWithIP("203.0.113.7")
	case "mws:vpc/network:Network":
		outs["metadata"] = metadataWithID("network-id")
	}

	return args.Name + "-mock-id", outs, nil
}

func (m *captureMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func (m *captureMocks) namesByType(typeToken string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, a := range m.captured {
		if a.TypeToken == typeToken {
			out = append(out, a.Name)
		}
	}
	sort.Strings(out)
	return out
}

// inputsOf returns the inputs of the single resource of a type, failing the
// assertion if there is not exactly one.
func (m *captureMocks) inputsOf(typeToken string) resource.PropertyMap {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []resource.PropertyMap
	for _, a := range m.captured {
		if a.TypeToken == typeToken {
			found = append(found, a.Inputs)
		}
	}
	Expect(found).To(HaveLen(1), "expected exactly one %s", typeToken)
	return found[0]
}

// withTrapFreeContext opens a pulumi.Context with mocks via the exported
// RunInfo.Mocks field. Close() is a no-op under mocks (every real RPC is
// intercepted), so the handler's return is what the assertion lands on.
func withTrapFreeContext(t *testing.T, mocks *captureMocks, fn func(*pulumi.Context) error) {
	t.Helper()
	pctx, err := pulumi.NewContext(context.Background(), pulumi.RunInfo{
		Project: "test-project",
		Stack:   "test-stack",
		Mocks:   mocks,
	})
	Expect(err).ToNot(HaveOccurred())
	defer pctx.Close()

	Expect(fn(pctx)).To(Succeed())
}

// testAccountJSON is the `credentials:` blob, on the TOKEN branch rather than the
// authorized-key one. Deliberate: the key branch would make Provider() write a
// real 0600 file into the OS temp dir on every test run, and these tests assert
// names, not credential materialisation (credential_test.go covers that).
func testAccountJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"token":   "t1.fake-iam-token-for-tests",
		"project": "test-project",
	})
	Expect(err).ToNot(HaveOccurred())
	return string(b)
}

// testRequest builds a VmHostsRequest the way SC core would, with one public port
// and one internal one so both firewall-source branches are exercised.
func testRequest(t *testing.T, mutate func(cfg *vm.VmConfig)) *scpluginv1.VmHostsRequest {
	t.Helper()

	cfg := vm.VmConfig{
		Driver:      vm.DriverMws,
		Region:      "ru-central1",
		Zones:       []string{"ru-central1-a"},
		Credentials: api.Credentials{Credentials: testAccountJSON(t)},
	}
	if mutate != nil {
		mutate(&cfg)
	}

	cfgJSON, err := json.Marshal(cfg)
	Expect(err).ToNot(HaveOccurred())

	return &scpluginv1.VmHostsRequest{
		ResourceName: "web",
		Driver:       vm.DriverMws,
		ConfigJson:   cfgJSON,
		Header:       &scpluginv1.RequestHeader{Environment: "staging"},
		Ports: []*scpluginv1.VmPortRange{
			{Label: "ssh", Protocol: "tcp", From: 22, To: 22},
			{Label: "kubelet", Protocol: "tcp", From: 10250, To: 10250, Internal: true},
		},
	}
}

// TestURNContract_VmHosts pins every resource name the driver registers.
//
// Read the suffixes as the contract: `-mws` is the provider, `-deploy-key` and
// `-host-key` the two tls pairs, `-net`/`-subnet`/`-addr`/`-extaddr` the network
// path, the bare name the machine, and `-fw-<proto>-<port>` one rule per port.
// All are `toResName(...)`-wrapped, so each carries the `--staging` environment
// suffix — dropping that would collide two environments of one stack onto one URN.
func TestURNContract_VmHosts(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		resp, err := VmHosts(ctx, testRequest(t, nil))
		if err != nil {
			return err
		}
		Expect(resp.GetHosts()).To(HaveLen(1))
		return nil
	})

	Expect(m.namesByType("pulumi:providers:mws")).To(Equal([]string{"web-mws--staging"}))
	Expect(m.namesByType("tls:index/privateKey:PrivateKey")).To(Equal([]string{
		"web-deploy-key--staging", "web-host-key--staging",
	}))
	Expect(m.namesByType("mws:vpc/network:Network")).To(Equal([]string{"web-net--staging"}))
	Expect(m.namesByType("mws:vpc/subnet:Subnet")).To(Equal([]string{"web-subnet--staging"}))
	Expect(m.namesByType("mws:vpc/address:Address")).To(Equal([]string{"web-addr--staging"}))
	Expect(m.namesByType("mws:vpc/externalAddress:ExternalAddress")).To(Equal([]string{"web-ext-addr--staging"}))
	// `-host-0`, not the bare resource name: the machine is named off the HOST,
	// which is what leaves room for a second one without renaming the first. A vm
	// resource is one host today, and a driver that named the machine `web` would
	// have to replace it the day `web-host-1` appears.
	Expect(m.namesByType("mws:compute/virtualMachine:VirtualMachine")).To(Equal([]string{"web-host-0--staging"}))

	// One rule per port, named after the PORT and not the loop index — see
	// portSlug's doc for why an index is a port-closing rename.
	Expect(m.namesByType("mws:vpc/firewallRule:FirewallRule")).To(Equal([]string{
		"web-fw-tcp-10250--staging", "web-fw-tcp-22--staging",
	}))

	// No separate compute.Disk: the boot disk is declared INLINE in the machine's
	// `storage.disk.spec`. A standalone disk resource would be independently
	// destroyable and SC's model names nothing that would clean it up, so deleting
	// the vm would strand a paid volume.
	Expect(m.namesByType("mws:compute/disk:Disk")).To(BeEmpty())
}

// TestURNContract_FirewallSourceHonoursInternal is the regression test for a bug
// that was live in this file: `PortRange.Internal` was decoded and then dropped,
// so every rule got 0.0.0.0/0. On a multi-node cluster that is the kubelet port
// open to the internet.
//
// The destination is asserted alongside it because it is the matching trap from
// the other direction: MWS evaluates the firewall AFTER the one-to-one NAT
// translation, so the destination must be the INTERNAL address. A rule written
// against the external one is accepted, looks right in the console, and matches
// nothing.
func TestURNContract_FirewallSourceHonoursInternal(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		_, err := VmHosts(ctx, testRequest(t, nil))
		return err
	})

	sources := map[string]string{}
	destinations := map[string]string{}
	m.mu.Lock()
	for _, a := range m.captured {
		if a.TypeToken != "mws:vpc/firewallRule:FirewallRule" {
			continue
		}
		cidrOf := func(key string) string {
			spec := a.Inputs[resource.PropertyKey(key)].ObjectValue()["spec"].ObjectValue()
			cidrs := spec["cidrs"].ArrayValue()
			Expect(cidrs).To(HaveLen(1))
			return cidrs[0].StringValue()
		}
		sources[a.Name] = cidrOf("source")
		destinations[a.Name] = cidrOf("destination")
	}
	m.mu.Unlock()

	Expect(sources).To(HaveLen(2))
	Expect(sources["web-fw-tcp-22--staging"]).To(Equal("0.0.0.0/0"),
		"a public port's source is the internet")
	Expect(sources["web-fw-tcp-10250--staging"]).To(Equal(defaultSubnetCIDR),
		"an `internal: true` port must be narrowed to the subnet, never left at 0.0.0.0/0 — see sourceCIDRFor")

	for name, dest := range destinations {
		Expect(dest).To(Equal("192.168.0.10/32"),
			"rule %s must target the INTERNAL address; MWS evaluates the firewall after NAT", name)
	}
}

// TestVmHostsRendersCloudInitIntoUserData — the payload the machine actually
// boots with. Three separate things are asserted because each fails differently
// and silently:
//
//   - the deploy key's PUBLIC half is present: without it the machine boots with
//     no authorized_keys and SC can never connect;
//   - the readiness marker is present: core waits for `touch <marker>` and a
//     payload without it looks like an infinitely slow boot;
//   - the host PRIVATE key is present AND the whole value is marked SECRET.
//     An unmarked resource input is rendered verbatim in Pulumi's diff and stored
//     unencrypted in the checkpoint, so this input leaking the host identity is a
//     one-missing-call mistake with no visible symptom.
func TestVmHostsRendersCloudInitIntoUserData(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		_, err := VmHosts(ctx, testRequest(t, nil))
		return err
	})

	osBlock := m.inputsOf("mws:compute/virtualMachine:VirtualMachine")["os"]

	// Secret-ness is checked on the way in, before unwrapping: the mock receives
	// the input exactly as the engine would.
	Expect(osBlock.ContainsSecrets()).To(BeTrue(),
		"user-data carries the host PRIVATE key; an unmarked input is printed in Pulumi's diff and stored unencrypted in the checkpoint")

	userData := resolveSecrets(osBlock).ObjectValue()["metadata"].
		ObjectValue()["attributes"].ObjectValue()["user-data"].StringValue()

	Expect(userData).To(ContainSubstring("ssh-ed25519"))
	Expect(userData).To(ContainSubstring("web-deploy-key--staging"),
		"the DEPLOY key's public half must be authorised on the machine")
	Expect(userData).To(ContainSubstring("web-host-key--staging"),
		"the HOST key must be seeded, which is what makes the pinned host key verifiable")
	Expect(userData).To(ContainSubstring(vm.CloudInitDoneMarker),
		"core waits for this marker; a payload without it is indistinguishable from a hung boot")
}

// resolveSecrets unwraps secret and output property values so an assertion can
// read through them. Written here rather than reached for from a helper package
// because `resource.PropertyValue` nests secrets at any depth and the test needs
// the leaf string.
func resolveSecrets(v resource.PropertyValue) resource.PropertyValue {
	switch {
	case v.IsSecret():
		return resolveSecrets(v.SecretValue().Element)
	case v.IsOutput():
		return resolveSecrets(v.OutputValue().Element)
	case v.IsObject():
		out := resource.PropertyMap{}
		for k, item := range v.ObjectValue() {
			out[k] = resolveSecrets(item)
		}
		return resource.NewObjectProperty(out)
	case v.IsArray():
		arr := make([]resource.PropertyValue, 0, len(v.ArrayValue()))
		for _, item := range v.ArrayValue() {
			arr = append(arr, resolveSecrets(item))
		}
		return resource.NewArrayProperty(arr)
	default:
		return v
	}
}

// Trap 19 — a plugin must refuse a bad config ITSELF, before registering
// anything.
//
// This is not defence in depth, it is the only defence: core's decode of a
// plugin-owned auth block is OPAQUE (`stackread.registerSchemaTypes` installs a
// pass-through decoder), so core performs NO parse-time validation of the auth
// tree. Every refusal an operator ever sees for an MWS credential comes from
// `api.ConvertAuth` + `AccountConfig.Validate()` in this handler.
//
// Each case asserts the error is actionable AND that nothing was registered: a
// handler that refuses on the fifth resource leaves a half-built network in the
// operator's project, which is worse than either succeeding or failing cleanly.
func TestVmHostsRefusesBadConfigBeforeRegisteringAnything(t *testing.T) {
	RegisterTestingT(t)

	cases := []struct {
		name     string
		mutate   func(cfg *vm.VmConfig)
		contains string
	}{
		{
			name:     "no credentials at all",
			mutate:   func(cfg *vm.VmConfig) { cfg.Credentials = api.Credentials{} },
			contains: "is required for auth type",
		},
		{
			name: "credentials with no project",
			mutate: func(cfg *vm.VmConfig) {
				cfg.Credentials = api.Credentials{Credentials: `{"token":"t1.x"}`}
			},
			contains: "`project` is required",
		},
		{
			name: "both auth shapes at once",
			mutate: func(cfg *vm.VmConfig) {
				cfg.Credentials = api.Credentials{
					Credentials: `{"token":"t1.x","serviceAccountAuthorizedKey":"{}","project":"p"}`,
				}
			},
			contains: "mutually exclusive",
		},
		{
			// A zone outside the table must be refused here, not by the cloud: an
			// unknown zone reaches MWS as a provider input and comes back as a 4xx
			// naming neither the field nor the legal values.
			name:     "unknown zone",
			mutate:   func(cfg *vm.VmConfig) { cfg.Zones = []string{"eu-west-1a"} },
			contains: "ru-central1-a",
		},
		{
			name: "unknown size",
			mutate: func(cfg *vm.VmConfig) {
				cfg.Size = &vm.VmSize{Cores: 7, Memory: "13Gi"}
			},
			contains: "gen",
		},
		{
			// The pinned-network path is refused rather than half-honoured — see
			// VmHosts' doc. The message must name `provider: byo` as the way out.
			name:     "pinned network",
			mutate:   func(cfg *vm.VmConfig) { cfg.Network = &vm.VmNetwork{ID: "existing"} },
			contains: vm.DriverByo,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			RegisterTestingT(t)

			m := &captureMocks{}
			pctx, err := pulumi.NewContext(context.Background(), pulumi.RunInfo{
				Project: "test-project",
				Stack:   "test-stack",
				Mocks:   m,
			})
			Expect(err).ToNot(HaveOccurred())
			defer pctx.Close()

			_, err = VmHosts(pctx, testRequest(t, tc.mutate))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(tc.contains))
			Expect(err.Error()).To(ContainSubstring("web"),
				"the refusal must name the vm resource; an operator with several cannot act on an anonymous one")

			m.mu.Lock()
			registered := len(m.captured)
			m.mu.Unlock()
			Expect(registered).To(BeZero(),
				"refused after registering %d resource(s) — a partial deploy leaves orphans in the operator's project", registered)
		})
	}
}
