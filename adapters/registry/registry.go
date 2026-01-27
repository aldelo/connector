package registry

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

import (
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
	"time"

	"strings"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	"github.com/aws/aws-sdk-go/aws"
)

const instanceIdMaxLen = 64
const uuidLength = 36

// explicit max lengths for DNS vs HTTP/custom namespaces
const namespaceNameDNSMaxLen = 253   // RFC-compliant DNS max
const namespaceNameHTTPMaxLen = 1024 // Cloud Map HTTP namespace limit

// namespace ID pattern (ns-xxxxxxxxxxxxxxxx)
var namespaceIdPattern = regexp.MustCompile(`^ns-[a-z0-9]{17}$`)

const instanceVersionMaxLen = 256 // guard attribute length

// enforce AWS Cloud Map allowed instance id charset.
var instanceIdPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// basic Cloud Map service ID guard (srv-xxxxxxxxxxxxxxxx pattern).
var serviceIdPattern = regexp.MustCompile(`^srv-[a-z0-9]{17}$`)

// Broaden service name validation to support both DNS and HTTP namespace rules
var serviceNameDNSPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var serviceNameHTTPPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-_.]{0,61}[A-Za-z0-9])?$`)

// Validate namespace names as dotted DNS labels (public/private DNS namespaces).
var namespaceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// broaden namespace validation to allow HTTP/custom namespace rules (mixed case, underscores, longer length)
var namespaceNameHTTPPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-_.]{0,1022}[A-Za-z0-9])?$`)

const serviceNameMaxLen = 63

// centralize instance ID validation for reuse across all call sites.
func validateInstanceID(id string) error {
	id = strings.TrimSpace(id)
	if len(id) == 0 {
		return fmt.Errorf("InstanceId is Required")
	}
	if len(id) > instanceIdMaxLen {
		return fmt.Errorf("InstanceId exceeds maximum length of %d characters", instanceIdMaxLen)
	}
	if !instanceIdPattern.MatchString(id) {
		return fmt.Errorf("InstanceId must match pattern %s", instanceIdPattern.String())
	}
	return nil
}

func validateNamespaceID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("NamespaceId is Required")
	}
	if !namespaceIdPattern.MatchString(id) {
		return fmt.Errorf("NamespaceId must match pattern %s", namespaceIdPattern.String())
	}
	return nil
}

func validateServiceID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("ServiceId is Required")
	}
	if !serviceIdPattern.MatchString(id) {
		return fmt.Errorf("ServiceId must match pattern %s", serviceIdPattern.String())
	}
	return nil
}

// validate service names against Cloud Map rules (1-63 chars, alnum with single dashes)
func validateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("Service Name is Required")
	}
	if len(name) > serviceNameMaxLen {
		return fmt.Errorf("Service Name exceeds maximum length of %d characters", serviceNameMaxLen)
	}
	if serviceNameDNSPattern.MatchString(name) || serviceNameHTTPPattern.MatchString(name) {
		return nil
	}
	return fmt.Errorf("Service Name must match DNS pattern %s or HTTP pattern %s", serviceNameDNSPattern.String(), serviceNameHTTPPattern.String())
}

// namespace validation to fail fast on invalid DNS names.
func validateNamespaceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("Namespace Name is Required")
	}

	// DNS namespace acceptance
	if len(name) <= namespaceNameDNSMaxLen && namespaceNamePattern.MatchString(name) {
		return nil
	}

	// HTTP/custom namespace acceptance (Cloud Map HTTP namespaces)
	if len(name) <= namespaceNameHTTPMaxLen && namespaceNameHTTPPattern.MatchString(name) {
		return nil
	}

	if len(name) > namespaceNameHTTPMaxLen {
		return fmt.Errorf("Namespace Name exceeds maximum length of %d characters", namespaceNameHTTPMaxLen)
	}

	return fmt.Errorf("Namespace Name must match DNS pattern %s or HTTP pattern %s", namespaceNamePattern.String(), namespaceNameHTTPPattern.String())
}

// validate instance prefix so generated IDs always meet Cloud Map constraints
func validateInstancePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if !instanceIdPattern.MatchString(prefix) {
		return fmt.Errorf("Instance prefix must match pattern %s", instanceIdPattern.String())
	}
	if len(prefix)+uuidLength > instanceIdMaxLen {
		return fmt.Errorf("Instance prefix too long; maximum prefix length is %d characters", instanceIdMaxLen-uuidLength)
	}
	return nil
}

// clamp DiscoverInstances maxResults to Cloud Map bounds (1..100) and ignore non-positive values.
func sanitizeMaxResults(maxResults *int64) *int64 {
	if maxResults == nil {
		return nil
	}
	v := *maxResults
	if v <= 0 { // drop non-positive requests to use service default
		return nil
	}
	if v > 100 { // AWS cap
		v = 100
	}
	return aws.Int64(v)
}

type InstanceInfo struct {
	ServiceId string

	ServiceName   string
	NamespaceName string

	InstanceId      string
	InstanceIP      string
	InstancePort    uint
	InstanceVersion string
	InstanceHealthy bool
}

// CreateService will create a service under given namespaceId that was already created in cloud map
//
// name = (required) name of the service to create, under the given namespaceId
// namespaceId = (required) namespace that this service be created under
// dnsConf = (conditional) required for public and private dns namespaces, configures the dns parameters for this service
// healthCheckConf = (optional) nil will not set health check, otherwise sets a health check condition for this services' instances
// description = (optional) public dns namespace description
// timeOutDuration = (optional) maximum time before timeout via context
func CreateService(sd *cloudmap.CloudMap,
	name string,
	namespaceId string,
	dnsConf *cloudmap.DnsConf,
	healthCheckConf *cloudmap.HealthCheckConf,
	description string,
	timeoutDuration ...time.Duration) (serviceId string, err error) {

	if sd == nil {
		return "", fmt.Errorf("SD Client is Required")
	}

	// normalize inputs before validation/use
	name = strings.TrimSpace(name)
	namespaceId = strings.TrimSpace(namespaceId)
	description = strings.TrimSpace(description)

	// explicit Cloud Map name validation for clear, early errors
	if err := validateServiceName(name); err != nil {
		return "", err
	}

	// validate namespace ID upfront
	if err := validateNamespaceID(namespaceId); err != nil {
		return "", err
	}

	if svc, e := sd.CreateService(name, util.NewUUID(), namespaceId, dnsConf, healthCheckConf, description, nil, timeoutDuration...); e != nil {
		return "", e
	} else if svc == nil || svc.Id == nil {
		return "", fmt.Errorf("Create Service Failed, No ServiceId Returned")
	} else {
		return *svc.Id, nil
	}
}

// RegisterInstance will register a service endpoint to aws cloudmap
// use the operationId with GetOperationStatus to check on progress
//
// sd = (required) cloudmap service discovery object
// serviceId = (required) cloudmap service id
// instancePrefix = (optional) such as hellosvc- or just leave as blank; guid will be appended the prefix
// ip = (required) ip address of the instance being registered
// port = (required) port number of the instance being registered
// healthy = (reequired) indicates the initial instance state as healthy or unhealthy when registered to cloudmap
// version = (optional) such as v1.0.1, semver semantic, for internal use by services
func RegisterInstance(sd *cloudmap.CloudMap,
	serviceId string,
	instancePrefix string,
	ip string,
	port uint,
	healthy bool,
	version string,
	timeoutDuration ...time.Duration) (instanceId string, operationId string, err error) {

	if sd == nil {
		return "", "", fmt.Errorf("SD Client is Required")
	}

	// normalize inputs before validation/use
	serviceId = strings.TrimSpace(serviceId)
	instancePrefix = strings.TrimSpace(instancePrefix)
	ip = strings.TrimSpace(ip)
	version = strings.TrimSpace(version)

	if err := validateServiceID(serviceId); err != nil {
		return "", "", err
	}

	// validate prefix to avoid generating illegal InstanceIds
	if err := validateInstancePrefix(instancePrefix); err != nil {
		return "", "", err
	}

	if ip == "" {
		return "", "", fmt.Errorf("Instance IP is Required")
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil || parsedIP.To4() == nil { // only ipv4 supported by aws cloud map at this time
		return "", "", fmt.Errorf("Instance IP Must Be A Valid IPv4 Address")
	}

	if port == 0 || port > 65535 {
		return "", "", fmt.Errorf("Instance Port Must Be Between 1 and 65535")
	}

	// prevent overlong attribute values rejected by Cloud Map
	if len(version) > instanceVersionMaxLen {
		return "", "", fmt.Errorf("Instance version exceeds maximum length of %d characters", instanceVersionMaxLen)
	}

	// centralize ID generation + validation so both initial and retry paths are safe.
	generateInstanceID := func() (string, error) {
		id := instancePrefix + util.NewUUID()
		if len(id) > instanceIdMaxLen { // enforce AWS length constraint
			return "", fmt.Errorf("InstanceId exceeds maximum length of %d characters", instanceIdMaxLen)
		}
		if !instanceIdPattern.MatchString(id) { // enforce allowed characters
			return "", fmt.Errorf("InstanceId must match pattern %s", instanceIdPattern.String())
		}
		return id, nil
	}

	health := "UNHEALTHY"
	if healthy {
		health = "HEALTHY"
	}

	// helper to build a fresh attributes map per attempt to avoid mutation leaks.
	buildAttributes := func(includeInitHealth bool) map[string]string {
		attrs := map[string]string{
			"AWS_INSTANCE_IPV4": parsedIP.To4().String(),
			"AWS_INSTANCE_PORT": fmt.Sprintf("%d", port),
			"SERVICE_ID":        serviceId,
		}
		if version != "" { // avoid empty attribute values that Cloud Map rejects
			attrs["INSTANCE_VERSION"] = version
		}
		if includeInitHealth {
			attrs["AWS_INIT_HEALTH_STATUS"] = health
		}
		return attrs
	}

	// helpers to classify retryable errors.
	isHealthStatusError := func(e error) bool {
		return e != nil && strings.Contains(strings.ToLower(e.Error()), "aws_init_health_status")
	}

	isDuplicateInstanceError := func(e error) bool {
		if e == nil {
			return false
		}
		msg := strings.ToLower(e.Error())
		return strings.Contains(msg, "duplicate") || strings.Contains(msg, "already exists")
	}

	const maxAttempts = 3 // bounded retries for rare InstanceId collisions.

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if instanceId, err = generateInstanceID(); err != nil {
			return "", "", err
		}

		attrs := buildAttributes(true)
		if operationId, err = sd.RegisterInstance(serviceId, instanceId, instanceId, attrs, timeoutDuration...); err != nil {
			// fallback if service does not support custom health checks
			if isHealthStatusError(err) {
				attrs = buildAttributes(false)
				if operationId, err = sd.RegisterInstance(serviceId, instanceId, instanceId, attrs, timeoutDuration...); err == nil {
					if strings.TrimSpace(operationId) == "" { // ensure a usable operation id is returned
						return "", "", fmt.Errorf("RegisterInstance succeeded but no operationId was returned")
					}
					break
				}
			}

			// retry with a new InstanceId on duplicate, up to maxAttempts
			if isDuplicateInstanceError(err) && attempt < maxAttempts {
				continue
			}

			return "", "", err
		}

		// ensure a usable operation id is returned
		if strings.TrimSpace(operationId) == "" {
			return "", "", fmt.Errorf("RegisterInstance succeeded but no operationId was returned")
		}

		// success
		break
	}

	if err != nil {
		return "", "", err
	}

	// register instance ok, check via operation to see if completed
	return instanceId, operationId, nil
}

// GetOperationStatus will check if a given operationId was successfully completed
func GetOperationStatus(sd *cloudmap.CloudMap,
	operationId string,
	timeoutDuration ...time.Duration) (status sdoperationstatus.SdOperationStatus, err error) {

	if sd == nil {
		return sdoperationstatus.UNKNOWN, fmt.Errorf("SD Client is Required")
	}

	// normalize input
	operationId = strings.TrimSpace(operationId)
	if operationId == "" {
		return sdoperationstatus.UNKNOWN, fmt.Errorf("OperationId is Required")
	}

	// check if a operation has completed
	if op, err := sd.GetOperation(operationId, timeoutDuration...); err != nil {
		return sdoperationstatus.UNKNOWN, err
	} else if op == nil || op.Status == nil {
		return sdoperationstatus.UNKNOWN, fmt.Errorf("Operation Status Not Available")
	} else {
		switch *op.Status {
		case sdoperationstatus.Submitted.Key():
			return sdoperationstatus.Submitted, nil
		case sdoperationstatus.Pending.Key():
			return sdoperationstatus.Pending, nil
		case sdoperationstatus.Success.Key():
			return sdoperationstatus.Success, nil
		case sdoperationstatus.Fail.Key():
			return sdoperationstatus.Fail, fmt.Errorf("%s [%s]", aws.StringValue(op.ErrorMessage), aws.StringValue(op.ErrorCode))
		default:
			return sdoperationstatus.UNKNOWN, fmt.Errorf("Unknown operation status: %s", aws.StringValue(op.Status))
		}
	}
}

// UpdateHealthStatus will update the specified instance healthy status (for instances with custom health check config enabled only)
func UpdateHealthStatus(sd *cloudmap.CloudMap,
	instanceId string,
	serviceId string,
	healthy bool,
	timeoutDuration ...time.Duration) error {

	if sd == nil {
		return fmt.Errorf("SD Client is Required")
	}

	// normalize inputs
	instanceId = strings.TrimSpace(instanceId)
	serviceId = strings.TrimSpace(serviceId)

	if err := validateInstanceID(instanceId); err != nil {
		return err
	}

	if err := validateServiceID(serviceId); err != nil {
		return err
	}

	return sd.UpdateInstanceCustomHealthStatus(instanceId, serviceId, healthy, timeoutDuration...)
}

// DiscoverInstances will query cloudmap for instances matching given criteria
func DiscoverInstances(sd *cloudmap.CloudMap,
	serviceName string,
	namespaceName string,
	healthy bool,
	customAttributes map[string]string,
	maxResults *int64,
	timeoutDuration ...time.Duration) (instanceList []*InstanceInfo, err error) {

	if sd == nil {
		return []*InstanceInfo{}, fmt.Errorf("SD Client is Required")
	}

	// normalize inputs
	serviceName = strings.TrimSpace(serviceName)
	namespaceName = strings.TrimSpace(namespaceName)

	// validate service name to avoid opaque Cloud Map errors during discovery
	if err := validateServiceName(serviceName); err != nil {
		return []*InstanceInfo{}, err
	}

	if err := validateNamespaceName(namespaceName); err != nil { // early validation for namespace name
		return []*InstanceInfo{}, err
	}

	limit := sanitizeMaxResults(maxResults)

	if lst, e := sd.DiscoverInstances(namespaceName, serviceName, healthy, customAttributes, limit, timeoutDuration...); e != nil {
		log.Printf("Discover Instances Failed for Service: %v, %s.%s", e, serviceName, namespaceName)
		return []*InstanceInfo{}, e
	} else {
		for _, v := range lst {
			if v == nil || v.Attributes == nil {
				continue
			}

			attrs := v.Attributes

			ipPtr, okIP := attrs["AWS_INSTANCE_IPV4"]
			portPtr, okPort := attrs["AWS_INSTANCE_PORT"]

			if !okIP || ipPtr == nil || !okPort || portPtr == nil {
				continue
			}
			if v.ServiceName == nil || v.NamespaceName == nil || v.InstanceId == nil {
				continue
			}

			svcID := ""
			if svcIDPtr, okSvcID := attrs["SERVICE_ID"]; okSvcID && svcIDPtr != nil {
				svcID = aws.StringValue(svcIDPtr)
			}

			ipStr := aws.StringValue(ipPtr) // reuse parsed strings
			ipParsed := net.ParseIP(ipStr)
			if ipParsed == nil || ipParsed.To4() == nil {
				log.Printf("Discover Instances Skipping non-ipv4 for instance %s: %s", aws.StringValue(v.InstanceId), ipStr)
				continue
			}

			portStr := aws.StringValue(portPtr)
			portUint64, parsedErr := strconv.ParseUint(portStr, 10, 64)
			if parsedErr != nil || portUint64 == 0 || portUint64 > 65535 {
				log.Printf("Discover Instances Skipping invalid port for instance %s: %s", aws.StringValue(v.InstanceId), portStr)
				continue
			}

			version := ""
			if verPtr, ok := attrs["INSTANCE_VERSION"]; ok && verPtr != nil {
				version = *verPtr
			}

			healthyStatus := false
			if v.HealthStatus != nil {
				healthyStatus = strings.EqualFold(*v.HealthStatus, "HEALTHY")
			}

			instanceList = append(instanceList, &InstanceInfo{
				ServiceId:       svcID,
				ServiceName:     *v.ServiceName,
				NamespaceName:   *v.NamespaceName,
				InstanceId:      *v.InstanceId,
				InstanceIP:      ipParsed.To4().String(),
				InstancePort:    uint(portUint64),
				InstanceVersion: version,
				InstanceHealthy: healthyStatus,
			})
		}

		if len(instanceList) == 0 {
			log.Printf("Discover Instances Returned No Results for Service: %s.%s", serviceName, namespaceName)
		} else {
			log.Printf("Discover Instances Returned %v for Service: %s.%s", instanceList, serviceName, namespaceName)
		}

		// ensure a non-nil slice is always returned to avoid caller panics.
		if instanceList == nil {
			instanceList = []*InstanceInfo{}
		}

		return instanceList, nil
	}
}

// DiscoverApiIps will use cloud map api to query a given service and namespace associated healthy ip addresses, up to 100 is returned if maxResult is not set,
// this call can be used outside of vpc for private dns namespaces,
// this call returns ip address : port (support for dynamic service ports when registered)
func DiscoverApiIps(sd *cloudmap.CloudMap, serviceName string, namespaceName string, version string, maxResult *int64) (ipList []string, err error) {
	if sd == nil {
		return []string{}, fmt.Errorf("SD Client is Required")
	}

	limit := sanitizeMaxResults(maxResult)

	// normalize inputs
	serviceName = strings.TrimSpace(serviceName)
	namespaceName = strings.TrimSpace(namespaceName)
	version = strings.TrimSpace(version)

	var attributes map[string]string

	if util.LenTrim(version) > 0 {
		attributes = make(map[string]string)
		attributes["INSTANCE_VERSION"] = version
	}

	if lst, e := DiscoverInstances(sd, serviceName, namespaceName, true, attributes, limit); e != nil {
		return []string{}, e
	} else {
		if len(lst) > 0 {
			for _, v := range lst {
				ipList = append(ipList, fmt.Sprintf("%s:%d", v.InstanceIP, v.InstancePort))
			}
		}

		return ipList, nil
	}
}

// DiscoverDnsIps will use dns to query a given hostname's associated ip addresses or srv hosts,
// if hostname is in public dns under route 53, then this action will work from outside of aws vpc
// if hostname is in private dns under route 53, then this action will work ONLY from within aws vpc that matches to this hostname's namespace vpc
//
// hostName = (required) host name to lookup via dns
// srv = (required) true: lookup via srv and return srv host and port; false: lookup via A and return ip only
func DiscoverDnsIps(hostName string, srv bool) (ipList []string, err error) {
	// normalize input
	hostName = strings.TrimSpace(hostName)

	if hostName == "" {
		return []string{}, fmt.Errorf("HostName is Required")
	}

	if !srv {
		// A
		lst := util.DnsLookupIps(hostName)

		if len(lst) > 0 {
			for _, v := range lst {
				ipList = append(ipList, v.String())
			}
		}
	} else {
		// SRV
		ipList = util.DnsLookupSrvs(hostName)
	}

	return ipList, nil
}

// DeregisterInstance will remove the given instance from cloudmap and route 53
// use the operationId with GetOperationStatus to check on progress
func DeregisterInstance(sd *cloudmap.CloudMap,
	instanceId string,
	serviceId string,
	timeoutDuration ...time.Duration) (operationId string, err error) {

	if sd == nil {
		return "", fmt.Errorf("SD Client is Required")
	}

	// normalize inputs
	instanceId = strings.TrimSpace(instanceId)
	serviceId = strings.TrimSpace(serviceId)

	if err := validateInstanceID(instanceId); err != nil {
		return "", err
	}

	if err := validateServiceID(serviceId); err != nil {
		return "", err
	}

	if operationId, err = sd.DeregisterInstance(instanceId, serviceId, timeoutDuration...); err != nil { // capture opId + err
		return "", err
	}
	if strings.TrimSpace(operationId) == "" { // ensure a usable operation id is returned
		return "", fmt.Errorf("DeregisterInstance succeeded but no operationId was returned")
	}

	return operationId, nil
}
