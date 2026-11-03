package db

import "fmt"

func mcpFields(server *MCPServer) []recordField {
	return []recordField{
		field("id", &server.ID).immutable(), field("name", &server.Name), field("transport", &server.Transport),
		field("command", &server.Command).defaultRead("''").nullableText(), field("args", &server.Args), field("env", &server.Env),
		field("url", &server.URL).defaultRead("''").nullableText(), field("enabled", &server.Enabled),
		field("insecure", &server.Insecure), field("request_timeout_seconds", &server.RequestTimeoutSeconds),
	}
}

func (database *DB) mcpRecords() ([]*MCPServer, error) {
	statement := "SELECT " + recordProjection(mcpFields(new(MCPServer))) + " FROM mcp_servers ORDER BY id"
	servers, failure := graphRows(database, statement, nil, func(row rowDecoder) (*MCPServer, error) {
		server := new(MCPServer)
		failure := decodeRecord(row, mcpFields(server))
		return server, failure
	})
	if failure != nil || len(servers) == 0 {
		return servers, failure
	}
	// The first reader is closed before loading the cache. One batch replaces
	// per-server queries while keeping absent/unavailable cache data best-effort.
	index := make(map[int64]*MCPServer, len(servers))
	for _, server := range servers {
		index[server.ID] = server
	}
	type cachedName struct {
		owner int64
		name  string
	}
	names, failure := graphRows(database, "SELECT server_id, tool_name FROM mcp_tools_cache ORDER BY server_id, tool_name", nil, func(row rowDecoder) (cachedName, error) {
		var item cachedName
		failure := row.Scan(&item.owner, &item.name)
		return item, failure
	})
	if failure == nil {
		for _, item := range names {
			if server := index[item.owner]; server != nil {
				server.Tools = append(server.Tools, item.name)
			}
		}
	}
	return servers, nil
}

func (database *DB) storeMCP(input *MCPServer) (int64, error) {
	server := *input
	if server.RequestTimeoutSeconds < 0 || server.RequestTimeoutSeconds > 3600 {
		return 0, fmt.Errorf("MCP request timeout must be between 1 and 3600 seconds (0 preserves/defaults)")
	}
	if len(server.Args) == 0 {
		server.Args = []byte("[]")
	}
	if len(server.Env) == 0 {
		server.Env = []byte("{}")
	}
	if server.ID == 0 {
		if server.RequestTimeoutSeconds == 0 {
			server.RequestTimeoutSeconds = 120
		}
		return persistRecord(database, "mcp_servers", 0, mcpFields(&server))
	}
	transaction, failure := database.Begin()
	if failure != nil {
		return 0, failure
	}
	defer transaction.Rollback()
	var previous string
	var timeout int
	if failure := transaction.QueryRow("SELECT name, request_timeout_seconds FROM mcp_servers WHERE id = $1 FOR UPDATE", server.ID).Scan(&previous, &timeout); failure != nil {
		return 0, failure
	}
	if server.RequestTimeoutSeconds == 0 {
		server.RequestTimeoutSeconds = timeout
	}
	if _, failure := persistRecord(transaction, "mcp_servers", server.ID, mcpFields(&server)); failure != nil {
		return 0, failure
	}
	if previous != server.Name {
		_, failure = transaction.Exec(`UPDATE mcp_tools_cache SET tool_name=$1 || substr(tool_name,char_length($2)+1)
WHERE server_id=$3 AND starts_with(tool_name,$2)`, "mcp__"+server.Name+"__", "mcp__"+previous+"__", server.ID)
		if failure != nil {
			return 0, failure
		}
	}
	return server.ID, transaction.Commit()
}
