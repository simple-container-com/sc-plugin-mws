// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"context"
	"sort"
	"sync"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Why this test exists, carried over verbatim from sc-plugin-cloudflare's
// in-tree twin: the Pulumi state's URN embeds the resource names below.
// SC's plugin extraction promise is that no pre-split stack will
// replace+create on first plugin-era `sc deploy` — which only holds if
// the plugin reproduces its predecessor's resource names byte-for-byte.
// If this file ever fails, any live stack running the previous plugin
// version will destroy real resources on upgrade. See
// 04-no-destroy-invariant.md.
//
// Why it uses `pulumi.NewContext` + `RunInfo.Mocks` instead of the
// shorter `pulumi.RunErr(..., WithMocks)`: `RunErr` ends in the
// context's `wait()`, which drains the engine's RPC queue — any
// registration stranded on an unresolved input is **silently completed
// by the harness itself on the test's behalf**. That hid the whole
// 2026-10-28 await-returns bug class behind a green suite (Addendum R
// §R3 of 04-no-destroy-invariant.md). Opening the context by hand lands
// the assertion on the handler's RETURN rather than the harness's drain:
// if a handler returned before awaiting a registration, the mock would
// not have been called and the URN assertion would fail — exactly the
// shape we want. Every plugin's test suite must copy this pattern, not
// the RunErr one.

type captureMocks struct {
	mu       sync.Mutex
	captured []pulumi.MockResourceArgs
}

func (m *captureMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.captured = append(m.captured, args)
	return args.Name + "-mock-id", args.Inputs, nil
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

// withTrapFreeContext opens a pulumi.Context with mocks via the exported
// RunInfo.Mocks field. Close() is a no-op under mocks (every real-world
// RPC is intercepted by the mock), so the handler's return is what the
// assertion lands on.
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

// TestURNContract_RegistrarRecord — the echo DNS registrar registers one
// resource per record, named `<record>-<type>`. This asserts the contract
// a real plugin's registrar must preserve byte-for-byte.
func TestURNContract_RegistrarRecord(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		r, err := NewRegistrar(ctx, "example.com", "example.com")
		if err != nil {
			return err
		}
		_, err = r.NewRecord(ctx, "www", "A", "192.0.2.1")
		return err
	})

	Expect(m.namesByType("echo:dns:Record")).To(Equal([]string{"www-A"}),
		"echo registrar record name must be `<name>-<type>`. Changing this invalidates every pre-existing state file.")
}

// TestURNContract_ProvisionEcho — the catch-all Provision RPC registers a
// resource named with the operator's resource name (no suffix). Preserved
// so SC's state URNs survive the next template bump.
func TestURNContract_ProvisionEcho(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		_, err := ProvisionEcho(ctx, "echo-bucket", "my-bucket")
		return err
	})

	Expect(m.namesByType("echo:index:echo-bucket")).To(Equal([]string{"my-bucket"}),
		"Provision resource name must be the operator's `name:` verbatim. Changing this invalidates existing state.")
}

// TestURNContract_NewRegistrarRefusesEmptyZone — a registrar whose
// neither configZone nor baseDnsZone is set must refuse at init rather
// than invent a zone. Fast-fail that locks the zone-vs-baseDnsZone
// contract: both can be empty only when the operator genuinely declared
// no DNS at all, which is a misconfiguration we flag at parse time.
func TestURNContract_NewRegistrarRefusesEmptyZone(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		_, err := NewRegistrar(ctx, "", "")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("baseDnsZone"))
		Expect(err.Error()).To(ContainSubstring("zoneName"))
		return nil
	})
}

// TestURNContract_StackZoneWins — the deploying stack's `baseDnsZone`
// wins over the config's `zoneName` default. This is the contract that,
// when broken, caused the 2026-10-28 incident: a child-stack deploy
// writing a record into the wrong zone and the delete-replace half
// removing a live record. Addendum R, run 40638575178.
func TestURNContract_StackZoneWins(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		r, err := NewRegistrar(ctx, "stack.example.com", "configured-default.example.com")
		if err != nil {
			return err
		}
		Expect(r.MainDomain()).To(Equal("stack.example.com"),
			"baseDnsZone (stack) must win over zoneName (config default) — see Addendum R")
		return nil
	})
}

// TestURNContract_ConfigZoneFallback — when the stack states no
// preference (empty baseDnsZone), the registrar falls back to the
// config's zoneName. This is the "no override" case; both branches of
// the fork must be exercised to lock the contract.
func TestURNContract_ConfigZoneFallback(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(ctx *pulumi.Context) error {
		r, err := NewRegistrar(ctx, "", "fallback.example.com")
		if err != nil {
			return err
		}
		Expect(r.MainDomain()).To(Equal("fallback.example.com"))
		return nil
	})
}
