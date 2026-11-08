package execution

import (
	"context"
	"errors"
	"net/http"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) sourceToolAccess(ctx context.Context, p c.Principal, id string) error {
	if h.cfg.Metadata == nil {
		return problem(503, "metadata_unavailable", "定义服务未配置")
	}
	var definition c.CurrentDefinition
	var err error
	if reader, ok := h.cfg.Metadata.(DraftMetadataReader); ok {
		definition, err = reader.Draft(ctx, p.TenantID, id)
		if err != nil {
			var failure *apiError
			if !errors.As(err, &failure) || failure.status != 404 {
				return err
			}
			definition, err = h.cfg.Metadata.Current(ctx, p.TenantID, id)
		}
	} else {
		definition, err = h.cfg.Metadata.Current(ctx, p.TenantID, id)
	}
	if err != nil {
		return err
	}
	if definition.Definition.ID != id || definition.Definition.TenantID != p.TenantID || (definition.Status != "draft" && definition.Status != "active") || (definition.Definition.Kind != c.KindFunction && definition.Definition.Kind != c.KindAction) {
		return problem(404, "not_found", "未找到可编辑的源码定义")
	}
	return h.authorizeOperation(ctx, p, "definition.edit", definition.Definition.Kind, id)
}

func (h *handler) sourceTools(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	var request c.SourceToolRequest
	if err := decodeRequest(w, r, &request); err != nil {
		return err
	}
	if !validID(request.DefinitionID) || request.Runtime == nil || request.Runtime.Language != "go" || (request.Operation != "check" && request.Operation != "format") {
		return problem(422, "unsupported_source_tool", "Go源码工具须指定已保存的定义、源码和check或format操作")
	}
	if err := c.ValidateScriptRuntime(request.Runtime); err != nil {
		return problem(422, "invalid_runtime", err.Error())
	}
	if err := h.sourceToolAccess(r.Context(), p, request.DefinitionID); err != nil {
		return err
	}
	runner, ok := h.cfg.Runner.(sourceToolRunner)
	if !ok {
		return problem(503, "source_tools_unavailable", "Go源码工具尚未就绪")
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return problem(429, "execution_busy", "源码工具与计算并发已满，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	imageID, err := readyRuntime(ctx, h.cfg.Runner, "go")
	if err != nil {
		return problem(503, "runtime_unavailable", "Go运行器尚未就绪")
	}
	result, err := runner.SourceTools(ctx, request.Runtime, imageID, request.Operation)
	if err != nil {
		return problem(503, "source_tools_failed", "Go源码工具处理失败或超过时限，请重试")
	}
	if err := h.session(r.Context(), r, p); err != nil {
		return err
	}
	if err := h.sourceToolAccess(r.Context(), p, request.DefinitionID); err != nil {
		return err
	}
	writeJSON(w, 200, result)
	return nil
}
