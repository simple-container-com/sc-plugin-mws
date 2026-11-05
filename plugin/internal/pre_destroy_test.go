// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	scPlugin "github.com/simple-container-com/api/pkg/plugin"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
)

// TestPreDestroy_NarratesEveryV2Field — asserts the hook publishes a
// progress event for every v2 PreDestroyRequest field. If a future
// edit silently drops a field (regression), the counts stop matching.
func TestPreDestroy_NarratesEveryV2Field(t *testing.T) {
	RegisterTestingT(t)

	pub := scPlugin.NewPublisher(0)

	req := &scpluginv1.PreDestroyRequest{
		Header:               &scpluginv1.RequestHeader{StackName: "backend", Environment: "prod"},
		ResourceType:         "mongodb-atlas",
		StackOutputs:         map[string][]byte{"primary-service-user": []byte(`{"value":{"user":"u1"},"secret":false}`)},
		ClientDescriptorJson: []byte(`{"type":"cloud-compose"}`),
		DestroyParamsJson:    []byte(`{"environment":"prod","destroySecretsStack":false}`),
	}

	Expect(PreDestroy(context.Background(), pub, req)).To(Succeed())

	batch, ok := pub.Drain()
	Expect(ok).To(BeTrue())
	Expect(len(batch)).To(BeNumerically(">=", 4), "expected at least 4 progress events — one for the summary, one for the output, one for the client descriptor, one for destroy params")

	// Sanity-check that the key names rode through to the log.
	var text string
	for _, ev := range batch {
		text += ev.GetMessage() + "\n"
	}
	Expect(text).To(ContainSubstring("resource_type"))
	Expect(text).To(ContainSubstring("primary-service-user"))
	Expect(text).To(ContainSubstring("cloud-compose"))
	Expect(text).To(ContainSubstring("destroySecretsStack"))
}

// TestPreDestroy_HandlesAbsentFieldsCleanly — a destroy triggered
// against an SC core that didn't populate the v2 fields (older core,
// or a destroy against a stack with no outputs) must land as an
// explicit log line, not a panic or a silent no-op.
func TestPreDestroy_HandlesAbsentFieldsCleanly(t *testing.T) {
	RegisterTestingT(t)

	pub := scPlugin.NewPublisher(0)

	req := &scpluginv1.PreDestroyRequest{
		Header:       &scpluginv1.RequestHeader{StackName: "backend", Environment: "prod"},
		ResourceType: "echo-bucket",
	}

	Expect(PreDestroy(context.Background(), pub, req)).To(Succeed())

	batch, ok := pub.Drain()
	Expect(ok).To(BeTrue())
	var text string
	for _, ev := range batch {
		text += ev.GetMessage() + "\n"
	}
	Expect(text).To(ContainSubstring("0 key"))
	Expect(text).To(ContainSubstring("client descriptor was absent"))
}
