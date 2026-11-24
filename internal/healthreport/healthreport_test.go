package healthreport

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import "testing"

// TestV1Source pins the v1 concatenation order: identity triple + date.
func TestV1Source(t *testing.T) {
	got := V1Source("ns-1", "srv-0123456789abcdef", "x-nae-a", "2026-11-24")
	want := "ns-1srv-0123456789abcdefx-nae-a2026-11-24"

	if got != want {
		t.Fatalf("V1Source = %q, want %q", got, want)
	}
}

// TestV2Source pins the v2 concatenation order: every mutable field + date.
// Field order is a wire contract between the service-side sender and the
// gateway-side verifier — any change here is a breaking protocol change.
func TestV2Source(t *testing.T) {
	got := V2Source("ns-1", "srv-0123456789abcdef", "x-nae-a", "us-east-1", "svc.dev.aldelo.nae", "10.2.0.1:50000", "2026-11-24")
	want := "ns-1srv-0123456789abcdefx-nae-aus-east-1svc.dev.aldelo.nae10.2.0.1:500002026-11-24"

	if got != want {
		t.Fatalf("V2Source = %q, want %q", got, want)
	}
}

// TestV2SourceCoversAllMutableFields: each field must change the v2 source —
// a field that does not influence the signature is not covered by it.
func TestV2SourceCoversAllMutableFields(t *testing.T) {
	base := V2Source("ns", "srv", "inst", "region", "svcinfo", "host:1", "2026-11-24")

	variants := map[string]string{
		"namespace":  V2Source("nsX", "srv", "inst", "region", "svcinfo", "host:1", "2026-11-24"),
		"service":    V2Source("ns", "srvX", "inst", "region", "svcinfo", "host:1", "2026-11-24"),
		"instance":   V2Source("ns", "srv", "instX", "region", "svcinfo", "host:1", "2026-11-24"),
		"awsregion":  V2Source("ns", "srv", "inst", "regionX", "svcinfo", "host:1", "2026-11-24"),
		"serviceinf": V2Source("ns", "srv", "inst", "region", "svcinfoX", "host:1", "2026-11-24"),
		"hostinfo":   V2Source("ns", "srv", "inst", "region", "svcinfo", "host:2", "2026-11-24"),
		"date":       V2Source("ns", "srv", "inst", "region", "svcinfo", "host:1", "2026-11-25"),
	}

	for name, v := range variants {
		if v == base {
			t.Fatalf("v2 source unchanged when %q differs — field not covered by the signature", name)
		}
	}
}
