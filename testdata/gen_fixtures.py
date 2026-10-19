#!/usr/bin/env python3
"""Regenerate the synthetic, redacted transcript corpus in testdata/projects.

The corpus mimics two Claude Code transcript shapes:
  * v1 (1.0.x): user message content is a plain string
  * v2 (2.0.x): content is an array of blocks, with attachments

Run from the repo root: python3 testdata/gen_fixtures.py
"""
import json
import os
import shutil
from datetime import datetime, timedelta, timezone

ROOT = os.path.join(os.path.dirname(__file__), "projects")
BASE = datetime(2026, 9, 21, 9, 0, tzinfo=timezone.utc)


class Session:
    def __init__(self, sid, cwd, day, version="2.0.14", branch="main"):
        self.sid, self.cwd, self.version, self.branch = sid, cwd, version, branch
        self.t = BASE + timedelta(days=day)
        self.lines = []
        self.last = None
        self.n = 0

    def _uuid(self):
        self.n += 1
        return f"{self.sid[:8]}-{self.n:04d}"

    def _ev(self, typ, parent="__last__", **kw):
        self.t += timedelta(seconds=20)
        u = self._uuid()
        ev = {
            "parentUuid": self.last if parent == "__last__" else parent,
            "isSidechain": False,
            "userType": "external",
            "cwd": self.cwd,
            "sessionId": self.sid,
            "version": self.version,
            "gitBranch": self.branch,
            "type": typ,
            "uuid": u,
            "timestamp": self.t.strftime("%Y-%m-%dT%H:%M:%S.000Z"),
        }
        ev.update(kw)
        self.lines.append(json.dumps(ev))
        self.last = u
        return u

    def user(self, text, parent="__last__"):
        if self.version.startswith("1."):
            content = text
        else:
            content = [{"type": "text", "text": text}]
        return self._ev("user", parent=parent, message={"role": "user", "content": content})

    def say(self, text):
        return self._ev("assistant", message={"role": "assistant", "content": [{"type": "text", "text": text}]})

    def tool(self, name, inp, result, is_error=False, tool_use_result=None):
        tid = f"toolu_{self.sid[:6]}_{self.n}"
        self._ev("assistant", message={"role": "assistant", "content": [
            {"type": "tool_use", "id": tid, "name": name, "input": inp}]})
        kw = {"message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": tid, "content": result, "is_error": is_error}]}}
        if tool_use_result is not None:
            kw["toolUseResult"] = tool_use_result
        self._ev("user", **kw)

    def raw(self, obj):
        self.lines.append(json.dumps(obj))

    def write(self, extra=""):
        enc = self.cwd.replace("/", "-")
        d = os.path.join(ROOT, enc)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, self.sid + ".jsonl"), "w") as f:
            f.write("\n".join(self.lines) + "\n" + extra)


API, INFRA, WEB = "/work/api", "/work/infra", "/work/web"

TESTIFY = [
    "No, don't use testify/assert. We use table-driven tests with the standard library.",
    "don't pull in testify again, use plain table-driven tests with t.Run",
    "Why did you use testify? We use stdlib table-driven tests here, not assert.",
    "no testify please. table-driven tests, standard library only",
]
LOCK = [
    "Don't edit package-lock.json by hand, run npm install instead.",
    "stop editing package-lock.json directly. regenerate it with npm install",
    "never hand-edit package-lock.json, revert that and run npm install",
]
TF = [
    "Use snake_case for terraform resource names, not camelCase.",
    "terraform resource names should be snake_case, not camelCase. I said this before",
    "wrong naming again: snake_case resource names in terraform, never camelCase",
]
COMMIT = "Before committing, run make lint and make test, then write a conventional commit message with a scope and push the branch."


def go_session(sid, day, i, version="2.0.14"):
    s = Session(sid, API if i != 2 else WEB, day, version=version)
    s.user("Add a handler for GET /widgets with tests")
    s.say("I'll add the handler and tests.")
    s.tool("Write", {"file_path": f"{s.cwd}/widgets_test.go", "content": "package api\n\nimport \"github.com/stretchr/testify/assert\"\n"},
           "File created successfully")
    s.user(TESTIFY[i])
    s.say("Switching to table-driven tests.")
    # A failure loop: the same command failing twice in a row.
    s.tool("Bash", {"command": "go test ./...", "description": "Run tests"}, "FAIL\tapi [build failed]\n# api\n./widgets.go:12: undefined: Widget", is_error=True)
    s.tool("Bash", {"command": "go test ./...", "description": "Run tests"}, "FAIL\tapi [build failed]\n# api\n./widgets.go:12: undefined: Widget", is_error=True)
    s.tool("Edit", {"file_path": f"{s.cwd}/widgets.go", "old_string": "Widget", "new_string": "type Widget struct{ ID string }"}, "The file has been updated.")
    s.tool("Bash", {"command": "go test ./...", "description": "Run tests"}, "ok\tapi\t0.01s")
    # Manual fix-up: Claude wrote handler.go, a later Read shows other content.
    s.tool("Write", {"file_path": f"{s.cwd}/handler.go", "content": "package api\n\nfunc Handle() {}\n"}, "File created successfully")
    s.user("ok I tweaked handler.go, take a look and continue")
    s.tool("Read", {"file_path": f"{s.cwd}/handler.go"}, "     1\tpackage api\n     2\t\n     3\t// Handle serves widgets.\n     4\tfunc Handle() error { return nil }\n",
           tool_use_result={"type": "text", "file": {"filePath": f"{s.cwd}/handler.go", "content": "package api\n\n// Handle serves widgets.\nfunc Handle() error { return nil }\n", "numLines": 4, "startLine": 1, "totalLines": 4}})
    s.user(COMMIT)
    s.tool("Bash", {"command": "make lint && make test"}, "ok")
    return s


def lock_session(sid, day, i):
    s = Session(sid, WEB, day)
    s.user("Bump react-query to v5")
    s.tool("Edit", {"file_path": f"{WEB}/package.json", "old_string": "\"4.0.0\"", "new_string": "\"5.0.0\""}, "The file has been updated.")
    s.tool("Edit", {"file_path": f"{WEB}/package-lock.json", "old_string": "4.0.0", "new_string": "5.0.0"},
           "PreToolUse:Edit hook error: [~/src/claude-hooks/no-lockfiles.sh]: Editing lockfiles is not allowed (package-lock.json)", is_error=True)
    s.user(LOCK[i])
    s.tool("Bash", {"command": "npm install"}, "added 3 packages")
    s.user(COMMIT)
    return s


def tf_session(sid, day, i, version="2.0.14"):
    s = Session(sid, INFRA, day, version=version)
    s.user("Add an S3 bucket for build artifacts")
    s.tool("Write", {"file_path": f"{INFRA}/s3.tf", "content": "resource \"aws_s3_bucket\" \"buildArtifacts\" {}\n"}, "File created successfully")
    s.user(TF[i])
    s.tool("Edit", {"file_path": f"{INFRA}/s3.tf", "old_string": "buildArtifacts", "new_string": "build_artifacts"}, "The file has been updated.")
    s.user(COMMIT)
    return s


def main():
    shutil.rmtree(ROOT, ignore_errors=True)

    sessions = [
        go_session("a1111111-0000-4000-8000-000000000001", 0, 0, version="1.0.98"),
        go_session("a2222222-0000-4000-8000-000000000002", 5, 1),
        go_session("a3333333-0000-4000-8000-000000000003", 11, 2),
        go_session("a4444444-0000-4000-8000-000000000004", 18, 3),
        lock_session("b1111111-0000-4000-8000-000000000001", 2, 0),
        lock_session("b2222222-0000-4000-8000-000000000002", 9, 1),
        lock_session("b3333333-0000-4000-8000-000000000003", 16, 2),
        tf_session("c1111111-0000-4000-8000-000000000001", 3, 0),
        tf_session("c2222222-0000-4000-8000-000000000002", 10, 1),
        tf_session("c3333333-0000-4000-8000-000000000003", 17, 2, version="1.0.98"),
    ]

    # Interrupts, a rejected tool call with feedback, a rewind, a secret,
    # an unknown event type, a malformed line and a partial trailing line.
    s = Session("d1111111-0000-4000-8000-000000000001", API, 20)
    hello = s.say("Ready.")
    s.user("Clean up the build directory")
    s.tool("Bash", {"command": "rm -rf ./build ./dist ./vendor"}, "[Request interrupted by user for tool use]", is_error=True)
    s.user("[Request interrupted by user for tool use]")
    s.user("don't delete vendor, we commit it. only remove build")
    s.tool("Bash", {"command": "git push --force origin main"},
           "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\nnever force push to main, open a PR instead", is_error=True)
    s.say("Understood.")
    # Rewind: a second user message sharing the first prompt's parent.
    s.user("Clean up only the build directory, leave vendor alone", parent=hello)
    s.user("my token is ghp_abcdefghijklmnopqrstuvwxyz0123456789AB, use it to push")
    s.raw({"type": "weird-new-type", "sessionId": s.sid, "uuid": "x1"})
    s.raw({"type": "summary", "summary": "Cleanup", "leafUuid": s.last})
    s.raw({"type": "attachment", "sessionId": s.sid, "uuid": "att1", "parentUuid": s.last, "cwd": API,
           "timestamp": s.t.strftime("%Y-%m-%dT%H:%M:%S.000Z"),
           "attachment": {"type": "edited_text_file", "filename": f"{API}/Makefile"}})
    s.lines.append("{this is not json")
    sessions.append(s)

    for x in sessions[:-1]:
        x.write()
    sessions[-1].write(extra='{"type":"user","sessionId":"d1111111-0000-4000-8000-000000000001","message":{"role":"user","content":"partial')


if __name__ == "__main__":
    main()
