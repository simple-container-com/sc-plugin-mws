// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"

	mwsApi "github.com/simple-container-com/sc-plugin-mws/api"
)

// awaitRegistration blocks until a resource's registration RPC has completed.
//
// Needed only in THIS file, and the reason is the same trap withTrapFreeContext
// exists for: RegisterResource runs in a goroutine, so a test that registers a
// provider and nothing else returns before the mock is ever called and sees an
// empty capture list. The URN-contract tests do not need it because the handler
// they drive awaits outputs that depend on every resource it registers; a test
// that registers a provider in isolation has nothing to await, so it must await
// the URN by hand.
func awaitRegistration(ctx context.Context, res pulumi.Resource) {
	_, err := internals.UnsafeAwaitOutput(ctx, res.URN())
	Expect(err).ToNot(HaveOccurred())
}

// Every test here redirects os.TempDir() with TMPDIR so nothing lands in the
// real /tmp. That matters more than usual: the thing being written is a
// service-account private key, and a test that leaked one into a shared /tmp
// would be the same incident the code under test exists to prevent.
func isolatedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	Expect(os.TempDir()).To(Equal(dir), "TMPDIR is not honoured on this platform; these tests would write to the real temp dir")

	// The package-level registry is global, so a test that writes must not leave
	// entries behind for the next one.
	t.Cleanup(func() {
		credentialMu.Lock()
		credentialPaths = map[string]bool{}
		credentialMu.Unlock()
	})
	return dir
}

// The path must be DETERMINISTIC, and this is the expensive one to get wrong:
// the path is a PROVIDER INPUT, so a fresh random path per deploy is a provider
// config diff per deploy, and a changed provider REPLACES every resource under
// it — destroying the machine and its boot disk because a temp directory got a
// new name.
//
// It must also be stable across a KEY ROTATION (so rotating a credential is not
// a machine replacement) and DISTINCT per provider resource name (so two vm
// resources do not overwrite each other's credential).
func TestMaterialiseAuthorizedKeyPathIsDeterministic(t *testing.T) {
	RegisterTestingT(t)
	isolatedTempDir(t)

	first, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a"}`)
	Expect(err).ToNot(HaveOccurred())

	second, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a"}`)
	Expect(err).ToNot(HaveOccurred())
	Expect(second).To(Equal(first), "the path is a provider input; a changing one replaces the machine every deploy")

	rotated, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"b-rotated"}`)
	Expect(err).ToNot(HaveOccurred())
	Expect(rotated).To(Equal(first), "a key rotation must not change the path, or rotating a credential rebuilds the machine")

	other, err := materialiseAuthorizedKey("db-mws--staging", `{"keyId":"a"}`)
	Expect(err).ToNot(HaveOccurred())
	Expect(other).ToNot(Equal(first), "two vm resources must not share one credential file")
}

// The mode is the real protection, not the unlink: the file outlives the RPC
// that wrote it (the provider reads it during the engine's apply) and survives
// until shutdown, so it has to be right on the way IN.
func TestMaterialiseAuthorizedKeyWritesRestrictiveModes(t *testing.T) {
	RegisterTestingT(t)
	isolatedTempDir(t)

	path, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a","privateKey":"secret"}`)
	Expect(err).ToNot(HaveOccurred())

	info, err := os.Stat(path)
	Expect(err).ToNot(HaveOccurred())
	Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

	dirInfo, err := os.Stat(filepath.Dir(path))
	Expect(err).ToNot(HaveOccurred())
	Expect(dirInfo.Mode().Perm()).To(Equal(os.FileMode(0o700)),
		"a 0755 directory in a shared /tmp lets any local user list the file names, which on a CI runner says which stack is deploying")

	content, err := os.ReadFile(path)
	Expect(err).ToNot(HaveOccurred())
	Expect(string(content)).To(Equal(`{"keyId":"a","privateKey":"secret"}`))
}

// O_TRUNC, not a partial overwrite: the previous deploy's key may be a different
// and LONGER document, and a partial overwrite leaves trailing bytes that are not
// valid JSON — which the provider reports as a bad temp file rather than a bad
// secret.
func TestMaterialiseAuthorizedKeyTruncatesAShorterRewrite(t *testing.T) {
	RegisterTestingT(t)
	isolatedTempDir(t)

	_, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a-very-long-previous-document"}`)
	Expect(err).ToNot(HaveOccurred())

	path, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"b"}`)
	Expect(err).ToNot(HaveOccurred())

	content, err := os.ReadFile(path)
	Expect(err).ToNot(HaveOccurred())
	Expect(string(content)).To(Equal(`{"keyId":"b"}`))
}

// MkdirAll leaves an EXISTING directory's permissions alone, so a 0777 leftover
// from an earlier run — or one planted by another local user in a shared /tmp —
// would otherwise stand while the code looked like it had set 0700.
func TestMaterialiseAuthorizedKeyReassertsTheModeOfAnExistingDirectory(t *testing.T) {
	RegisterTestingT(t)
	tmp := isolatedTempDir(t)

	planted := filepath.Join(tmp, credentialRoot, "web-mws--staging")
	Expect(os.MkdirAll(planted, 0o777)).To(Succeed())
	Expect(os.Chmod(planted, 0o777)).To(Succeed())

	_, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a"}`)
	Expect(err).ToNot(HaveOccurred())

	info, err := os.Stat(planted)
	Expect(err).ToNot(HaveOccurred())
	Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
}

// A symlink where the directory should be must be an ERROR, not a redirect:
// MkdirAll is a no-op on a symlink that stats as a directory, so without the
// lstat check the 0600 key file lands wherever the link points — which in a
// shared /tmp is an attacker's choice.
func TestMaterialiseAuthorizedKeyRefusesASymlinkedDirectory(t *testing.T) {
	RegisterTestingT(t)
	tmp := isolatedTempDir(t)

	elsewhere := filepath.Join(tmp, "attacker-controlled")
	Expect(os.MkdirAll(elsewhere, 0o700)).To(Succeed())
	Expect(os.MkdirAll(filepath.Join(tmp, credentialRoot), 0o700)).To(Succeed())
	Expect(os.Symlink(elsewhere, filepath.Join(tmp, credentialRoot, "web-mws--staging"))).To(Succeed())

	_, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a"}`)
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("not a real directory"))

	// And nothing was written through it.
	entries, readErr := os.ReadDir(elsewhere)
	Expect(readErr).ToNot(HaveOccurred())
	Expect(entries).To(BeEmpty(), "the key was written through the symlink")
}

// O_NOFOLLOW on the final path: an existing symlink AT the file name is the other
// half of the same attack, and MkdirAll/lstat on the directory does not see it.
func TestMaterialiseAuthorizedKeyRefusesASymlinkedFile(t *testing.T) {
	RegisterTestingT(t)
	tmp := isolatedTempDir(t)

	dir := filepath.Join(tmp, credentialRoot, "web-mws--staging")
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	target := filepath.Join(tmp, "stolen.json")
	Expect(os.Symlink(target, filepath.Join(dir, authorizedKeyFile))).To(Succeed())

	_, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a"}`)
	Expect(err).To(HaveOccurred())

	_, statErr := os.Stat(target)
	Expect(os.IsNotExist(statErr)).To(BeTrue(), "the key was written through a symlinked file name")
}

func TestCleanupCredentialsRemovesEverythingItWrote(t *testing.T) {
	RegisterTestingT(t)
	isolatedTempDir(t)

	webPath, err := materialiseAuthorizedKey("web-mws--staging", `{"keyId":"a"}`)
	Expect(err).ToNot(HaveOccurred())
	dbPath, err := materialiseAuthorizedKey("db-mws--staging", `{"keyId":"b"}`)
	Expect(err).ToNot(HaveOccurred())

	CleanupCredentials()

	for _, p := range []string{webPath, dbPath} {
		_, statErr := os.Stat(p)
		Expect(os.IsNotExist(statErr)).To(BeTrue(), "%s survived cleanup", p)
	}

	// Idempotent: it runs during shutdown, where a second call must not panic.
	CleanupCredentials()
}

// The token is a BEARER credential, and an unmarked provider property is
// rendered verbatim in Pulumi's update summary — which on a CI runner means the
// deploy log. The assertion is on the INPUT the engine received, because that is
// what the summary and the checkpoint are rendered from.
//
// Read the name as "the token IS secret-wrapped", not "credential.go is what
// wraps it". Measured 2026-11-06 by mutation: replacing this package's
// secretStringPtr with a plain sdk.StringPtr does NOT fail this test, because
// pulumi-mws's generated NewProvider independently applies ToSecret +
// AdditionalSecretOutputs to mwsToken (sdk/go/mws/provider.go:66-71). So what is
// pinned here is the END STATE across both layers — which is the property that
// matters for a leak, and the one that would break if a regenerated SDK ever
// dropped upstream's sensitive marking. It is NOT evidence that our own wrap is
// load-bearing; see the note at credential.go's MwsToken assignment.
//
// The serviceAccountAuthorizedKeyPath assertion in the sibling test below has no
// such caveat: nothing upstream marks that field, so it fails if this package
// wraps it.
func TestProviderSecretWrapsTheToken(t *testing.T) {
	RegisterTestingT(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(pctx *pulumi.Context) error {
		p, err := Provider(pctx, "web-mws--staging", "ru-central1-a", &mwsApi.AccountConfig{
			Token:   "t1.bearer-token",
			Project: "test-project",
		})
		if err != nil {
			return err
		}
		awaitRegistration(pctx.Context(), p)
		return nil
	})

	inputs := m.inputsOf("pulumi:providers:mws")
	Expect(inputs["mwsToken"].ContainsSecrets()).To(BeTrue(),
		"the IAM token reached the engine unmarked; it will be printed in the update summary")
	Expect(resolveSecrets(inputs["mwsToken"]).StringValue()).To(Equal("t1.bearer-token"))
	Expect(inputs).ToNot(HaveKey("serviceAccountAuthorizedKeyPath"),
		"the token branch must not also set a key path; the provider would prefer the file and ignore the token")

	// The RESOLVED zone, not the account default — a vm resource's own `zones:`
	// wins, and every resource without a zone of its own inherits this one.
	Expect(resolveSecrets(inputs["zone"]).StringValue()).To(Equal("ru-central1-a"))
	Expect(resolveSecrets(inputs["project"]).StringValue()).To(Equal("test-project"))
	Expect(resolveSecrets(inputs["endpoint"]).StringValue()).To(Equal(mwsApi.DefaultEndpoint))
}

// The mirror of the above, and the one property here that must NOT be secret: it
// names a file, and marking it would hide from the Pulumi summary the one value
// an operator needs to see when the provider cannot read its credential.
func TestProviderPassesTheKeyPathUnmarkedAndWithoutTheToken(t *testing.T) {
	RegisterTestingT(t)
	isolatedTempDir(t)

	m := &captureMocks{}
	withTrapFreeContext(t, m, func(pctx *pulumi.Context) error {
		p, err := Provider(pctx, "web-mws--staging", "ru-central1-a", &mwsApi.AccountConfig{
			ServiceAccountAuthorizedKey: `{"id":"fake","service_account_id":"fake"}`,
			Project:                     "test-project",
		})
		if err != nil {
			return err
		}
		awaitRegistration(pctx.Context(), p)
		return nil
	})

	inputs := m.inputsOf("pulumi:providers:mws")
	path := inputs["serviceAccountAuthorizedKeyPath"]
	Expect(path.ContainsSecrets()).To(BeFalse(),
		"the key PATH is not the key; marking it secret hides the one value an operator needs when the provider cannot read its credential")
	Expect(path.StringValue()).To(HaveSuffix(filepath.Join(credentialRoot, "web-mws--staging", authorizedKeyFile)))
	Expect(inputs).ToNot(HaveKey("mwsToken"))

	// And the key itself is on disk at that path, 0600 — the provider reads it
	// during the engine's apply, long after this call returned.
	content, err := os.ReadFile(path.StringValue())
	Expect(err).ToNot(HaveOccurred())
	Expect(string(content)).To(Equal(`{"id":"fake","service_account_id":"fake"}`))
}

// sanitisePathSegment is what keeps a resource name inside the directory whose
// mode protects the key. Resource names are constrained by SC, but `--<env>` and
// an operator's own naming are not a filesystem contract, and a `/` or a `..`
// here would place the credential somewhere else entirely.
func TestSanitisePathSegmentCannotEscapeTheDirectory(t *testing.T) {
	RegisterTestingT(t)

	Expect(sanitisePathSegment("web-mws--staging")).To(Equal("web-mws--staging"))
	Expect(sanitisePathSegment("web.mws_1")).To(Equal("web.mws_1"))

	for _, hostile := range []string{"..", ".", "../../etc/ssh", "/etc/passwd", "a/b"} {
		got := sanitisePathSegment(hostile)
		Expect(got).ToNot(ContainSubstring("/"), "input %q produced a path separator", hostile)
		Expect(strings.Trim(got, ".")).ToNot(BeEmpty(), "input %q produced a dots-only segment (`.` or `..`)", hostile)
	}
}
