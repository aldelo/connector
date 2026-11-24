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

/*
 * Package healthreport pins the health-report hash-signature construction
 * shared by the service-side sender (connector/service) and the gateway-side
 * verifier (connector/notifiergateway). Both sides MUST build the signature
 * source from this package: the concatenation order is a wire contract, and
 * duplicating it as prose in two packages previously let them drift silently
 * (a field-order edit on one side would disable healing fleet-wide with no
 * test failing).
 *
 * The SHA-256 hashing itself (crypto.Sha256(source, hashSecret)) is performed
 * by the callers; this package owns only the source construction.
 */

// V1Source returns the legacy (v1) signature source: the identity triple plus
// the current date in UTC (yyyy-mm-dd). v1 authenticates the identity but NOT
// the mutable fields — a captured body can be replayed with a forged HostInfo.
func V1Source(namespaceId string, serviceId string, instanceId string, dateUTC string) string {
	return namespaceId + serviceId + instanceId + dateUTC
}

// V2Source returns the v2 signature source: every mutable field of the health
// report plus the current date in UTC (yyyy-mm-dd). A report carrying a valid
// v2 signature cannot have had its AwsRegion, ServiceInfo, or HostInfo
// altered in transit or replay, which is what qualifies a row for the
// gateway's registration reconciler (HostInfoVerified).
func V2Source(namespaceId string, serviceId string, instanceId string, awsRegion string, serviceInfo string, hostInfo string, dateUTC string) string {
	return namespaceId + serviceId + instanceId + awsRegion + serviceInfo + hostInfo + dateUTC
}
