# Historical release provenance

`v0.1.0-beta.9.sigstore.jsonl` contains the original three public Sigstore
bundles for the immutable beta.9 release at
`cb254e9af694f6fe25f5a0ad416456174bda3e44`. Each signed statement covers one
platform's binary, archive and checksum manifest. These are signed provenance
bundles, without local verification receipts or operational data.

GitHub's attestation API returned HTTP 404 for these subjects after the
repository moved to `layercompute/layercache`. The installer discovers the
release and tag at the new repository, then verifies these preserved bundles
against the original `ziahamza/layercache` source and release workflow. It
requires the exact historical tag and source commit and denies self-hosted
signers. It fails if any subject bytes or identity differ.

This exception applies only to the new canonical repository's beta.9 release.
Other repositories and releases require their live GitHub attestations and
their own signer identity. A network or verification failure never activates
the historical policy.
