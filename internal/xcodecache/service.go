// Package xcodecache adapts LLVM's compilation-cache gRPC protocol to Layer
// Cache's verified, quota-managed artifact store. It runs only on a private Unix
// socket: the compiler's file-path messages are local filesystem operations.
package xcodecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/compatibility"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const MaxMessageBytes = 128 << 20

// Stats reports protocol reuse, not estimated compilation time saved.
type Stats struct {
	CASReads     uint64 `json:"casReads"`
	CASHits      uint64 `json:"casHits"`
	CASWrites    uint64 `json:"casWrites"`
	ActionReads  uint64 `json:"actionReads"`
	ActionHits   uint64 `json:"actionHits"`
	ActionWrites uint64 `json:"actionWrites"`
}

type Service struct {
	cas.UnimplementedCASDBServiceServer
	kv.UnimplementedKeyValueDBServer
	store                                                               *artifact.Store
	project, compatibility                                              string
	casReads, casHits, casWrites, actionReads, actionHits, actionWrites atomic.Uint64
}

func New(store *artifact.Store, project, abi string) (*Service, error) {
	if store == nil || project == "" || len(project) > 256 || strings.ContainsAny(project, "\x00\r\n\t ") {
		return nil, errors.New("an artifact store and project are required")
	}
	if err := compatibility.Validate(abi); err != nil {
		return nil, err
	}
	return &Service{store: store, project: project, compatibility: abi}, nil
}

func (s *Service) Stats() Stats {
	return Stats{s.casReads.Load(), s.casHits.Load(), s.casWrites.Load(), s.actionReads.Load(), s.actionHits.Load(), s.actionWrites.Load()}
}

func (s *Service) key(kind string, id []byte) artifact.Key {
	return artifact.Key{Integration: "xcode", Project: s.project, Compatibility: s.compatibility, Native: kind + "-" + hex.EncodeToString(id), Version: "1"}
}

func (s *Service) put(ctx context.Context, kind string, id, data []byte) error {
	_, _, err := s.store.Put(ctx, s.key(kind, id), artifact.Metadata{}, bytes.NewReader(data))
	return err
}

func (s *Service) get(ctx context.Context, kind string, id []byte) ([]byte, error) {
	entry, reader, err := s.store.Get(ctx, s.key(kind, id))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if entry.Size > MaxMessageBytes {
		return nil, artifact.ErrCorrupt
	}
	return io.ReadAll(io.LimitReader(reader, MaxMessageBytes+1))
}

func inline(blob *cas.CASBytes) ([]byte, error) {
	if blob == nil {
		return nil, errors.New("missing CAS bytes")
	}
	switch value := blob.Contents.(type) {
	case *cas.CASBytes_Data:
		if len(value.Data) > MaxMessageBytes {
			return nil, errors.New("CAS blob exceeds message limit")
		}
		return value.Data, nil
	case *cas.CASBytes_FilePath:
		// A FIFO or device must never block a compiler RPC before it is rejected.
		file, err := os.OpenFile(value.FilePath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxMessageBytes {
			return nil, errors.New("CAS path must be a bounded regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, MaxMessageBytes+1))
		if len(data) > MaxMessageBytes {
			return nil, errors.New("CAS blob exceeds message limit")
		}
		return data, err
	default:
		return nil, errors.New("missing CAS contents")
	}
}

func (s *Service) storeObject(ctx context.Context, object *cas.CASObject) (*cas.CASDataID, error) {
	if object == nil {
		return nil, status.Error(codes.InvalidArgument, "missing CAS object")
	}
	data, err := inline(object.Blob)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid CAS bytes")
	}
	for _, ref := range object.References {
		if len(ref.GetId()) != sha256.Size {
			return nil, status.Error(codes.InvalidArgument, "invalid CAS reference")
		}
	}
	normalized := &cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: data}}, References: object.References}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(normalized)
	if err != nil {
		return nil, err
	}
	// Save and Put share one hash schema, including ordered object references.
	digest := sha256.Sum256(encoded)
	if err = s.put(ctx, "cas", digest[:], encoded); err != nil {
		return nil, status.Error(codes.ResourceExhausted, "compilation cache could not store object")
	}
	s.casWrites.Add(1)
	return &cas.CASDataID{Id: digest[:]}, nil
}

func (s *Service) readObject(ctx context.Context, id *cas.CASDataID) (*cas.CASObject, error) {
	s.casReads.Add(1)
	if len(id.GetId()) != sha256.Size {
		return nil, status.Error(codes.InvalidArgument, "invalid CAS ID")
	}
	data, err := s.get(ctx, "cas", id.Id)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(digest[:], id.Id) {
		return nil, artifact.ErrCorrupt
	}
	object := new(cas.CASObject)
	if err = proto.Unmarshal(data, object); err != nil {
		return nil, artifact.ErrCorrupt
	}
	s.casHits.Add(1)
	return object, nil
}

func (s *Service) Put(ctx context.Context, request *cas.CASPutRequest) (*cas.CASPutResponse, error) {
	id, err := s.storeObject(ctx, request.GetData())
	if err != nil {
		return nil, err
	}
	return &cas.CASPutResponse{Contents: &cas.CASPutResponse_CasId{CasId: id}}, nil
}

func (s *Service) Save(ctx context.Context, request *cas.CASSaveRequest) (*cas.CASSaveResponse, error) {
	id, err := s.storeObject(ctx, &cas.CASObject{Blob: request.GetData().GetBlob()})
	if err != nil {
		return nil, err
	}
	return &cas.CASSaveResponse{Contents: &cas.CASSaveResponse_CasId{CasId: id}}, nil
}

func (s *Service) Get(ctx context.Context, request *cas.CASGetRequest) (*cas.CASGetResponse, error) {
	object, err := s.readObject(ctx, request.GetCasId())
	if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrCorrupt) {
		return &cas.CASGetResponse{Outcome: cas.CASGetResponse_OBJECT_NOT_FOUND}, nil
	}
	if err != nil {
		return nil, err
	}
	// write_to_disk is optional. Inline bytes avoid unmanaged export files.
	return &cas.CASGetResponse{Outcome: cas.CASGetResponse_SUCCESS, Contents: &cas.CASGetResponse_Data{Data: object}}, nil
}

func (s *Service) Load(ctx context.Context, request *cas.CASLoadRequest) (*cas.CASLoadResponse, error) {
	object, err := s.readObject(ctx, request.GetCasId())
	if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrCorrupt) {
		return &cas.CASLoadResponse{Outcome: cas.CASLoadResponse_OBJECT_NOT_FOUND}, nil
	}
	if err != nil {
		return nil, err
	}
	return &cas.CASLoadResponse{Outcome: cas.CASLoadResponse_SUCCESS, Contents: &cas.CASLoadResponse_Data{Data: &cas.CASBlob{Blob: object.Blob}}}, nil
}

func (s *Service) GetValue(ctx context.Context, request *kv.GetValueRequest) (*kv.GetValueResponse, error) {
	s.actionReads.Add(1)
	if len(request.GetKey()) == 0 || len(request.Key) > 8192 {
		return nil, status.Error(codes.InvalidArgument, "invalid compilation key")
	}
	id := sha256.Sum256(request.Key)
	data, err := s.get(ctx, "kv", id[:])
	if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrCorrupt) {
		return &kv.GetValueResponse{Outcome: kv.GetValueResponse_KEY_NOT_FOUND}, nil
	}
	if err != nil {
		return nil, err
	}
	value := new(kv.Value)
	if err = proto.Unmarshal(data, value); err != nil {
		return &kv.GetValueResponse{Outcome: kv.GetValueResponse_KEY_NOT_FOUND}, nil
	}
	s.actionHits.Add(1)
	return &kv.GetValueResponse{Outcome: kv.GetValueResponse_SUCCESS, Contents: &kv.GetValueResponse_Value{Value: value}}, nil
}

func (s *Service) PutValue(ctx context.Context, request *kv.PutValueRequest) (*kv.PutValueResponse, error) {
	if len(request.GetKey()) == 0 || len(request.Key) > 8192 || request.GetValue() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid compilation value")
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request.Value)
	if err != nil {
		return nil, err
	}
	id := sha256.Sum256(request.Key)
	if err = s.put(ctx, "kv", id[:], data); err != nil {
		// An immutable key collision must never replace previously cached outputs.
		return &kv.PutValueResponse{Error: &kv.ResponseError{Description: fmt.Sprintf("compilation cache write failed: %v", err)}}, nil
	}
	s.actionWrites.Add(1)
	return &kv.PutValueResponse{}, nil
}
