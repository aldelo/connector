package registry

/*
 * Copyright 2020-2023 Aldelo, LP
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
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	"time"
)

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

	if util.LenTrim(name) == 0 {
		return "", fmt.Errorf("Service Name is Required")
	}

	if util.LenTrim(namespaceId) == 0 {
		return "", fmt.Errorf("NamespaceId is Required")
	}

	if svc, e := sd.CreateService(name, util.NewUUID(), namespaceId, dnsConf, healthCheckConf, description, nil, timeoutDuration...); e != nil {
		return "", e
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

	if util.LenTrim(serviceId) == 0 {
		return "", "", fmt.Errorf("ServiceId is Required")
	}

	if util.LenTrim(ip) == 0 {
		return "", "", fmt.Errorf("Instance IP is Required")
	}

	if port > 65535 {
		return "", "", fmt.Errorf("Instance Port Maximum is 65535")
	}

	// create instance id
	instanceId = instancePrefix + util.NewUUID()

	health := "UNHEALTHY"

	if healthy {
		health = "HEALTHY"
	}

	// register instance to cloud map
	if operationId, err = sd.RegisterInstance(serviceId, instanceId, instanceId, map[string]string{
		"AWS_INSTANCE_IPV4":      ip,
		"AWS_INSTANCE_PORT":      fmt.Sprintf("%d", port),
		"AWS_INIT_HEALTH_STATUS": health,
		"INSTANCE_VERSION":       version,
		"SERVICE_ID":             serviceId,
	}, timeoutDuration...); err != nil {
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

	if util.LenTrim(operationId) == 0 {
		return sdoperationstatus.UNKNOWN, fmt.Errorf("OperationId is Required")
	}

	// check if a operation has completed
	if op, err := sd.GetOperation(operationId, timeoutDuration...); err != nil {
		return sdoperationstatus.UNKNOWN, err
	} else {
		switch *op.Status {
		case sdoperationstatus.Submitted.Key():
			return sdoperationstatus.Submitted, nil
		case sdoperationstatus.Pending.Key():
			return sdoperationstatus.Pending, nil
		case sdoperationstatus.Success.Key():
			return sdoperationstatus.Success, nil
		case sdoperationstatus.Fail.Key():
			return sdoperationstatus.Fail, fmt.Errorf("%s [%s]", *op.ErrorMessage, *op.ErrorCode)
		default:
			return sdoperationstatus.UNKNOWN, fmt.Errorf("%s", "Unknown Error")
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

	if util.LenTrim(instanceId) == 0 {
		return fmt.Errorf("InstanceId is Required")
	}

	if util.LenTrim(serviceId) == 0 {
		return fmt.Errorf("ServiceId is Required")
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

	if util.LenTrim(serviceName) == 0 {
		return []*InstanceInfo{}, fmt.Errorf("Service Name is Required")
	}

	if util.LenTrim(namespaceName) == 0 {
		return []*InstanceInfo{}, fmt.Errorf("Namespace Name is Required")
	}

	if lst, e := sd.DiscoverInstances(namespaceName, serviceName, healthy, customAttributes, maxResults, timeoutDuration...); e != nil {
		return []*InstanceInfo{}, e
	} else {
		for _, v := range lst {
			instanceList = append(instanceList, &InstanceInfo{
				ServiceId:       *v.Attributes["SERVICE_ID"],
				ServiceName:     *v.ServiceName,
				NamespaceName:   *v.NamespaceName,
				InstanceId:      *v.InstanceId,
				InstanceIP:      *v.Attributes["AWS_INSTANCE_IPV4"],
				InstancePort:    util.StrToUint(*v.Attributes["AWS_INSTANCE_PORT"]),
				InstanceVersion: *v.Attributes["INSTANCE_VERSION"],
				InstanceHealthy: *v.HealthStatus == "HEALTHY",
			})
		}

		return instanceList, nil
	}
}

// DiscoverApiIps will use cloud map api to query a given service and namespace associated healthy ip addresses, up to 100 is returned if maxResult is not set,
// this call can be used outside of vpc for private dns namespaces,
// this call returns ip address : port (support for dynamic service ports when registered)
func DiscoverApiIps(sd *cloudmap.CloudMap, serviceName string, namespaceName string, version string, maxResult *int64) (ipList []string, err error) {
	if maxResult != nil {
		if *maxResult <= 0 {
			maxResult = nil
		}
	}

	var attributes map[string]string

	if util.LenTrim(version) > 0 {
		attributes = make(map[string]string)
		attributes["INSTANCE_VERSION"] = version
	}

	if lst, e := DiscoverInstances(sd, serviceName, namespaceName, true, attributes, maxResult); e != nil {
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
	if util.LenTrim(hostName) == 0 {
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

	if util.LenTrim(instanceId) == 0 {
		return "", fmt.Errorf("InstanceId is Required")
	}

	if util.LenTrim(serviceId) == 0 {
		return "", fmt.Errorf("ServiceId is Required")
	}

	return sd.DeregisterInstance(instanceId, serviceId, timeoutDuration...)
}
