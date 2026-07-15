package resolver

import (
	"strings"
	"testing"
)

// Tests for the per-connection owned-resolver API (NewManualResolverInstance /
// UpdateManualResolverInstance) added to fix the shared-resolver orphaning bug.
// See go-ms-shared/docs/xieyanlei/dal-config-stale-ip-shared-resolver-FIX-plan.md §4/§8.

// (a) NewManualResolverInstance returns a usable resolver + normalized scheme/service,
//     and does NOT register into the process-global schemeMap.
func TestNewManualResolverInstance_ReturnsInstanceWithoutGlobalRegistration(t *testing.T) {
	r, scheme, svc, err := NewManualResolverInstance("CLBFoo", "My-Service.My-NS", []string{"10.0.0.1:5000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected a non-nil *manual.Resolver instance")
	}
	if scheme != "clbfoo" {
		t.Errorf("normalized scheme = %q; want %q", scheme, "clbfoo")
	}
	if svc != "my-service.my-ns" {
		t.Errorf("normalized service = %q; want %q", svc, "my-service.my-ns")
	}
	if r.Scheme() != scheme {
		t.Errorf("resolver.Scheme() = %q; want %q", r.Scheme(), scheme)
	}
	// It must NOT have been stored in the global schemeMap (that is the whole point:
	// an owned instance is local to one ClientConn, not shared via the global map).
	if got, gerr := getResolver(scheme, svc); gerr == nil && got != nil {
		t.Errorf("owned instance must NOT be registered in the global schemeMap; getResolver found one")
	}
}

// (a') same validation/error shapes as the global path.
func TestNewManualResolverInstance_RejectsBadInputs(t *testing.T) {
	if _, _, _, err := NewManualResolverInstance("", "svc.ns", []string{"10.0.0.1:5000"}); err == nil {
		// empty scheme normalizes to "clb" (valid) — so this is NOT an error; document that.
		// (kept for parity awareness; empty scheme is allowed via normalizeSchemeName default)
		_ = err
	}
	if _, _, _, err := NewManualResolverInstance("clbfoo", "", []string{"10.0.0.1:5000"}); err == nil {
		t.Error("expected error for empty service name")
	}
	if _, _, _, err := NewManualResolverInstance("clbfoo", "svc.ns", nil); err == nil {
		t.Error("expected error for empty endpoint addresses")
	}
	// a bare IP literal with no port is explicitly rejected by normalizeAddresses.
	if _, _, _, err := NewManualResolverInstance("clbfoo", "svc.ns", []string{"10.0.0.1"}); err == nil {
		t.Error("expected error for invalid endpoint address (bare IP missing port)")
	}
}

// (b) THE CORE ANTI-ORPHAN GUARANTEE: two independent instances do not share state.
//     Updating instance A must not affect instance B — this is what the global-map
//     sharing violated (one Update overwrote another conn's resolver).
func TestOwnedResolverInstances_AreIndependent(t *testing.T) {
	rA, _, _, err := NewManualResolverInstance("clbsame", "dal-config-read-get-service.dev.aldelo.nae", []string{"10.0.0.1:5000"})
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	rB, _, _, err := NewManualResolverInstance("clbsame", "dal-config-read-get-service.dev.aldelo.nae", []string{"10.0.0.2:5000"})
	if err != nil {
		t.Fatalf("build B: %v", err)
	}
	// Even with the SAME scheme+service (the exact collision that caused orphaning in the
	// global map), the two instances are distinct objects.
	if rA == rB {
		t.Fatal("expected two DISTINCT resolver instances for the same scheme+service; got the same pointer")
	}

	// Updating A must succeed and must not error/panic on B independently.
	if e := UpdateManualResolverInstance(rA, []string{"10.0.0.9:5000"}); e != nil {
		t.Errorf("UpdateManualResolverInstance(A) unexpected error: %v", e)
	}
	if e := UpdateManualResolverInstance(rB, []string{"10.0.0.8:5000"}); e != nil {
		t.Errorf("UpdateManualResolverInstance(B) unexpected error: %v", e)
	}
	// (State isolation is guaranteed structurally: each manual.Resolver holds its own
	// lastSeenState/cc; there is no shared map entry. The distinct-pointer assertion above
	// plus independent successful updates capture the anti-orphan property at unit level;
	// end-to-end IP divergence is covered by /diag/resolver-collision, pilot §8.15.)
}

// UpdateManualResolverInstance nil-guard matches the global path's "resolver is nil" shape.
func TestUpdateManualResolverInstance_NilResolver(t *testing.T) {
	err := UpdateManualResolverInstance(nil, []string{"10.0.0.1:5000"})
	if err == nil {
		t.Fatal("expected error for nil resolver instance")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error %q should mention nil", err.Error())
	}
}

// UpdateManualResolverInstance rejects empty address set (parity with global UpdateManualResolver).
func TestUpdateManualResolverInstance_EmptyAddrs(t *testing.T) {
	r, _, _, err := NewManualResolverInstance("clbfoo", "svc.ns", []string{"10.0.0.1:5000"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if e := UpdateManualResolverInstance(r, nil); e == nil {
		t.Error("expected error for empty endpoint addresses on update")
	}
}
