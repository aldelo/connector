package client

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newCache() *Cache {
	return &Cache{
		DisableLogging: true,
	}
}

func ep(host string, port uint, version string, expire time.Time) *serviceEndpoint {
	return &serviceEndpoint{
		Host:        host,
		Port:        port,
		Version:     version,
		CacheExpire: expire,
	}
}

var (
	live    = func() time.Time { return time.Now().Add(1 * time.Hour) }
	expired = func() time.Time { return time.Now().Add(-1 * time.Hour) }
)

// ---------------------------------------------------------------------------
// 1. normalizeServiceName
// ---------------------------------------------------------------------------

func TestNormalizeServiceName_Empty(t *testing.T) {
	if got := normalizeServiceName(""); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestNormalizeServiceName_Whitespace(t *testing.T) {
	if got := normalizeServiceName("   "); got != "" {
		t.Errorf("expected empty string for whitespace-only input, got %q", got)
	}
}

func TestNormalizeServiceName_CaseAndSpaces(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"MyService", "myservice"},
		{"  My  Service  ", "my service"},
		{"SERVICE.NAMESPACE", "service.namespace"},
		{"\t Hello \n World \t", "hello world"},
	}
	for _, tt := range tests {
		got := normalizeServiceName(tt.input)
		if got != tt.want {
			t.Errorf("normalizeServiceName(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Cache.AddServiceEndpoints
// ---------------------------------------------------------------------------

func TestAddServiceEndpoints_NewEntries(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
		ep("host2", 9090, "v1", live()),
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(got))
	}
}

func TestAddServiceEndpoints_Deduplication(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
		ep("host1", 8080, "v1", live()), // duplicate
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint after dedup, got %d", len(got))
	}
}

func TestAddServiceEndpoints_MergeWithExisting(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host2", 9090, "v1", live()),
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 2 {
		t.Fatalf("expected 2 endpoints after merge, got %d", len(got))
	}
}

func TestAddServiceEndpoints_OverwritesDuplicateOnMerge(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})
	// Same host:port|version but with a different InstanceId -- the new entry should replace the old.
	replacement := ep("host1", 8080, "v1", live())
	replacement.InstanceId = "new-instance"
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{replacement})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint after overwrite merge, got %d", len(got))
	}
	if got[0].InstanceId != "new-instance" {
		t.Errorf("expected InstanceId %q, got %q", "new-instance", got[0].InstanceId)
	}
}

func TestAddServiceEndpoints_SkipsNilAndInvalid(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		nil,
		ep("", 8080, "v1", live()),  // empty host
		ep("host1", 0, "v1", live()), // zero port
		ep("host1", 8080, "v1", live()),
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 valid endpoint, got %d", len(got))
	}
}

func TestAddServiceEndpoints_SkipsExpiredIncoming(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", expired()),
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 0 {
		t.Fatalf("expected 0 endpoints (expired), got %d", len(got))
	}
}

func TestAddServiceEndpoints_EmptyServiceName(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})

	// The cache map should remain nil / empty.
	if c.serviceEndpoints != nil && len(c.serviceEndpoints) > 0 {
		t.Fatalf("expected empty cache for blank service name, got %d entries", len(c.serviceEndpoints))
	}
}

func TestAddServiceEndpoints_NilReceiver(t *testing.T) {
	var c *Cache
	// Should not panic.
	c.AddServiceEndpoints("svc", []*serviceEndpoint{ep("h", 1, "v1", live())})
}

// ---------------------------------------------------------------------------
// 3. Cache.GetServiceEndpoints
// ---------------------------------------------------------------------------

func TestGetServiceEndpoints_NonExistentKey(t *testing.T) {
	c := newCache()
	got := c.GetServiceEndpoints("no-such-service")
	if got != nil {
		t.Fatalf("expected nil for non-existent key, got %v", got)
	}
}

func TestGetServiceEndpoints_ExistingKey(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(got))
	}
	if got[0].Host != "host1" {
		t.Errorf("expected host %q, got %q", "host1", got[0].Host)
	}
}

func TestGetServiceEndpoints_DeepCopy(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})

	copy1 := c.GetServiceEndpoints("svc-a")
	copy1[0].Host = "mutated"

	copy2 := c.GetServiceEndpoints("svc-a")
	if copy2[0].Host == "mutated" {
		t.Fatal("GetServiceEndpoints did not return a deep copy; mutation leaked to cache")
	}
}

func TestGetServiceEndpoints_NilReceiver(t *testing.T) {
	var c *Cache
	got := c.GetServiceEndpoints("svc")
	if got != nil {
		t.Fatalf("expected nil from nil receiver, got %v", got)
	}
}

func TestGetServiceEndpoints_EmptyServiceName(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})
	got := c.GetServiceEndpoints("")
	if got != nil {
		t.Fatalf("expected nil for empty service name, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// 4. Cache.GetLiveServiceEndpoints
// ---------------------------------------------------------------------------

func TestGetLiveServiceEndpoints_FiltersExpired(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("live-host", 8080, "v1", live()),
	})
	// Manually inject an expired entry to bypass AddServiceEndpoints sanitization.
	c._mu.Lock()
	c.serviceEndpoints["svc-a"] = append(c.serviceEndpoints["svc-a"],
		ep("expired-host", 9090, "v1", expired()),
	)
	c._mu.Unlock()

	got := c.GetLiveServiceEndpoints("svc-a", "")
	if len(got) != 1 {
		t.Fatalf("expected 1 live endpoint, got %d", len(got))
	}
	if got[0].Host != "live-host" {
		t.Errorf("expected host %q, got %q", "live-host", got[0].Host)
	}

	// Verify the expired entry was purged from the cache.
	remaining := c.GetServiceEndpoints("svc-a")
	for _, ep := range remaining {
		if ep.Host == "expired-host" {
			t.Error("expired endpoint was not purged from the cache")
		}
	}
}

func TestGetLiveServiceEndpoints_VersionFiltering(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
		ep("host2", 9090, "v2", live()),
		ep("host3", 7070, "v1", live()),
	})

	got := c.GetLiveServiceEndpoints("svc-a", "v1")
	if len(got) != 2 {
		t.Fatalf("expected 2 v1 endpoints, got %d", len(got))
	}
	for _, e := range got {
		if e.Version != "v1" {
			t.Errorf("expected version v1, got %q", e.Version)
		}
	}
}

func TestGetLiveServiceEndpoints_EmptyVersionReturnsAll(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
		ep("host2", 9090, "v2", live()),
	})

	got := c.GetLiveServiceEndpoints("svc-a", "")
	if len(got) != 2 {
		t.Fatalf("expected 2 endpoints with empty version filter, got %d", len(got))
	}
}

func TestGetLiveServiceEndpoints_ForceRefreshIgnoreExpired(t *testing.T) {
	c := newCache()
	// Manually inject an expired entry.
	c._mu.Lock()
	c.serviceEndpoints = map[string][]*serviceEndpoint{
		"svc-a": {
			ep("expired-host", 8080, "v1", expired()),
			ep("live-host", 9090, "v1", live()),
		},
	}
	c._mu.Unlock()

	// ignoreExpired=true: expired entries should still be returned but purged from the cache.
	got := c.GetLiveServiceEndpoints("svc-a", "", true)
	// Both expired and live should appear in the returned slice.
	foundExpired := false
	foundLive := false
	for _, e := range got {
		if e.Host == "expired-host" {
			foundExpired = true
		}
		if e.Host == "live-host" {
			foundLive = true
		}
	}
	if !foundExpired {
		t.Error("expected expired-host to be returned when ignoreExpired is true")
	}
	if !foundLive {
		t.Error("expected live-host to be returned")
	}

	// After the call, the expired entry should have been purged from cache.
	remaining := c.GetServiceEndpoints("svc-a")
	for _, e := range remaining {
		if e.Host == "expired-host" {
			t.Error("expired endpoint should have been purged from cache even with ignoreExpired")
		}
	}
}

func TestGetLiveServiceEndpoints_NonExistentService(t *testing.T) {
	c := newCache()
	got := c.GetLiveServiceEndpoints("no-such-svc", "")
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 endpoints, got %d", len(got))
	}
}

func TestGetLiveServiceEndpoints_NilReceiver(t *testing.T) {
	var c *Cache
	got := c.GetLiveServiceEndpoints("svc", "v1")
	if got == nil || len(got) != 0 {
		t.Fatalf("expected non-nil empty slice from nil receiver, got %v", got)
	}
}

func TestGetLiveServiceEndpoints_EmptyServiceName(t *testing.T) {
	c := newCache()
	got := c.GetLiveServiceEndpoints("", "v1")
	if len(got) != 0 {
		t.Fatalf("expected 0 endpoints for empty service name, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// 5. Cache.PurgeServiceEndpointByHostAndPort
// ---------------------------------------------------------------------------

func TestPurgeServiceEndpointByHostAndPort_RemovesSpecific(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
		ep("host2", 9090, "v1", live()),
	})

	c.PurgeServiceEndpointByHostAndPort("svc-a", "host1", 8080)

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint after purge, got %d", len(got))
	}
	if got[0].Host != "host2" {
		t.Errorf("expected remaining host %q, got %q", "host2", got[0].Host)
	}
}

func TestPurgeServiceEndpointByHostAndPort_NonExistentEndpoint(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})

	// Purge a host:port that does not exist -- should be a no-op.
	c.PurgeServiceEndpointByHostAndPort("svc-a", "no-such-host", 1234)

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint unchanged, got %d", len(got))
	}
}

func TestPurgeServiceEndpointByHostAndPort_NonExistentService(t *testing.T) {
	c := newCache()
	// Should not panic.
	c.PurgeServiceEndpointByHostAndPort("no-such-svc", "host1", 8080)
}

func TestPurgeServiceEndpointByHostAndPort_RemovesLastEntry(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})

	c.PurgeServiceEndpointByHostAndPort("svc-a", "host1", 8080)

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 0 {
		t.Fatalf("expected 0 endpoints after purging last entry, got %d", len(got))
	}
}

func TestPurgeServiceEndpointByHostAndPort_CaseInsensitiveHost(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("HostOne", 8080, "v1", live()),
	})

	c.PurgeServiceEndpointByHostAndPort("svc-a", "HOSTONE", 8080)

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 0 {
		t.Fatalf("expected 0 endpoints after case-insensitive purge, got %d", len(got))
	}
}

func TestPurgeServiceEndpointByHostAndPort_InvalidArgs(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("host1", 8080, "v1", live()),
	})

	// Empty service name, empty host, zero port -- all should be no-ops.
	c.PurgeServiceEndpointByHostAndPort("", "host1", 8080)
	c.PurgeServiceEndpointByHostAndPort("svc-a", "", 8080)
	c.PurgeServiceEndpointByHostAndPort("svc-a", "host1", 0)

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint unchanged after invalid purge args, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// 6. Concurrent access safety
// ---------------------------------------------------------------------------

func TestConcurrentAccess(t *testing.T) {
	c := newCache()
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 4) // 4 operation types

	// Writers -- AddServiceEndpoints
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c.AddServiceEndpoints("svc-concurrent", []*serviceEndpoint{
					ep("host1", uint(8080+id), "v1", live()),
				})
			}
		}(g)
	}

	// Readers -- GetServiceEndpoints
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = c.GetServiceEndpoints("svc-concurrent")
			}
		}()
	}

	// Readers -- GetLiveServiceEndpoints
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = c.GetLiveServiceEndpoints("svc-concurrent", "v1")
			}
		}()
	}

	// Purgers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c.PurgeServiceEndpointByHostAndPort("svc-concurrent", "host1", uint(8080+id))
			}
		}(g)
	}

	wg.Wait()
	// If we get here without a race detector complaint, the test passes.
}
