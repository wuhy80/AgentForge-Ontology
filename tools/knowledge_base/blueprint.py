"""Reusable current-model inputs. Blueprint keys are not wire property IDs."""

from copy import deepcopy


def field(key, name, data_type="text", required=False):
    return {"key": key, "name": name, "dataType": data_type, "required": required, "nullable": not required}


COMMON = [
    field("source_key", "来源键", "string", True),
    field("library_key", "来源知识库标识", "string", True),
    field("source_locator", "来源定位", "text", True),
    field("source_fingerprint", "来源定位指纹", "string", True),
    field("available", "来源可用", "boolean", True),
    field("synced_at", "系统同步时间", "timestamp"),
]


def schema(key, group, name, description, fields):
    properties = deepcopy(COMMON)
    if key != "knowledge_base":
        properties.append(field("library_id", "所属知识库标识", "uuid", True))
        description += " 所属知识库标识为普通 UUID 值，不提供外键约束或 Link 实例导航。"
    return {
        "key": key, "group": group, "name": name, "description": description,
        "schema": {"mode": "schema_2_db", "storageLayout": "single", "properties": properties + fields},
        "indexes": [
            {"name": "来源键唯一", "unique": True, "fields": ["source_key"]},
            {"name": "按知识库查询", "unique": False, "fields": ["library_key"]},
        ],
    }


SCHEMAS = [
    schema("knowledge_base", "content", "知识库", "Git 单向同步的知识库；本机绝对路径仅存于导入端配置。来源键不随本次扫描范围变化。", [
        field("vault_key", "稳定库标识", "string", True), field("name", "知识库名称", "string", True),
        field("repository_key", "来源仓库标识"), field("sync_scope", "同步范围"),
    ]),
    schema("notes", "content", "笔记", "源 Markdown 与 YAML 原样保存，不执行插件或脚本；来源业务日期保留原字符串。来源键为定位凭证，实例身份使用服务端 UUID，重命名须人工映射。", [
        field("title", "标题", required=True), field("path", "来源相对路径", required=True),
        field("raw_markdown", "原始Markdown", "longtext", True), field("raw_yaml", "原始YAML", "longtext"),
        field("source_type", "来源类型"), field("source_status", "来源状态"),
        field("source_created", "来源创建日期", "string"), field("source_updated", "来源更新日期", "string"),
        field("content_hash", "内容摘要哈希", "string", True), field("byte_length", "原文UTF8字节数", "int64", True),
        field("frontmatter_status", "YAML解析状态", "string", True),
    ]),
    schema("attachments", "content", "附件", "仅保存授权扫描范围内的附件元数据，不将二进制塞入普通属性；不读取范围外附件。", [
        field("name", "附件名称", required=True), field("path", "附件相对路径", required=True),
        field("media_type", "附件媒体类型", "string"), field("byte_length", "附件字节数", "int64", True),
        field("content_hash", "附件内容哈希", "string", True),
    ]),
    schema("tags", "organization", "标签", "保留每个非空原始标签值，不将多值拼接为字符串，也不推断标签层次。", [
        field("raw_value", "原始标签值", required=True), field("display_name", "标签展示名", required=True),
    ]),
    schema("resources", "provenance", "外部资源", "HTTP/HTTPS URL 保留原值及完整查询参数；首批规范定位与原 URL 相同，不推断资源标题。", [
        field("raw_url", "原始URL", required=True), field("normalized_url", "规范化定位", required=True),
        field("title", "资源标题"), field("description", "资源说明", "longtext"), field("resource_type", "资源类型", "string", True),
    ]),
    schema("references", "provenance", "引用记录", "逐条保存普通引用、嵌入、来源 URL 与标签关联；来源/目标 UUID 是普通属性，无外键或 Link 实例导航。目标 UUID 至多一项；歧义或范围外目标不填 UUID。行列与字符偏移均针对原 Markdown，行列从1开始，偏移从0开始；YAML派生条目以source_section及原文位置区分。", [
        field("source_note_id", "来源笔记标识", "uuid", True),
        field("target_note_id", "目标笔记标识", "uuid"), field("target_resource_id", "目标外部资源标识", "uuid"),
        field("target_attachment_id", "目标附件标识", "uuid"), field("target_tag_id", "目标标签标识", "uuid"),
        field("raw_text", "引用原始写法", "longtext", True), field("target_text", "原始目标", required=True),
        field("reference_kind", "引用类型", "string", True), field("alias", "显示别名"),
        field("heading", "标题定位"), field("block", "块定位"), field("parameters", "附件参数"),
        field("source_section", "来源段落类型", "string", True), field("offset", "开始字符偏移", "int64", True),
        field("end_offset", "结束字符偏移", "int64", True), field("line", "出现行号", "int64", True),
        field("column", "出现列号", "int64", True), field("source_hash", "来源内容哈希", "string", True),
        field("resolution", "解析状态", "string", True), field("candidates", "候选目标清单", "longtext"),
    ]),
]


def link(key, name, source, target, forward, reverse, cardinality):
    return {"key": key, "group": "provenance", "name": name,
            "description": "候选关系定义；当前仅登记元数据，未实现 Link 实例读写与导航。",
            "source_schema": source, "target_schema": target, "forwardName": forward,
            "reverseName": reverse, "cardinality": cardinality, "onDelete": "restrict", "runtime_supported": False}


BLUEPRINT = {
    "category_name": "个人知识库",
    "groups": [{"key": "content", "name": "内容资产"}, {"key": "organization", "name": "知识组织"}, {"key": "provenance", "name": "引用溯源"}],
    "schemas": SCHEMAS,
    "links": [
        link("note_library", "笔记归属知识库", "notes", "knowledge_base", "归属知识库", "包含笔记", "N:1"),
        link("note_tag", "笔记标记标签", "notes", "tags", "标记标签", "标记笔记", "N:M"),
        link("note_reference", "笔记产生引用", "notes", "references", "产生引用", "源自笔记", "1:N"),
        link("reference_note", "引用指向笔记", "references", "notes", "指向笔记", "被引用记录", "N:1"),
        link("reference_resource", "引用指向资源", "references", "resources", "指向资源", "被引用记录", "N:1"),
        link("reference_attachment", "引用指向附件", "references", "attachments", "指向附件", "被引用记录", "N:1"),
    ],
}


def schema_inputs(category_id, group_ids):
    """Return {key,payload,indexes} before ID binding; do not send indexes yet.

    The platform assigns property IDs at creation. Caller saves those bindings,
    then translates index fields to fieldIds in a later draft save. No calls here.
    """
    result = []
    for item in BLUEPRINT["schemas"]:
        spec = deepcopy(item["schema"])
        spec["properties"] = [{key: value for key, value in prop.items() if key != "key"} for prop in spec["properties"]]
        result.append({"key": item["key"], "indexes": deepcopy(item["indexes"]), "payload": {
            "categoryId": category_id, "groupId": group_ids[item["group"]], "kind": "schema",
            "name": item["name"], "description": item["description"], "schema": spec,
        }})
    return result


def link_inputs(category_id, group_ids, schema_ids):
    """Bind candidate Link metadata to published Schema IDs; no Link facts."""
    return [{"key": item["key"], "payload": {
        "categoryId": category_id, "groupId": group_ids[item["group"]], "kind": "link",
        "name": item["name"], "description": item["description"], "link": {
            "sourceSchemaId": schema_ids[item["source_schema"]], "targetSchemaId": schema_ids[item["target_schema"]],
            **{key: item[key] for key in ("forwardName", "reverseName", "cardinality", "onDelete")},
        },
    }} for item in BLUEPRINT["links"]]


def definition_inputs(category_id, group_ids, schema_ids=None):
    """Create Schema inputs, optionally followed by bound candidate Link inputs."""
    return schema_inputs(category_id, group_ids) + (link_inputs(category_id, group_ids, schema_ids) if schema_ids is not None else [])
