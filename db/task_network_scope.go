package db

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

// TaskNetworkScopeSnapshot is the network-executable projection of a task's
// declared authorization boundary at one instant. Seeds only decide where a
// crawler starts; RootDomains, ExactHosts and CIDRs are the actual authority.
// The Katana runner normalizes and fingerprints this value before starting.
type TaskNetworkScopeSnapshot struct {
	RootDomains []string `json:"root_domains,omitempty"`
	ExactHosts  []string `json:"exact_hosts,omitempty"`
	CIDRs       []string `json:"cidrs,omitempty"`
	Seeds       []string `json:"seeds,omitempty"`
}

type taskNetworkAsset struct {
	Type   string
	Domain string
	IP     string
	URL    string
}

// TaskNetworkScope freezes the current task plus its permitted direct-source
// context into a crawler-ready scope. Company/ICP scope becomes executable only
// through explicit company domain/network rules or already-known matching assets;
// a company name, ICP registration or keyword is never sent to the network.
func (s *AssetStore) TaskNetworkScope(taskID int64) (TaskNetworkScopeSnapshot, error) {
	if taskID <= 0 {
		return TaskNetworkScopeSnapshot{}, fmt.Errorf("task id must be positive")
	}
	scopes, err := s.ListTaskScopeWithSources(taskID)
	if err != nil {
		return TaskNetworkScopeSnapshot{}, err
	}
	companyRules := make(map[int64][]ScopeRule)
	for _, scope := range scopes {
		if scope.Kind != "company" || scope.CompanyID == nil || *scope.CompanyID <= 0 {
			continue
		}
		if _, ok := companyRules[*scope.CompanyID]; ok {
			continue
		}
		rules, err := s.company.GetScope(*scope.CompanyID)
		if err != nil {
			return TaskNetworkScopeSnapshot{}, err
		}
		companyRules[*scope.CompanyID] = rules
	}

	rows, err := s.db.Query(`WITH `+scopeTargetCTE+`
SELECT a.type, COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.url,'')
FROM assets a JOIN target ON target.id=a.id
ORDER BY a.id`, taskID)
	if err != nil {
		return TaskNetworkScopeSnapshot{}, err
	}
	defer rows.Close()
	assets := make([]taskNetworkAsset, 0)
	for rows.Next() {
		var asset taskNetworkAsset
		if err := rows.Scan(&asset.Type, &asset.Domain, &asset.IP, &asset.URL); err != nil {
			return TaskNetworkScopeSnapshot{}, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return TaskNetworkScopeSnapshot{}, err
	}
	return assembleTaskNetworkScope(scopes, companyRules, assets), nil
}

func assembleTaskNetworkScope(scopes []TaskScope, companyRules map[int64][]ScopeRule, assets []taskNetworkAsset) TaskNetworkScopeSnapshot {
	roots := map[string]struct{}{}
	hosts := map[string]struct{}{}
	nets := map[string]struct{}{}
	seeds := map[string]struct{}{}

	addRoot := func(raw string) {
		if host := canonicalNetworkHost(raw); host != "" && net.ParseIP(host) == nil {
			roots[host] = struct{}{}
		}
	}
	addHost := func(raw string) {
		if host := canonicalNetworkHost(raw); host != "" {
			if ip := net.ParseIP(host); ip != nil {
				if prefix := hostPrefix(ip); prefix != "" {
					nets[prefix] = struct{}{}
				}
				return
			}
			hosts[host] = struct{}{}
		}
	}
	addNet := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if addr, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
			addr = addr.Unmap()
			nets[netip.PrefixFrom(addr, addr.BitLen()).String()] = struct{}{}
			return
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			nets[prefix.Masked().String()] = struct{}{}
		}
	}
	addSeed := func(raw string) {
		if seed := canonicalHTTPSeed(raw); seed != "" {
			seeds[seed] = struct{}{}
		}
	}

	for _, scope := range scopes {
		switch scope.Kind {
		case "root_domain":
			addRoot(scope.Domain)
		case "subdomain":
			addHost(scope.Domain)
		case "ip", "cidr":
			addNet(scope.Net)
		case "company":
			if scope.CompanyID == nil {
				continue
			}
			for _, rule := range companyRules[*scope.CompanyID] {
				switch rule.Kind {
				case "domain":
					addRoot(rule.Domain)
				case "ip", "cidr":
					addNet(rule.Net)
				}
			}
		}
	}

	// Known assets contribute concrete destinations without changing the declared
	// semantic scope. This is what makes ICP/company scope useful even when it has
	// no domain rule while avoiding arbitrary keyword-to-network expansion.
	for _, asset := range assets {
		addHost(asset.Domain)
		addHost(asset.IP)
		if asset.Type == "service" || asset.Type == "endpoint" {
			addSeed(asset.URL)
			if parsed, err := url.Parse(asset.URL); err == nil {
				addHost(parsed.Hostname())
			}
		}
	}

	// With no known HTTP service, try both common schemes for explicit names and
	// host-sized IP scopes. A broad CIDR is never enumerated here; httpx/naabu or a
	// known service must produce its concrete seeds first.
	if len(seeds) == 0 {
		for host := range roots {
			addSeed("https://" + urlHost(host) + "/")
			addSeed("http://" + urlHost(host) + "/")
		}
		for host := range hosts {
			addSeed("https://" + urlHost(host) + "/")
			addSeed("http://" + urlHost(host) + "/")
		}
		for raw := range nets {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix.Bits() != prefix.Addr().BitLen() {
				continue
			}
			host := prefix.Addr().String()
			addSeed("https://" + urlHost(host) + "/")
			addSeed("http://" + urlHost(host) + "/")
		}
	}

	return TaskNetworkScopeSnapshot{
		RootDomains: sortedStringSet(roots),
		ExactHosts:  sortedStringSet(hosts),
		CIDRs:       sortedStringSet(nets),
		Seeds:       sortedStringSet(seeds),
	}
}

func canonicalNetworkHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		raw = parsed.Hostname()
	} else if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String()
	}
	return DomainKey(strings.TrimSuffix(raw, "."))
}

func canonicalHTTPSeed(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := canonicalNetworkHost(parsed.Hostname())
	if host == "" {
		return ""
	}
	port := parsed.Port()
	parsed.Host = urlHost(host)
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}
	// Credentials and query values are part of the executable seed. The Katana
	// runner transports seeds over stdin (never argv) and persists a separately
	// redacted projection, so keeping them here does not leak them into job metadata.
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

func hostPrefix(ip net.IP) string {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return ""
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()).String()
}

func urlHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + strings.Trim(host, "[]") + "]"
	}
	return host
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
