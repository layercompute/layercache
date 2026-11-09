export declare const TRANSFER_MEDIA_TYPE = "application/vnd.layercache.transfer.v1+json";
export interface ReadDescriptor {
    url: URL;
    digest: string;
    size: number;
    lease: string;
}
export interface UploadGrant {
    url: URL;
    headers: Record<string, string>;
    upload: string;
}
export declare const directBreaker: {
    allow(): boolean;
    fail(): void;
    succeed(): void;
    reset(): void;
};
export declare function isDescriptorResponse(response: Response): boolean;
export declare function readDescriptor(body: ReadableStream<Uint8Array> | null, maxBytes: number): Promise<ReadDescriptor | null>;
export declare function readUploadGrant(body: ReadableStream<Uint8Array> | null, size: number): Promise<UploadGrant | null>;
