// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/simple-container-com/api/pkg/api"
	pApi "github.com/simple-container-com/api/pkg/clouds/pulumi/api"
	"github.com/simple-container-com/api/pkg/plugin/runtime"
)

// noopMocks is the minimum Pulumi mock surface sdk.RunErr needs so a
// ComputeProcessor that touches sdk.StringOutput + ApplyT can run
// in-process without a real engine.
type noopMocks struct{}

func (noopMocks) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name + "-id", args.Inputs, nil
}

func (noopMocks) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) { return args.Args, nil }

// TestEchoComputeProcessor_RunComputeProcess_DrainsCapturedOutput locks
// in the shape every real plugin's ComputeProcess body wears:
// runtime.RunComputeProcess drives the EchoComputeProcessor, the shared
// Collector captures the ApplyT-wrapped secret mutation via AddOutput,
// and the drain blocks on it BEFORE marshalling — so the response
// carries both the direct env var and the ApplyT-added secret.
//
// This is the trap-16 regression test for the template. If a derived
// plugin reimplements the collector with AddOutput as a no-op (or
// skips RunComputeProcess entirely and calls the processor directly),
// the ApplyT-added ECHO_SECRET_TOKEN will be missing from the response
// and this test will fail.
func TestEchoComputeProcessor_RunComputeProcess_DrainsCapturedOutput(t *testing.T) {
	RegisterTestingT(t)

	err := sdk.RunErr(func(pctx *sdk.Context) error {
		rctx := &runtime.Context{Pulumi: pctx}
		stack := api.Stack{Name: "backend-prod"}
		input := api.ResourceInput{
			Descriptor: &api.ResourceDescriptor{
				Type: "echo-bucket",
				Name: "my-bucket",
			},
			StackParams: &api.StackParams{
				StackName:   "backend-prod",
				Environment: "staging",
			},
		}
		resp, out, procErr := runtime.RunComputeProcess(
			context.Background(), rctx, stack, input,
			pApi.ProvisionParams{},
			EchoComputeProcessor,
		)
		Expect(procErr).ToNot(HaveOccurred())
		Expect(out).ToNot(BeNil(), "ResourceOutput ref must be returned so SC core can mint a URN")

		// Two env vars — one direct, one from inside an ApplyT callback.
		// The ApplyT one is the trap-16 regression: without the shared
		// Collector's drain, it would never land.
		Expect(resp.GetEnvVars()).To(HaveLen(2))

		var plain, secret bool
		for _, v := range resp.GetEnvVars() {
			switch v.GetName() {
			case "ECHO_RESOURCE_NAME":
				Expect(v.GetSecret()).To(BeFalse())
				Expect(v.GetValue()).To(Equal("my-bucket"))
				Expect(v.GetStackName()).To(Equal("backend-prod"))
				plain = true
			case "ECHO_SECRET_TOKEN":
				Expect(v.GetSecret()).To(BeTrue(), "ApplyT-added secret must be captured by the drain")
				secret = true
			}
		}
		Expect(plain).To(BeTrue(), "direct AddEnvVariableIfNotExist mutation missing")
		Expect(secret).To(BeTrue(), "ApplyT-wrapped AddSecretEnvVariableIfNotExist missing — trap 16 regressed")

		// Two tpl extensions — one resource, one dependency.
		Expect(resp.GetTplExtensions()).To(HaveLen(2))
		return nil
	}, sdk.WithMocks("project", "stack", noopMocks{}))
	Expect(err).ToNot(HaveOccurred())
}
