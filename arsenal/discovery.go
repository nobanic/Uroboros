package arsenal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
)

var (
	discoveryProviderRoots = []string{"anubisdb.com", "rapiddns.io"}
	discoveryProviderHosts = []string{"anubisdb.com", "www.anubisdb.com", "rapiddns.io", "www.rapiddns.io"}
)

type discoveryBinding struct {
	Host    string `json:"host"`
	Address string `json:"address"`
}

type discoveryService struct {
	Host    string `json:"host"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type resolvedHost struct {
	host      string
	addresses []string
	err       error
}

func (m *Manager) executeDiscovery(parent context.Context, job *db.ArsenalJob, prepared preparedRun) {
	finish := db.ArsenalJobFinish{Status: "failed"}
	stats := map[string]any{"workflow": "subfinder_dns_naabu_httpx", "stages": map[string]any{}}
	finish.Stats = stats
	secrets := make([]string, 0, 4)
	defer func() {
		finish.Error = redactRuntimeSecrets(finish.Error, secrets...)
		if err := m.pg.FinishArsenalJob(context.Background(), job.ID, finish); err != nil {
			logArsenalFinish(job.ID, err)
		}
	}()

	budget, _ := integerValue(prepared.config["budget_seconds"])
	runCtx, cancel := context.WithTimeout(parent, time.Duration(budget)*time.Second)
	defer cancel()
	if err := m.pg.MarkArsenalJobRunning(context.Background(), job.ID); err != nil {
		finish.Error = err.Error()
		return
	}
	caPEM, err := os.ReadFile(m.traffic.CACertPath())
	if err != nil {
		finish.Error = "read recording proxy CA: " + err.Error()
		return
	}

	providerResolved, providerErrors := m.resolveHosts(runCtx, discoveryProviderHosts, 4)
	stats["provider_dns_errors"] = providerErrors
	providerRoutes := routesForResolved(providerResolved, []int{443})
	if len(providerRoutes) == 0 {
		finish.Error = "passive discovery providers have no resolvable frozen route"
		return
	}
	providerSecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create passive provider lease: " + err.Error()
		return
	}
	secrets = append(secrets, providerSecret)
	providerSOCKS, err := m.traffic.StartScopedSOCKS5(runCtx, traffic.ScopedSOCKS5Policy{
		RunID: strconv.FormatInt(job.ID, 10) + "-providers", Routes: providerRoutes,
		ListenHost: m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost,
		Username: "uroboros", Password: providerSecret,
	})
	if err != nil {
		finish.Error = "start passive provider TCP lease: " + err.Error()
		return
	}
	defer providerSOCKS.Close() //nolint:errcheck
	providerHTTPSecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create passive provider proxy lease: " + err.Error()
		return
	}
	secrets = append(secrets, providerHTTPSecret)
	providerProxy, err := m.traffic.StartScopedProxy(runCtx, traffic.ScopedProxyPolicy{
		RunID:          strconv.FormatInt(job.ID, 10) + "-providers",
		Scope:          traffic.NetworkScopeSnapshot{RootDomains: discoveryProviderRoots},
		AllowedMethods: []string{"GET"}, MaxRequests: 64,
		ListenHost: m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost,
		ProxyToken: providerHTTPSecret, UpstreamProxyURL: providerSOCKS.ProxyURL(),
	})
	if err != nil {
		finish.Error = "start passive provider evidence proxy: " + err.Error()
		return
	}
	defer providerProxy.Close() //nolint:errcheck

	common := m.discoveryBackendArgs(job, prepared)
	subdomainArgs := cloneArgs(common)
	subdomainArgs["proxy_url"] = providerProxy.ProxyURL()
	subdomainArgs["ca_pem"] = string(caPEM)
	subdomains, subdomainStats, err := m.callDiscoveryStage(runCtx, job, prepared,
		"subfinder", "arsenal_task_subdomains", subdomainArgs)
	_ = providerProxy.Close()
	_ = providerSOCKS.Close()
	providerProxyStats := providerProxy.Stats()
	providerSOCKSStats := providerSOCKS.Stats()
	providerExchanges := providerProxy.CapturedExchanges()
	stats["provider_proxy"] = providerProxyStats
	stats["provider_tcp"] = providerSOCKSStats
	stats["provider_exchanges"] = len(providerExchanges)
	stats["stages"].(map[string]any)["subfinder"] = redactDiscoveryValue(subdomainStats, providerHTTPSecret, providerSecret)
	if err != nil {
		finish.Error = err.Error()
		finish.ExitState = subdomains.ExitState
		m.finishDiscoveryContext(parent, runCtx, &finish)
		return
	}
	candidates, candidateRecords, candidateLimited, err := discoveryCandidates(prepared.target,
		subdomains.Records, intConfig(prepared.config, "max_subdomains"))
	if err != nil {
		finish.Error = err.Error()
		return
	}
	stats["subfinder_reported"] = len(subdomains.Records)
	stats["subdomain_candidates"] = len(candidates)
	stats["subdomain_limit_reached"] = candidateLimited
	if err := m.persistObservations(job, candidateRecords, nil, providerHTTPSecret); err != nil {
		finish.Error = err.Error()
		return
	}
	finish.ExitState = subdomains.ExitState
	if !subdomains.Complete || (subdomains.ExitState != "ok" && subdomains.ExitState != "ok_empty") {
		finish.Error = fmt.Sprintf("subfinder stage ended with %s", subdomains.ExitState)
		return
	}
	if providerProxyStats.Requests == 0 || providerProxyStats.Allowed == 0 || providerProxyStats.Captured == 0 {
		finish.Error = "subfinder did not complete an audited passive provider exchange"
		return
	}
	if err := validatePassiveProviderEvidence(providerExchanges); err != nil {
		finish.Error = err.Error()
		return
	}
	if providerProxyStats.ScopeBlocked != 0 || providerProxyStats.AuthBlocked != 0 ||
		providerProxyStats.BudgetBlocked != 0 || providerSOCKSStats.ScopeBlocked != 0 ||
		providerSOCKSStats.AuthBlocked != 0 || providerSOCKSStats.DialFailed != 0 {
		finish.Error = "passive provider traffic crossed or exhausted its fixed egress boundary"
		return
	}

	resolved, dnsErrors := m.resolveHosts(runCtx, candidates,
		intConfig(prepared.config, "max_addresses_per_host"))
	bindings, dnsRecords := discoveryDNSRecords(resolved)
	stats["dns"] = map[string]any{
		"hosts_attempted": len(candidates), "hosts_resolved": len(resolved),
		"bindings": len(bindings), "errors": dnsErrors, "resolver": "go_system_frozen",
	}
	if err := m.persistObservations(job, dnsRecords, nil, ""); err != nil {
		finish.Error = err.Error()
		return
	}
	if err := runCtx.Err(); err != nil {
		finish.Error = err.Error()
		m.finishDiscoveryContext(parent, runCtx, &finish)
		return
	}
	if len(bindings) == 0 {
		finish.Status, finish.ExitState, finish.Complete, finish.Error = "completed", "ok_empty", true, ""
		return
	}

	ports := discoveryPorts(prepared.config)
	portRoutes := routesForBindings(bindings, ports, false)
	portSecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create port-scan lease: " + err.Error()
		return
	}
	secrets = append(secrets, portSecret)
	portSOCKS, err := m.traffic.StartScopedSOCKS5(runCtx, traffic.ScopedSOCKS5Policy{
		RunID: strconv.FormatInt(job.ID, 10) + "-naabu", Routes: portRoutes,
		ListenHost: m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost,
		Username: "uroboros", Password: portSecret,
	})
	if err != nil {
		finish.Error = "start naabu TCP lease: " + err.Error()
		return
	}
	defer portSOCKS.Close() //nolint:errcheck
	portArgs := cloneArgs(common)
	portArgs["dns_targets_json"] = mustJSON(bindings)
	portArgs["socks_url"] = portSOCKS.ProxyURL()
	portResult, portStats, err := m.callDiscoveryStage(runCtx, job, prepared,
		"naabu", "arsenal_task_ports", portArgs)
	_ = portSOCKS.Close()
	portSOCKSStats := portSOCKS.Stats()
	stats["port_tcp"] = portSOCKSStats
	stats["stages"].(map[string]any)["naabu"] = redactDiscoveryValue(portStats, portSecret)
	if err != nil {
		finish.Error = err.Error()
		finish.ExitState = portResult.ExitState
		m.finishDiscoveryContext(parent, runCtx, &finish)
		return
	}
	services, portRecords, err := discoveryOpenServices(bindings, ports, portResult.Records)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	stats["open_host_ports"] = len(services)
	if err := m.persistObservations(job, portRecords, nil, portSecret); err != nil {
		finish.Error = err.Error()
		return
	}
	finish.ExitState = portResult.ExitState
	if !portResult.Complete || (portResult.ExitState != "ok" && portResult.ExitState != "ok_empty") {
		finish.Error = fmt.Sprintf("naabu stage ended with %s", portResult.ExitState)
		return
	}
	if portSOCKSStats.ScopeBlocked != 0 || portSOCKSStats.AuthBlocked != 0 {
		finish.Error = "naabu attempted a destination outside its frozen TCP lease"
		return
	}
	if len(services) == 0 {
		finish.Status, finish.ExitState, finish.Complete, finish.Error = "completed", "ok_empty", true, ""
		return
	}

	serviceTargets, serviceRoutes := collapseServiceTargets(services)
	serviceSecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create service-verification TCP lease: " + err.Error()
		return
	}
	secrets = append(secrets, serviceSecret)
	serviceSOCKS, err := m.traffic.StartScopedSOCKS5(runCtx, traffic.ScopedSOCKS5Policy{
		RunID: strconv.FormatInt(job.ID, 10) + "-httpx", Routes: serviceRoutes,
		ListenHost: m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost,
		Username: "uroboros", Password: serviceSecret,
	})
	if err != nil {
		finish.Error = "start httpx TCP lease: " + err.Error()
		return
	}
	defer serviceSOCKS.Close() //nolint:errcheck
	targetSecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create service evidence proxy lease: " + err.Error()
		return
	}
	secrets = append(secrets, targetSecret)
	targetProxy, err := m.traffic.StartScopedProxy(runCtx, traffic.ScopedProxyPolicy{
		RunID: strconv.FormatInt(job.ID, 10), Scope: prepared.scope,
		MaxRequests: prepared.request.MaxRequests,
		ListenHost:  m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost,
		ProxyToken: targetSecret, UpstreamProxyURL: serviceSOCKS.ProxyURL(),
	})
	if err != nil {
		finish.Error = "start httpx task evidence proxy: " + err.Error()
		return
	}
	defer targetProxy.Close() //nolint:errcheck
	serviceArgs := cloneArgs(common)
	serviceArgs["service_targets_json"] = mustJSON(serviceTargets)
	serviceArgs["proxy_url"] = targetProxy.ProxyURL()
	serviceArgs["ca_pem"] = string(caPEM)
	serviceResult, serviceStats, err := m.callDiscoveryStage(runCtx, job, prepared,
		"httpx", "arsenal_task_services", serviceArgs)
	_ = targetProxy.Close()
	_ = serviceSOCKS.Close()
	targetProxyStats := targetProxy.Stats()
	serviceSOCKSStats := serviceSOCKS.Stats()
	stats["target_proxy"] = targetProxyStats
	stats["service_tcp"] = serviceSOCKSStats
	stats["stages"].(map[string]any)["httpx"] = redactDiscoveryValue(serviceStats, targetSecret, serviceSecret)
	if err != nil {
		finish.Error = err.Error()
		finish.ExitState = serviceResult.ExitState
		m.finishDiscoveryContext(parent, runCtx, &finish)
		return
	}
	if err := validateHTTPXDiscoveryRecords(serviceTargets, serviceResult.Records); err != nil {
		finish.Error = err.Error()
		return
	}
	if err := m.persistObservations(job, serviceResult.Records, targetProxy.CapturedExchanges(), targetSecret); err != nil {
		finish.Error = err.Error()
		return
	}
	finish.ExitState = serviceResult.ExitState
	if targetProxyStats.Requests == 0 || targetProxyStats.Allowed == 0 || targetProxyStats.Captured == 0 {
		finish.Error = "httpx did not complete a scope-approved HTTP exchange through Traffic"
		return
	}
	if targetProxyStats.ScopeBlocked != 0 || targetProxyStats.AuthBlocked != 0 ||
		targetProxyStats.BudgetBlocked != 0 || serviceSOCKSStats.ScopeBlocked != 0 ||
		serviceSOCKSStats.AuthBlocked != 0 {
		finish.Error = "httpx crossed or exhausted its frozen DNS/Traffic boundary"
		return
	}
	if !serviceResult.Complete || (serviceResult.ExitState != "ok" && serviceResult.ExitState != "ok_empty") {
		finish.Error = fmt.Sprintf("httpx stage ended with %s", serviceResult.ExitState)
		return
	}
	finish.Status, finish.Complete, finish.Error = "completed", true, ""
}

func logArsenalFinish(jobID int64, err error) {
	if err != nil {
		log.Printf("[arsenal] job %d finalization failed: %v", jobID, err)
	}
}

func (m *Manager) finishDiscoveryContext(parent, runCtx context.Context, finish *db.ArsenalJobFinish) {
	if cause := context.Cause(parent); cause != nil {
		finish.Status = "canceled"
		finish.Complete = false
		finish.Error = cause.Error()
		return
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		finish.Status = "failed"
		finish.ExitState = "timeout"
		finish.Complete = false
		finish.Error = "discovery workflow budget expired"
	}
}

func (m *Manager) discoveryBackendArgs(job *db.ArsenalJob, prepared preparedRun) map[string]any {
	return map[string]any{
		"task_id": job.TaskID, "run_id": strconv.FormatInt(job.ID, 10), "domain": prepared.target,
		"scope_json": string(prepared.scopeJSON), "scope_hash": prepared.scopeHash,
		"config_json": string(prepared.configJSON), "config_hash": prepared.configHash,
	}
}

func cloneArgs(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+2)
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (m *Manager) callDiscoveryStage(ctx context.Context, job *db.ArsenalJob, prepared preparedRun,
	stage, backendTool string, args map[string]any) (scanResult, map[string]any, error) {
	client, err := m.connect(ctx)
	if err != nil {
		return scanResult{}, nil, err
	}
	raw, callErr := client.Call(ctx, backendTool, args)
	closeErr := client.Close()
	if callErr != nil {
		return scanResult{}, nil, fmt.Errorf("call Arsenal %s stage: %w", stage, callErr)
	}
	if closeErr != nil {
		return scanResult{}, nil, fmt.Errorf("close Arsenal %s session: %w", stage, closeErr)
	}
	var result scanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return scanResult{}, nil, fmt.Errorf("decode Arsenal %s result: %w", stage, err)
	}
	backendStats, provenance, err := validateResult(job, prepared, result)
	if err != nil {
		return result, nil, err
	}
	if result.Stage != stage {
		return result, nil, errors.New("Arsenal backend returned a mismatched discovery stage")
	}
	if err := compareMetadata(prepared.metadata, provenance); err != nil {
		return result, nil, err
	}
	return result, backendStats, nil
}

func (m *Manager) resolveHosts(ctx context.Context, hosts []string, maximum int) ([]resolvedHost, int) {
	if maximum < 1 {
		maximum = 1
	}
	unique := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		unique[host] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for host := range unique {
		ordered = append(ordered, host)
	}
	sort.Strings(ordered)
	results := make(chan resolvedHost, len(ordered))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, host := range ordered {
		host := host
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results <- resolvedHost{host: host, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			addresses, err := m.resolveHost(lookupCtx, host)
			if err != nil {
				results <- resolvedHost{host: host, err: err}
				return
			}
			set := make(map[string]struct{}, len(addresses))
			for _, raw := range addresses {
				address, ok := netip.AddrFromSlice(raw)
				if ok {
					set[address.Unmap().String()] = struct{}{}
				}
			}
			values := make([]string, 0, len(set))
			for address := range set {
				values = append(values, address)
			}
			sort.Strings(values)
			if len(values) > maximum {
				values = values[:maximum]
			}
			results <- resolvedHost{host: host, addresses: values}
		}()
	}
	wg.Wait()
	close(results)
	out := make([]resolvedHost, 0, len(ordered))
	errorsCount := 0
	for result := range results {
		if result.err != nil || len(result.addresses) == 0 {
			errorsCount++
			continue
		}
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].host < out[j].host })
	return out, errorsCount
}

func (m *Manager) resolveHost(ctx context.Context, host string) ([]net.IP, error) {
	if m.cfg.Resolve != nil {
		return m.cfg.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func routesForResolved(resolved []resolvedHost, ports []int) []traffic.ScopedTCPRoute {
	routes := make([]traffic.ScopedTCPRoute, 0, len(resolved)*len(ports))
	for _, item := range resolved {
		for _, port := range ports {
			routes = append(routes, traffic.ScopedTCPRoute{Host: item.host, Port: port, Addresses: item.addresses})
		}
	}
	return routes
}

func routesForBindings(bindings []discoveryBinding, ports []int, names bool) []traffic.ScopedTCPRoute {
	grouped := make(map[string][]string)
	for _, binding := range bindings {
		host := binding.Address
		if names {
			host = binding.Host
		}
		grouped[host] = append(grouped[host], binding.Address)
	}
	routes := make([]traffic.ScopedTCPRoute, 0, len(grouped)*len(ports))
	for host, addresses := range grouped {
		sort.Strings(addresses)
		for _, port := range ports {
			routes = append(routes, traffic.ScopedTCPRoute{Host: host, Port: port, Addresses: addresses})
		}
	}
	return routes
}

func discoveryCandidates(domain string, input []scannerRecord, maximum int) ([]string, []scannerRecord, bool, error) {
	set := map[string]scannerRecord{
		domain: {Tool: "subfinder", Stage: "subfinder", Type: "subdomain", Severity: "info",
			Name: domain, Host: domain, Evidence: "source=task-root"},
	}
	for _, record := range input {
		if record.Tool != "subfinder" || record.Stage != "subfinder" || record.Type != "subdomain" {
			return nil, nil, false, errors.New("subfinder returned an invalid discovery record")
		}
		host, err := canonicalDiscoveryHost(record.Host)
		if err != nil || (host != domain && !strings.HasSuffix(host, "."+domain)) {
			return nil, nil, false, errors.New("subfinder returned a host outside the authorized root domain")
		}
		record.Host, record.Name = host, host
		set[host] = record
	}
	hosts := make([]string, 0, len(set))
	for host := range set {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	limited := false
	limit := maximum + 1 // the authorized apex is always retained in addition to the typed subdomain budget
	if len(hosts) > limit {
		limited = true
		selected := []string{domain}
		for _, host := range hosts {
			if host != domain && len(selected) < limit {
				selected = append(selected, host)
			}
		}
		hosts = selected
		sort.Strings(hosts)
	}
	records := make([]scannerRecord, 0, len(hosts))
	for _, host := range hosts {
		records = append(records, set[host])
	}
	return hosts, records, limited, nil
}

func discoveryDNSRecords(resolved []resolvedHost) ([]discoveryBinding, []scannerRecord) {
	bindings := make([]discoveryBinding, 0)
	records := make([]scannerRecord, 0)
	for _, item := range resolved {
		for _, address := range item.addresses {
			bindings = append(bindings, discoveryBinding{Host: item.host, Address: address})
			records = append(records, scannerRecord{
				Tool: "dns", Stage: "dns", Type: "dns", Severity: "info", Name: "A/AAAA",
				Host: item.host, Address: address, Evidence: "frozen system resolver answer",
			})
		}
	}
	return bindings, records
}

func discoveryOpenServices(bindings []discoveryBinding, ports []int, input []scannerRecord) ([]discoveryService, []scannerRecord, error) {
	allowedAddresses := make(map[string]struct{}, len(bindings))
	hostsByAddress := make(map[string][]string)
	for _, binding := range bindings {
		allowedAddresses[binding.Address] = struct{}{}
		hostsByAddress[binding.Address] = append(hostsByAddress[binding.Address], binding.Host)
	}
	allowedPorts := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		allowedPorts[port] = struct{}{}
	}
	serviceSet := make(map[string]discoveryService)
	records := make([]scannerRecord, 0)
	for _, record := range input {
		address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(record.Address), "[]"))
		if err != nil || record.Tool != "naabu" || record.Stage != "naabu" || record.Type != "port" {
			return nil, nil, errors.New("naabu returned an invalid discovery record")
		}
		canonical := address.Unmap().String()
		if _, ok := allowedAddresses[canonical]; !ok {
			return nil, nil, errors.New("naabu returned an address outside the frozen DNS set")
		}
		if _, ok := allowedPorts[record.Port]; !ok {
			return nil, nil, errors.New("naabu returned a port outside the typed port set")
		}
		for _, host := range hostsByAddress[canonical] {
			service := discoveryService{Host: host, Address: canonical, Port: record.Port}
			key := host + "\x00" + canonical + "\x00" + strconv.Itoa(record.Port)
			serviceSet[key] = service
			records = append(records, scannerRecord{
				Tool: "naabu", Stage: "naabu", Type: "port", Severity: "info",
				Name: "tcp/" + strconv.Itoa(record.Port), Host: host, Address: canonical,
				Port: record.Port, Evidence: "connect=open through scoped SOCKS lease",
				ObservationID: record.ObservationID, DedupFP: record.DedupFP,
			})
		}
	}
	services := make([]discoveryService, 0, len(serviceSet))
	for _, service := range serviceSet {
		services = append(services, service)
	}
	sort.Slice(services, func(i, j int) bool {
		if services[i].Host != services[j].Host {
			return services[i].Host < services[j].Host
		}
		if services[i].Port != services[j].Port {
			return services[i].Port < services[j].Port
		}
		return services[i].Address < services[j].Address
	})
	return services, records, nil
}

func collapseServiceTargets(input []discoveryService) ([]discoveryService, []traffic.ScopedTCPRoute) {
	type routeKey struct {
		host string
		port int
	}
	grouped := make(map[routeKey]map[string]struct{})
	for _, service := range input {
		key := routeKey{host: service.Host, port: service.Port}
		if grouped[key] == nil {
			grouped[key] = make(map[string]struct{})
		}
		grouped[key][service.Address] = struct{}{}
	}
	keys := make([]routeKey, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].host != keys[j].host {
			return keys[i].host < keys[j].host
		}
		return keys[i].port < keys[j].port
	})
	targets := make([]discoveryService, 0, len(keys))
	routes := make([]traffic.ScopedTCPRoute, 0, len(keys))
	for _, key := range keys {
		addresses := make([]string, 0, len(grouped[key]))
		for address := range grouped[key] {
			addresses = append(addresses, address)
		}
		sort.Strings(addresses)
		targets = append(targets, discoveryService{Host: key.host, Address: addresses[0], Port: key.port})
		routes = append(routes, traffic.ScopedTCPRoute{Host: key.host, Port: key.port, Addresses: addresses})
	}
	return targets, routes
}

func validateHTTPXDiscoveryRecords(targets []discoveryService, records []scannerRecord) error {
	allowed := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		allowed[target.Host+"\x00"+strconv.Itoa(target.Port)] = struct{}{}
	}
	for index := range records {
		record := &records[index]
		if record.Tool != "httpx" || record.Type != "service" {
			return errors.New("httpx returned an invalid discovery record")
		}
		record.Stage = "httpx"
		endpoint, err := url.Parse(record.Endpoint)
		if err != nil || endpoint.Hostname() == "" {
			return errors.New("httpx returned an invalid service URL")
		}
		host, err := canonicalDiscoveryHost(endpoint.Hostname())
		if err != nil {
			return errors.New("httpx returned an invalid service host")
		}
		port := 80
		if endpoint.Scheme == "https" {
			port = 443
		}
		if endpoint.Port() != "" {
			port, err = strconv.Atoi(endpoint.Port())
			if err != nil {
				return errors.New("httpx returned an invalid service port")
			}
		}
		if _, ok := allowed[host+"\x00"+strconv.Itoa(port)]; !ok {
			return errors.New("httpx returned a service outside the naabu-open set")
		}
		record.Host, record.Port = host, port
	}
	return nil
}

func validatePassiveProviderEvidence(exchanges []traffic.ScopedExchange) error {
	successful := make(map[string]bool, len(discoveryProviderRoots))
	for _, exchange := range exchanges {
		if strings.ToUpper(strings.TrimSpace(exchange.Method)) != "GET" || exchange.Status < 200 || exchange.Status >= 300 {
			continue
		}
		parsed, err := url.Parse(exchange.URL)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host, err := canonicalDiscoveryHost(parsed.Hostname())
		if err != nil {
			continue
		}
		for _, root := range discoveryProviderRoots {
			if host == root || strings.HasSuffix(host, "."+root) {
				successful[root] = true
			}
		}
	}
	missing := make([]string, 0, len(discoveryProviderRoots))
	for _, root := range discoveryProviderRoots {
		if !successful[root] {
			missing = append(missing, root)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("subfinder lacks a successful audited response from fixed provider(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func discoveryPorts(config map[string]any) []int {
	values, _ := integerSlice(config["ports"])
	out := make([]int, len(values))
	for index, value := range values {
		out[index] = int(value)
	}
	return out
}

func intConfig(config map[string]any, name string) int {
	value, _ := integerValue(config[name])
	return int(value)
}

func mustJSON(value any) string {
	raw, _ := canonicalJSON(value)
	return string(raw)
}

func redactRuntimeSecrets(value string, secrets ...string) string {
	for _, secret := range secrets {
		value = redactRuntimeSecret(value, secret)
	}
	return value
}

func redactDiscoveryValue(value any, secrets ...string) any {
	out := value
	for _, secret := range secrets {
		out = redactValue(out, secret)
	}
	return out
}
