import type {Cache, CostReport, Scope} from '../dashboard/cache-view.js';
import type {ExecutionInventory} from '../dashboard/execution-view.js';

export type Team = {id: string; name: string; role: string; kind: 'team' | 'personal'; operatorManaged?: boolean};
export type Project = {id: string; teamId: string; name: string; repository: string; role: string; operatorManaged?: boolean};
export type Member = {userId: string; login: string; role: string};
export type Session = {
  user: {id: string; login: string}; csrfToken: string; cloudOrigin: string;
  authMethod: 'github' | 'password'; passwordChangeRequired: boolean;
  canChangePassword: boolean; canManageGitHub: boolean; canCreateTeam: boolean;
  teams: Team[] | null; projects: Project[] | null;
  invitations: {id: string; teamName: string; role: string}[] | null;
};
export type PublicConfig = {githubClientId: string; passwordLogin: boolean};
export type Snapshot = {cache: Cache; scope?: Scope; cost?: CostReport; updatedAt: string; executions?: ExecutionInventory | null};
export type TeamUsage = {cost?: CostReport; projectCount: number; allocatedCacheBytes: number | null; allocationComplete: boolean};
export type ProjectPage = 'overview' | 'activity' | 'infrastructure' | 'storage' | 'settings';
export type API = <T>(path: string, method?: string, body?: unknown, expireSession?: boolean) => Promise<T>;
