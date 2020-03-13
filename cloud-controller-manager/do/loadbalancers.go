/*
Copyright 2017 DigitalOcean

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package do

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog"

	"github.com/digitalocean/godo"
)

const (
	// annoDOLoadBalancerID is the annotation specifying the load-balancer ID
	// used to enable fast retrievals of load-balancers from the API by UUID.
	annoDOLoadBalancerID = "kubernetes.digitalocean.com/load-balancer-id"

	// annDOLoadBalancerName is the annotation used to specify a name of the load
	// balancer that is going to be created by the controller. Adding this
	// annotation to an existing service might also result in renaming an
	// existing LoadBalancer associated with it.
	//
	// The name must not be longer than 255 characters and must adhere to the
	// following naming convention:
	// * it must start with an alphanumeric character;
	// * it must consist of alphanumeric characters or the '.' (dot) or '-'
	// (dash) characters; except for the final character which must not be '-'
	// (dash). Violations to any of these rules will lead to reconciliation
	// errors. If no custom name is given, a default name is chosen consisting of
	// the character 'a' appended by the Service UID.
	annDOLoadBalancerName = "service.beta.kubernetes.io/do-load-balancer-name"

	// annDOProtocol is the annotation used to specify the default protocol
	// for DO load balancers. For ports specified in annDOTLSPorts, this protocol
	// is overwritten to https. Options are tcp, http and https. Defaults to tcp.
	annDOProtocol = "service.beta.kubernetes.io/do-loadbalancer-protocol"

	// annDOHealthCheckPath is the annotation used to specify the health check path
	// for DO load balancers. Defaults to '/'.
	annDOHealthCheckPath = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-path"

	// annDOHealthCheckPort is the annotation used to specify the health check port
	// for DO load balancers. Defaults to the first Service Port
	annDOHealthCheckPort = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-port"

	// annDOHealthCheckProtocol is the annotation used to specify the health check protocol
	// for DO load balancers. Defaults to the protocol used in
	// 'service.beta.kubernetes.io/do-loadbalancer-protocol'.
	annDOHealthCheckProtocol = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-protocol"

	// annDOHealthCheckIntervalSeconds is the annotation used to specify the
	// number of seconds between between two consecutive health checks. The
	// value must be between 3 and 300. Defaults to 3.
	annDOHealthCheckIntervalSeconds = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-check-interval-seconds"

	// annDOHealthCheckResponseTimeoutSeconds is the annotation used to specify the
	// number of seconds the Load Balancer instance will wait for a response
	// until marking a health check as failed. The value must be between 3 and
	// 300. Defaults to 5.
	annDOHealthCheckResponseTimeoutSeconds = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-response-timeout-seconds"

	// annDOHealthCheckUnhealthyThreshold is the annotation used to specify the
	// number of times a health check must fail for a backend Droplet to be
	// marked "unhealthy" and be removed from the pool for the given service.
	// The value must be between 2 and 10. Defaults to 3.
	annDOHealthCheckUnhealthyThreshold = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-unhealthy-threshold"

	// annDOHealthCheckHealthyThreshold is the annotation used to specify the
	// number of times a health check must pass for a backend Droplet to be
	// marked "healthy" for the given service and be re-added to the pool. The
	// value must be between 2 and 10. Defaults to 5.
	annDOHealthCheckHealthyThreshold = "service.beta.kubernetes.io/do-loadbalancer-healthcheck-healthy-threshold"

	// annDOTLSPorts is the annotation used to specify which ports of the load balancer
	// should use the HTTPS protocol. This is a comma separated list of ports
	// (e.g., 443,6443,7443).
	annDOTLSPorts = "service.beta.kubernetes.io/do-loadbalancer-tls-ports"

	// annDOHTTP2Ports is the annotation used to specify which ports of the load balancer
	// should use the HTTP2 protocol. This is a comma separated list of ports
	// (e.g., 443,6443,7443).
	annDOHTTP2Ports = "service.beta.kubernetes.io/do-loadbalancer-http2-ports"

	// annDOTLSPassThrough is the annotation used to specify whether the
	// DO loadbalancer should pass encrypted data to backend droplets.
	// This is optional and defaults to false.
	annDOTLSPassThrough = "service.beta.kubernetes.io/do-loadbalancer-tls-passthrough"

	// annDOCertificateID is the annotation specifying the certificate ID
	// used for https protocol. This annotation is required if annDOTLSPorts
	// is passed.
	annDOCertificateID = "service.beta.kubernetes.io/do-loadbalancer-certificate-id"

	// annDOHostname is the annotation specifying the hostname to use for the LB.
	annDOHostname = "service.beta.kubernetes.io/do-loadbalancer-hostname"

	// annDOAlgorithm is the annotation specifying which algorithm DO load balancer
	// should use. Options are round_robin and least_connections. Defaults
	// to round_robin.
	annDOAlgorithm = "service.beta.kubernetes.io/do-loadbalancer-algorithm"

	// annDOStickySessionsType is the annotation specifying which sticky session type
	// DO loadbalancer should use. Options are none and cookies. Defaults
	// to none.
	annDOStickySessionsType = "service.beta.kubernetes.io/do-loadbalancer-sticky-sessions-type"

	// annDOStickySessionsCookieName is the annotation specifying what cookie name to use for
	// DO loadbalancer sticky session. This annotation is required if
	// annDOStickySessionType is set to cookies.
	annDOStickySessionsCookieName = "service.beta.kubernetes.io/do-loadbalancer-sticky-sessions-cookie-name"

	// annDOStickySessionsCookieTTL is the annotation specifying TTL of cookie used for
	// DO load balancer sticky session. This annotation is required if
	// annDOStickySessionType is set to cookies.
	annDOStickySessionsCookieTTL = "service.beta.kubernetes.io/do-loadbalancer-sticky-sessions-cookie-ttl"

	// annDORedirectHTTPToHTTPS is the annotation specifying whether or not Http traffic
	// should be redirected to Https. Defaults to false
	annDORedirectHTTPToHTTPS = "service.beta.kubernetes.io/do-loadbalancer-redirect-http-to-https"

	// annDOEnableProxyProtocol is the annotation specifying whether PROXY protocol should
	// be enabled. Defaults to false.
	annDOEnableProxyProtocol = "service.beta.kubernetes.io/do-loadbalancer-enable-proxy-protocol"

	// defaultActiveTimeout is the number of seconds to wait for a load balancer to
	// reach the active state.
	defaultActiveTimeout = 90

	// defaultActiveCheckTick is the number of seconds between load balancer
	// status checks when waiting for activation.
	defaultActiveCheckTick = 5

	// statuses for Digital Ocean load balancer
	lbStatusNew     = "new"
	lbStatusActive  = "active"
	lbStatusErrored = "errored"

	// This is the DO-specific tag component prepended to the cluster ID.
	tagPrefixClusterID = "k8s"

	// Sticky sessions types.
	stickySessionsTypeNone    = "none"
	stickySessionsTypeCookies = "cookies"

	// Protocol values.
	protocolTCP   = "tcp"
	protocolHTTP  = "http"
	protocolHTTPS = "https"
	protocolHTTP2 = "http2"

	// Port protocol values.
	portProtocolTCP = "TCP"

	defaultSecurePort = 443
)

var (
	errLBNotFound = errors.New("loadbalancer not found")
)

func buildK8sTag(val string) string {
	return fmt.Sprintf("%s:%s", tagPrefixClusterID, val)
}

type loadBalancers struct {
	resources         *resources
	region            string
	clusterID         string
	lbActiveTimeout   int
	lbActiveCheckTick int
}

type servicePatcher struct {
	kclient kubernetes.Interface
	base    *v1.Service
	updated *v1.Service
}

func newServicePatcher(kclient kubernetes.Interface, base *v1.Service) servicePatcher {
	return servicePatcher{
		kclient: kclient,
		base:    base.DeepCopy(),
		updated: base,
	}
}

// Patch will submit a patch request for the Service unless the updated service
// reference contains the same set of annotations as the base copied during
// servicePatcher initialization.
func (sp *servicePatcher) Patch(err error) error {
	if reflect.DeepEqual(sp.base.Annotations, sp.updated.Annotations) {
		return err
	}
	perr := patchService(sp.kclient, sp.base, sp.updated)
	return utilerrors.NewAggregate([]error{err, perr})
}

// newLoadbalancers returns a cloudprovider.LoadBalancer whose concrete type is a *loadbalancer.
func newLoadBalancers(resources *resources, client *godo.Client, region string) cloudprovider.LoadBalancer {
	return &loadBalancers{
		resources:         resources,
		region:            region,
		lbActiveTimeout:   defaultActiveTimeout,
		lbActiveCheckTick: defaultActiveCheckTick,
	}
}

// GetLoadBalancer returns the *v1.LoadBalancerStatus of service.
//
// GetLoadBalancer will not modify service.
func (l *loadBalancers) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (status *v1.LoadBalancerStatus, exists bool, err error) {
	patcher := newServicePatcher(l.resources.kclient, service)
	defer func() { err = patcher.Patch(err) }()

	var lb *godo.LoadBalancer
	lb, err = l.retrieveAndAnnotateLoadBalancer(ctx, service)
	if err != nil {
		if err == errLBNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}

	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{
			{
				IP: lb.IP,
			},
		},
	}, true, nil
}

// GetLoadBalancerName returns the name of the load balancer. Implementations must treat the
// *v1.Service parameter as read-only and not modify it.
func (l *loadBalancers) GetLoadBalancerName(_ context.Context, clusterName string, service *v1.Service) string {
	return getLoadBalancerName(service)
}

func getLoadBalancerName(service *v1.Service) string {
	name := service.Annotations[annDOLoadBalancerName]

	if len(name) > 0 {
		return name
	}

	return getLoadBalancerLegacyName(service)
}

func getLoadBalancerLegacyName(service *v1.Service) string {
	return cloudprovider.DefaultLoadBalancerName(service)
}

// EnsureLoadBalancer ensures that the cluster is running a load balancer for
// service.
//
// EnsureLoadBalancer will not modify service or nodes.
func (l *loadBalancers) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (lbs *v1.LoadBalancerStatus, err error) {
	patcher := newServicePatcher(l.resources.kclient, service)
	defer func() { err = patcher.Patch(err) }()

	var lbRequest *godo.LoadBalancerRequest
	lbRequest, err = l.buildLoadBalancerRequest(ctx, service, nodes)
	if err != nil {
		return nil, fmt.Errorf("failed to build load-balancer request: %s", err)
	}

	var lb *godo.LoadBalancer
	lb, err = l.retrieveAndAnnotateLoadBalancer(ctx, service)
	switch err {
	case nil:
		// LB existing
		lb, err = l.updateLoadBalancer(ctx, lb, service, nodes)
		if err != nil {
			return nil, err
		}

	case errLBNotFound:
		// LB missing
		lb, _, err = l.resources.gclient.LoadBalancers.Create(ctx, lbRequest)
		if err != nil {
			return nil, fmt.Errorf("failed to create load-balancer: %s", err)
		}

		updateServiceAnnotation(service, annoDOLoadBalancerID, lb.ID)

	default:
		// unrecoverable LB retrieval error
		return nil, err
	}

	if lb.Status != lbStatusActive {
		return nil, fmt.Errorf("load-balancer is not yet active (current status: %s)", lb.Status)
	}

	// If a LB hostname annotation is specified, return with it instead of the IP.
	hostname := getHostname(service)
	if hostname != "" {
		return &v1.LoadBalancerStatus{
			Ingress: []v1.LoadBalancerIngress{
				{
					Hostname: hostname,
				},
			},
		}, nil
	}

	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{
			{
				IP: lb.IP,
			},
		},
	}, nil
}

func getCertificateIDFromLB(lb *godo.LoadBalancer) string {
	for _, rule := range lb.ForwardingRules {
		if rule.CertificateID != "" {
			return rule.CertificateID
		}
	}
	return ""
}

// recordUpdatedLetsEncryptCert ensures that when DO LBaaS updates its
// lets_encrypt type certificate associated with a Service that the certificate
// annotation on the Service gets newly-updated certificate ID from the
// Load Balancer.
func (l *loadBalancers) recordUpdatedLetsEncryptCert(ctx context.Context, service *v1.Service, lbCertID, serviceCertID string) error {
	if lbCertID != "" && lbCertID != serviceCertID {
		lbCert, _, err := l.resources.gclient.Certificates.Get(ctx, lbCertID)
		if err != nil {
			respErr, ok := err.(*godo.ErrorResponse)
			if ok && respErr.Response.StatusCode == http.StatusNotFound {
				return nil
			}
			return fmt.Errorf("failed to get DO certificate for load-balancer: %s", err)
		}

		if lbCert.Type == certTypeLetsEncrypt {
			updateServiceAnnotation(service, annDOCertificateID, lbCertID)
		}
	}

	return nil
}

func (l *loadBalancers) updateLoadBalancer(ctx context.Context, lb *godo.LoadBalancer, service *v1.Service, nodes []*v1.Node) (*godo.LoadBalancer, error) {
	// call buildLoadBalancerRequest for its error checking; we have to call it
	// again just before actually updating the loadbalancer in case
	// checkAndUpdateLBAndServiceCerts modifies the service
	_, err := l.buildLoadBalancerRequest(ctx, service, nodes)
	if err != nil {
		return nil, fmt.Errorf("failed to build load-balancer request: %s", err)
	}

	lbCertID := getCertificateIDFromLB(lb)
	serviceCertID := getCertificateID(service)
	err = l.recordUpdatedLetsEncryptCert(ctx, service, lbCertID, serviceCertID)
	if err != nil {
		return nil, err
	}

	lbRequest, err := l.buildLoadBalancerRequest(ctx, service, nodes)
	if err != nil {
		return nil, fmt.Errorf("failed to build load-balancer request (post-certificate update): %s", err)
	}

	lbID := lb.ID
	lb, _, err = l.resources.gclient.LoadBalancers.Update(ctx, lb.ID, lbRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to update load-balancer with ID %s: %s", lbID, err)
	}

	return lb, nil
}

// UpdateLoadBalancer updates the load balancer for service to balance across
// the droplets in nodes.
//
// UpdateLoadBalancer will not modify service or nodes.
func (l *loadBalancers) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (err error) {
	patcher := newServicePatcher(l.resources.kclient, service)
	defer func() { err = patcher.Patch(err) }()

	var lb *godo.LoadBalancer
	lb, err = l.retrieveAndAnnotateLoadBalancer(ctx, service)
	if err != nil {
		return err
	}

	_, err = l.updateLoadBalancer(ctx, lb, service, nodes)
	return err
}

// EnsureLoadBalancerDeleted deletes the specified loadbalancer if it exists.
// nil is returned if the load balancer for service does not exist or is
// successfully deleted.
//
// EnsureLoadBalancerDeleted will not modify service.
func (l *loadBalancers) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	// Not calling retrieveAndAnnotateLoadBalancer to save a potential PATCH API
	// call: the load-balancer is destined to be removed anyway.
	lb, err := l.retrieveLoadBalancer(ctx, service)
	if err != nil {
		if err == errLBNotFound {
			return nil
		}
		return err
	}

	resp, err := l.resources.gclient.LoadBalancers.Delete(ctx, lb.ID)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("failed to delete load-balancer: %s", err)
	}

	return nil
}

func (l *loadBalancers) retrieveAndAnnotateLoadBalancer(ctx context.Context, service *v1.Service) (*godo.LoadBalancer, error) {
	lb, err := l.retrieveLoadBalancer(ctx, service)
	if err != nil {
		// Return bare error to easily compare for errLBNotFound. Converting to
		// a full error type doesn't seem worth it.
		return nil, err
	}

	updateServiceAnnotation(service, annoDOLoadBalancerID, lb.ID)

	return lb, nil
}

func (l *loadBalancers) retrieveLoadBalancer(ctx context.Context, service *v1.Service) (*godo.LoadBalancer, error) {
	id := getLoadBalancerID(service)
	if len(id) > 0 {
		klog.V(2).Infof("looking up load-balancer for service %s/%s by ID %s", service.Namespace, service.Name, id)

		return l.findLoadBalancerByID(ctx, id)
	}

	allLBs, err := allLoadBalancerList(ctx, l.resources.gclient)
	if err != nil {
		return nil, err
	}

	return findLoadBalancerByName(service, allLBs)
}

func (l *loadBalancers) findLoadBalancerByID(ctx context.Context, id string) (*godo.LoadBalancer, error) {
	lb, resp, err := l.resources.gclient.LoadBalancers.Get(ctx, id)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, errLBNotFound
		}

		return nil, fmt.Errorf("failed to get load-balancer by ID %s: %s", id, err)
	}

	return lb, nil
}

func findLoadBalancerByName(service *v1.Service, allLBs []godo.LoadBalancer) (*godo.LoadBalancer, error) {
	newName := getLoadBalancerName(service)
	legacyName := getLoadBalancerLegacyName(service)

	klog.V(2).Infof("Looking up load-balancer for service %s/%s by either %s or %s name", service.Namespace, service.Name, newName, legacyName)

	for _, lb := range allLBs {
		if lb.Name == newName || lb.Name == legacyName {
			return &lb, nil
		}
	}

	return nil, errLBNotFound
}

func findLoadBalancerID(service *v1.Service, allLBs []godo.LoadBalancer) (string, error) {
	id := getLoadBalancerID(service)
	if len(id) > 0 {
		return id, nil
	}

	lb, err := findLoadBalancerByName(service, allLBs)
	if err != nil {
		return "", err
	}

	return lb.ID, nil
}

func updateServiceAnnotation(service *v1.Service, annotName, annotValue string) {
	if service.ObjectMeta.Annotations == nil {
		service.ObjectMeta.Annotations = map[string]string{}
	}
	service.ObjectMeta.Annotations[annotName] = annotValue
}

// nodesToDropletID returns a []int containing ids of all droplets identified by name in nodes.
//
// Node names are assumed to match droplet names.
func (l *loadBalancers) nodesToDropletIDs(ctx context.Context, nodes []*v1.Node) ([]int, error) {
	var dropletIDs []int
	missingDroplets := map[string]bool{}

	for _, node := range nodes {
		providerID := node.Spec.ProviderID
		if providerID != "" {
			dropletID, err := dropletIDFromProviderID(providerID)
			if err != nil {
				return nil, fmt.Errorf("failed to parse provider ID %q: %s", providerID, err)
			}
			dropletIDs = append(dropletIDs, dropletID)
		} else {
			missingDroplets[node.Name] = true
		}
	}

	if len(missingDroplets) > 0 {
		// Discover missing droplets by matching names.
		droplets, err := allDropletList(ctx, l.resources.gclient)
		if err != nil {
			return nil, fmt.Errorf("failed to list all droplets: %s", err)
		}

		for _, droplet := range droplets {
			if missingDroplets[droplet.Name] {
				dropletIDs = append(dropletIDs, droplet.ID)
				delete(missingDroplets, droplet.Name)
				continue
			}
			addresses, err := nodeAddresses(&droplet)
			if err != nil {
				klog.Errorf("Error getting node addresses for %s: %s", droplet.Name, err)
				continue
			}
			for _, address := range addresses {
				if missingDroplets[address.Address] {
					dropletIDs = append(dropletIDs, droplet.ID)
					delete(missingDroplets, droplet.Name)
					break
				}
			}
		}
	}

	if len(missingDroplets) > 0 {
		// Sort node names for stable output.
		missingNames := make([]string, 0, len(missingDroplets))
		for missingName := range missingDroplets {
			missingNames = append(missingNames, missingName)
		}
		sort.Strings(missingNames)

		klog.Errorf("Failed to find droplets for nodes %s", strings.Join(missingNames, " "))
	}

	return dropletIDs, nil
}

// buildLoadBalancerRequest returns a *godo.LoadBalancerRequest to balance
// requests for service across nodes.
func (l *loadBalancers) buildLoadBalancerRequest(ctx context.Context, service *v1.Service, nodes []*v1.Node) (*godo.LoadBalancerRequest, error) {
	lbName := getLoadBalancerName(service)

	dropletIDs, err := l.nodesToDropletIDs(ctx, nodes)
	if err != nil {
		return nil, err
	}

	forwardingRules, err := buildForwardingRules(service)
	if err != nil {
		return nil, err
	}

	healthCheck, err := buildHealthCheck(service)
	if err != nil {
		return nil, err
	}

	stickySessions, err := buildStickySessions(service)
	if err != nil {
		return nil, err
	}

	algorithm := getAlgorithm(service)

	redirectHTTPToHTTPS := getRedirectHTTPToHTTPS(service)
	enableProxyProtocol, err := getEnableProxyProtocol(service)
	if err != nil {
		return nil, err
	}

	var tags []string
	if l.resources.clusterID != "" {
		tags = []string{buildK8sTag(l.resources.clusterID)}
	}

	return &godo.LoadBalancerRequest{
		Name:                lbName,
		DropletIDs:          dropletIDs,
		Region:              l.region,
		ForwardingRules:     forwardingRules,
		HealthCheck:         healthCheck,
		StickySessions:      stickySessions,
		Tags:                tags,
		Algorithm:           algorithm,
		RedirectHttpToHttps: redirectHTTPToHTTPS,
		EnableProxyProtocol: enableProxyProtocol,
		VPCUUID:             l.resources.clusterVPCID,
	}, nil
}

// buildHealthChecks returns a godo.HealthCheck for service.
func buildHealthCheck(service *v1.Service) (*godo.HealthCheck, error) {
	healthCheckPort, err := healthCheckPort(service)
	if err != nil {
		return nil, err
	}

	healthCheckProtocol, err := healthCheckProtocol(service)
	if err != nil {
		return nil, err
	}

	checkIntervalSecs, err := healthCheckIntervalSeconds(service)
	if err != nil {
		return nil, err
	}
	responseTimeoutSecs, err := healthCheckResponseTimeoutSeconds(service)
	if err != nil {
		return nil, err
	}
	unhealthyThreshold, err := healthCheckUnhealthyThreshold(service)
	if err != nil {
		return nil, err
	}
	healthyThreshold, err := healthCheckHealthyThreshold(service)
	if err != nil {
		return nil, err
	}

	healthCheckPath := healthCheckPath(service)

	return &godo.HealthCheck{
		Protocol:               healthCheckProtocol,
		Port:                   healthCheckPort,
		Path:                   healthCheckPath,
		CheckIntervalSeconds:   checkIntervalSecs,
		ResponseTimeoutSeconds: responseTimeoutSecs,
		UnhealthyThreshold:     unhealthyThreshold,
		HealthyThreshold:       healthyThreshold,
	}, nil
}

// buildForwardingRules returns the forwarding rules of the Load Balancer of
// service.
func buildForwardingRules(service *v1.Service) ([]godo.ForwardingRule, error) {
	var forwardingRules []godo.ForwardingRule

	defaultProtocol, err := getProtocol(service)
	if err != nil {
		return nil, err
	}

	httpsPorts, err := getHTTPSPorts(service)
	if err != nil {
		return nil, err
	}

	http2Ports, err := getHTTP2Ports(service)
	if err != nil {
		return nil, err
	}

	securePortDups := findDups(append(httpsPorts, http2Ports...))
	if len(securePortDups) > 0 {
		return nil, fmt.Errorf("%q and %q cannot share values but found: %s", annDOTLSPorts, annDOHTTP2Ports, strings.Join(securePortDups, ", "))
	}

	certificateID := getCertificateID(service)
	tlsPassThrough := getTLSPassThrough(service)
	needSecureProto := certificateID != "" || tlsPassThrough

	if needSecureProto && len(httpsPorts) == 0 && !contains(http2Ports, defaultSecurePort) {
		httpsPorts = append(httpsPorts, defaultSecurePort)
	}

	httpsPortMap := map[int32]bool{}
	for _, port := range httpsPorts {
		httpsPortMap[int32(port)] = true
	}
	http2PortMap := map[int32]bool{}
	for _, port := range http2Ports {
		http2PortMap[int32(port)] = true
	}

	for _, port := range service.Spec.Ports {
		protocol := defaultProtocol
		// Set secure protocols explicitly if correspondingly configured ports
		// are found.
		if httpsPortMap[port.Port] {
			protocol = protocolHTTPS
		}
		if http2PortMap[port.Port] {
			protocol = protocolHTTP2
		}

		forwardingRule, err := buildForwardingRule(service, &port, protocol, certificateID, tlsPassThrough)
		if err != nil {
			return nil, err
		}
		forwardingRules = append(forwardingRules, *forwardingRule)
	}

	return forwardingRules, nil
}

func buildForwardingRule(service *v1.Service, port *v1.ServicePort, protocol, certificateID string, tlsPassThrough bool) (*godo.ForwardingRule, error) {
	var forwardingRule godo.ForwardingRule

	if port.Protocol != portProtocolTCP {
		return nil, fmt.Errorf("only TCP protocol is supported, got: %q", port.Protocol)
	}

	forwardingRule.EntryProtocol = protocol
	forwardingRule.TargetProtocol = protocol

	forwardingRule.EntryPort = int(port.Port)
	forwardingRule.TargetPort = int(port.NodePort)

	if protocol == protocolHTTPS || protocol == protocolHTTP2 {
		err := buildTLSForwardingRule(&forwardingRule, service, port.Port, certificateID, tlsPassThrough)
		if err != nil {
			return nil, fmt.Errorf("failed to build TLS part(s) of forwarding rule: %s", err)
		}
	}

	return &forwardingRule, nil
}

func buildTLSForwardingRule(forwardingRule *godo.ForwardingRule, service *v1.Service, port int32, certificateID string, tlsPassThrough bool) error {
	if certificateID == "" && !tlsPassThrough {
		return errors.New("must set certificate id or enable tls pass through")
	}

	if certificateID != "" && tlsPassThrough {
		return errors.New("either certificate id should be set or tls pass through enabled, not both")
	}

	if tlsPassThrough {
		forwardingRule.TlsPassthrough = tlsPassThrough
		// We don't explicitly set the TargetProtocol here since in buildForwardingRule
		// we already assign the annotation-defined protocol to both the EntryProtocol
		// and TargetProtocol, and in the tlsPassthrough case we want the TargetProtocol
		// to match the EntryProtocol.
	} else {
		forwardingRule.CertificateID = certificateID
		forwardingRule.TargetProtocol = protocolHTTP
	}

	return nil
}

func buildStickySessions(service *v1.Service) (*godo.StickySessions, error) {
	t := getStickySessionsType(service)

	if t == stickySessionsTypeNone {
		return &godo.StickySessions{
			Type: t,
		}, nil
	}

	name, err := getStickySessionsCookieName(service)
	if err != nil {
		return nil, err
	}

	ttl, err := getStickySessionsCookieTTL(service)
	if err != nil {
		return nil, err
	}

	return &godo.StickySessions{
		Type:             t,
		CookieName:       name,
		CookieTtlSeconds: ttl,
	}, nil
}

// getProtocol returns the desired protocol of service.
func getProtocol(service *v1.Service) (string, error) {
	protocol, ok := service.Annotations[annDOProtocol]
	if !ok {
		return protocolTCP, nil
	}

	if protocol != protocolTCP && protocol != protocolHTTP && protocol != protocolHTTPS && protocol != protocolHTTP2 {
		return "", fmt.Errorf("invalid protocol: %q specified in annotation: %q", protocol, annDOProtocol)
	}

	return protocol, nil
}

// getHostname returns the desired hostname for the LB service.
func getHostname(service *v1.Service) string {
	return strings.ToLower(service.Annotations[annDOHostname])
}

// healthCheckPort returns the health check port specified, defaulting
// to the first port in the service otherwise.
func healthCheckPort(service *v1.Service) (int, error) {
	ports, err := getPorts(service, annDOHealthCheckPort)
	if err != nil {
		return 0, fmt.Errorf("failed to get health check port: %v", err)
	}

	if len(ports) > 1 {
		return 0, fmt.Errorf("annotation %s only supports a single port, but found multiple: %v", annDOHealthCheckPort, ports)
	}

	if len(ports) == 1 {
		for _, servicePort := range service.Spec.Ports {
			if int(servicePort.Port) == ports[0] {
				return int(servicePort.NodePort), nil
			}
		}
		return 0, fmt.Errorf("specified health check port %d does not exist on service %s/%s", ports[0], service.Namespace, service.Name)
	}

	return int(service.Spec.Ports[0].NodePort), nil
}

// healthCheckProtocol returns the health check protocol as specified in the service,
// falling back to TCP if not specified.
func healthCheckProtocol(service *v1.Service) (string, error) {
	protocol := service.Annotations[annDOHealthCheckProtocol]
	path := healthCheckPath(service)

	if protocol == "" {
		if path != "" {
			return protocolHTTP, nil
		}
		return protocolTCP, nil
	}

	if protocol != protocolTCP && protocol != protocolHTTP {
		return "", fmt.Errorf("invalid protocol: %q specified in annotation: %q", protocol, annDOProtocol)
	}

	return protocol, nil
}

// getHealthCheckPath returns the desired path for health checking
// health check path should default to / if not specified
func healthCheckPath(service *v1.Service) string {
	path, ok := service.Annotations[annDOHealthCheckPath]
	if !ok {
		return ""
	}

	return path
}

// healthCheckIntervalSeconds returns the health check interval in seconds
func healthCheckIntervalSeconds(service *v1.Service) (int, error) {
	valStr, ok := service.Annotations[annDOHealthCheckIntervalSeconds]
	if !ok {
		return 3, nil
	}

	val, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse health check interval annotation %q: %s", annDOHealthCheckIntervalSeconds, err)
	}

	return val, nil
}

// healthCheckIntervalSeconds returns the health response timeout in seconds
func healthCheckResponseTimeoutSeconds(service *v1.Service) (int, error) {
	valStr, ok := service.Annotations[annDOHealthCheckResponseTimeoutSeconds]
	if !ok {
		return 5, nil
	}

	val, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse health check response timeout annotation %q: %s", annDOHealthCheckResponseTimeoutSeconds, err)
	}

	return val, nil
}

// healthCheckUnhealthyThreshold returns the health check unhealthy threshold
func healthCheckUnhealthyThreshold(service *v1.Service) (int, error) {
	valStr, ok := service.Annotations[annDOHealthCheckUnhealthyThreshold]
	if !ok {
		return 3, nil
	}

	val, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse health check unhealthy threshold annotation %q: %s", annDOHealthCheckUnhealthyThreshold, err)
	}

	return val, nil
}

// healthCheckHealthyThreshold returns the health check healthy threshold
func healthCheckHealthyThreshold(service *v1.Service) (int, error) {
	valStr, ok := service.Annotations[annDOHealthCheckHealthyThreshold]
	if !ok {
		return 5, nil
	}

	val, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse health check healthy threshold annotation %q: %s", annDOHealthCheckHealthyThreshold, err)
	}

	return val, nil
}

// getHTTP2Ports returns the ports for the given service that are set to use
// HTTP2.
func getHTTP2Ports(service *v1.Service) ([]int, error) {
	return getPorts(service, annDOHTTP2Ports)
}

// getHTTPSPorts returns the ports for the given service that are set to use
// HTTPS.
func getHTTPSPorts(service *v1.Service) ([]int, error) {
	return getPorts(service, annDOTLSPorts)
}

// getPorts returns the ports for the given service and annotation.
func getPorts(service *v1.Service, anno string) ([]int, error) {
	ports, ok := service.Annotations[anno]
	if !ok {
		return nil, nil
	}

	portsSlice := strings.Split(ports, ",")

	portsInt := make([]int, len(portsSlice))
	for i, port := range portsSlice {
		port, err := strconv.Atoi(port)
		if err != nil {
			return nil, err
		}

		portsInt[i] = port
	}

	return portsInt, nil
}

// getCertificateID returns the certificate ID of service to use for fowarding
// rules.
func getCertificateID(service *v1.Service) string {
	return service.Annotations[annDOCertificateID]
}

// getTLSPassThrough returns true if there should be TLS pass through to
// backend nodes.
func getTLSPassThrough(service *v1.Service) bool {
	passThrough, ok := service.Annotations[annDOTLSPassThrough]
	if !ok {
		return false
	}

	passThroughBool, err := strconv.ParseBool(passThrough)
	if err != nil {
		return false
	}

	return passThroughBool
}

// getAlgorithm returns the load balancing algorithm to use for service.
// round_robin is returned when service does not specify an algorithm.
func getAlgorithm(service *v1.Service) string {
	algo := service.Annotations[annDOAlgorithm]

	switch algo {
	case "least_connections":
		return "least_connections"
	default:
		return "round_robin"
	}
}

// getStickySessionsType returns the sticky session type to use for
// loadbalancer. none is returned when a type is not specified.
func getStickySessionsType(service *v1.Service) string {
	t := service.Annotations[annDOStickySessionsType]

	switch t {
	case stickySessionsTypeCookies:
		return stickySessionsTypeCookies
	default:
		return stickySessionsTypeNone
	}
}

// getStickySessionsCookieName returns cookie name used for
// loadbalancer sticky sessions.
func getStickySessionsCookieName(service *v1.Service) (string, error) {
	name, ok := service.Annotations[annDOStickySessionsCookieName]
	if !ok || name == "" {
		return "", fmt.Errorf("sticky session cookie name not specified, but required")
	}

	return name, nil
}

// getStickySessionsCookieTTL returns ttl for cookie used for
// loadbalancer sticky sessions.
func getStickySessionsCookieTTL(service *v1.Service) (int, error) {
	ttl, ok := service.Annotations[annDOStickySessionsCookieTTL]
	if !ok || ttl == "" {
		return 0, fmt.Errorf("sticky session cookie ttl not specified, but required")
	}

	return strconv.Atoi(ttl)
}

// getRedirectHTTPToHTTPS returns whether or not Http traffic should be redirected
// to Https traffic for the loadbalancer. false is returned if not specified.
func getRedirectHTTPToHTTPS(service *v1.Service) bool {
	redirectHTTPToHTTPS, ok := service.Annotations[annDORedirectHTTPToHTTPS]
	if !ok {
		return false
	}

	redirectHTTPToHTTPSBool, err := strconv.ParseBool(redirectHTTPToHTTPS)
	if err != nil {
		return false
	}

	return redirectHTTPToHTTPSBool
}

// getEnableProxyProtocol returns whether PROXY protocol should be enabled.
// False is returned if not specified.
func getEnableProxyProtocol(service *v1.Service) (bool, error) {
	enableProxyProtocolStr, ok := service.Annotations[annDOEnableProxyProtocol]
	if !ok {
		return false, nil
	}

	enableProxyProtocol, err := strconv.ParseBool(enableProxyProtocolStr)
	if err != nil {
		return false, fmt.Errorf("failed to parse proxy protocol flag %q from annotation %q: %s", enableProxyProtocolStr, annDOEnableProxyProtocol, err)
	}

	return enableProxyProtocol, nil
}

func getLoadBalancerID(service *v1.Service) string {
	return service.ObjectMeta.Annotations[annoDOLoadBalancerID]
}

func findDups(vals []int) []string {
	occurrences := map[int]int{}

	for _, val := range vals {
		occurrences[val]++
	}

	var dups []string
	for val, occur := range occurrences {
		if occur > 1 {
			dups = append(dups, strconv.Itoa(val))
		}
	}

	sort.Strings(dups)
	return dups
}

func contains(vals []int, val int) bool {
	for _, v := range vals {
		if v == val {
			return true
		}
	}
	return false
}
