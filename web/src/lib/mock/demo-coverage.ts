import type { CoverageGraphData, CoverageGraphNode } from '../types';

/** A seed-time tree assembler. Parent references own the matching edge; output stays mutable. */
class CoverageTree {
  private readonly nodes: CoverageGraphNode[] = [];
  private readonly edges: CoverageGraphData['edges'] = [];

  private add(node: CoverageGraphNode, parent?: string) {
    this.nodes.push(node);
    if (parent !== undefined) this.edges.push({ src: node.key, dst: parent });
  }

  company(id: number, label: string) {
    this.add({ key: 'c:' + id, kind: 'company', label, tested: false, in_scope: false, company_id: id });
  }

  domain(id: number, domain: string, parent: string, tested = false, company?: number, root?: string) {
    const isRoot = company !== undefined;
    this.add({
      key: 'a:' + id, kind: isRoot ? 'root_domain' : 'subdomain', label: domain,
      tested, in_scope: !isRoot, asset_id: id, domain,
      ...(isRoot ? { company_id: company } : { root_domain: root }),
    }, parent);
  }

  service(id: number, domain: string, url: string, title: string, status: number, parent: string, tested = false) {
    this.add({
      key: 'a:' + id, kind: 'service', label: domain, tested, in_scope: true,
      asset_id: id, domain, url, port: 443, page_title: title, status_code: status,
    }, parent);
  }

  endpoint(id: number, label: string, url: string, domain: string, parent: string, tested = false) {
    this.add({
      key: 'a:' + id, kind: 'endpoint', label, tested, in_scope: true, asset_id: id, url, domain,
    }, parent);
  }

  address(id: number, ip: string, company: number, parent: string) {
    this.add({
      key: 'a:' + id, kind: 'ip', label: ip, tested: true, in_scope: true,
      asset_id: id, ip, company_id: company,
    }, parent);
  }

  graph(): CoverageGraphData { return { nodes: this.nodes, edges: this.edges }; }
}

export function createCoverageCatalog() {
  const tree = new CoverageTree();
  tree.company(1, "Acme Corp");
  tree.domain(1, "acme.com", "c:1", false, 1);
  tree.domain(2, "www.acme.com", "a:1", true, undefined, "acme.com");
  tree.domain(3, "api.acme.com", "a:1", true, undefined, "acme.com");
  tree.domain(4, "admin.acme.com", "a:1", false, undefined, "acme.com");
  tree.service(5, "www.acme.com", "https://www.acme.com", "Uroboros Lab application Context: Acme.", 200, "a:2", true);
  tree.endpoint(6, "https://www.acme.com/search", "https://www.acme.com/search?q=", "www.acme.com", "a:5", true);
  tree.service(7, "api.acme.com", "https://api.acme.com", "API Gateway", 401, "a:3", true);
  tree.endpoint(8, "https://api.acme.com/v1/orders", "https://api.acme.com/v1/orders?id=", "api.acme.com", "a:7", false);
  tree.service(9, "admin.acme.com", "https://admin.acme.com", "Uroboros Lab application", 200, "a:4", false);
  tree.address(10, "203.0.113.10", 1, "c:1");
  const by_type = [
    { type: 'subdomain', total: 6, tested: 4 },
    { type: 'service', total: 9, tested: 4 },
    { type: 'endpoint', total: 8, tested: 2 },
    { type: 'ip', total: 3, tested: 1 },
  ];
  const denominator = by_type.reduce((total, row) => total + row.total, 0);
  const tested = by_type.reduce((total, row) => total + row.tested, 0);
  return {
    coverage: { enabled: true, scope_rows: by_type.length, denominator, tested, pct: tested / denominator, by_type },
    coverageGraph: tree.graph(),
  };
}
