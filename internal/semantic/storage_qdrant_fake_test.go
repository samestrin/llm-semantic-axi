package semantic

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file stands up an in-process gRPC server that speaks enough of the
// Qdrant wire protocol for QdrantStorage to drive it end to end: collections,
// point upsert/get/delete/count/scroll/query and payload updates. It keeps its
// points in a map, so no external service and no network beyond a loopback
// listener is involved. Filter evaluation, cosine scoring and scroll paging
// are modelled on the real server so the assertions below pin real behaviour
// rather than the shape of a stub.

// fakePoint is one stored point: its id, dense vector and payload.
type fakePoint struct {
	id      *qdrant.PointId
	vector  []float32
	payload map[string]*qdrant.Value
	seq     uint64 // insertion order, used to make scroll paging deterministic
}

type fakeCollection struct {
	dim    uint64
	points map[string]*fakePoint
}

// fakeQdrant implements the three Qdrant gRPC services QdrantStorage calls.
type fakeQdrant struct {
	qdrant.UnimplementedQdrantServer
	qdrant.UnimplementedCollectionsServer

	mu          sync.Mutex
	seq         uint64
	collections map[string]*fakeCollection

	// failRPC makes a named RPC return an error instead of a result, so the
	// error-wrapping branches in QdrantStorage can be reached.
	failRPC map[string]error

	// calls counts RPCs by name so tests can assert on request construction
	// (e.g. that a large batch really was split into several Upsert calls).
	calls map[string]int

	// lastScrollLimit records the Limit of the most recent Scroll request.
	lastScrollLimit uint32
	// lastQueryLimit records the Limit of the most recent Query request.
	lastQueryLimit uint64
}

func newFakeQdrant() *fakeQdrant {
	return &fakeQdrant{
		collections: make(map[string]*fakeCollection),
		failRPC:     make(map[string]error),
		calls:       make(map[string]int),
	}
}

// setFailure arms one RPC name to fail with the given gRPC error.
func (f *fakeQdrant) setFailure(rpc string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failRPC[rpc] = err
}

func (f *fakeQdrant) clearFailure(rpc string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failRPC, rpc)
}

func (f *fakeQdrant) callCount(rpc string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[rpc]
}

// enter records the call and reports an armed failure. Callers hold no lock.
func (f *fakeQdrant) enter(rpc string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[rpc]++
	return f.failRPC[rpc]
}

// pointCount reports how many points a collection holds (0 if absent).
func (f *fakeQdrant) pointCount(collection string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.collections[collection]
	if !ok {
		return 0
	}
	return len(c.points)
}

// payloadOf returns a copy of one stored point's payload, or nil.
func (f *fakeQdrant) payloadOf(collection, id string) map[string]*qdrant.Value {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.collections[collection]
	if !ok {
		return nil
	}
	p, ok := c.points[id]
	if !ok {
		return nil
	}
	return clonePayload(p.payload)
}

// hasCollection reports whether the named collection currently exists.
func (f *fakeQdrant) hasCollection(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.collections[name]
	return ok
}

// collectionDim returns the vector size the collection was created with.
func (f *fakeQdrant) collectionDim(name string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.collections[name]
	if !ok {
		return 0
	}
	return c.dim
}

// ===== Qdrant service =====

func (f *fakeQdrant) HealthCheck(context.Context, *qdrant.HealthCheckRequest) (*qdrant.HealthCheckReply, error) {
	return &qdrant.HealthCheckReply{Title: "qdrant-fake", Version: "1.16.0"}, nil
}

// ===== Collections service =====

func (f *fakeQdrant) List(_ context.Context, _ *qdrant.ListCollectionsRequest) (*qdrant.ListCollectionsResponse, error) {
	if err := f.enter("ListCollections"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	names := make([]string, 0, len(f.collections))
	for name := range f.collections {
		names = append(names, name)
	}
	sort.Strings(names)

	descs := make([]*qdrant.CollectionDescription, 0, len(names))
	for _, name := range names {
		descs = append(descs, &qdrant.CollectionDescription{Name: name})
	}
	return &qdrant.ListCollectionsResponse{Collections: descs}, nil
}

func (f *fakeQdrant) Create(_ context.Context, req *qdrant.CreateCollection) (*qdrant.CollectionOperationResponse, error) {
	if err := f.enter("CreateCollection"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	name := req.GetCollectionName()
	if _, exists := f.collections[name]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "collection %s already exists", name)
	}
	var dim uint64
	if params := req.GetVectorsConfig().GetParams(); params != nil {
		dim = params.GetSize()
	}
	f.collections[name] = &fakeCollection{dim: dim, points: make(map[string]*fakePoint)}
	return &qdrant.CollectionOperationResponse{Result: true}, nil
}

func (f *fakeQdrant) Delete(_ context.Context, req *qdrant.DeleteCollection) (*qdrant.CollectionOperationResponse, error) {
	if err := f.enter("DeleteCollection"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	name := req.GetCollectionName()
	if _, exists := f.collections[name]; !exists {
		return &qdrant.CollectionOperationResponse{Result: false}, nil
	}
	delete(f.collections, name)
	return &qdrant.CollectionOperationResponse{Result: true}, nil
}

func (f *fakeQdrant) Get(_ context.Context, req *qdrant.GetCollectionInfoRequest) (*qdrant.GetCollectionInfoResponse, error) {
	if err := f.enter("GetCollectionInfo"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	c, ok := f.collections[req.GetCollectionName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "collection %s not found", req.GetCollectionName())
	}
	count := uint64(len(c.points))
	return &qdrant.GetCollectionInfoResponse{
		Result: &qdrant.CollectionInfo{PointsCount: &count},
	}, nil
}

// ===== Points service =====

func (f *fakeQdrant) Upsert(_ context.Context, req *qdrant.UpsertPoints) (*qdrant.PointsOperationResponse, error) {
	if err := f.enter("Upsert"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}
	for _, p := range req.GetPoints() {
		key := pointKey(p.GetId())
		seq := f.seq
		if existing, ok := c.points[key]; ok {
			seq = existing.seq // keep stable ordering across overwrites
		} else {
			f.seq++
		}
		c.points[key] = &fakePoint{
			id:      p.GetId(),
			vector:  append([]float32(nil), denseOf(p.GetVectors().GetVector())...),
			payload: clonePayload(p.GetPayload()),
			seq:     seq,
		}
	}
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed}}, nil
}

// getPoints is the Points service's Get. It shares a name with the Collections
// service's Get, so the two services cannot be served by the same receiver
// method — fakePointsService wraps this one.
func (f *fakeQdrant) getPoints(_ context.Context, req *qdrant.GetPoints) (*qdrant.GetResponse, error) {
	if err := f.enter("GetPoints"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}

	var result []*qdrant.RetrievedPoint
	for _, id := range req.GetIds() {
		p, ok := c.points[pointKey(id)]
		if !ok {
			continue
		}
		result = append(result, &qdrant.RetrievedPoint{
			Id:      p.id,
			Payload: selectPayload(p.payload, req.GetWithPayload()),
			Vectors: selectVectors(p.vector, req.GetWithVectors()),
		})
	}
	return &qdrant.GetResponse{Result: result}, nil
}

// deletePoints is the Points service's Delete (see getPoints for the naming).
func (f *fakeQdrant) deletePoints(_ context.Context, req *qdrant.DeletePoints) (*qdrant.PointsOperationResponse, error) {
	if err := f.enter("DeletePoints"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}

	switch sel := req.GetPoints().GetPointsSelectorOneOf().(type) {
	case *qdrant.PointsSelector_Points:
		for _, id := range sel.Points.GetIds() {
			delete(c.points, pointKey(id))
		}
	case *qdrant.PointsSelector_Filter:
		for key, p := range c.points {
			if matchFilter(sel.Filter, p) {
				delete(c.points, key)
			}
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "delete requires a points selector")
	}
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed}}, nil
}

func (f *fakeQdrant) Count(_ context.Context, req *qdrant.CountPoints) (*qdrant.CountResponse, error) {
	if err := f.enter("Count"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}
	var n uint64
	for _, p := range c.points {
		if matchFilter(req.GetFilter(), p) {
			n++
		}
	}
	return &qdrant.CountResponse{Result: &qdrant.CountResult{Count: n}}, nil
}

func (f *fakeQdrant) SetPayload(_ context.Context, req *qdrant.SetPayloadPoints) (*qdrant.PointsOperationResponse, error) {
	if err := f.enter("SetPayload"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}

	apply := func(p *fakePoint) {
		if p.payload == nil {
			p.payload = make(map[string]*qdrant.Value)
		}
		for k, v := range req.GetPayload() {
			p.payload[k] = v
		}
	}

	switch sel := req.GetPointsSelector().GetPointsSelectorOneOf().(type) {
	case *qdrant.PointsSelector_Points:
		for _, id := range sel.Points.GetIds() {
			if p, ok := c.points[pointKey(id)]; ok {
				apply(p)
			}
		}
	case *qdrant.PointsSelector_Filter:
		for _, p := range c.points {
			if matchFilter(sel.Filter, p) {
				apply(p)
			}
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "set payload requires a points selector")
	}
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed}}, nil
}

// Scroll pages through points in insertion order. The Offset is the id to
// start at (inclusive), matching Qdrant, and NextPageOffset is the id of the
// first point that did not fit in the page.
func (f *fakeQdrant) Scroll(_ context.Context, req *qdrant.ScrollPoints) (*qdrant.ScrollResponse, error) {
	if err := f.enter("Scroll"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastScrollLimit = req.GetLimit()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}

	ordered := f.orderedLocked(c)

	// Honour the start offset.
	start := 0
	if off := req.GetOffset(); off != nil {
		want := pointKey(off)
		for i, p := range ordered {
			if pointKey(p.id) == want {
				start = i
				break
			}
		}
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 10
	}

	var result []*qdrant.RetrievedPoint
	var next *qdrant.PointId
	for i := start; i < len(ordered); i++ {
		p := ordered[i]
		if !matchFilter(req.GetFilter(), p) {
			continue
		}
		if len(result) == limit {
			next = p.id
			break
		}
		result = append(result, &qdrant.RetrievedPoint{
			Id:      p.id,
			Payload: selectPayload(p.payload, req.GetWithPayload()),
			Vectors: selectVectors(p.vector, req.GetWithVectors()),
		})
	}

	return &qdrant.ScrollResponse{Result: result, NextPageOffset: next}, nil
}

// Query answers a dense nearest-neighbour query with cosine similarity.
func (f *fakeQdrant) Query(_ context.Context, req *qdrant.QueryPoints) (*qdrant.QueryResponse, error) {
	if err := f.enter("Query"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastQueryLimit = req.GetLimit()

	c, err := f.collectionLocked(req.GetCollectionName())
	if err != nil {
		return nil, err
	}

	query := req.GetQuery().GetNearest().GetDense().GetData()

	type scored struct {
		p     *fakePoint
		score float32
	}
	var hits []scored
	for _, p := range f.orderedLocked(c) {
		if !matchFilter(req.GetFilter(), p) {
			continue
		}
		s := cosine(query, p.vector)
		if req.ScoreThreshold != nil && s < req.GetScoreThreshold() {
			continue
		}
		hits = append(hits, scored{p: p, score: s})
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 10
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}

	result := make([]*qdrant.ScoredPoint, 0, len(hits))
	for _, h := range hits {
		result = append(result, &qdrant.ScoredPoint{
			Id:      h.p.id,
			Payload: selectPayload(h.p.payload, req.GetWithPayload()),
			Vectors: selectVectors(h.p.vector, req.GetWithVectors()),
			Score:   h.score,
		})
	}
	return &qdrant.QueryResponse{Result: result}, nil
}

// collectionLocked resolves a collection or reports NotFound like Qdrant does.
// The caller must hold f.mu.
func (f *fakeQdrant) collectionLocked(name string) (*fakeCollection, error) {
	c, ok := f.collections[name]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "collection %s not found", name)
	}
	return c, nil
}

// orderedLocked returns the collection's points in insertion order.
func (f *fakeQdrant) orderedLocked(c *fakeCollection) []*fakePoint {
	out := make([]*fakePoint, 0, len(c.points))
	for _, p := range c.points {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// ===== service adapters =====
//
// The Collections and Points services both declare Get and Delete, so one Go
// type cannot implement both directly. These thin wrappers give the Points
// service its own receiver while sharing the single fakeQdrant state.

type fakePointsService struct {
	qdrant.UnimplementedPointsServer
	f *fakeQdrant
}

func (s *fakePointsService) Upsert(ctx context.Context, req *qdrant.UpsertPoints) (*qdrant.PointsOperationResponse, error) {
	return s.f.Upsert(ctx, req)
}

func (s *fakePointsService) Get(ctx context.Context, req *qdrant.GetPoints) (*qdrant.GetResponse, error) {
	return s.f.getPoints(ctx, req)
}

func (s *fakePointsService) Delete(ctx context.Context, req *qdrant.DeletePoints) (*qdrant.PointsOperationResponse, error) {
	return s.f.deletePoints(ctx, req)
}

func (s *fakePointsService) Count(ctx context.Context, req *qdrant.CountPoints) (*qdrant.CountResponse, error) {
	return s.f.Count(ctx, req)
}

func (s *fakePointsService) SetPayload(ctx context.Context, req *qdrant.SetPayloadPoints) (*qdrant.PointsOperationResponse, error) {
	return s.f.SetPayload(ctx, req)
}

func (s *fakePointsService) Scroll(ctx context.Context, req *qdrant.ScrollPoints) (*qdrant.ScrollResponse, error) {
	return s.f.Scroll(ctx, req)
}

func (s *fakePointsService) Query(ctx context.Context, req *qdrant.QueryPoints) (*qdrant.QueryResponse, error) {
	return s.f.Query(ctx, req)
}

// ===== helpers =====

func pointKey(id *qdrant.PointId) string {
	if id == nil {
		return ""
	}
	if u := id.GetUuid(); u != "" {
		return u
	}
	return fmt.Sprintf("num:%d", id.GetNum())
}

func clonePayload(in map[string]*qdrant.Value) map[string]*qdrant.Value {
	if in == nil {
		return nil
	}
	out := make(map[string]*qdrant.Value, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func selectPayload(payload map[string]*qdrant.Value, sel *qdrant.WithPayloadSelector) map[string]*qdrant.Value {
	if sel == nil || payload == nil {
		return nil
	}
	switch s := sel.GetSelectorOptions().(type) {
	case *qdrant.WithPayloadSelector_Enable:
		if !s.Enable {
			return nil
		}
		return clonePayload(payload)
	case *qdrant.WithPayloadSelector_Include:
		out := make(map[string]*qdrant.Value)
		for _, field := range s.Include.GetFields() {
			if v, ok := payload[field]; ok {
				out[field] = v
			}
		}
		return out
	case *qdrant.WithPayloadSelector_Exclude:
		drop := make(map[string]bool)
		for _, field := range s.Exclude.GetFields() {
			drop[field] = true
		}
		out := make(map[string]*qdrant.Value)
		for k, v := range payload {
			if !drop[k] {
				out[k] = v
			}
		}
		return out
	}
	return nil
}

// denseOf pulls the dense component out of an inbound vector. Newer clients
// send it under the Dense oneof; the flat Data field is the deprecated form.
func denseOf(v *qdrant.Vector) []float32 {
	if v == nil {
		return nil
	}
	if d := v.GetDense(); d != nil {
		return d.GetData()
	}
	return v.GetData()
}

// selectVectors renders a stored vector the way Qdrant does when vectors are
// requested: the deprecated flat Data field and the Dense oneof both carry it.
func selectVectors(vec []float32, sel *qdrant.WithVectorsSelector) *qdrant.VectorsOutput {
	if sel == nil {
		return nil
	}
	enable, ok := sel.GetSelectorOptions().(*qdrant.WithVectorsSelector_Enable)
	if !ok || !enable.Enable {
		return nil
	}
	copied := append([]float32(nil), vec...)
	return &qdrant.VectorsOutput{
		VectorsOptions: &qdrant.VectorsOutput_Vector{
			Vector: &qdrant.VectorOutput{
				Data:   copied,
				Vector: &qdrant.VectorOutput_Dense{Dense: &qdrant.DenseVector{Data: copied}},
			},
		},
	}
}

func matchFilter(f *qdrant.Filter, p *fakePoint) bool {
	if f == nil {
		return true
	}
	for _, c := range f.GetMust() {
		if !matchCondition(c, p) {
			return false
		}
	}
	for _, c := range f.GetMustNot() {
		if matchCondition(c, p) {
			return false
		}
	}
	if should := f.GetShould(); len(should) > 0 {
		any := false
		for _, c := range should {
			if matchCondition(c, p) {
				any = true
				break
			}
		}
		if !any {
			return false
		}
	}
	return true
}

func matchCondition(c *qdrant.Condition, p *fakePoint) bool {
	switch v := c.GetConditionOneOf().(type) {
	case *qdrant.Condition_Field:
		return matchFieldCondition(v.Field, p)
	case *qdrant.Condition_HasId:
		want := pointKey(p.id)
		for _, id := range v.HasId.GetHasId() {
			if pointKey(id) == want {
				return true
			}
		}
		return false
	case *qdrant.Condition_Filter:
		return matchFilter(v.Filter, p)
	}
	return false
}

func matchFieldCondition(fc *qdrant.FieldCondition, p *fakePoint) bool {
	val, ok := p.payload[fc.GetKey()]
	if !ok {
		return false
	}
	switch m := fc.GetMatch().GetMatchValue().(type) {
	case *qdrant.Match_Keyword:
		return val.GetStringValue() == m.Keyword
	case *qdrant.Match_Text:
		return strings.Contains(val.GetStringValue(), m.Text)
	case *qdrant.Match_Integer:
		return val.GetIntegerValue() == m.Integer
	case *qdrant.Match_Boolean:
		return val.GetBoolValue() == m.Boolean
	}
	return false
}

// cosine returns the cosine similarity of two vectors, 0 when either is zero.
func cosine(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
	}
	for _, v := range a {
		na += float64(v) * float64(v)
	}
	for _, v := range b {
		nb += float64(v) * float64(v)
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// startFakeQdrant serves the fake on a loopback port and returns its http URL.
func startFakeQdrant(t *testing.T) (*fakeQdrant, string) {
	t.Helper()

	fq := newFakeQdrant()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	qdrant.RegisterQdrantServer(srv, fq)
	qdrant.RegisterCollectionsServer(srv, fq)
	qdrant.RegisterPointsServer(srv, &fakePointsService{f: fq})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	return fq, "http://" + lis.Addr().String()
}

// newFakeQdrantStorage wires a QdrantStorage to a fresh fake server, with its
// parallel FTS index on disk in the test's temp dir.
func newFakeQdrantStorage(t *testing.T, collection string, dim int) (*QdrantStorage, *fakeQdrant) {
	t.Helper()

	fq, url := startFakeQdrant(t)
	storage, err := NewQdrantStorage(QdrantConfig{
		APIKey:         "test-key",
		URL:            url,
		CollectionName: collection,
		EmbeddingDim:   dim,
		FTSDataDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewQdrantStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return storage, fq
}
