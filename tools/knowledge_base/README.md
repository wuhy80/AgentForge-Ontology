# 个人知识库工具

本目录的离线层只读取显式选中的源库目录，生成六类对象及引用清单，不调用 API、不连接数据库、不修改或改写源文件。`blueprint.py` 维护“个人知识库”分类、内容资产/知识组织/引用溯源三个分组、六个 Schema 与六个候选 Link 的可复用蓝图；Link 仅登记定义，当前没有 Link 实例读写或导航。

## 只读扫描

依赖 Python 3.12 与 [requirements.txt](requirements.txt) 中的 PyYAML。源 YAML 使用 SafeLoader 的安全构造器，日期标量保留字符串，重复键、非法对象、无法解析的 YAML 和非字符串多值会报告错误，不静默转换。

```powershell
python -m pip install -r tools/knowledge_base/requirements.txt
python -m tools.knowledge_base.scan D:/code/obsidian-vault --scope Ontology --library-key obsidian-vault
python -m unittest tools.knowledge_base.test_scanner -v
```

扫描 JSON 写到标准输出；`complete=true` 时退出码为 0，存在文件读取、YAML 或来源冲突错误时退出码为 1。参数错误退出码为 2。输出包含原始笔记内容，应沿租户授权范围保存和使用。

`scan_vault(vault_root, scope="Ontology", *, library_key=None)` 返回可 JSON 序列化的字典。`library_key` 默认源目录名，可显式设为独立于本机路径的稳定库标识；知识库对象的来源键不含 scope。允许 scope 的第一层仅为技术笔记、大模型、Ontology、Golang编程、Rust编程、读书笔记、英语学习、数学学习、网址收藏，可进一步指定其下某个目录。扫描不会枚举其他内容根目录，也不会为了解析目标而读取 scope 外文件。

读取前即排除隐藏目录/文件、缓存、模板、私密路径、`_site`、`node_modules`、协作指令文件，以及源公开导出规则排除的 `技术笔记/虚拟机`、`技术笔记/安全` 和 `技术笔记/版本管理/知识库双仓库拆分与多机同步.md`。不跟随符号链接或 junction，scope 自身也不能穿过这些路径。

原文以 `read_bytes().decode("utf-8")` 读取，保留 BOM、CRLF、完整 Markdown 与完整 YAML 内容；内容摘要针对原始字节计算。原始 Markdown 包含 frontmatter；`raw_yaml` 是 frontmatter 分隔符之间的原文。读取前后比较文件大小、修改时间和文件标识，扫描期间可检测的修改会使 `complete=false`；这不是 Git 历史快照，也不能保证多个文件在同一时刻的事务快照。调用者不能根据不完整扫描判定来源删除。

## 扫描结果与导入约定

结果包括 `entities`、`associations`、`diagnostics`、`counts` 和 `complete`。`entities` 的键为 `knowledge_base`、`notes`、`attachments`、`tags`、`resources`、`references`，每个元素包含：

```json
{
  "source_key": "notes:当前来源定位的sha256",
  "data": {
    "source_key": "与外层一致",
    "library_key": "obsidian-vault",
    "source_locator": "Ontology/某篇笔记.md",
    "source_fingerprint": "库标识与完整来源定位的sha256",
    "available": true
  },
  "bindings": {"library_id": "知识库对象的source_key"}
}
```

`source_key` 是当前来源的确定性查找凭证，最多 80 个 ASCII 字符，不是实例 UUID。`source_locator`、`source_fingerprint` 也只是当前定位；笔记路径变更会改变来源键，不能凭标题、文件名或内容相似度自动沿用实例身份。在线导入端维护来源键到服务端 UUID 的映射，重命名须显式映射。离线扫描不生成永久实体身份，也不根据缺失项删除对象。

所有 Schema 共享 `source_key`、`library_key`、`source_locator`、`source_fingerprint`、`available`、`synced_at` 属性，除知识库外另有 `library_id`。扫描不填 `synced_at`，在线导入在实际新建或更新时填真实同步时刻。Data 的 int64 线协议为字符串，扫描输出的字节数、偏移和行列均为十进制字符串。可选值不存在时不输出该属性，蓝图将它们声明为 nullable；更新时在线层须明确清理失效目标 UUID，不能遗漏字段而保留过期绑定。

笔记 envelope 额外保留 `aliases`、`tags`、`source_values` 列表；这三个列表是扫描/解析信息，并非 Schema 的集合属性。每个标签值建立独立标签对象及逐条 `reference_kind=tag` 引用记录，来源 URL 和来源 WikiLink 也逐条记录。该方案可通过现有单 Schema 对象写入运行，不依赖 Link 事实服务。`associations` 是去重后的候选关系清单，不代表已写入关系。

引用的 `bindings.source_note_id` 指向来源笔记来源键，已解析目标会有 `target_note_id`、`target_resource_id`、`target_attachment_id` 或 `target_tag_id` 中的一项。在线导入将这些来源键换成服务端 UUID，写入引用记录的普通属性；没有外键约束或自动反向 Link 导航。未知、范围外或歧义目标不填目标 UUID。

## 引用解析边界

扫描围栏代码块、缩进代码、行内反引号、HTML 注释和 Obsidian `%%` 注释之外的 WikiLink、嵌入、HTTP/HTTPS Markdown/裸 URL 和内联标签。YAML 中的 `source` 独立解析，不会因同时扫描 Markdown 而重复计数。没有联网获取标题、附件或 URL 内容，也不执行 Dataview、Templater、HTML 脚本或其他插件。

每条引用保留 `raw_text`、原始目标、heading、block、alias 和附件参数；表格里的 `\|` 别名分隔符可解析，但原文保持原样。`offset/end_offset` 是原 Markdown 的 Python Unicode 字符偏移，前闭后开；`line/column` 从 1 开始，不是 UTF-8 字节偏移。引用使用 `source_hash` 标记解析所依据的原文字节摘要。

路径先在显式范围内索引；带 `./`/`../` 的路径按来源笔记目录解释，`/` 和 scope 前缀按库相对路径解释，无路径裸名与别名在已扫描候选中查找。没有路径的相同文件名、大小写/Unicode 折叠后冲突或相同别名返回 `ambiguous`，不会取第一个候选。精确路径可以消除裸名歧义。无候选且无法证明范围内目标时返回 `unresolved`；明确的 scope 外路径返回 `out_of_scope`，不检测它是否存在。位置锚点保留但首期不验证 heading/block 是否真实存在，`resolved` 只表示目标文件或已登记标签/URL 唯一。

source YAML 的普通文字值保留为待解析来源记录；不将“Palantir 官方文档”等文字擅自创建为外部 URL 对象。URL 规范定位首期与原始 URL 相同，保留查询参数与 fragment，不猜测等价 URL 或删参。标签首期按原始完整值区分，不推断层级。

## 蓝图使用

蓝图属性含 `key`，索引含 `fields`，二者属于本地映射键，不能直接传 API。`schema_inputs(category_id, group_ids)` 返回除本地键后的 Schema 输入与待绑定索引；`link_inputs(category_id, group_ids, schema_ids)` 绑定已生效 Schema ID 后返回候选 Link 定义输入。`definition_inputs` 是二者的组合。在线层可为属性/索引生成 UUID 后一次提交完整定义，或按服务器返回 ID 建立映射后保存索引；实际 wire 协议以 `contracts/` 为准。

六个 Schema 均有来源键唯一索引和知识库标识普通索引。正文与 YAML 使用 `longtext`，不截断为 `text`；Data/存储对 longtext 的实际容量和请求体限制由所属服务验证，扫描本身不代表已成功导入。

## 创建模型与单向导入

在线入口默认仍只扫描；显式 `--apply` 后才登录并调用现有 Metadata/Data 公开 API，不直接写数据库、不修改授权策略、不创建测试账号。通过工作台账号密码和 Cookie/CSRF 完成授权，会话在结束时注销，状态文件不保存密码、Cookie 或 CSRF。默认只扫描 `Ontology`，不扩展全库。

在仓库根目录执行：

```powershell
# 预览：只读扫描，完整产物含原文，放入忽略目录。
python -m tools.knowledge_base --vault D:/code/obsidian-vault --output .cache/knowledge-base/ontology-scan.json

# 本地开发账号文件仅由工具读取，密码不进入命令行或输出。
python -m tools.knowledge_base --vault D:/code/obsidian-vault --credentials .cache/dev/admin.json --state .cache/knowledge-base/ontology-state.json --apply

# 只构建/生效六个对象和六个关系定义，不写实例。
python -m tools.knowledge_base --vault D:/code/obsidian-vault --credentials .cache/dev/admin.json --state .cache/knowledge-base/ontology-state.json --apply --models-only

# 登录并逐条回读比对已有实例，业务数据不写入。
python -m tools.knowledge_base --vault D:/code/obsidian-vault --credentials .cache/dev/admin.json --state .cache/knowledge-base/ontology-state.json --verify

# 无数据库测试：扫描、冲突、幂等及发布中断恢复。
python -m unittest discover -s tools/knowledge_base -t . -v
```

未指定 `--credentials` 时可交互输入租户、账号和密码，或配合 `--tenant`、`--username`、`ONTOLOGY_PASSWORD` 环境变量；密码没有命令行参数。`--url` 默认本机工作台地址，远程地址要求 HTTPS，API 重定向会被拒绝。依赖与服务启动见[开发指南](../../docs/开发指南.md)。

同一来源、范围、租户和工作台重复执行会复用定义及来源键唯一实例，不更新未改变的同步时间。状态默认保存到 `.cache/knowledge-base/`，可显式指定；状态、临时文件、锁和完整扫描产物均不得写入源库，也不能覆盖凭据或彼此重叠。OS 文件锁防止同一状态并发使用，原子替换保存恢复意图。跨来源或租户复用状态会拒绝。

每个模型先预览再生效，来源对象通过现有单条接口逐项提交；中途失败可能部分完成，不能视为整库事务或平台批量导入功能。重试使用保存的实例创建幂等意图和发布 ETag。已提交而未激活、已准备而未提交的发布均通过原操作恢复；未绑定的已有草稿不会自动发布。Schema 创建响应丢失时仅按预存的精确属性/索引 ID 意图认领草稿，Link 创建响应丢失且尚未获得 ID 时会安全停止，需核对恢复。

已有实例按 ETag 更新，系统独立修改与来源更新冲突时停止。完整扫描中确实缺失、且状态文件此前拥有的来源会标记不可用，不删除实例；同时发现笔记路径新增与消失时视为疑似重命名，先停止等待核对，不自动合并或换 UUID。当前尚无重命名映射界面或自动 Git 重命名同步。

平台当前 Schema 结构发布后不可演化，遇到已有不兼容模型会停止，不修改它的字段。定义展示改名按状态中的稳定 ID 复用，现有模型草稿会保留。状态丢失时可按来源键恢复内容完全相同的实例；与现有内容不同时拒绝覆盖。一个知识库的多 scope 协调尚未实现，首批保持单一 scope，不同时运行重叠范围的多份状态。

成功导入后会逐项回读所有目标字段（包括完整原文与 YAML）并比对。Link 仅有定义，`linkFactsWritten=0`；标签和来源关联实际保存在引用记录普通 UUID 属性中，无数据库外键、Link 导航或跨模型联查。Markdown 目前通过实例详情查看原文，尚无专门阅读器、全文搜索或问答。
