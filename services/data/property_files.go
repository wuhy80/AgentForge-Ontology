package data

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"strconv"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const maxPropertyFileBytes = 4 << 20
const maxPropertyFiles = 10

func parsePropertyFileList(raw json.RawMessage) ([]c.FileSummary, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, problem(500, "invalid_file_summary", "文件列表摘要无效")
	}
	if value == "" && string(raw) == "null" {
		return nil, nil
	}
	var files []c.FileSummary
	if json.Unmarshal([]byte(value), &files) != nil || len(files) == 0 || len(files) > maxPropertyFiles {
		return nil, problem(500, "invalid_file_summary", "文件列表摘要无效")
	}
	seen := map[string]bool{}
	for _, file := range files {
		if !uuidPattern.MatchString(file.ID) || seen[file.ID] || file.Name == "" || file.MediaType == "" || file.Size < 1 || file.Size > maxPropertyFileBytes {
			return nil, problem(500, "invalid_file_summary", "文件列表摘要无效")
		}
		seen[file.ID] = true
	}
	return files, nil
}

func propertyFileField(d c.Definition, id string) (c.Property, error) {
	if d.Schema != nil && d.Schema.StorageBinding == nil {
		p, ok := findProperty(d.Schema, id)
		if ok && p.ID == id && p.FileKind != "" {
			return p, nil
		}
	}
	return c.Property{}, problem(404, "file_property_not_found", "文件属性不存在或不可访问")
}

func (h *handler) readPropertyFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(ctx, p.TenantID, r.PathValue("schemaId"))
	if err != nil {
		writeError(w, err)
		return
	}
	d := current.Definition
	dec, err := h.authorize(ctx, p, "instance.read", d.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	field, err := propertyFileField(d, r.PathValue("fieldId"))
	if err != nil {
		writeError(w, err)
		return
	}
	fileID := r.URL.Query().Get("fileId")
	if field.FileKind.Multiple() != (fileID != "") || (fileID != "" && !uuidPattern.MatchString(fileID)) {
		writeError(w, problem(404, "not_found", "文件不存在或不可访问"))
		return
	}
	if !uuidPattern.MatchString(r.PathValue("entityId")) || !fieldAllowed(dec.ReadFields, field.ID) {
		writeError(w, problem(404, "not_found", "文件不存在或不可访问"))
		return
	}
	var file []byte
	var summary c.FileSummary
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		row, err := selectInstanceFields(ctx, tx, d, []c.Property{field}, p.TenantID, r.PathValue("entityId"), dec.RowFilter, false)
		if err != nil {
			return err
		}
		if field.FileKind.Multiple() {
			files, err := parsePropertyFileList(row.Values[field.ID])
			if err != nil {
				return err
			}
			for _, entry := range files {
				if entry.ID == fileID {
					summary = entry
					break
				}
			}
			if summary.ID == "" {
				return problem(404, "not_found", "文件不存在或不可访问")
			}
		} else {
			var value string
			if json.Unmarshal(row.Values[field.ID], &value) != nil || value == "" {
				return problem(404, "not_found", "文件不存在或不可访问")
			}
			if json.Unmarshal([]byte(value), &summary) != nil {
				return problem(500, "invalid_file_summary", "文件摘要无效")
			}
		}
		err = tx.QueryRowContext(ctx, "SELECT content FROM af_data_property_files WHERE tenant_id=? AND schema_id=? AND entity_id=? AND field_id=? AND file_id=?", p.TenantID, d.ID, row.ID, field.ID, fileID).Scan(&file)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(404, "not_found", "文件不存在或不可访问")
		}
		if err != nil {
			return err
		}
		return h.recheck(ctx, p, "instance.read", d.ID, dec)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	disposition := "attachment"
	if field.FileKind.Image() && r.URL.Query().Get("preview") == "1" {
		disposition = "inline"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": summary.Name}))
	w.Header().Set("Content-Type", summary.MediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(file)))
	_, _ = w.Write(file)
}

func (h *handler) writePropertyFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, decisions, err := h.prepareMutation(ctx, p, r.PathValue("schemaId"), "update", "check")
	if err != nil {
		writeError(w, err)
		return
	}
	d := current.Definition
	field, err := propertyFileField(d, r.PathValue("fieldId"))
	if err != nil {
		writeError(w, err)
		return
	}
	if len(r.Header.Values("If-Match")) != 1 {
		writeError(w, problem(428, "precondition_required", "文件修改需要单个强 If-Match"))
		return
	}
	fileID := r.URL.Query().Get("fileId")
	if r.Method == http.MethodPut && fileID != "" ||
		r.Method == http.MethodDelete && (field.FileKind.Multiple() != (fileID != "") || (fileID != "" && !uuidPattern.MatchString(fileID))) {
		writeError(w, problem(422, "invalid_file_id", "多文件删除需要指定有效文件 ID"))
		return
	}
	etag, err := mutationETag(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, err)
		return
	}
	id, err := mutationID(current, r.PathValue("entityId"))
	if err != nil {
		writeError(w, problem(404, "not_found", "实例不存在或不可访问"))
		return
	}
	relevant := decisions.relevant(mutationPermission("update"))
	for _, dec := range relevant {
		if !fieldAllowed(dec.WriteFields, field.ID) {
			writeError(w, problem(403, "forbidden_field", "不允许修改此属性"))
			return
		}
	}
	raw := json.RawMessage("null")
	var file []byte
	var summary c.FileSummary
	if r.Method == http.MethodPut {
		file, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxPropertyFileBytes))
		if err != nil {
			writeError(w, problem(400, "invalid_file_body", "无法读取文件或超过 4 MiB"))
			return
		}
		summary, err = c.DescribePropertyFile(field.FileKind, r.URL.Query().Get("name"), file, maxPropertyFileBytes)
		if err != nil {
			writeError(w, problem(422, "invalid_file", err.Error()))
			return
		}
		if field.FileKind.Image() {
			config, _, err := image.DecodeConfig(bytes.NewReader(file))
			if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 {
				writeError(w, problem(422, "invalid_image", "图片须为有效 PNG、JPEG 或 GIF，边长不超过 4096 像素"))
				return
			}
			if _, _, err = image.Decode(bytes.NewReader(file)); err != nil {
				writeError(w, problem(422, "invalid_image", "无法解码图片"))
				return
			}
		}
		if field.FileKind.Multiple() {
			fileID = c.NewID()
			summary.ID = fileID
		}
		encoded, _ := json.Marshal(summary)
		raw, _ = json.Marshal(string(encoded))
	}
	var next c.Instance
	err = h.withMutationStore(ctx, p, current, func(store mutationStore) error {
		old, found, err := store.Lookup(ctx, mutationMatch{ID: id})
		if err != nil {
			return err
		}
		if !found {
			return problem(404, "not_found", "实例不存在或不可访问")
		}
		if err = mutationCheckOld(ctx, store, d.Schema, old, relevant, false); err != nil {
			return err
		}
		if old.ETag != etag {
			return problem(412, "precondition_failed", "实例已被修改，请重新读取")
		}
		if field.FileKind.Multiple() {
			files, err := parsePropertyFileList(old.Values[field.ID])
			if err != nil {
				return err
			}
			if file != nil {
				if len(files) >= maxPropertyFiles {
					return problem(422, "file_limit", "每个文件属性最多保存 10 个文件")
				}
				files = append(files, summary)
			} else {
				found := false
				for i, entry := range files {
					if entry.ID == fileID {
						files = append(files[:i], files[i+1:]...)
						found = true
						break
					}
				}
				if !found {
					return problem(404, "not_found", "文件不存在或不可访问")
				}
			}
			if len(files) > 0 {
				encoded, _ := json.Marshal(files)
				raw, _ = json.Marshal(string(encoded))
			}
		}
		next = old
		next.ETag = c.NewID()
		next.Values = projectInstance(old, []string{"*"}).Values
		next.Values[field.ID] = raw
		for _, dec := range relevant {
			if err = checkNewRow(ctx, store.Tx(), d.Schema, next.Values, dec); err != nil {
				return err
			}
		}
		if err = store.Update(ctx, old, next, []string{field.ID}); err != nil {
			return err
		}
		if file == nil {
			_, err = store.Tx().ExecContext(ctx, "DELETE FROM af_data_property_files WHERE tenant_id=? AND schema_id=? AND entity_id=? AND field_id=? AND file_id=?", p.TenantID, d.ID, id, field.ID, fileID)
		} else if field.FileKind.Multiple() {
			_, err = store.Tx().ExecContext(ctx, "INSERT INTO af_data_property_files (tenant_id,schema_id,entity_id,field_id,file_id,content) VALUES (?,?,?,?,?,?)", p.TenantID, d.ID, id, field.ID, fileID, file)
		} else {
			_, err = store.Tx().ExecContext(ctx, "INSERT INTO af_data_property_files (tenant_id,schema_id,entity_id,field_id,file_id,content) VALUES (?,?,?,?,?,?) ON DUPLICATE KEY UPDATE content=?", p.TenantID, d.ID, id, field.ID, fileID, file, file)
		}
		if err != nil {
			return err
		}
		if err = store.Emit(ctx, "instance.updated", next); err != nil {
			return err
		}
		return h.recheckMutations(ctx, p, current, decisions)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+next.ETag+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
