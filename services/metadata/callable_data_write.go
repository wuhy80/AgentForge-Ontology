package metadata

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func canonicalDataWrite(body *c.Callable) error {
	if body.SideEffects != "dataWrite" {
		if len(body.DataWrite) != 0 {
			return problem(422, "invalid_data_write", "仅dataWrite定义允许声明业务写入范围")
		}
		body.DataWrite = nil
		return nil
	}
	if len(body.DataWrite) == 0 || len(body.DataWrite) > 32 {
		return problem(422, "invalid_data_write", "业务写入须声明1到32个不同的Schema及操作")
	}
	seen := map[string]bool{}
	for i := range body.DataWrite {
		item := &body.DataWrite[i]
		id := item.SchemaID
		if id == "" || len(id) > 128 || !utf8.ValidString(id) || strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 || seen[strings.ToLower(id)] {
			return problem(422, "invalid_data_write", "写入Schema标识不能为空、重复或含非法字符")
		}
		seen[strings.ToLower(id)] = true
		operations := map[string]bool{}
		if len(item.Operations) == 0 || len(item.Operations) > 4 {
			return problem(422, "invalid_data_write", "每个Schema须声明1到4种基本写操作")
		}
		for _, operation := range item.Operations {
			if (operation != "create" && operation != "update" && operation != "delete" && operation != "upsert") || operations[operation] {
				return problem(422, "invalid_data_write", "写操作仅允许不重复的create、update、delete、upsert；批量操作沿用基本操作声明")
			}
			operations[operation] = true
		}
		sort.Strings(item.Operations)
	}
	sort.Slice(body.DataWrite, func(i, j int) bool { return body.DataWrite[i].SchemaID < body.DataWrite[j].SchemaID })
	return nil
}

func dataWriteCapabilitiesEqual(a, b []c.DataWriteCapability) bool {
	normalize := func(items []c.DataWriteCapability) []c.DataWriteCapability {
		out := make([]c.DataWriteCapability, len(items))
		for i, item := range items {
			out[i] = c.DataWriteCapability{SchemaID: item.SchemaID, Operations: append([]string(nil), item.Operations...)}
			sort.Strings(out[i].Operations)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].SchemaID < out[j].SchemaID })
		return out
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func validateDataWriteSchema(d, target c.Definition, id string) error {
	if target.ID != id || target.TenantID != d.TenantID || target.Kind != c.KindSchema || target.Schema == nil {
		return problem(422, "invalid_data_write_reference", "业务写入范围必须引用同租户已生效的Schema稳定标识")
	}
	return nil
}

func validateDataWriteReferences(ctx context.Context, tx *sql.Tx, d c.Definition) error {
	for _, body := range []*c.Callable{d.Action, d.Function} {
		if body == nil {
			continue
		}
		for _, item := range body.DataWrite {
			var raw sql.NullString
			err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='schema' AND status='active' AND current_json IS NOT NULL", d.TenantID, item.SchemaID).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return problem(422, "invalid_data_write_reference", "业务写入范围必须引用同租户已生效的Schema稳定标识")
			}
			if err != nil {
				return err
			}
			target, err := parseDefinition(raw)
			if err != nil {
				return err
			}
			if err := validateDataWriteSchema(d, target, item.SchemaID); err != nil {
				return err
			}
		}
	}
	return nil
}
