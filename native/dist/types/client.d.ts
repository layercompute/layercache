export interface Identity {
    project: string;
    compatibility: string;
    key: string;
}
export interface Options {
    endpoint?: string;
    token?: string;
    cacheDir?: string;
    maxBytes?: number;
    timeoutMs?: number;
    maxAgeMs?: number;
}
export declare class NativeCachePublicationError extends Error {
    constructor();
}
export declare class NativeCacheConflictError extends Error {
    constructor();
}
export interface Result {
    hit: boolean;
    source: 'local' | 'team' | 'miss';
    path?: string;
    digest?: string;
    bytes: number;
    elapsedMs: number;
}
export declare function environmentOptions(options?: Options, env?: NodeJS.ProcessEnv): Options;
export declare function artifactKey(identity: Identity): string;
export declare function digestFile(path: string): Promise<string>;
export declare class NativeCache {
    readonly root: string;
    readonly maxBytes: number;
    readonly options: Options;
    constructor(options?: Options);
    private locked;
    private url;
    private headers;
    private evict;
    private verified;
    restore(identity: Identity, destination?: string, signal?: AbortSignal): Promise<Result>;
    restoreManaged(identity: Identity, validate?: (destination: string) => Promise<void>): Promise<Result>;
    private restoreInternal;
    save(identity: Identity, input: string): Promise<Result>;
    saveImmutable(identity: Identity, input: string, signal?: AbortSignal): Promise<Result>;
    private saveInternal;
}
