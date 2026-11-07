"""Read only one explicit vault scope; never fetch links or access APIs/DBs.

source_key identifies a current source location, not permanent entity identity.
The online importer maps it to server UUIDs; moves require explicit mapping.
"""

from __future__ import annotations

import hashlib
import json
import mimetypes
import os
from pathlib import Path, PurePosixPath
import posixpath
import re
from urllib.parse import unquote, urlsplit

import yaml
from yaml.nodes import MappingNode, ScalarNode, SequenceNode
from yaml.tokens import AliasToken


EXCLUDED_PARTS = frozenset({
    "_site", "_templates", "templates", "template", "node_modules", "__pycache__",
    "cache", "caches", "private", "privacy", "私密", "私密库", "隐私", "缓存", "模板",
})
EXCLUDED_FILES = frozenset({"agents.md", "claude.md"})
ALLOWED_ROOTS = frozenset({"技术笔记", "大模型", "Ontology", "Golang编程", "Rust编程", "读书笔记", "英语学习", "数学学习", "网址收藏"})
EXCLUDED_PATHS = (
    "技术笔记/虚拟机", "技术笔记/安全", "技术笔记/版本管理/知识库双仓库拆分与多机同步.md",
)
SCHEMA_KEYS = ("knowledge_base", "notes", "attachments", "tags", "resources", "references")


class SourceLoader(yaml.SafeLoader):
    """Safe YAML constructors, with date scalars intentionally kept as strings."""

    yaml_implicit_resolvers = {
        key: [(tag, pattern) for tag, pattern in values if tag != "tag:yaml.org,2002:timestamp"]
        for key, values in yaml.SafeLoader.yaml_implicit_resolvers.items()
    }

    def construct_mapping(self, node, deep=False):
        seen = set()
        for key, _ in node.value:
            if not isinstance(key, ScalarNode) or key.tag != "tag:yaml.org,2002:str":
                raise ValueError("YAML mappings require string keys")
            if key.value in seen:
                raise ValueError("duplicate YAML mapping key")
            seen.add(key.value)
        return super().construct_mapping(node, deep=deep)


SourceLoader.add_constructor("tag:yaml.org,2002:timestamp", lambda loader, node: loader.construct_scalar(node))


def _excluded(parts):
    return any(part.startswith(".") or part.casefold() in EXCLUDED_PARTS for part in parts)


def _excluded_path(path):
    folded = _normalize(path)
    return _excluded(PurePosixPath(path).parts) or any(folded == _normalize(item) or folded.startswith(_normalize(item) + "/") for item in EXCLUDED_PATHS)


def _normalize(value):
    # Case folding only creates candidate sets; collisions remain ambiguous.
    import unicodedata
    return unicodedata.normalize("NFC", value).casefold()


def _sha(value):
    return hashlib.sha256(value).hexdigest()


def source_key(schema_key, library_key, locator):
    """A bounded current-location lookup key, never a server UUID."""
    return schema_key + ":" + _sha((library_key + "\0" + locator).encode("utf-8"))


def _entity(schema_key, library_key, locator, data, library_source_key):
    key = source_key(schema_key, library_key, locator)
    data = {
        "source_key": key, "library_key": library_key, "source_locator": locator,
        "source_fingerprint": _sha((library_key + "\0" + locator).encode("utf-8")),
        "available": True, **data,
    }
    return {"source_key": key, "data": data,
            "bindings": {} if schema_key == "knowledge_base" else {"library_id": library_source_key}}


def _frontmatter(text):
    """Return raw YAML, body offset, data, nodes, YAML start, status/error."""
    start = 1 if text.startswith("\ufeff") else 0
    lines = text[start:].splitlines(keepends=True)
    if not lines or lines[0].rstrip("\r\n") != "---":
        return "", start, {}, {}, start, "absent", None
    yaml_start = start + len(lines[0])
    offset = yaml_start
    end = None
    for line in lines[1:]:
        if line.rstrip("\r\n") in ("---", "..."):
            end = offset
            body_start = offset + len(line)
            break
        offset += len(line)
    if end is None:
        return text[yaml_start:], len(text), {}, {}, yaml_start, "invalid", "frontmatter closing delimiter missing"
    raw = text[yaml_start:end]
    try:
        if len(raw.encode("utf-8")) > 1024 * 1024:
            raise ValueError("frontmatter exceeds 1 MiB parsing budget")
        alias_count = sum(isinstance(token, AliasToken) for token in yaml.scan(raw))
        if alias_count > 64:
            raise ValueError("frontmatter exceeds 64 YAML aliases")
        data = yaml.load(raw, Loader=SourceLoader)
        node = yaml.compose(raw, Loader=SourceLoader)
        if data is None:
            return raw, body_start, {}, {}, yaml_start, "parsed", None
        if not isinstance(data, dict) or not isinstance(node, MappingNode):
            raise ValueError("frontmatter must be a mapping")
        nodes = {key.value: value for key, value in node.value}
        return raw, body_start, data, nodes, yaml_start, "parsed", None
    except (yaml.YAMLError, ValueError, RecursionError) as error:
        return raw, body_start, {}, {}, yaml_start, "invalid", type(error).__name__ + ": invalid or unsupported YAML"


def _scalars(data, nodes, key):
    """Do not split scalar tags/sources on commas or silently coerce objects."""
    value = data.get(key)
    node = nodes.get(key)
    if value is None:
        return [], False
    values = value if isinstance(value, list) else [value]
    children = node.value if isinstance(node, SequenceNode) else [node]
    result = []
    invalid = False
    for index, item in enumerate(values):
        if not isinstance(item, str):
            invalid = True
            continue
        if item.strip():
            child = children[index] if index < len(children) else node
            result.append((item, child))
    return result, invalid


def _visible(text, body_start):
    """Mask code/comments using spaces, retaining every original character offset."""
    masked = list(text)

    def hide(begin, end):
        for index in range(begin, end):
            if masked[index] not in "\r\n":
                masked[index] = " "

    hide(0, body_start)
    fence = None
    offset = 0
    for line in text.splitlines(keepends=True):
        end = offset + len(line)
        if end <= body_start:
            offset = end
            continue
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)", line)
        if fence:
            hide(offset, end)
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= fence[1] and not marker[2].strip():
                fence = None
        elif marker:
            fence = marker[1][0], len(marker[1])
            hide(offset, end)
        elif line.startswith("    ") or line.startswith("\t"):
            hide(offset, end)
        offset = end
    value = "".join(masked)
    index = body_start
    while index < len(value):
        if value.startswith("<!--", index) or value.startswith("%%", index):
            marker = "-->" if value.startswith("<!--", index) else "%%"
            begin_length = 4 if marker == "-->" else 2
            stop = value.find(marker, index + begin_length)
            stop = len(value) if stop < 0 else stop + len(marker)
            hide(index, stop)
            index = stop
        elif value[index] == "`" and not _escaped(value, index):
            run = re.match(r"`+", value[index:])[0]
            closing = re.search(r"(?<!`)" + re.escape(run) + r"(?!`)", value[index + len(run):])
            if closing:
                stop = index + len(run) + closing.end()
                hide(index, stop)
                index = stop
            else:
                index += len(run)
        else:
            index += 1
    return "".join(masked)


def _escaped(text, offset):
    count = 0
    while offset > 0 and text[offset - 1] == "\\":
        count += 1
        offset -= 1
    return count % 2 == 1


def _wiki_parts(inside, embedded):
    # Obsidian Markdown tables escape the alias separator as \|. Preserve raw
    # spelling in raw_text, while interpreting that table spelling as a pipe.
    parts = inside.replace("\\|", "|").split("|")
    target = parts[0].strip()
    alias = "|".join(parts[1:]) if len(parts) > 1 else ""
    path, _, anchor = target.partition("#")
    heading, block = "", ""
    if anchor.startswith("^"):
        block = anchor[1:]
    elif "#^" in anchor:
        heading, block = anchor.split("#^", 1)
    else:
        heading = anchor
    if not path and not anchor and target.startswith("^"):
        path, block = "", target[1:]
    parameters = {}
    if anchor.startswith("page="):
        parameters["page"] = anchor[5:]
    if embedded and re.fullmatch(r"\d+(?:x\d+)?", alias):
        parameters["display"] = alias
    return {"target_text": target, "path_target": target if _http_url(target) else path, "alias": alias,
            "heading": heading, "block": block,
            "parameters": json.dumps(parameters, ensure_ascii=False, sort_keys=True) if parameters else ""}


def _http_url(value):
    try:
        parsed = urlsplit(value)
        return parsed.scheme.casefold() in ("http", "https") and bool(parsed.netloc)
    except ValueError:
        return False


def _trim_url(value):
    value = value.rstrip(".,;!?:，。；！、")
    while value.endswith(")") and value.count(")") > value.count("("):
        value = value[:-1]
    return value


def _tokens(original, visible):
    result = []
    occupied = []
    for match in re.finditer(r"(!?)\[\[([^\]\r\n]+)\]\]", visible):
        if _escaped(original, match.start()):
            continue
        result.append({"start": match.start(), "end": match.end(), "raw_text": original[match.start():match.end()],
                       "reference_kind": "embed" if match[1] else "wiki", **_wiki_parts(match[2], bool(match[1]))})
        occupied.append((match.start(), match.end()))

    def overlaps(start, end):
        return any(start < right and end > left for left, right in occupied)

    # Explicit HTTP Markdown links, including balanced parentheses in URLs.
    for match in re.finditer(r"(!?)\[([^\]\r\n]*)\]\(\s*(<?)(https?://)", visible):
        if _escaped(original, match.start()) or overlaps(match.start(), match.end()):
            continue
        start = match.end() - len(match[4])
        index, depth = start, 0
        while index < len(visible):
            char = visible[index]
            if char in "\r\n" or match[3] and char == ">" or not match[3] and char.isspace():
                break
            if not match[3] and char == ")" and depth == 0:
                break
            if char == "(":
                depth += 1
            elif char == ")":
                depth -= 1
            index += 1
        url = original[start:index]
        closing = index + 1 if match[3] and index < len(visible) and visible[index] == ">" else index
        suffix = re.match(r"\s*(?:\"[^\"\r\n]*\"|'[^'\r\n]*')?\s*\)", visible[closing:])
        if not suffix or not _http_url(url):
            continue
        end = closing + suffix.end()
        result.append({"start": match.start(), "end": end, "raw_text": original[match.start():end],
                       "reference_kind": "external_embed" if match[1] else "external", "target_text": url,
                       "path_target": url, "alias": match[2], "heading": "", "block": "", "parameters": ""})
        occupied.append((match.start(), end))
    for match in re.finditer(r"https?://[^\s<>\"'\]\u3000]+", visible):
        if overlaps(match.start(), match.end()):
            continue
        url = _trim_url(match[0])
        if _http_url(url):
            end = match.start() + len(url)
            result.append({"start": match.start(), "end": end, "raw_text": original[match.start():end],
                           "reference_kind": "external", "target_text": url, "path_target": url,
                           "alias": "", "heading": "", "block": "", "parameters": ""})
            occupied.append((match.start(), end))
    for match in re.finditer(r"(?<![\w/#])#([\w][\w/-]*)", visible):
        if overlaps(match.start(), match.end()) or _escaped(original, match.start()):
            continue
        result.append({"start": match.start(), "end": match.end(), "raw_text": original[match.start():match.end()],
                       "reference_kind": "tag", "target_text": match[1], "path_target": "",
                       "alias": "", "heading": "", "block": "", "parameters": ""})
    return sorted(result, key=lambda token: (token["start"], token["end"]))


class _Resolver:
    def __init__(self, files, scope):
        self.scope = scope
        self.paths = {}
        self.names = {}
        for path, item in files.items():
            self.paths.setdefault(_normalize(path), []).append(item)
            names = {PurePosixPath(path).name}
            if item["schema_key"] == "notes":
                names.add(PurePosixPath(path).stem)
                names.update(item.get("aliases", []))
            for name in names:
                self.names.setdefault(_normalize(name), []).append(item)

    def resolve(self, target, source_path):
        target = unquote(target).replace("\\", "/").strip()
        if not target:
            candidates = self.paths.get(_normalize(source_path), [])
            return self._result(candidates)
        if ":" in target.split("/", 1)[0]:
            return "unresolved", [], None
        if "/" not in target:
            return self._result(self.names.get(_normalize(target), []))
        if target.startswith(("./", "../")):
            paths = [posixpath.normpath(posixpath.join(posixpath.dirname(source_path), target))]
        elif target.startswith("/"):
            paths = [posixpath.normpath(target.lstrip("/"))]
        elif target == self.scope or target.startswith(self.scope + "/"):
            paths = [posixpath.normpath(target)]
        else:
            paths = [posixpath.normpath(target), posixpath.normpath(posixpath.join(posixpath.dirname(source_path), target))]
        candidates = []
        in_scope = False
        for path in paths:
            allowed = (path == self.scope or path.startswith(self.scope + "/")) and not _excluded_path(path)
            if not allowed:
                continue
            in_scope = True
            candidates.extend(self.paths.get(_normalize(path), []))
            if not path.casefold().endswith(".md"):
                candidates.extend(self.paths.get(_normalize(path + ".md"), []))
        status, candidates, selected = self._result(candidates)
        if status == "unresolved" and not in_scope:
            status = "out_of_scope"
        # A vault-root target from a different top-level directory is explicitly
        # outside scope; we never probe that path for existence.
        if status == "unresolved" and not target.startswith(("./", "../", "/")) and not target.startswith(self.scope + "/"):
            status = "out_of_scope"
        return status, candidates, selected

    @staticmethod
    def _result(items):
        unique = {item["source_key"]: item for item in items}
        candidates = sorted(unique.values(), key=lambda item: item["data"]["source_locator"])
        return ("resolved" if len(candidates) == 1 else "ambiguous" if candidates else "unresolved",
                candidates, candidates[0] if len(candidates) == 1 else None)


def scan_vault(vault_root, scope="Ontology", *, library_key=None):
    """Read admitted files under scope only and return a JSON serializable plan.

    Invalid scan configuration raises ValueError. File/YAML failures remain in
    diagnostics and set complete=False; callers must not infer source deletion.
    """
    root = Path(vault_root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError("vault_root must be a directory")
    scope = str(scope).replace("\\", "/").rstrip("/")
    scope_path = PurePosixPath(scope)
    if not scope or scope_path.is_absolute() or any(part in ("", ".", "..") for part in scope.split("/")) or ":" in scope or scope_path.parts[0] not in ALLOWED_ROOTS or _excluded_path(scope):
        raise ValueError("scope must name one explicit admitted relative directory")
    selected = root.joinpath(*scope_path.parts)
    for ancestor in [selected, *selected.parents]:
        if ancestor == root:
            break
        if ancestor.is_symlink() or getattr(ancestor, "is_junction", lambda: False)():
            raise ValueError("scope must not traverse symlinks or junctions")
    if not selected.is_dir() or not selected.resolve().is_relative_to(root):
        raise ValueError("scope must be an existing directory inside vault_root")
    library_key = root.name if library_key is None else library_key
    if not isinstance(library_key, str) or not library_key.strip() or len(library_key) > 255 or any(ord(char) < 32 for char in library_key):
        raise ValueError("library_key must contain 1 to 255 printable characters")
    entities = {key: [] for key in SCHEMA_KEYS}
    diagnostics = []
    library = _entity("knowledge_base", library_key, "knowledge_base", {
        "vault_key": library_key, "name": library_key, "repository_key": library_key, "sync_scope": scope,
    }, "")
    entities["knowledge_base"].append(library)
    library_source_key = library["source_key"]
    files = {}
    pending_notes = []
    associations = []

    def diagnostic(code, path, severity="error", message=""):
        diagnostics.append({"code": code, "path": path, "severity": severity, "message": message})

    def walk_error(error):
        try:
            path = Path(error.filename).relative_to(root).as_posix()
        except (TypeError, ValueError):
            path = scope
        diagnostic("directory_read_failed", path, message=type(error).__name__)

    for directory, dirs, names in os.walk(selected, topdown=True, followlinks=False, onerror=walk_error):
        base = Path(directory)
        admitted = []
        for name in sorted(dirs):
            candidate = base / name
            if _excluded_path(candidate.relative_to(root).as_posix()):
                continue
            if candidate.is_symlink() or getattr(candidate, "is_junction", lambda: False)():
                diagnostic("symlink_excluded", candidate.relative_to(root).as_posix(), "warning")
                continue
            admitted.append(name)
        dirs[:] = admitted
        for name in sorted(names):
            if _excluded_path((base / name).relative_to(root).as_posix()) or name.casefold() in EXCLUDED_FILES:
                continue
            path = base / name
            relative = path.relative_to(root).as_posix()
            if path.is_symlink() or not path.resolve().is_relative_to(selected.resolve()):
                diagnostic("symlink_excluded", relative, "warning")
                continue
            try:
                before = path.stat()
                if path.suffix.casefold() == ".md":
                    raw = path.read_bytes()
                    text = raw.decode("utf-8")
                    yaml_raw, body_start, metadata, nodes, yaml_start, status, error = _frontmatter(text)
                    if error:
                        diagnostic("invalid_frontmatter", relative, message=error)
                    aliases, alias_invalid = _scalars(metadata, nodes, "aliases")
                    if alias_invalid:
                        diagnostic("invalid_aliases", relative, message="aliases must be a string or a list of strings")
                    body = _visible(text, body_start)
                    heading = re.search(r"(?m)^ {0,3}#\s+(.+?)\s*#*\s*$", body)
                    title = metadata.get("title") if isinstance(metadata.get("title"), str) else heading[1] if heading else path.stem
                    item = _entity("notes", library_key, relative, {
                        "title": title, "path": relative, "raw_markdown": text, "raw_yaml": yaml_raw,
                        "content_hash": _sha(raw), "byte_length": str(len(raw)), "frontmatter_status": status,
                    }, library_source_key)
                    for source_name, field_name in (("type", "source_type"), ("status", "source_status"), ("created", "source_created"), ("updated", "source_updated")):
                        value = metadata.get(source_name)
                        if value is not None:
                            if isinstance(value, str):
                                item["data"][field_name] = value
                            else:
                                diagnostic("unsupported_metadata_scalar", relative, message=source_name + " is not a string; preserved in raw YAML")
                    item["aliases"] = [value for value, _ in aliases]
                    item["schema_key"] = "notes"
                    files[relative] = item
                    entities["notes"].append(item)
                    pending_notes.append((item, text, body, metadata, nodes, yaml_start))
                    associations.append({"kind": "note_library", "source_key": item["source_key"], "target_key": library_source_key})
                else:
                    digest = hashlib.sha256()
                    size = 0
                    with path.open("rb") as stream:
                        while chunk := stream.read(1024 * 1024):
                            digest.update(chunk)
                            size += len(chunk)
                    item = _entity("attachments", library_key, relative, {
                        "name": path.name, "path": relative, "media_type": mimetypes.guess_type(path.name)[0] or "application/octet-stream",
                        "byte_length": str(size), "content_hash": digest.hexdigest(),
                    }, library_source_key)
                    item["schema_key"] = "attachments"
                    files[relative] = item
                    entities["attachments"].append(item)
                after = path.stat()
                if (before.st_size, before.st_mtime_ns, before.st_ino) != (after.st_size, after.st_mtime_ns, after.st_ino):
                    diagnostic("source_changed_during_scan", relative, message="file changed while it was being read")
            except (OSError, UnicodeError) as error:
                diagnostic("file_read_failed", relative, message=type(error).__name__)

    normalized_paths = {}
    for relative in files:
        normalized_paths.setdefault(_normalize(relative), []).append(relative)
    for paths in normalized_paths.values():
        if len(paths) > 1:
            diagnostic("source_path_collision", paths[0], message=json.dumps(sorted(paths), ensure_ascii=False))

    resolver = _Resolver(files, scope)
    tags = {}
    resources = {}
    reference_occurrences = {}

    def tag(value):
        if value not in tags:
            tags[value] = _entity("tags", library_key, "tag:" + value, {"raw_value": value, "display_name": value}, library_source_key)
            entities["tags"].append(tags[value])
        return tags[value]

    def resource(url):
        if url not in resources:
            resources[url] = _entity("resources", library_key, "url:" + url, {
                "raw_url": url, "normalized_url": url, "resource_type": "web",
            }, library_source_key)
            entities["resources"].append(resources[url])
        return resources[url]

    def reference(note, text, token, section):
        begin, end = token["start"], token["end"]
        kind = token["reference_kind"]
        locator = "reference:" + note["data"]["path"] + ":" + section + ":" + str(begin) + ":" + kind
        occurrence = reference_occurrences.get(locator, 0)
        reference_occurrences[locator] = occurrence + 1
        if occurrence:
            locator += ":occurrence:" + str(occurrence + 1)
        data = {key: token[key] for key in ("raw_text", "target_text", "reference_kind", "alias", "heading", "block", "parameters")}
        data.update({"source_section": section, "offset": str(begin), "end_offset": str(end),
                     "line": str(text.count("\n", 0, begin) + 1), "column": str(begin - text.rfind("\n", 0, begin)),
                     "source_hash": note["data"]["content_hash"]})
        entry = _entity("references", library_key, locator, data, library_source_key)
        entry["bindings"]["source_note_id"] = note["source_key"]
        if kind == "tag":
            target = tag(token["target_text"])
            data["resolution"] = "resolved"
            entry["bindings"]["target_tag_id"] = target["source_key"]
            associations.append({"kind": "note_tag", "source_key": note["source_key"], "target_key": target["source_key"]})
        elif _http_url(token["path_target"]):
            target = resource(token["path_target"])
            data["resolution"] = "resolved"
            entry["bindings"]["target_resource_id"] = target["source_key"]
            associations.append({"kind": "reference_resource", "source_key": entry["source_key"], "target_key": target["source_key"]})
        else:
            status, candidates, target = resolver.resolve(token["path_target"], note["data"]["path"])
            data["resolution"] = status
            if candidates:
                data["candidates"] = json.dumps([{"source_key": item["source_key"], "source_locator": item["data"]["source_locator"]} for item in candidates], ensure_ascii=False, sort_keys=True)
            if target:
                target_kind = "note" if target["schema_key"] == "notes" else "attachment"
                entry["bindings"]["target_" + target_kind + "_id"] = target["source_key"]
                associations.append({"kind": "reference_" + target_kind, "source_key": entry["source_key"], "target_key": target["source_key"]})
        entry["data"].update(data)
        entities["references"].append(entry)
        associations.append({"kind": "note_reference", "source_key": note["source_key"], "target_key": entry["source_key"]})

    for note, text, body, metadata, nodes, yaml_start in pending_notes:
        for token in _tokens(text, body):
            reference(note, text, token, "body")
        tag_values, invalid_tags = _scalars(metadata, nodes, "tags")
        source_values, invalid_sources = _scalars(metadata, nodes, "source")
        note["tags"] = [value for value, _ in tag_values]
        note["source_values"] = [value for value, _ in source_values]
        if invalid_tags:
            diagnostic("invalid_tags", note["data"]["path"], message="tags must be a string or a list of strings")
        if invalid_sources:
            diagnostic("invalid_sources", note["data"]["path"], message="source must be a string or a list of strings")
        for value, node in tag_values:
            begin, end = yaml_start + node.start_mark.index, yaml_start + node.end_mark.index
            reference(note, text, {"start": begin, "end": end, "raw_text": text[begin:end], "target_text": value,
                                 "reference_kind": "tag", "path_target": "", "alias": "", "heading": "", "block": "", "parameters": ""}, "frontmatter.tags")
        for value, node in source_values:
            begin, end = yaml_start + node.start_mark.index, yaml_start + node.end_mark.index
            fragment = text[begin:end]
            # A YAML source scalar can contain WikiLinks, URLs, or a plain source
            # label. Labels remain explicit unresolved provenance records.
            tokens = [token for token in _tokens(fragment, fragment) if token["reference_kind"] != "tag"]
            if not tokens:
                tokens = [{"start": 0, "end": len(fragment), "raw_text": fragment, "target_text": value,
                           "reference_kind": "source", "path_target": value, "alias": "", "heading": "", "block": "", "parameters": ""}]
            for token in tokens:
                token = {**token, "start": begin + token["start"], "end": begin + token["end"]}
                reference(note, text, token, "frontmatter.source")

    for key in entities:
        entities[key].sort(key=lambda item: item["source_key"])
    # Repeated tag mentions have distinct provenance records but one candidate
    # note-tag relationship. No Link instance is written by this module.
    associations = [json.loads(value) for value in sorted({json.dumps(item, sort_keys=True, ensure_ascii=False) for item in associations})]
    return {
        "format": "agentforge.knowledge-base.scan.v1", "library_key": library_key, "scope": scope,
        "complete": not any(item["severity"] == "error" for item in diagnostics),
        "entities": entities, "associations": associations, "diagnostics": diagnostics,
        "counts": {key: len(value) for key, value in entities.items()}, "link_instances_supported": False,
    }
