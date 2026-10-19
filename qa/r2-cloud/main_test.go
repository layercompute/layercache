package main

import (
	"context"
	"errors"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestBucketDenialRequiresProviderEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		exists bool
		err    error
		valid  bool
	}{
		{name: "foreign bucket readable", exists: true},
		{name: "DNS failure", err: errors.New("lookup failed")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "provider unavailable", err: minio.ErrorResponse{StatusCode: 503, Code: "AccessDenied"}},
		{name: "invalid credentials", err: minio.ErrorResponse{StatusCode: 401, Code: "AccessDenied"}},
		{name: "explicit access denied", err: minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}, valid: true},
		{name: "explicit hidden existing bucket", err: minio.ErrorResponse{StatusCode: 404, Code: "NoSuchBucket"}, valid: true},
		{name: "SDK normalized hidden existing bucket", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, got := bucketDenialResult(test.exists, test.err)
			if got != test.valid {
				t.Fatalf("valid denial = %t, want %t", got, test.valid)
			}
		})
	}
}
