package server

import (
	"encoding/json"
	"log"
	"os/exec"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
	actool "github.com/nobanic/norma/tool"
)

func installToolResolution(database *db.DB, domain map[string]actool.CoreTool, overrides []discoveryReviewChecker) {
	review := discoveryReviewChecker(database)
	if len(overrides) > 0 && overrides[0] != nil {
		review = overrides[0]
	}
	agent.FindingTrafficBindingEnabled = func() bool { return database.GetBool(settingAgentTrafficBinding, false) }
	catalog := &toolResolution{database: database, domain: domain, review: review, provenance: newSourceProvenanceGovernor(database)}
	catalog.seed()
	_, err := exec.LookPath("bash")
	catalog.bash = err == nil
	if !catalog.bash {
		log.Printf("[tools] bash executable is unavailable; hiding Bash and interactive-shell capabilities")
	}
	agent.ToolResolve = catalog.resolve
}

func (catalog *toolResolution) seed() {
	for _, definition := range agent.BuiltinToolSeeds() {
		schema, _ := json.Marshal(definition.Schema)
		roles, _ := json.Marshal(definition.Agents)
		catalog.seedTool(definition.Key, definition.Desc, schema, roles)
	}
	worker, _ := json.Marshal([]string{"worker"})
	for _, tool := range traffic.SeedToolMetas() {
		schema, _ := json.Marshal(tool.InputSchema())
		catalog.seedTool(tool.Name(), tool.Description(), schema, worker)
	}
}

func (catalog *toolResolution) seedTool(name, description string, schema, roles json.RawMessage) {
	if err := catalog.database.SeedTool(name, description, schema, roles); err != nil {
		log.Printf("[tools] seed %s failed: %v", name, err)
	}
}
