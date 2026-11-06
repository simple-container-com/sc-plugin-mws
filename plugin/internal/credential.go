// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/pkg/errors"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	mwsprovider "github.com/simple-container-com/pulumi-mws/sdk/go/mws"

	mwsApi "github.com/simple-container-com/sc-plugin-mws/api"
)

// This file bridges the one real mismatch between how SC carries credentials and
// how MWS's provider accepts them.
//
// SC resolves `${secret:…}` to a VALUE. MWS's Terraform provider accepts
// `service_account_authorized_key_path` — a FILESYSTEM PATH — and has no
// inline-JSON field at all (read off the provider source at tag 0.0.38,
// `internal/provider/config.go:18-36`). There is no file on the deploying
// machine to point at, so the plugin writes one.
//
// # Why the path must be DETERMINISTIC
//
// The obvious implementation is `os.MkdirTemp` per call. It is wrong, and
// wrong in the expensive direction: the path is a PROVIDER INPUT. A fresh random
// path on every deploy is a provider config diff on every deploy, and a changed
// provider REPLACES every resource that depends on it — which for this plugin
// means destroying the machine and its boot disk because a temp directory got a
// new name. (sc-plugin-cloudru's registrar carries a comment about the same
// hazard from the other direction.)
//
// Two deterministic alternatives were considered and rejected:
//
//   - a hash of the key document — churns on key rotation, turning a credential
//     rotation into a machine replacement;
//   - a hash of project+zone+endpoint — collides when two service accounts serve
//     one project, which is exactly how an access-narrowing migration is done.
//
// What is used instead is the PROVIDER RESOURCE NAME. It is already URN-stable
// (it is derived from the vm resource name and the environment, the same formula
// core uses), unique per vm resource, and unaffected by rotating the key the file
// holds.
//
// # Why the file is not unlinked by the caller
//
// The provider plugin reads the path during the engine's apply, which happens
// after VmHosts has returned. A `defer os.Remove` in the handler deletes the
// credential before it is used; the failure is a `load service account
// authorized key file: no such file or directory` from inside the provider,
// naming a path that no longer exists. So removal is registered here and run by
// `main` after `scPlugin.Serve` returns — see CleanupCredentials.
//
// The real protection is the mode, not the unlink: 0700 on the directory and
// 0600 on the file, with an lstat check and `O_NOFOLLOW` so a pre-existing
// symlink in a shared `/tmp` cannot redirect the write. If the process is killed
// rather than shut down, the file survives until the OS reaps the temp
// directory — which is why the mode has to be right on the way in.

// credentialRoot is the directory every materialised key lives under, inside the
// OS temp dir. One level of its own so CleanupCredentials has something bounded
// to remove and so the files are identifiable in an incident.
const credentialRoot = "sc-mws"

// authorizedKeyFile is the per-provider file name.
const authorizedKeyFile = "authorized-key.json"

var (
	credentialMu    sync.Mutex
	credentialPaths = map[string]bool{}
)

// materialiseAuthorizedKey writes the authorized-key document to a deterministic
// 0600 file and returns its path.
//
// providerResName must be the name the provider resource is registered under —
// it is what makes the path stable across deploys and distinct between vm
// resources. See the file header for why both properties are required.
func materialiseAuthorizedKey(providerResName, keyJSON string) (string, error) {
	dir := filepath.Join(os.TempDir(), credentialRoot, sanitisePathSegment(providerResName))

	// 0700 before anything is written: a 0755 directory in a shared /tmp lets any
	// local user list the file names, and on a CI runner the names say which
	// stack is deploying.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errors.Wrapf(err, "failed to create credential directory for MWS provider %q", providerResName)
	}

	// MkdirAll is a no-op on an existing path of any kind that stat()s as a
	// directory — including a symlink to one. lstat is what distinguishes them,
	// and a symlink here would mean the 0600 file lands wherever it points.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", errors.Wrapf(err, "failed to stat credential directory %q", dir)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Errorf(
			"credential directory %q is not a real directory (mode %s); refusing to write an MWS "+
				"service-account key through it", dir, info.Mode(),
		)
	}
	// Re-assert the mode: MkdirAll leaves an EXISTING directory's permissions
	// alone, so a 0777 leftover from an earlier run (or planted by another user)
	// would otherwise stand.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", errors.Wrapf(err, "failed to restrict credential directory %q", dir)
	}

	path := filepath.Join(dir, authorizedKeyFile)

	// O_NOFOLLOW so an existing symlink at the final path is an error rather than
	// a redirect; O_TRUNC because the previous deploy's key may be a different
	// one and a partial overwrite is not valid JSON.
	//
	// syscall.O_NOFOLLOW is used unguarded because this plugin ships for
	// linux/{amd64,arm64} and darwin/arm64 only — windows is intentionally
	// unsupported (see .github/workflows/release.yml). If that ever changes, this
	// is the line that needs a build-tagged constant, not a silent 0.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", errors.Wrapf(err, "failed to open credential file %q for MWS provider %q", path, providerResName)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(keyJSON); err != nil {
		return "", errors.Wrapf(err, "failed to write credential file %q", path)
	}
	// An earlier run may have created the file with a different mode before
	// O_NOFOLLOW/0600 was in place; OpenFile's perm argument only applies on
	// create.
	if err := f.Chmod(0o600); err != nil {
		return "", errors.Wrapf(err, "failed to restrict credential file %q", path)
	}

	credentialMu.Lock()
	credentialPaths[dir] = true
	credentialMu.Unlock()

	return path, nil
}

// CleanupCredentials removes every directory materialiseAuthorizedKey created.
//
// Called from main AFTER scPlugin.Serve returns, never from a handler: the
// provider plugin reads these paths during the engine's apply, which outlives
// the VmHosts RPC that produced them. Errors are swallowed deliberately — this
// runs during shutdown, there is nothing useful to do with a failure, and the
// mode is what guards the content either way.
func CleanupCredentials() {
	credentialMu.Lock()
	dirs := make([]string, 0, len(credentialPaths))
	for d := range credentialPaths {
		dirs = append(dirs, d)
	}
	credentialPaths = map[string]bool{}
	credentialMu.Unlock()

	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
}

// sanitisePathSegment keeps a resource name usable as one path component.
// Resource names are already constrained by SC, but `--<env>` and an operator's
// own naming are not a filesystem contract, and a `/` or a `..` here would place
// the credential outside the directory whose mode protects it.
func sanitisePathSegment(name string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	// A segment of only dots is `.` or `..`.
	if strings.Trim(mapped, ".") == "" {
		return "provider"
	}
	return mapped
}

// Provider builds the MWS Pulumi provider for one resolved account config.
//
// The provider plugin itself is resolved by the engine from the baked
// PluginDownloadURL in the generated SDK (simple-container-com/pulumi-mws) —
// nothing here wires that up, exactly as in sc-plugin-yandex. If a deploy cannot
// find `pulumi-resource-mws`, the bug is in that repo's ProviderInfo or its
// releases, not in this function.
func Provider(pctx *sdk.Context, name, zone string, account *mwsApi.AccountConfig) (*mwsprovider.Provider, error) {
	args := &mwsprovider.ProviderArgs{
		Project:  sdk.StringPtr(account.Project),
		Endpoint: sdk.StringPtr(account.EffectiveEndpoint()),
		// The RESOLVED zone, not account.EffectiveZone(): a vm resource's own
		// `zones:` wins over the account default, and the provider's zone is what
		// every resource that does not carry one of its own inherits.
		Zone: sdk.StringPtr(zone),
	}

	if account.UsesAuthorizedKey() {
		path, err := materialiseAuthorizedKey(name, account.ServiceAccountAuthorizedKey)
		if err != nil {
			return nil, err
		}
		// A path, and so NOT secret-wrapped: it names a file, and marking it
		// secret would hide from the Pulumi summary the one value an operator
		// needs to see when the provider cannot read its credential.
		args.ServiceAccountAuthorizedKeyPath = sdk.StringPtr(path)
	} else {
		// secretStringPtr, never sdk.StringPtr: an IAM token is a bearer
		// credential and an unmarked provider property is printed verbatim in
		// Pulumi's summary. Same reasoning as sc-plugin-yandex's
		// ServiceAccountKeyFile.
		//
		// Measured 2026-11-06: this is belt-and-braces, not the only guard.
		// pulumi-mws's generated NewProvider already does
		// `pulumi.ToSecret(args.MwsToken)` plus
		// `AdditionalSecretOutputs(["mwsToken"])` (sdk/go/mws/provider.go:66-71),
		// because upstream's schema marks the field sensitive. So swapping this
		// line for sdk.StringPtr does NOT currently leak — which also means a
		// test asserting ContainsSecrets() on this input cannot tell the two
		// apart (verified by mutation). Keep the wrap anyway: the SDK is
		// regenerated from a provider we control, the marking is upstream's
		// choice rather than ours, and the cost is one function call.
		args.MwsToken = secretStringPtr(account.Token)
	}

	provider, err := mwsprovider.NewProvider(pctx, name, args)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to init MWS provider %q", name)
	}
	return provider, nil
}

// secretStringPtr marks a provider property secret. Copied from
// sc-plugin-yandex's credentials.go for the same reason it exists there: an
// unmarked provider property is rendered verbatim in Pulumi's update summary.
func secretStringPtr(value string) sdk.StringPtrOutput {
	return sdk.ToSecret(sdk.String(value)).(sdk.StringOutput).ToStringPtrOutput()
}
