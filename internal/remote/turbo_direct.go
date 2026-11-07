package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/directtransfer"
)

// completionTimeout bounds the best-effort report of a verified direct read.
const completionTimeout = 5 * time.Second

// Fetch delivers one Team Cache artifact to install. When the gateway offers
// direct transfer, install receives the object's bytes from object storage
// through a body whose final read fails unless the complete size and digest
// match. If that direct attempt fails in transport, at storage, by truncation
// or by verification, Fetch repeats the request once without the opt-in and
// calls install again with the proxied download; it never retries the URL.
// Install must leave nothing behind when it returns an error. Fetch returns
// install's error unchanged.
func (client *TurboClient) Fetch(ctx context.Context, hash string, install func(TurboDownload) error) error {
	if client.breaker.Allow() {
		handled, err := client.fetchDirect(ctx, hash, install)
		if handled {
			return err
		}
	}
	download, err := client.Get(ctx, hash)
	if err != nil {
		return err
	}
	defer download.Body.Close()
	return install(download)
}

// fetchDirect returns handled=false only when the request must be repeated
// without the opt-in.
func (client *TurboClient) fetchDirect(ctx context.Context, hash string, install func(TurboDownload) error) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.artifactURL(hash), nil)
	if err != nil {
		return true, err
	}
	client.authorize(request)
	request.Header.Set("Accept", directtransfer.MediaType)
	response, err := client.http.Do(request)
	if err != nil {
		return true, fmt.Errorf("read remote cache: %w", err)
	}
	if response.StatusCode != http.StatusOK || !directtransfer.IsDescriptor(response.Header) {
		// A miss, a failure, or the gateway chose the proxied response.
		download, err := turboResponse(response, client.transferIdle)
		if err != nil {
			return true, err
		}
		defer download.Body.Close()
		return true, install(download)
	}
	metadata := turboResponseMetadata(response)
	descriptor, location, err := directtransfer.Decode(response.Body, time.Now().UTC())
	_ = response.Body.Close()
	if err != nil {
		client.breaker.Fail()
		return false, nil
	}
	verifier, err := directtransfer.Get(ctx, client.direct, descriptor, location)
	if err != nil {
		client.breaker.Fail()
		return false, nil
	}
	body := withIdleReadTimeout(verifier, client.transferIdle)
	installErr := install(TurboDownload{
		Body: body, ExpectedDigest: descriptor.Digest, Size: descriptor.Size, Metadata: metadata,
	})
	_ = body.Close()
	switch {
	case verifier.Verified():
		client.breaker.Succeed()
		client.completeDirectRead(ctx, hash, descriptor)
		return true, installErr
	case verifier.Failed():
		client.breaker.Fail()
		return false, nil
	default:
		// Install stopped before the end of the body for its own reasons.
		return true, installErr
	}
}

// completeDirectRead reports a verified direct read so the gateway records the
// hit and the read for retention. A lost report leaves the read unknown.
func (client *TurboClient) completeDirectRead(ctx context.Context, hash string, descriptor directtransfer.Descriptor) {
	body, err := json.Marshal(directtransfer.Completion{Digest: descriptor.Digest, Size: descriptor.Size})
	if err != nil {
		return
	}
	reportContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	target := client.artifactURL(hash) + "/transfers/" + url.PathEscape(descriptor.Lease) + "/complete"
	response, err := client.postTransfer(reportContext, target, body, nil)
	if err != nil {
		return
	}
	closeResponse(response)
}

// PutFrom publishes one artifact. When the gateway admits a direct write, the
// bytes go to object storage under a signed checksum and the gateway then
// commits them. Any direct failure repeats the publication once through the
// proxied PUT. open is called once per attempt and must return the artifact's
// bytes from the beginning.
func (client *TurboClient) PutFrom(
	ctx context.Context,
	hash string,
	entry artifact.Entry,
	open func() (io.ReadCloser, error),
) error {
	if _, valid := directtransfer.ParseDigest(directtransfer.Digest(entry.Digest)); valid && entry.Size >= 0 && client.breaker.Allow() {
		handled, err := client.putDirect(ctx, hash, entry, open)
		if handled {
			return err
		}
	}
	body, err := open()
	if err != nil {
		return err
	}
	defer body.Close()
	return client.Put(ctx, hash, entry, body)
}

func (client *TurboClient) putDirect(
	ctx context.Context,
	hash string,
	entry artifact.Entry,
	open func() (io.ReadCloser, error),
) (bool, error) {
	declaration, err := json.Marshal(directtransfer.Declaration{Digest: directtransfer.Digest(entry.Digest), Size: entry.Size})
	if err != nil {
		return true, err
	}
	response, err := client.postTransfer(ctx, client.artifactURL(hash)+"/transfers/upload", declaration, nil)
	if err != nil {
		// The gateway itself is unreachable; a proxied PUT would be too.
		return true, fmt.Errorf("publish remote cache: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		closeResponse(response)
		return true, ErrUnauthorized
	}
	if response.StatusCode != http.StatusOK {
		// Direct write is off or not admitted: the proxied PUT answers
		// authoritatively.
		closeResponse(response)
		return false, nil
	}
	var upload directtransfer.Upload
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&upload)
	closeResponse(response)
	if decodeErr != nil || upload.Upload == "" || len(upload.Upload) > 128 {
		client.breaker.Fail()
		return false, nil
	}
	body, err := open()
	if err != nil {
		return true, err
	}
	requestContext, cancel := context.WithCancel(ctx)
	progress := newProgressReader(body, client.transferIdle, cancel)
	putErr := directtransfer.Put(requestContext, client.direct, upload, progress, entry.Size)
	progress.stop()
	cancel()
	_ = body.Close()
	if putErr != nil {
		client.breaker.Fail()
		return false, nil
	}
	target := client.artifactURL(hash) + "/transfers/" + url.PathEscape(upload.Upload) + "/commit"
	response, err = client.postTransfer(ctx, target, nil, &entry)
	if err != nil {
		// The outcome is unknown. A proxied PUT of the same bytes is idempotent.
		client.breaker.Fail()
		return false, nil
	}
	closeResponse(response)
	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated:
		client.breaker.Succeed()
		return true, nil
	case http.StatusConflict:
		client.breaker.Succeed()
		return true, ErrConflict
	case http.StatusUnauthorized, http.StatusForbidden:
		return true, ErrUnauthorized
	default:
		client.breaker.Fail()
		return false, nil
	}
}

func (client *TurboClient) artifactURL(hash string) string {
	return client.baseURL + "/v8/artifacts/" + url.PathEscape(hash)
}

func (client *TurboClient) postTransfer(ctx context.Context, target string, body []byte, entry *artifact.Entry) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	client.authorize(request)
	request.Header.Set("Content-Type", "application/json")
	if entry != nil {
		request.Header.Set("x-artifact-duration", strconv.FormatInt(entry.Metadata.DurationMS, 10))
		if entry.Metadata.Tag != "" {
			request.Header.Set("x-artifact-tag", entry.Metadata.Tag)
		}
	}
	return client.http.Do(request)
}

func closeResponse(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
}

// turboResponse maps a Team Cache GET response. On success the caller owns the
// returned body.
func turboResponse(response *http.Response, idle time.Duration) (TurboDownload, error) {
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		response.Body.Close()
		return TurboDownload{}, ErrMiss
	case http.StatusUnauthorized, http.StatusForbidden:
		response.Body.Close()
		return TurboDownload{}, ErrUnauthorized
	default:
		response.Body.Close()
		return TurboDownload{}, fmt.Errorf("remote cache returned HTTP %d", response.StatusCode)
	}
	return TurboDownload{
		Body:           withIdleReadTimeout(response.Body, idle),
		ExpectedDigest: response.Header.Get("x-layercache-digest"),
		Size:           response.ContentLength,
		Metadata:       turboResponseMetadata(response),
	}, nil
}

func turboResponseMetadata(response *http.Response) artifact.Metadata {
	duration, _ := strconv.ParseInt(response.Header.Get("x-artifact-duration"), 10, 64)
	return artifact.Metadata{DurationMS: duration, Tag: response.Header.Get("x-artifact-tag")}
}
