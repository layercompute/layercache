#!/usr/bin/env bash
set -euo pipefail
export GH_HOST=github.com

# Internal installer interface: repository, exact tag, peeled source commit,
# and an artifact already downloaded from that immutable release.
if (($# != 4)); then
  echo 'Usage: verify-release-provenance.sh OWNER/REPO TAG SOURCE FILE' >&2
  exit 2
fi
repository=$1
version=$2
source=$3
subject=$4
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]
[[ "$source" =~ ^[a-f0-9]{40}$ ]]
test -f "$subject"

provenance_repository=$repository
bundle=""
if [[ "$repository" == layercompute/layercache && "$version" == v0.1.0-beta.9 ]]; then
  # GitHub no longer serves this release's attestations after the transfer.
  # Preserve its original signed identity only for this exact published source.
  test "$source" = cb254e9af694f6fe25f5a0ad416456174bda3e44
  provenance_repository=ziahamza/layercache
  script_directory=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
  bundle="$script_directory/provenance/v0.1.0-beta.9.sigstore.jsonl"
fi
verification_args=(attestation verify "$subject" --repo "$provenance_repository"
  --signer-workflow "$provenance_repository/.github/workflows/release.yml"
  --source-ref "refs/tags/$version" --source-digest "$source"
  --deny-self-hosted-runners)
if [[ -n "$bundle" ]]; then verification_args+=(--bundle "$bundle"); fi
gh "${verification_args[@]}" >/dev/null
