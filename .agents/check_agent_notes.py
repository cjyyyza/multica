#!/usr/bin/env python3
"""Agent Notes v1: portable, standard-library-only, read-only by default."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
from urllib.parse import unquote, urlsplit

PROTOCOL = "agent-notes/v1"
STATES = {"proposed", "implemented", "rejected"}
KINDS = {"feature", "bug-fix", "simplification", "architecture", "process", "testing"}
SECTIONS = {
    "problem": {"问题", "Problem"},
    "proposal": {"方案", "Proposal"},
    "alternatives": {"替代方案", "Alternatives considered"},
    "acceptance": {"验收标准", "Acceptance criteria"},
    "risks": {"风险", "Risks"},
    "decision": {"决策", "Decision"},
    "consequences": {"后果", "Consequences"},
    "verification": {"验证与限制", "Verification"},
}
REQUIRED = {
    "proposed": ("problem", "proposal", "alternatives", "acceptance", "risks"),
    "implemented": ("problem", "decision", "alternatives", "consequences", "verification"),
    "rejected": ("problem", "proposal", "alternatives"),
}
LINK = re.compile(r"\[[^\]\n]+\]\(\s*(<[^>\n]+>|[^\s)]+)(?:\s+['\"][^)\n]*)?\s*\)")
REFERENCE_LINK = re.compile(r"^\s*\[[^\]\n]+\]:\s*(<[^>\n]+>|\S+)", re.MULTILINE)
DATE_NAME = re.compile(r"^(\d{4}-\d{2}-\d{2})-.+\.md$")


def read_json(path: Path):
    return json.loads(path.read_text(encoding="utf-8"))


def inside(root: Path, relative: str) -> Path:
    if not isinstance(relative, str) or not relative or "\\" in relative:
        raise ValueError(f"expected a non-empty forward-slash relative path: {relative!r}")
    if Path(relative).is_absolute() or re.match(r"^[A-Za-z]:", relative):
        raise ValueError(f"absolute path is not allowed: {relative}")
    target = (root / relative).resolve()
    if not target.is_relative_to(root) or target == root:
        raise ValueError(f"path escapes or names the project root: {relative}")
    return target


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes().replace(b"\r\n", b"\n")).hexdigest()


def linked(path: Path) -> bool:
    if path.is_symlink():
        return True
    if os.name == "nt" and path.exists():
        return bool(path.lstat().st_file_attributes & stat.FILE_ATTRIBUTE_REPARSE_POINT)
    return False


def unfenced(text: str) -> str:
    lines, fence = [], None
    for line in text.splitlines():
        match = re.match(r"^\s*(`{3,}|~{3,})", line)
        if match:
            token = match[1]
            if fence is None:
                fence = token
            elif token[0] == fence[0] and len(token) >= len(fence):
                fence = None
            continue
        if fence is None:
            lines.append(line)
    return "\n".join(lines)


def local_link(project: Path, owner: Path, raw: str) -> Path | None:
    value = raw.strip("<>")
    parsed = urlsplit(value)
    if parsed.scheme in {"https", "http", "mailto"} or value.startswith("#"):
        return None
    if parsed.scheme or parsed.netloc or value.startswith(("/", "\\")):
        raise ValueError(f"unsupported or absolute link: {value}")
    target = (owner.parent / unquote(parsed.path)).resolve()
    if not target.is_relative_to(project):
        raise ValueError(f"link escapes project: {value}")
    return target


def markdown_files(root: Path, errors: list[str]) -> list[Path]:
    result = []
    for parent, dirs, files in os.walk(root, followlinks=False):
        for name in list(dirs):
            child = Path(parent) / name
            if linked(child):
                errors.append(f"linked note directory is not supported: {child}")
                dirs.remove(name)
        for name in files:
            child = Path(parent) / name
            if child.suffix.lower() == ".md":
                if linked(child):
                    errors.append(f"linked note file is not supported: {child}")
                else:
                    result.append(child)
    return sorted(result)


def baseline_manifest(project: Path, ref: str, manifest: Path) -> dict:
    def git(*args: str) -> str:
        completed = subprocess.run(
            ["git", "-C", str(project), *args], capture_output=True, text=True,
            encoding="utf-8", check=False,
        )
        if completed.returncode:
            raise ValueError(f"git baseline read failed: {completed.stderr.strip()}")
        return completed.stdout.strip()

    sha = git("rev-parse", "--verify", "--end-of-options", ref + "^{commit}")
    git_root = Path(git("rev-parse", "--show-toplevel")).resolve()
    rel = manifest.relative_to(git_root).as_posix()
    if not git("ls-tree", "--full-tree", "--name-only", sha, "--", rel):
        return {}
    data = json.loads(git("show", f"{sha}:{rel}"))
    if data.get("protocol") != PROTOCOL or not isinstance(data.get("files"), dict):
        raise ValueError("invalid baseline archive manifest")
    return data["files"]


def check(project: Path, seal: bool = False, base_ref: str | None = None) -> dict:
    project = project.resolve()
    config_path = inside(project, ".agents/agent-notes.json")
    config = read_json(config_path)
    if config.get("protocol") != PROTOCOL or config.get("layout") not in {"lifecycle", "flat"}:
        raise ValueError("unsupported protocol or layout")
    notes_root = inside(project, config.get("notes_root"))
    unresolved = project / config["notes_root"]
    for part in [unresolved, *unresolved.parents]:
        if part == project:
            break
        if linked(part):
            raise ValueError(f"notes_root crosses a linked directory: {part}")
    legacy = config.get("legacy_files", [])
    if not isinstance(legacy, list) or len(set(legacy)) != len(legacy):
        raise ValueError("legacy_files must be a list of unique explicit file paths")
    for entry in legacy:
        if any(char in entry for char in "*?[]"):
            raise ValueError(f"legacy wildcard is forbidden: {entry}")
        if not inside(notes_root, entry).is_file():
            raise ValueError(f"legacy file is missing: {entry}")
    for entry in config.get("current_sources", []):
        if not inside(project, entry).is_file():
            raise ValueError(f"current source is missing: {entry}")

    errors, records, archives, names, archived_paths = [], {}, {}, {}, set()
    paths = markdown_files(notes_root, errors) if notes_root.exists() else []
    for path in paths:
        rel = path.relative_to(notes_root).as_posix()
        if rel in legacy:
            continue
        label = path.relative_to(project).as_posix()
        fail = lambda message: errors.append(f"{label}: {message}")
        match = DATE_NAME.match(path.name)
        if not match:
            fail("filename must be YYYY-MM-DD-topic.md")
        else:
            try:
                dt.date.fromisoformat(match[1])
            except ValueError:
                fail("invalid filename date")
        if path.name in names:
            fail(f"duplicate note identity, also at {names[path.name]}")
        names[path.name] = label
        text = path.read_text(encoding="utf-8")
        prose = unfenced(text)
        lines = text.splitlines()
        if not lines or not lines[0].startswith("# Agent Note: ") or not lines[0][14:].strip():
            fail("missing Agent Note title")
        if len(lines) < 3 or lines[1] != "" or not lines[2].startswith("Status: "):
            fail("expected blank line then Status on line 3")
        header = prose.split("\n## ", 1)[0]
        metadata = {}
        for line in header.splitlines():
            field = re.match(r"^([A-Za-z][A-Za-z-]*): (.+)$", line)
            if field:
                if field[1] in metadata:
                    fail(f"duplicate metadata: {field[1]}")
                metadata[field[1]] = field[2].strip()
        status = metadata.get("Status", "")
        state = "rejected" if status.startswith("rejected — ") and status[11:].strip() else status
        if state not in STATES or status == "rejected":
            fail("invalid status or missing rejection reason")
        if metadata.get("Protocol") != PROTOCOL:
            fail("missing Protocol: agent-notes/v1")
        if metadata.get("Kind") not in KINDS:
            fail("invalid or missing Kind")
        archived = path.relative_to(notes_root).parts[0] == "archived"
        if config["layout"] == "lifecycle" and not archived:
            if path.relative_to(notes_root).parts[0] != state:
                fail("status and lifecycle directory disagree")
        if archived:
            archived_paths.add(path.resolve())
            if state != "implemented":
                fail("only implemented notes may be archived")
            try:
                dt.date.fromisoformat(metadata.get("Archived", ""))
            except ValueError:
                fail("missing or invalid Archived date")
            archives[label] = digest(path)
        elif "Archived" in metadata:
            fail("Archived metadata outside archived directory")
        section_matches = list(re.finditer(r"^## ([^\n]+)(?:\n|$)", prose, re.MULTILINE))
        headings = [match[1].strip() for match in section_matches]
        bodies = {}
        for index, section_match in enumerate(section_matches):
            end = section_matches[index + 1].start() if index + 1 < len(section_matches) else len(prose)
            bodies[section_match[1].strip()] = prose[section_match.end():end].strip()
        if not headings or headings[0] not in SECTIONS["problem"]:
            fail("first section must be Problem / 问题")
        for required in REQUIRED.get(state, ()):
            accepted = SECTIONS[required]
            found = [h for h in headings if h in accepted]
            if len(found) != 1:
                fail(f"expected one {required} section")
            else:
                if not bodies[found[0]]:
                    fail(f"empty {required} section")
        if state == "implemented" and any(h in {
            "方案", "Proposal", "计划", "Plan", "迁移计划", "Migration plan",
            "验收标准", "Acceptance criteria",
        } for h in headings):
            fail("implemented record still contains proposal-stage sections")
        if not archived:
            for raw in LINK.findall(prose) + REFERENCE_LINK.findall(prose):
                try:
                    target = local_link(project, path, raw)
                    if target is not None and not target.exists():
                        fail(f"missing link target: {raw}")
                except ValueError as exc:
                    fail(str(exc))
        records[path.resolve()] = metadata

    for path, metadata in records.items():
        if path in archived_paths:
            continue
        for field, inverse in [("Supersedes", "Superseded-by"), ("Superseded-by", "Supersedes")]:
            if field not in metadata:
                continue
            links = LINK.findall(metadata[field])
            if len(links) != 1:
                errors.append(f"{path.name}: {field} must contain one Markdown link")
                continue
            target = local_link(project, path, links[0])
            if target == path or target not in records:
                errors.append(f"{path.name}: {field} must name another managed note")
                continue
            reverse = LINK.findall(records[target].get(inverse, ""))
            if len(reverse) != 1 or local_link(project, target, reverse[0]) != path:
                errors.append(f"{path.name}: missing reciprocal {inverse}")

    manifest_path = inside(project, ".agents/notes-archive.json")
    manifest = read_json(manifest_path) if manifest_path.exists() else {"protocol": PROTOCOL, "files": {}}
    if manifest.get("protocol") != PROTOCOL or not isinstance(manifest.get("files"), dict):
        raise ValueError("invalid archive manifest")
    sealed = manifest["files"]
    for path, expected in sealed.items():
        if archives.get(path) != expected:
            errors.append(f"sealed archive changed, moved or missing: {path}")
    if base_ref:
        for path, expected in baseline_manifest(project, base_ref, manifest_path).items():
            if sealed.get(path) != expected:
                errors.append(f"baseline archive seal changed or removed: {path}")
    unsealed = {key: value for key, value in archives.items() if key not in sealed}
    if unsealed and not seal:
        errors.append("unsealed archives; review the archival change then use --seal-archives")
    if seal and not errors and unsealed:
        manifest["files"] = dict(sorted({**sealed, **unsealed}.items()))
        temporary = manifest_path.with_suffix(".json.tmp")
        with temporary.open("x", encoding="utf-8", newline="\n") as handle:
            handle.write(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
        temporary.replace(manifest_path)
    return {
        "protocol": PROTOCOL, "project": str(project), "notes": len(records),
        "legacy_files": len(legacy), "archives": len(archives),
        "baseline_checked": base_ref is not None, "sealed_new": len(unsealed) if seal and not errors else 0,
        "errors": errors,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--seal-archives", action="store_true")
    parser.add_argument("--base-ref", help="trusted Git commit/ref for append-only archive verification")
    args = parser.parse_args()
    try:
        result = check(args.project, args.seal_archives, args.base_ref)
    except (OSError, ValueError, TypeError, KeyError) as exc:
        print(json.dumps({"errors": [str(exc)]}, ensure_ascii=False))
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return bool(result["errors"])


if __name__ == "__main__":
    sys.exit(main())
