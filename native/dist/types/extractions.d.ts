export declare class Extractions {
    readonly root: string;
    constructor(root: string);
    path(key: string, digest: string): string;
    usage(): Promise<{
        bytes: number;
        keys: Set<string>;
    }>;
    existing(path: string): Promise<boolean>;
    create(path: string, key: string, reservation: number, extract: (destination: string) => Promise<void>): Promise<string>;
}
