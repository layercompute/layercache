# Private snapshot authentication

Snapshot grants authorize one project's private archive namespace under one
exact toolchain compatibility profile. They reuse native archive transport and
the existing signed capability format. They never authorize administrator,
Public Build, retained-artifact, inventory, reporting or unrelated task access.

Each shared bootstrap has a reader or writer role, a fixed expiry and a key
prefix ending `/`. The suffix names an immutable snapshot generation. A writer
may exchange for read or write; a reader may exchange only for read. The service
does not trust a caller's branch, source commit or CI environment as evidence
of publication authority. Inject writer credentials only into a trusted
publisher workflow whose source and secret exposure are controlled. Give
eligible consumers the separate reader credential. This does not provide
Apple workload attestation.

## Operator configuration

Set the Team runtime's `snapshotCredentialsFile` to an absolute private file:

```json
{
  "version": 1,
  "credentials": [
    {
      "id": "xcode-reader",
      "secretSha256": "SHA256_OF_THE_COMPLETE_BOOTSTRAP",
      "project": "github.com/example/project",
      "compatibility": "your-exact-xcode-compiler-sdk-target-profile",
      "keyPrefix": "xcode-snapshots-v1/",
      "role": "reader",
      "expiresAt": "REPLACE_WITH_A_FIXED_FUTURE_UTC_TIMESTAMP"
    }
  ]
}
```

The bootstrap format is `lcs1.<grant-id>.<64 lowercase hex digits>`. Generate
32 random secret bytes, store the complete bootstrap only in the approved
secret manager or protected client file, and put its SHA256 in the server file.
The placeholder JSON is a schema example, not a valid production grant. Use a
separate independently generated bootstrap for the trusted writer. Keep both
bootstraps out of source, archives, command arguments, logs and build receipts.

The file must be a private regular single-link file owned by the service user.
Replace it atomically for rotation or revocation. The service rereads it for
each exchange and scoped artifact request. Removing a grant, changing its
contents or expiry, or losing access to the file invalidates its issued
capabilities. Rotation therefore does not depend on a service restart or token
expiry. A project binding in the file remains mandatory even when several
projects share the same hosted storage pool.

## Client exchange

The native CLI accepts `--credential-file` and an explicit `--endpoint`. Its
credential file contains only the bootstrap and optional surrounding whitespace;
it must be private, regular, single-link and owned by the current user. Scoped
mode does not silently fall back to unrelated environment credentials or a
local-only save.

The CLI sends `POST /v1/auth/snapshot/exchange` with the bootstrap as a Bearer
credential and `{project,compatibility,key,mode}` as JSON. The response is
version 1 and includes `token`, `expiresAt`, `project`, `compatibility`,
`nativeKey`, `mode`, `capabilities` and
`credentialKind:"shared-snapshot-bootstrap"`. The native key is the unchanged
`native-v1-` SHA256 identity used by the archive client. It is not a new
compilation key. The client checks that response against its requested identity.

The issued capability lasts at most fifteen minutes and never beyond the
grant's expiry. It permits only GET/HEAD of that exact archive and, for write
mode, its PUT. Bootstrap and capability values never appear in normal CLI
results. HTTP or validation errors return a failure; restore misses remain
explicit `hit:false` results. `--timeout-seconds` accepts 1 through 900 for each
archive HTTP transfer and local lock acquisition, with the existing 120-second
default. It does not bound the complete command's packing, checksum or extraction
work. The exchange has a separate ten-second timeout. There is no automatic retry
of an uncertain publication. Scoped local archives are separated by endpoint;
their budgets are per endpoint, not a global multi-origin disk ceiling.

Credential provisioning, backend deployment, archive integrity, a confirmed
closed-store boundary and actual language-specific compiler reuse are separate
acceptance steps. A successful snapshot transfer proves neither a retained
handoff nor fresh application test results.
