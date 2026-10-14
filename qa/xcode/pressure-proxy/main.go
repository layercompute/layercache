// This QA-only proxy delays a real compiler action hit while unrelated CAS
// writes force eviction. It never reads fixture objects through the CAS API.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/layercache/layercache/internal/xcodecache"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const messageLimit = 128 << 20
const maximumObservedObjects = 100000
const maximumObservedReferences = 262144

type observations struct {
	QuotaBytes                 int64    `json:"quotaBytes"`
	InitialUsageBytes          int64    `json:"initialUsageBytes"`
	MaxObservedUsageBytes      int64    `json:"maxObservedUsageBytes"`
	UsageAfterPressure         int64    `json:"usageAfterPressure"`
	PressurePayloadBytes       int64    `json:"pressurePayloadBytes"`
	PressureArtifactBytes      int64    `json:"pressureArtifactBytes"`
	PressureWrites             int      `json:"pressureWrites"`
	EvictedBytes               int64    `json:"evictedBytes"`
	PressureDurationNanos      int64    `json:"pressureDurationNanos"`
	QueryToRootFetchNanos      int64    `json:"queryToFirstRootFetchNanos"`
	QueryToFirstGraphReadNanos int64    `json:"queryToFirstPromisedReadNanos"`
	QueryToLastGraphReadNanos  int64    `json:"queryToLastPromisedReadNanos"`
	PromisedRoot               string   `json:"promisedRoot"`
	InlineActionRoot           bool     `json:"inlineActionRoot"`
	PromisedObjects            int      `json:"promisedObjects"`
	GraphObjectsInspected      int      `json:"graphObjectsInspected"`
	AvailablePromisedObjects   int      `json:"availablePromisedObjects"`
	CompilerGraphReads         int      `json:"compilerGraphReads"`
	CompilerRootReads          int      `json:"compilerRootReadsAfterPressure"`
	CompilerRootGets           int      `json:"compilerRootGetsAfterPressure"`
	FetchedPromisedObjects     int      `json:"fetchedPromisedObjects"`
	UnfetchedPromisedObjects   []string `json:"unfetchedPromisedObjects"`
	MissingPromisedObjects     []string `json:"missingPromisedObjects"`
	PressureError              string   `json:"pressureError,omitempty"`
}

type pressureProxy struct {
	cas.UnimplementedCASDBServiceServer
	kv.UnimplementedKeyValueDBServer
	objects        cas.CASDBServiceClient
	actions        kv.KeyValueDBClient
	db             *sql.DB
	storeRoot      string
	mu             sync.Mutex
	once           sync.Once
	graph          map[string]bool
	reads          map[string]int
	references     map[string][]string
	referenceCount int
	fetched        map[string]bool
	failed         map[string]bool
	root           string
	queried        time.Time
	finished       time.Time
	done           bool
	stats          observations
}

func newProxy(objects cas.CASDBServiceClient, actions kv.KeyValueDBClient, db *sql.DB, quota int64) *pressureProxy {
	return &pressureProxy{objects: objects, actions: actions, db: db, graph: make(map[string]bool), reads: make(map[string]int), references: make(map[string][]string), fetched: make(map[string]bool), failed: make(map[string]bool), stats: observations{QuotaBytes: quota}}
}

func (p *pressureProxy) usage(ctx context.Context) (int64, error) {
	var used int64
	err := p.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(size), 0) FROM artifacts").Scan(&used)
	return used, err
}

func (p *pressureProxy) applyPressure(ctx context.Context, roots []xcodecache.GraphReference, inline bool, queried time.Time) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	p.mu.Lock()
	p.root = string(roots[0].ID)
	p.queried = queried
	for _, root := range roots {
		p.graph[string(root.ID)] = true
	}
	p.stats.PromisedRoot = hex.EncodeToString(roots[0].ID)
	p.stats.InlineActionRoot = inline
	p.mu.Unlock()
	graph, err := p.inspectGraph(ctx, roots)
	if err == nil {
		p.mu.Lock()
		// The bounded read-only inspection establishes every promised edge,
		// including those omitted by Load responses. None are synthetic hits.
		p.graph = graph
		p.stats.GraphObjectsInspected = len(graph)
		p.mu.Unlock()
	}
	var initial int64
	if err == nil {
		initial, err = p.usage(ctx)
	}
	if err == nil {
		p.mu.Lock()
		p.stats.InitialUsageBytes = initial
		p.stats.MaxObservedUsageBytes = initial
		p.mu.Unlock()
		payloadSize := min(int64(16<<20), p.stats.QuotaBytes/4)
		if payloadSize < 16 || initial > p.stats.QuotaBytes {
			err = errors.New("invalid pressure quota or initial store usage")
		} else {
			var nonce [8]byte
			_, err = rand.Read(nonce[:])
			for write := 0; err == nil && int64(write)*payloadSize <= p.stats.QuotaBytes; write++ {
				data := make([]byte, payloadSize)
				copy(data, nonce[:])
				binary.LittleEndian.PutUint64(data[len(data)-8:], uint64(write))
				blob := &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: data}}
				var response *cas.CASSaveResponse
				response, err = p.objects.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob}})
				if err != nil {
					break
				}
				if response.GetError() != nil || len(response.GetCasId().GetId()) != 32 {
					err = errors.New("pressure Save returned no valid CAS ID")
					break
				}
				var used int64
				used, err = p.usage(ctx)
				if err != nil {
					break
				}
				p.mu.Lock()
				p.stats.PressureWrites++
				p.stats.PressurePayloadBytes += payloadSize
				p.stats.PressureArtifactBytes += int64(proto.Size(&cas.CASObject{Blob: blob}))
				p.stats.MaxObservedUsageBytes = max(p.stats.MaxObservedUsageBytes, used)
				p.stats.UsageAfterPressure = used
				p.mu.Unlock()
				if used > p.stats.QuotaBytes {
					err = fmt.Errorf("committed usage %d exceeds quota %d", used, p.stats.QuotaBytes)
					break
				}
			}
		}
	}
	if err == nil {
		var available map[string]bool
		available, err = p.inspectGraph(ctx, roots)
		if err == nil {
			if len(available) != len(graph) {
				err = errors.New("promised graph changed during pressure")
			}
			for id := range graph {
				if !available[id] {
					err = errors.New("promised graph lost an object during pressure")
				}
			}
			if err == nil {
				p.mu.Lock()
				p.stats.AvailablePromisedObjects = len(available)
				p.mu.Unlock()
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = true
	p.finished = time.Now()
	p.stats.PressureDurationNanos = time.Since(started).Nanoseconds()
	p.stats.EvictedBytes = initial + p.stats.PressureArtifactBytes - p.stats.UsageAfterPressure
	if err != nil {
		p.stats.PressureError = err.Error()
	}
}

func (p *pressureProxy) inspectGraph(ctx context.Context, roots []xcodecache.GraphReference) (map[string]bool, error) {
	graph := make(map[string]bool)
	queue := append([]xcodecache.GraphReference(nil), roots...)
	referencesChecked := len(roots)
	var bytesChecked int64
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		prior, known := graph[string(ref.ID)]
		if known && (prior || !ref.NativeNode) {
			continue
		}
		if len(ref.ID) != sha256.Size || (!known && len(graph) >= maximumObservedObjects) {
			return nil, errors.New("invalid or oversized promised graph")
		}
		graph[string(ref.ID)] = ref.NativeNode
		digest := hex.EncodeToString(ref.ID)
		var size int64
		if err := p.db.QueryRowContext(ctx, "SELECT size FROM artifacts WHERE digest = ?", digest).Scan(&size); err != nil {
			return nil, fmt.Errorf("inspect promised CAS metadata: %w", err)
		}
		if size < 0 || size > messageLimit || (!known && size > p.stats.QuotaBytes-bytesChecked) {
			return nil, errors.New("promised graph exceeds the inspection byte budget")
		}
		data, err := readInspectionBlob(filepath.Join(p.storeRoot, "blobs", "sha256", digest[:2], digest[2:]), size)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(data)
		if int64(len(data)) != size || hash != [32]byte(ref.ID) {
			return nil, errors.New("promised graph bytes do not match metadata and CAS identity")
		}
		var object cas.CASObject
		if err := proto.Unmarshal(data, &object); err != nil {
			return nil, err
		}
		children, err := xcodecache.ObjectReferences(ref, &object)
		if err != nil {
			return nil, err
		}
		if !known {
			bytesChecked += size
		}
		if len(children) > maximumObservedReferences-referencesChecked {
			return nil, errors.New("promised graph exceeds the inspection reference budget")
		}
		referencesChecked += len(children)
		queue = append(queue, children...)
	}
	// Every edge has now been inspected. Load-only compiler reads can use this
	// complete set without requiring a Get for an inline action root.
	for id := range graph {
		graph[id] = true
	}
	return graph, nil
}

func readInspectionBlob(path string, size int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return nil, errors.New("promised CAS file does not match its bounded metadata")
	}
	return io.ReadAll(io.LimitReader(file, size+1))
}

func (p *pressureProxy) recordRead(started time.Time, id []byte, success bool, references []*cas.CASDataID, get bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done || started.Before(p.finished) {
		return
	}
	identity := string(id)
	if _, known := p.reads[identity]; !known && len(p.reads) >= maximumObservedObjects {
		p.stats.PressureError = "compiler observations exceed the bounded object limit"
		return
	}
	p.reads[identity]++
	if p.graph[identity] {
		elapsed := time.Since(p.queried).Nanoseconds()
		if p.stats.QueryToFirstGraphReadNanos == 0 {
			p.stats.QueryToFirstGraphReadNanos = elapsed
		}
		p.stats.QueryToLastGraphReadNanos = elapsed
	}
	// A descendant can be fetched concurrently before its parent's response
	// reveals that edge. Remember successful post-pressure fetches by identity,
	// rather than use a repeatable RPC count as evidence of graph coverage.
	if success {
		p.fetched[identity] = true
	} else {
		p.failed[identity] = true
	}
	if !success {
		return
	}
	// Get replies can arrive before the parent which makes their object
	// reachable. Keep every successful adjacency list until the final closure
	// is known; discovering the parent must not discard a grandchild edge.
	if get {
		if _, known := p.references[identity]; !known {
			if len(references) > maximumObservedReferences-p.referenceCount {
				p.stats.PressureError = "compiler observations exceed the bounded reference limit"
				return
			}
			children := make([]string, len(references))
			for index, reference := range references {
				children[index] = string(reference.GetId())
			}
			p.references[identity] = children
			p.referenceCount += len(children)
		}
	}
	if identity == p.root {
		if p.stats.CompilerRootReads == 0 {
			p.stats.QueryToRootFetchNanos = time.Since(p.queried).Nanoseconds()
		}
		p.stats.CompilerRootReads++
		if get {
			p.stats.CompilerRootGets++
		}
	}
}

func (p *pressureProxy) Get(ctx context.Context, request *cas.CASGetRequest) (*cas.CASGetResponse, error) {
	started := time.Now()
	response, err := p.objects.Get(ctx, request)
	success := err == nil && response.GetOutcome() == cas.CASGetResponse_SUCCESS
	p.recordRead(started, request.GetCasId().GetId(), success, response.GetData().GetReferences(), true)
	return response, err
}

func (p *pressureProxy) Load(ctx context.Context, request *cas.CASLoadRequest) (*cas.CASLoadResponse, error) {
	started := time.Now()
	response, err := p.objects.Load(ctx, request)
	success := err == nil && response.GetOutcome() == cas.CASLoadResponse_SUCCESS
	p.recordRead(started, request.GetCasId().GetId(), success, nil, false)
	return response, err
}

func (p *pressureProxy) Put(ctx context.Context, request *cas.CASPutRequest) (*cas.CASPutResponse, error) {
	return p.objects.Put(ctx, request)
}

func (p *pressureProxy) Save(ctx context.Context, request *cas.CASSaveRequest) (*cas.CASSaveResponse, error) {
	return p.objects.Save(ctx, request)
}

func (p *pressureProxy) GetValue(ctx context.Context, request *kv.GetValueRequest) (*kv.GetValueResponse, error) {
	queried := time.Now()
	response, err := p.actions.GetValue(ctx, request)
	if err == nil && response.GetOutcome() == kv.GetValueResponse_SUCCESS {
		roots, graphError := xcodecache.ActionReferences(response.GetValue())
		if graphError != nil {
			return nil, status.Error(codes.Internal, "successful action contains an unsupported graph")
		}
		if len(roots) > 0 {
			_, relay := response.GetValue().GetEntries()["CASID"]
			inline := !relay
			p.once.Do(func() { p.applyPressure(ctx, roots, inline, queried) })
			p.mu.Lock()
			pressureError := p.stats.PressureError
			p.mu.Unlock()
			if pressureError != "" {
				return nil, status.Error(codes.Internal, pressureError)
			}
		}
	}
	return response, err
}

func (p *pressureProxy) PutValue(ctx context.Context, request *kv.PutValueRequest) (*kv.PutValueResponse, error) {
	return p.actions.PutValue(ctx, request)
}

func (p *pressureProxy) snapshot() observations {
	p.mu.Lock()
	defer p.mu.Unlock()
	stats := p.stats
	graph := make(map[string]bool)
	var queue []string
	for id := range p.graph {
		graph[id] = true
		queue = append(queue, id)
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		for _, child := range p.references[queue[cursor]] {
			if graph[child] {
				continue
			}
			if len(graph) >= maximumObservedObjects {
				stats.PressureError = "promised graph exceeds the bounded object limit"
				return stats
			}
			graph[child] = true
			queue = append(queue, child)
		}
	}
	stats.PromisedObjects = len(graph)
	stats.CompilerGraphReads = 0
	stats.UnfetchedPromisedObjects = []string{}
	stats.MissingPromisedObjects = []string{}
	for id := range graph {
		stats.CompilerGraphReads += p.reads[id]
		if p.failed[id] {
			stats.MissingPromisedObjects = append(stats.MissingPromisedObjects, hex.EncodeToString([]byte(id)))
		}
		if p.fetched[id] {
			stats.FetchedPromisedObjects++
		} else {
			stats.UnfetchedPromisedObjects = append(stats.UnfetchedPromisedObjects, hex.EncodeToString([]byte(id)))
		}
	}
	sort.Strings(stats.UnfetchedPromisedObjects)
	sort.Strings(stats.MissingPromisedObjects)
	return stats
}

func (s observations) validate() error {
	if s.PressureError != "" {
		return errors.New(s.PressureError)
	}
	if s.PromisedRoot == "" || s.CompilerGraphReads == 0 {
		return errors.New("real compiler did not read a promised CAS object after pressure")
	}
	if s.AvailablePromisedObjects != s.PromisedObjects || s.PromisedObjects == 0 || s.GraphObjectsInspected != s.PromisedObjects {
		return errors.New("complete promised graph was not verified available after pressure")
	}
	if s.QueryToFirstGraphReadNanos < s.PressureDurationNanos || s.QueryToLastGraphReadNanos >= int64(2*time.Minute) {
		return errors.New("compiler consumed a promised object outside the fixed fetch window")
	}
	if len(s.MissingPromisedObjects) != 0 {
		return fmt.Errorf("compiler lost %d promised CAS objects", len(s.MissingPromisedObjects))
	}
	if s.PressurePayloadBytes <= s.QuotaBytes || s.EvictedBytes <= 0 || s.MaxObservedUsageBytes > s.QuotaBytes {
		return errors.New("pressure did not prove bounded eviction")
	}
	return nil
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("pressure-proxy", flag.ContinueOnError)
	var socket, store, statsPath string
	var quota int64
	flags.StringVar(&socket, "socket", "", "private absolute proxy socket")
	flags.StringVar(&store, "store", "", "existing adapter store")
	flags.StringVar(&statsPath, "stats", "", "pressure evidence JSON")
	flags.Int64Var(&quota, "quota", 0, "candidate committed artifact quota")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := append([]string(nil), flags.Args()...)
	if len(command) == 0 || !filepath.IsAbs(socket) || len(socket) > 100 || quota < 64 || quota > 2<<30 || store == "" || statsPath == "" {
		return errors.New("socket, store, bounded quota, stats and compiler command are required")
	}
	const prefix = "COMPILATION_CACHE_REMOTE_SERVICE_PATH="
	var backend string
	for i, argument := range command {
		if strings.HasPrefix(argument, prefix) {
			backend = strings.TrimPrefix(argument, prefix)
			command[i] = prefix + socket
		}
	}
	if backend == "" || backend == socket {
		return errors.New("adapter must supply a separate backend socket")
	}
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("proxy socket directory must be private")
	}
	connection, err := grpc.NewClient("unix://"+backend, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(messageLimit), grpc.MaxCallSendMsgSize(messageLimit)))
	if err != nil {
		return err
	}
	defer connection.Close()
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(store, "metadata.db")}
	databaseURL.RawQuery = "mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return err
	}
	defer db.Close()
	proxy := newProxy(cas.NewCASDBServiceClient(connection), kv.NewKeyValueDBClient(connection), db, quota)
	proxy.storeRoot = store
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(messageLimit), grpc.MaxSendMsgSize(messageLimit), grpc.WaitForHandlers(true))
	cas.RegisterCASDBServiceServer(server, proxy)
	kv.RegisterKeyValueDBServer(server, proxy)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.WaitDelay = 5 * time.Second
	childError := child.Run()
	server.Stop()
	<-done
	stats := proxy.snapshot()
	encoded, err := json.MarshalIndent(stats, "", "  ")
	if err == nil {
		err = os.WriteFile(statsPath, append(encoded, '\n'), 0600)
	}
	if childError != nil {
		return childError
	}
	if err != nil {
		return err
	}
	return stats.validate()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Xcode pressure qualification:", err)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
