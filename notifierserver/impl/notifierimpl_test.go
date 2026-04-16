package impl

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pb "github.com/aldelo/connector/notifierserver/proto"
)

// ---------------------------------------------------------------------------
// validateServerURL
// ---------------------------------------------------------------------------

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string // substring expected in error
	}{
		{name: "valid https", url: "https://example.com", wantErr: false},
		{name: "valid http", url: "http://example.com:8080/path", wantErr: false},
		{name: "valid https with path", url: "https://host.internal/callback/key", wantErr: false},
		{name: "empty string", url: "", wantErr: true, errMsg: "server URL is required"},
		{name: "whitespace only", url: "   ", wantErr: true, errMsg: "server URL is required"},
		{name: "no scheme", url: "example.com", wantErr: true, errMsg: "http or https scheme"},
		{name: "ftp scheme", url: "ftp://example.com", wantErr: true, errMsg: "http or https scheme"},
		{name: "grpc scheme", url: "grpc://example.com", wantErr: true, errMsg: "http or https scheme"},
		{name: "scheme only no host", url: "http://", wantErr: true, errMsg: "must have a host"},
		{name: "leading whitespace trimmed valid", url: "  https://example.com", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServerURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if tt.errMsg != "" && !containsSubstring(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// clientEndpoint.safeClose — idempotency
// ---------------------------------------------------------------------------

func TestSafeClose_NilReceiver(t *testing.T) {
	// Must not panic
	var ep *clientEndpoint
	ep.safeClose()
}

func TestSafeClose_Idempotent(t *testing.T) {
	ep := &clientEndpoint{
		ClientId:   "close-test",
		DataToSend: make(chan *pb.NotificationData, 1),
	}

	ep.safeClose()
	// Second call must not panic (double-close on raw chan would panic)
	ep.safeClose()

	if !ep.closed {
		t.Fatal("expected closed flag to be true after safeClose")
	}
}

func TestSafeClose_ClosesChannel(t *testing.T) {
	ch := make(chan *pb.NotificationData, 1)
	ep := &clientEndpoint{
		ClientId:   "close-verify",
		DataToSend: ch,
	}

	ep.safeClose()

	// Verify channel is actually closed — receive returns zero value + ok=false
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed, but receive succeeded")
		}
	default:
		t.Fatal("expected channel to be closed, but receive would block")
	}
}

// ---------------------------------------------------------------------------
// clientEndpoint.safeSend
// ---------------------------------------------------------------------------

func TestSafeSend_NilReceiver(t *testing.T) {
	var ep *clientEndpoint
	got := ep.safeSend(&pb.NotificationData{Message: "x"}, time.Second)
	if got {
		t.Fatal("expected false from nil receiver")
	}
}

func TestSafeSend_SuccessfulSend(t *testing.T) {
	ep := &clientEndpoint{
		ClientId:   "send-ok",
		DataToSend: make(chan *pb.NotificationData, 1),
	}
	data := &pb.NotificationData{Message: "hello"}

	if !ep.safeSend(data, time.Second) {
		t.Fatal("expected safeSend to return true for buffered channel with capacity")
	}

	got := <-ep.DataToSend
	if got.Message != "hello" {
		t.Fatalf("expected message 'hello', got %q", got.Message)
	}
}

func TestSafeSend_ClosedChannel(t *testing.T) {
	ep := &clientEndpoint{
		ClientId:   "send-closed",
		DataToSend: make(chan *pb.NotificationData, 1),
	}
	ep.safeClose()

	// safeSend on a closed channel must return false, not panic
	got := ep.safeSend(&pb.NotificationData{Message: "x"}, time.Second)
	if got {
		t.Fatal("expected false when channel is closed via safeClose")
	}
}

func TestSafeSend_Timeout(t *testing.T) {
	// Unbuffered channel, no reader — must timeout
	ep := &clientEndpoint{
		ClientId:   "send-timeout",
		DataToSend: make(chan *pb.NotificationData),
	}

	start := time.Now()
	got := ep.safeSend(&pb.NotificationData{Message: "x"}, 50*time.Millisecond)
	elapsed := time.Since(start)

	if got {
		t.Fatal("expected false on timeout")
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected at least ~50ms delay, got %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// clientEndpointMap.Add — basic and validation
// ---------------------------------------------------------------------------

func newMap() *clientEndpointMap {
	return &clientEndpointMap{}
}

func makeEP(clientId, topicArn string) *clientEndpoint {
	return &clientEndpoint{
		ClientId: clientId,
		TopicArn: topicArn,
	}
}

func TestAdd_NilEndpoint(t *testing.T) {
	m := newMap()
	if m.Add(nil) {
		t.Fatal("expected Add(nil) to return false")
	}
}

func TestAdd_EmptyClientId(t *testing.T) {
	m := newMap()
	if m.Add(makeEP("", "arn:topic")) {
		t.Fatal("expected Add with empty ClientId to return false")
	}
}

func TestAdd_WhitespaceClientId(t *testing.T) {
	m := newMap()
	if m.Add(makeEP("   ", "arn:topic")) {
		t.Fatal("expected Add with whitespace-only ClientId to return false")
	}
}

func TestAdd_EmptyTopicArn(t *testing.T) {
	m := newMap()
	if m.Add(makeEP("client-1", "")) {
		t.Fatal("expected Add with empty TopicArn to return false")
	}
}

func TestAdd_Success(t *testing.T) {
	m := newMap()
	ep := makeEP("client-1", "arn:aws:sns:us-east-1:123:Topic1")
	if !m.Add(ep) {
		t.Fatal("expected Add to return true")
	}
	if ep.DataToSend == nil {
		t.Fatal("expected DataToSend channel to be initialized by Add")
	}
	if m.ClientsCount() != 1 {
		t.Fatalf("expected ClientsCount=1, got %d", m.ClientsCount())
	}
}

func TestAdd_InitializesChannel(t *testing.T) {
	m := newMap()
	ep := makeEP("chan-test", "arn:topic")
	m.Add(ep)

	// Channel must have dataChannelBufferSize capacity
	if cap(ep.DataToSend) != dataChannelBufferSize {
		t.Fatalf("expected channel buffer size %d, got %d", dataChannelBufferSize, cap(ep.DataToSend))
	}
}

func TestAdd_NilClientsMapInitialized(t *testing.T) {
	m := &clientEndpointMap{} // Clients is nil
	ep := makeEP("init-test", "arn:topic")
	if !m.Add(ep) {
		t.Fatal("expected Add to succeed with nil Clients map")
	}
	if m.Clients == nil {
		t.Fatal("expected Clients map to be initialized after Add")
	}
}

// ---------------------------------------------------------------------------
// Duplicate ClientId rejection
// ---------------------------------------------------------------------------

func TestAdd_DuplicateClientIdRejected(t *testing.T) {
	m := newMap()
	ep1 := makeEP("dup-client", "arn:topic1")
	ep2 := makeEP("dup-client", "arn:topic2")

	if !m.Add(ep1) {
		t.Fatal("first Add should succeed")
	}
	if m.Add(ep2) {
		t.Fatal("second Add with same ClientId should be rejected")
	}
	if m.ClientsCount() != 1 {
		t.Fatalf("expected ClientsCount=1 after duplicate rejection, got %d", m.ClientsCount())
	}
}

func TestAdd_DuplicateClientIdCaseInsensitive(t *testing.T) {
	m := newMap()
	ep1 := makeEP("Client-ABC", "arn:topic1")
	ep2 := makeEP("client-abc", "arn:topic2")

	if !m.Add(ep1) {
		t.Fatal("first Add should succeed")
	}
	if m.Add(ep2) {
		t.Fatal("second Add with case-different ClientId should be rejected")
	}
}

// ---------------------------------------------------------------------------
// Case-insensitive topic normalization
// ---------------------------------------------------------------------------

func TestAdd_TopicNormalizedToLowercase(t *testing.T) {
	m := newMap()
	ep := makeEP("norm-test", "ARN:AWS:SNS:US-EAST-1:123:MyTopic")
	m.Add(ep)

	// The TopicArn on the endpoint itself should be lowercased
	if ep.TopicArn != "arn:aws:sns:us-east-1:123:mytopic" {
		t.Fatalf("expected TopicArn to be lowercased, got %q", ep.TopicArn)
	}

	// Map key must be the lowercase version
	if m.TopicsCount() != 1 {
		t.Fatalf("expected 1 topic, got %d", m.TopicsCount())
	}
}

func TestAdd_SameTopicDifferentCase(t *testing.T) {
	m := newMap()
	ep1 := makeEP("client-1", "arn:aws:sns:us-east-1:123:Topic")
	ep2 := makeEP("client-2", "ARN:AWS:SNS:US-EAST-1:123:TOPIC")

	m.Add(ep1)
	m.Add(ep2)

	// Both should end up under the same topic key
	if m.TopicsCount() != 1 {
		t.Fatalf("expected 1 topic (case-normalized), got %d", m.TopicsCount())
	}
	if m.ClientsCount() != 2 {
		t.Fatalf("expected 2 clients under same topic, got %d", m.ClientsCount())
	}
}

// ---------------------------------------------------------------------------
// GetByClientId
// ---------------------------------------------------------------------------

func TestGetByClientId_Empty(t *testing.T) {
	m := newMap()
	if m.GetByClientId("") != nil {
		t.Fatal("expected nil for empty clientId")
	}
}

func TestGetByClientId_NotFound(t *testing.T) {
	m := newMap()
	m.Add(makeEP("exists", "arn:topic"))
	if m.GetByClientId("does-not-exist") != nil {
		t.Fatal("expected nil for non-existent clientId")
	}
}

func TestGetByClientId_Found(t *testing.T) {
	m := newMap()
	m.Add(makeEP("find-me", "arn:topic"))
	ep := m.GetByClientId("find-me")
	if ep == nil {
		t.Fatal("expected to find client")
	}
	if ep.ClientId != "find-me" {
		t.Fatalf("expected ClientId 'find-me', got %q", ep.ClientId)
	}
}

func TestGetByClientId_CaseInsensitive(t *testing.T) {
	m := newMap()
	m.Add(makeEP("CaseTest", "arn:topic"))
	ep := m.GetByClientId("casetest")
	if ep == nil {
		t.Fatal("expected case-insensitive lookup to find client")
	}
}

// ---------------------------------------------------------------------------
// GetByTopicArn
// ---------------------------------------------------------------------------

func TestGetByTopicArn_Empty(t *testing.T) {
	m := newMap()
	list := m.GetByTopicArn("")
	if len(list) != 0 {
		t.Fatal("expected empty list for empty topicArn")
	}
}

func TestGetByTopicArn_NotFound(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic-a"))
	list := m.GetByTopicArn("arn:topic-b")
	if len(list) != 0 {
		t.Fatalf("expected empty list for non-existent topic, got %d", len(list))
	}
}

func TestGetByTopicArn_Found(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic-a"))
	m.Add(makeEP("c2", "arn:topic-a"))

	list := m.GetByTopicArn("arn:topic-a")
	if len(list) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(list))
	}
}

func TestGetByTopicArn_CaseInsensitive(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic-lower"))
	list := m.GetByTopicArn("ARN:TOPIC-LOWER")
	if len(list) != 1 {
		t.Fatalf("expected case-insensitive topic lookup to return 1, got %d", len(list))
	}
}

func TestGetByTopicArn_ReturnsCopy(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic"))
	list := m.GetByTopicArn("arn:topic")

	// Mutating the returned slice should not affect the internal state
	list[0] = nil
	list2 := m.GetByTopicArn("arn:topic")
	if list2[0] == nil {
		t.Fatal("GetByTopicArn should return a copy — internal state was mutated")
	}
}

// ---------------------------------------------------------------------------
// RemoveByClientId
// ---------------------------------------------------------------------------

func TestRemoveByClientId_Empty(t *testing.T) {
	m := newMap()
	if m.RemoveByClientId("") {
		t.Fatal("expected false for empty clientId")
	}
}

func TestRemoveByClientId_NilMap(t *testing.T) {
	m := newMap() // Clients is nil
	if m.RemoveByClientId("any") {
		t.Fatal("expected false when Clients is nil")
	}
}

func TestRemoveByClientId_NotFound(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic"))
	if m.RemoveByClientId("nonexistent") {
		t.Fatal("expected false for non-existent clientId")
	}
}

func TestRemoveByClientId_Found(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic"))
	m.Add(makeEP("c2", "arn:topic"))

	if !m.RemoveByClientId("c1") {
		t.Fatal("expected RemoveByClientId to return true")
	}
	if m.ClientsCount() != 1 {
		t.Fatalf("expected ClientsCount=1 after removal, got %d", m.ClientsCount())
	}
	if m.GetByClientId("c1") != nil {
		t.Fatal("expected c1 to be gone after removal")
	}
	if m.GetByClientId("c2") == nil {
		t.Fatal("expected c2 to still exist")
	}
}

func TestRemoveByClientId_LastInTopic_RemovesTopic(t *testing.T) {
	m := newMap()
	m.Add(makeEP("only-client", "arn:topic-remove"))

	if !m.RemoveByClientId("only-client") {
		t.Fatal("expected removal to succeed")
	}
	if m.TopicsCount() != 0 {
		t.Fatalf("expected topic to be removed when last client leaves, got %d topics", m.TopicsCount())
	}
}

func TestRemoveByClientId_CaseInsensitive(t *testing.T) {
	m := newMap()
	m.Add(makeEP("RemoveMe", "arn:topic"))
	if !m.RemoveByClientId("removeme") {
		t.Fatal("expected case-insensitive removal to succeed")
	}
	if m.ClientsCount() != 0 {
		t.Fatalf("expected 0 clients after removal, got %d", m.ClientsCount())
	}
}

func TestRemoveByClientId_ClosesChannel(t *testing.T) {
	m := newMap()
	ep := makeEP("close-on-remove", "arn:topic")
	m.Add(ep)
	ch := ep.DataToSend // capture before removal

	m.RemoveByClientId("close-on-remove")

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after RemoveByClientId")
		}
	default:
		t.Fatal("expected channel to be closed, but it would block")
	}
}

func TestRemoveByClientId_CancelsFuncIfSet(t *testing.T) {
	m := newMap()
	ctx, cancel := context.WithCancel(context.Background())
	ep := &clientEndpoint{
		ClientId:      "cancel-test",
		TopicArn:      "arn:topic",
		ClientContext: ctx,
		cancelFunc:    cancel,
	}
	m.Add(ep)
	m.RemoveByClientId("cancel-test")

	select {
	case <-ctx.Done():
		// Context was cancelled as expected
	default:
		t.Fatal("expected context to be cancelled after RemoveByClientId")
	}
}

// ---------------------------------------------------------------------------
// RemoveByTopicArn
// ---------------------------------------------------------------------------

func TestRemoveByTopicArn_Empty(t *testing.T) {
	m := newMap()
	if m.RemoveByTopicArn("") {
		t.Fatal("expected false for empty topicArn")
	}
}

func TestRemoveByTopicArn_NotFound(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic-a"))
	if m.RemoveByTopicArn("arn:topic-b") {
		t.Fatal("expected false for non-existent topic")
	}
}

func TestRemoveByTopicArn_Found(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic-del"))
	m.Add(makeEP("c2", "arn:topic-del"))
	m.Add(makeEP("c3", "arn:other-topic"))

	if !m.RemoveByTopicArn("arn:topic-del") {
		t.Fatal("expected RemoveByTopicArn to return true")
	}
	if m.ClientsCount() != 1 {
		t.Fatalf("expected 1 client remaining, got %d", m.ClientsCount())
	}
	if m.TopicsCount() != 1 {
		t.Fatalf("expected 1 topic remaining, got %d", m.TopicsCount())
	}
}

func TestRemoveByTopicArn_CaseInsensitive(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic-case"))
	if !m.RemoveByTopicArn("ARN:TOPIC-CASE") {
		t.Fatal("expected case-insensitive topic removal to succeed")
	}
	if m.ClientsCount() != 0 {
		t.Fatalf("expected 0 clients after removal, got %d", m.ClientsCount())
	}
}

func TestRemoveByTopicArn_ClosesAllChannels(t *testing.T) {
	m := newMap()
	ep1 := makeEP("c1", "arn:topic-close")
	ep2 := makeEP("c2", "arn:topic-close")
	m.Add(ep1)
	m.Add(ep2)
	ch1 := ep1.DataToSend
	ch2 := ep2.DataToSend

	m.RemoveByTopicArn("arn:topic-close")

	// Both channels must be closed
	for i, ch := range []chan *pb.NotificationData{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("channel %d: expected closed after RemoveByTopicArn", i)
			}
		default:
			t.Fatalf("channel %d: expected closed but would block", i)
		}
	}
}

// ---------------------------------------------------------------------------
// RemoveAll
// ---------------------------------------------------------------------------

func TestRemoveAll_EmptyMap(t *testing.T) {
	m := newMap()
	m.RemoveAll() // Must not panic on nil Clients
}

func TestRemoveAll_ClearsEverything(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:t1"))
	m.Add(makeEP("c2", "arn:t1"))
	m.Add(makeEP("c3", "arn:t2"))

	m.RemoveAll()

	if m.ClientsCount() != 0 {
		t.Fatalf("expected 0 clients after RemoveAll, got %d", m.ClientsCount())
	}
	if m.TopicsCount() != 0 {
		t.Fatalf("expected 0 topics after RemoveAll, got %d", m.TopicsCount())
	}
}

func TestRemoveAll_ClosesAllChannels(t *testing.T) {
	m := newMap()
	ep1 := makeEP("c1", "arn:t1")
	ep2 := makeEP("c2", "arn:t2")
	m.Add(ep1)
	m.Add(ep2)

	ch1 := ep1.DataToSend
	ch2 := ep2.DataToSend

	m.RemoveAll()

	for i, ch := range []chan *pb.NotificationData{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("channel %d: expected closed after RemoveAll", i)
			}
		default:
			t.Fatalf("channel %d: expected closed but would block", i)
		}
	}
}

func TestRemoveAll_CancelsContexts(t *testing.T) {
	m := newMap()
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	ep1 := &clientEndpoint{ClientId: "c1", TopicArn: "arn:t1", ClientContext: ctx1, cancelFunc: cancel1}
	ep2 := &clientEndpoint{ClientId: "c2", TopicArn: "arn:t2", ClientContext: ctx2, cancelFunc: cancel2}
	m.Add(ep1)
	m.Add(ep2)

	m.RemoveAll()

	for i, ctx := range []context.Context{ctx1, ctx2} {
		select {
		case <-ctx.Done():
			// expected
		default:
			t.Fatalf("context %d: expected cancelled after RemoveAll", i)
		}
	}
}

// ---------------------------------------------------------------------------
// ClientsCount / TopicsCount / ClientsByTopicCount
// ---------------------------------------------------------------------------

func TestCounts_EmptyMap(t *testing.T) {
	m := newMap()
	if m.ClientsCount() != 0 {
		t.Fatalf("expected ClientsCount=0, got %d", m.ClientsCount())
	}
	if m.TopicsCount() != 0 {
		t.Fatalf("expected TopicsCount=0, got %d", m.TopicsCount())
	}
	if m.ClientsByTopicCount("arn:any") != 0 {
		t.Fatalf("expected ClientsByTopicCount=0, got %d", m.ClientsByTopicCount("arn:any"))
	}
}

func TestCounts_Accuracy(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:t1"))
	m.Add(makeEP("c2", "arn:t1"))
	m.Add(makeEP("c3", "arn:t2"))

	if m.ClientsCount() != 3 {
		t.Fatalf("expected ClientsCount=3, got %d", m.ClientsCount())
	}
	if m.TopicsCount() != 2 {
		t.Fatalf("expected TopicsCount=2, got %d", m.TopicsCount())
	}
	// Topics are stored normalized to lowercase
	if m.ClientsByTopicCount("arn:t1") != 2 {
		t.Fatalf("expected ClientsByTopicCount(arn:t1)=2, got %d", m.ClientsByTopicCount("arn:t1"))
	}
	if m.ClientsByTopicCount("arn:t2") != 1 {
		t.Fatalf("expected ClientsByTopicCount(arn:t2)=1, got %d", m.ClientsByTopicCount("arn:t2"))
	}
}

func TestClientsByTopicCount_CaseInsensitive(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:MyTopic"))

	if m.ClientsByTopicCount("ARN:MYTOPIC") != 1 {
		t.Fatal("expected ClientsByTopicCount to be case-insensitive")
	}
}

func TestCounts_AfterRemoval(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:t1"))
	m.Add(makeEP("c2", "arn:t1"))
	m.Add(makeEP("c3", "arn:t2"))

	m.RemoveByClientId("c1")

	if m.ClientsCount() != 2 {
		t.Fatalf("expected ClientsCount=2 after removal, got %d", m.ClientsCount())
	}
	if m.ClientsByTopicCount("arn:t1") != 1 {
		t.Fatalf("expected ClientsByTopicCount(arn:t1)=1 after removal, got %d", m.ClientsByTopicCount("arn:t1"))
	}
}

// ---------------------------------------------------------------------------
// SetDataToSendByClientId
// ---------------------------------------------------------------------------

func TestSetDataToSendByClientId_EmptyId(t *testing.T) {
	m := newMap()
	if m.SetDataToSendByClientId("", &pb.NotificationData{}) {
		t.Fatal("expected false for empty clientId")
	}
}

func TestSetDataToSendByClientId_NilData(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic"))
	if m.SetDataToSendByClientId("c1", nil) {
		t.Fatal("expected false for nil data")
	}
}

func TestSetDataToSendByClientId_NotFound(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic"))
	if m.SetDataToSendByClientId("nonexistent", &pb.NotificationData{Message: "test"}) {
		t.Fatal("expected false for non-existent client")
	}
}

func TestSetDataToSendByClientId_Success(t *testing.T) {
	m := newMap()
	ep := makeEP("c1", "arn:topic")
	m.Add(ep)

	data := &pb.NotificationData{Message: "hello-client"}
	if !m.SetDataToSendByClientId("c1", data) {
		t.Fatal("expected SetDataToSendByClientId to return true")
	}

	// Verify data was enqueued
	select {
	case got := <-ep.DataToSend:
		if got.Message != "hello-client" {
			t.Fatalf("expected message 'hello-client', got %q", got.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data on channel")
	}
}

// ---------------------------------------------------------------------------
// SetDataToSendByTopicArn
// ---------------------------------------------------------------------------

func TestSetDataToSendByTopicArn_EmptyTopic(t *testing.T) {
	m := newMap()
	if m.SetDataToSendByTopicArn("", &pb.NotificationData{}) {
		t.Fatal("expected false for empty topicArn")
	}
}

func TestSetDataToSendByTopicArn_NilData(t *testing.T) {
	m := newMap()
	m.Add(makeEP("c1", "arn:topic"))
	if m.SetDataToSendByTopicArn("arn:topic", nil) {
		t.Fatal("expected false for nil data")
	}
}

func TestSetDataToSendByTopicArn_NotFound(t *testing.T) {
	m := newMap()
	if m.SetDataToSendByTopicArn("arn:nonexistent", &pb.NotificationData{Message: "x"}) {
		t.Fatal("expected false for non-existent topic")
	}
}

func TestSetDataToSendByTopicArn_BroadcastToAll(t *testing.T) {
	m := newMap()
	ep1 := makeEP("c1", "arn:broadcast-topic")
	ep2 := makeEP("c2", "arn:broadcast-topic")
	m.Add(ep1)
	m.Add(ep2)

	data := &pb.NotificationData{Message: "broadcast"}
	if !m.SetDataToSendByTopicArn("arn:broadcast-topic", data) {
		t.Fatal("expected SetDataToSendByTopicArn to return true")
	}

	for i, ep := range []*clientEndpoint{ep1, ep2} {
		select {
		case got := <-ep.DataToSend:
			if got.Message != "broadcast" {
				t.Fatalf("ep%d: expected 'broadcast', got %q", i+1, got.Message)
			}
		case <-time.After(time.Second):
			t.Fatalf("ep%d: timed out waiting for broadcast data", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety
// ---------------------------------------------------------------------------

func TestConcurrentAddRemove(t *testing.T) {
	m := newMap()
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // half add, half remove

	// Add goroutines
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			clientId := "conc-" + time.Now().Format("150405.000000000") + "-" + string(rune('A'+id%26)) + "-add"
			m.Add(makeEP(clientId, "arn:conc-topic"))
		}(i)
	}

	// Remove goroutines (may remove nothing, that's fine — testing for data races)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			m.RemoveByClientId("conc-nonexistent-" + string(rune('A'+id%26)))
		}(i)
	}

	wg.Wait()
	// No panic or data race = pass (run with -race flag for full verification)
}

func TestConcurrentCountsDuringModification(t *testing.T) {
	m := newMap()
	const rounds = 100
	var wg sync.WaitGroup

	// Seed some data
	for i := 0; i < 10; i++ {
		m.Add(makeEP("seed-"+string(rune('A'+i)), "arn:count-topic"))
	}

	wg.Add(rounds)
	for i := 0; i < rounds; i++ {
		go func() {
			defer wg.Done()
			_ = m.ClientsCount()
			_ = m.TopicsCount()
			_ = m.ClientsByTopicCount("arn:count-topic")
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Integration: Add + Get + SetData + Remove lifecycle
// ---------------------------------------------------------------------------

func TestLifecycle_AddGetSendRemove(t *testing.T) {
	m := newMap()

	// 1. Add
	ep := makeEP("lifecycle-1", "arn:lifecycle-topic")
	if !m.Add(ep) {
		t.Fatal("Add failed")
	}

	// 2. Get
	found := m.GetByClientId("lifecycle-1")
	if found == nil {
		t.Fatal("GetByClientId returned nil")
	}

	// 3. Send
	data := &pb.NotificationData{Message: "lifecycle-msg"}
	if !m.SetDataToSendByClientId("lifecycle-1", data) {
		t.Fatal("SetDataToSendByClientId failed")
	}
	got := <-ep.DataToSend
	if got.Message != "lifecycle-msg" {
		t.Fatalf("expected 'lifecycle-msg', got %q", got.Message)
	}

	// 4. Remove
	if !m.RemoveByClientId("lifecycle-1") {
		t.Fatal("RemoveByClientId failed")
	}
	if m.GetByClientId("lifecycle-1") != nil {
		t.Fatal("client should be gone after removal")
	}
	if m.ClientsCount() != 0 {
		t.Fatal("expected 0 clients after removal")
	}
	if m.TopicsCount() != 0 {
		t.Fatal("expected 0 topics after removal of last client")
	}
}

// ---------------------------------------------------------------------------
// pkPattern / skPattern constants
// ---------------------------------------------------------------------------

func TestPkSkPatterns(t *testing.T) {
	// Verify the constant patterns produce expected strings
	tests := []struct {
		name     string
		format   string
		args     []interface{}
		expected string
	}{
		{
			name:     "pkPattern",
			format:   pkPattern,
			args:     []interface{}{pkPrefix, pkService},
			expected: "corems#notifier-server#service#discovery#host#target",
		},
		{
			name:     "skPattern",
			format:   skPattern,
			args:     []interface{}{"my-server-key-123"},
			expected: "ServerKey^my-server-key-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmt.Sprintf(tt.format, tt.args...)
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
