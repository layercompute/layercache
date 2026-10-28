package githubauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ProjectAudience identifies one project's GitHub OIDC exchange using an HTTPS
// audience. Hashing the exact project bytes keeps the identity stable and avoids
// URL escaping or path normalization differences between clients.
func ProjectAudience(project string) string {
	digest := sha256.Sum256([]byte(project))
	return "https://layercache.dev/oidc/projects/" + hex.EncodeToString(digest[:])
}

// VerifyProject accepts the canonical audience and the exact legacy audience for
// this project. Both use the same signature, issuer and lifetime validation.
func (verifier *OIDCVerifier) VerifyProject(ctx context.Context, rawToken, project string, now time.Time) (ActionsIdentity, error) {
	if project == "" {
		return ActionsIdentity{}, errors.New("invalid GitHub Actions OIDC project")
	}
	return verifier.verify(ctx, rawToken, []string{ProjectAudience(project), "layercache:" + project}, now)
}
