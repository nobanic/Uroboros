package db

import "database/sql"

type promptCatalog struct {
	catalog agentCatalog
	agentID int64
}

func (prompts promptCatalog) variables() ([]PromptVar, error) {
	return catalogRead(prompts.catalog, `SELECT var_name,COALESCE(description,''),COALESCE(example,''),source FROM agent_prompt_vars WHERE agent_id=$1 ORDER BY var_name`, []any{prompts.agentID}, []PromptVar{}, func(values *[]PromptVar, row rowDecoder) error {
		var variable PromptVar
		if err := row.Scan(&variable.Name, &variable.Description, &variable.Example, &variable.Source); err != nil {
			return err
		}
		*values = append(*values, variable)
		return nil
	})
}

func (prompts promptCatalog) current() (string, error) {
	var text sql.NullString
	err := prompts.catalog.database.QueryRow(`SELECT p.template_text FROM agents a JOIN agent_prompts p ON p.id=a.current_prompt_id WHERE a.id=$1`, prompts.agentID).Scan(&text)
	if err == sql.ErrNoRows {
		err = nil
	}
	return text.String, err
}

func (prompts promptCatalog) versions() ([]PromptVersion, error) {
	return catalogRead(prompts.catalog, `SELECT version,template_text,COALESCE(note,''),created_at FROM agent_prompts WHERE agent_id=$1 ORDER BY version DESC`, []any{prompts.agentID}, []PromptVersion{}, func(values *[]PromptVersion, row rowDecoder) error {
		var version PromptVersion
		if err := row.Scan(&version.Version, &version.Template, &version.Note, &version.CreatedAt); err != nil {
			return err
		}
		*values = append(*values, version)
		return nil
	})
}

func (prompts promptCatalog) append(template, note, author string) (int, error) {
	version := 0
	err := prompts.catalog.transaction(func(tx *sql.Tx) error {
		var next int
		if err := tx.QueryRow(`SELECT COALESCE(max(version),0)+1 FROM agent_prompts WHERE agent_id=$1`, prompts.agentID).Scan(&next); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,'')) RETURNING id`, prompts.agentID, next, template, note, author).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, id, prompts.agentID); err != nil {
			return err
		}
		// Preserve the computed version if Commit itself fails. Earlier statement
		// failures leave the public return value at zero.
		version = next
		return nil
	})
	return version, err
}
