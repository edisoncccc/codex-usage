# Codex Usage Dashboard Code-Signing Policy

[English](CODE_SIGNING.en.md) · [简体中文](CODE_SIGNING.md)

This policy describes the current source-only state of `edisoncccc/codex-usage` and the binary signing and release responsibilities that may be enabled only after real external gates are satisfied. It does not claim that signing or release has already occurred.

## 1. Current status

- The project has not received written approval from SignPath Foundation.
- [`install-policy.json`](install-policy.json) has `binary_release_enabled=false` and `windows.publisher_subject=null`.
- The repository has no signed Windows executable, trusted binary Release, or installable Linux binary.
- The current [Release workflow](.github/workflows/release.yml) is a manually triggered source-only guard with read-only permissions. It cannot build, upload, or publish assets.
- No SignPath organization/project/policy ID, certificate Subject, or signing secret has been configured. This repository will not guess these identities with placeholders.

Any signed EXE or binary download claiming to come from the current project is therefore outside the recognized release chain. The only supported installation path today is an explicitly approved source build from the canonical repository, as documented in [INSTALL.en.md](INSTALL.en.md).

## 2. Scope and objective

If a future trusted release is approved, it covers four final assets: Windows `amd64`/`arm64` and Linux `amd64`/`arm64`. An immutable GitHub Release is the only binary source of truth. Future package managers may only reference that same Release; they must not repackage it or become a version source.

Windows assets must carry publicly trusted Authenticode signatures and valid timestamps provided through SignPath Foundation. All four final assets require post-signing SHA256 values, GitHub Release API asset digests, and GitHub Artifact Attestations. Anything not checked must truthfully remain `not_checked`; it may not be reported as verified.

## 3. Trust and roles

The following defines future responsibilities. It does not claim that specific people, SignPath identities, or project IDs have already been assigned.

### 3.1 GitHub Actions

Only a reviewed GitHub Actions Release workflow in the canonical repository may build the four assets from an explicit `vX.Y.Z` tag. It must run the complete Go, Playwright, lifecycle, privacy, and release gates on a clean commit. Ordinary CI must not upload binary artifacts.

### 3.2 SignPath Foundation

SignPath Foundation hosts the signing private key inside its platform and applies Authenticode signatures and timestamps to the two final Windows executables. Maintainers must never download or export that key or store it in GitHub secrets. The actual SignPath publisher Subject may come only from the authoritative approval result.

### 3.3 Maintainers

Maintainers review application source, dependencies, build scripts, the Release workflow, installation policy, and bilingual documentation changes. They confirm commit provenance, test evidence, Fork/MIT attribution, and privacy boundaries. A change that enables the binary channel or changes the publisher must receive dedicated review and must not be bundled with unrelated features.

### 3.4 Release approvers

Release approvers verify the tag target, release notes, four assets, Windows signatures/timestamps/actual publisher, post-signing SHA256 values, Release API digests, Attestation repository/workflow/tag/commit identity, and the complete Draft Release asset list. Only a fully consistent Draft may be approved as an immutable Release.

## 4. Future release gates

Every condition below must be satisfied before enabling the binary channel:

1. The user separately authorizes an external application to SignPath Foundation.
2. SignPath provides explicit written approval for this public MIT Fork and returns a verifiable real publisher identity.
3. Real organization/project/policy details and permissions are configured privately; no key or credential is committed to the repository.
4. Test signing and a non-publishing rehearsal prove Authenticode, timestamp, and publisher validation on both Windows architectures.
5. One reviewed change updates this policy, the installation policy, and the Release workflow together; downloads are never enabled before the documentation and guardrails.
6. Formal release notes at `docs/releases/vX.Y.Z.md` have been reviewed, and all tests and supply-chain negative tests pass.

Until every condition is complete, `binary_release_enabled` must remain `false` and the Release workflow must remain unable to publish.

## 5. Signing, digest, and Attestation order

The formal release order is fixed:

```text
v* tag points to a specific commit
→ complete test, privacy, and release gates
→ GitHub Actions builds Windows/Linux amd64/arm64
→ SignPath signs and timestamps both Windows executables
→ verify Authenticode, timestamp, and actual publisher
→ recompute SHA256 for all four final files
→ generate GitHub Artifact Attestations for all four final files
→ create the Draft Release with reviewed release notes
→ upload the final assets, SHA256SUMS, and required supply-chain material
→ read and verify Release API asset digests, the complete asset list, signatures, Attestations, and release notes
→ release approvers approve and publish it as an Immutable Release
```

A signature changes executable bytes, so each Windows SHA256 must be recomputed after signing. `SHA256SUMS` and Attestations must describe the final bytes about to be uploaded, never a pre-signing result; Release API asset digests can be read and verified only after assets are uploaded to the Draft. Final review covers the Draft's complete asset list, signatures, digests, Attestations, and release notes together. Each Attestation must bind the canonical repository, reviewed workflow, same tag, and same commit.

## 6. Failure handling

If the SignPath application is rejected, conditions are unmet, signing fails, a timestamp is invalid, the publisher differs, digests disagree, an Attestation is wrong, tests fail, or release notes are missing:

- do not create or publish a formal binary Release;
- do not publish a Linux-only build or an unsigned Windows compromise;
- do not treat a Draft, ordinary CI artifact, or third-party mirror as an installation source;
- do not purchase a commercial certificate;
- remain source-only and keep `update` returning `release_channel_disabled`;
- retain redacted logs and gate results, then restart the full process from a clean commit after a fix.

## 7. Change control and security reporting

Changes to release permissions, signing identity, workflow, digest order, Attestation, or `install-policy.json` require explicit review of both maintainer and release-approval responsibilities and must pass the `internal/installpolicy` documentation/policy contract tests. Never place a private key, token, SignPath/GitHub secret, or non-public identity data in an Issue, PR, log, or document.

If you find a forged signature, unexpected publisher, replaced Release asset, digest or Attestation mismatch, or suspected signing-credential exposure, do not disclose exploitable details publicly. Use the private GitHub vulnerability-reporting path described in [SECURITY.md](SECURITY.md) and provide only minimal redacted evidence.
