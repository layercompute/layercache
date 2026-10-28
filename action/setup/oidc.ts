import { createHash } from 'node:crypto';

// The audience is a project identity, not a service URL to fetch. Hash exact UTF-8
// bytes so projects retain distinct audiences across repository or host changes.
export function projectAudience(project: string): string {
  return `https://layercache.dev/oidc/projects/${createHash('sha256').update(project, 'utf8').digest('hex')}`;
}
