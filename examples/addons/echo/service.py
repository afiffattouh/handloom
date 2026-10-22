"""The echo add-on: a reference service for the add-on protocol (Python 3.8 or newer, standard library only).

It reads one JSON object per line on standard input and answers with one JSON object per line on
standard output. See docs/direction/addons.md, "The add-on service".
"""
import json
import os
import sys
import time


def say(args, results):
    text = args["text"]
    files = []
    name = args.get("file")
    if name:
        # The add-on writes only into the results folder it is given, and only a plain file name.
        if os.path.basename(name) != name or name in (".", ".."):
            raise ValueError("the file name must be a plain name, without folders")
        if not results:
            raise ValueError("there is no results folder to save into")
        with open(os.path.join(results, name), "w", encoding="utf-8") as f:
            f.write(text)
        files.append(name)
    return {"text": text, "files": files}


def wait(args, results):
    seconds = float(args["seconds"])
    time.sleep(seconds)
    return {"text": "Waited %g seconds." % seconds}


TOOLS = {"say": say, "wait": wait}


def handle(req):
    tool = req.get("tool")
    if tool == "_health":
        return {"ok": True}
    fn = TOOLS.get(tool)
    if fn is None:
        return {"ok": False, "error": "this add-on has no tool %r" % tool}
    try:
        out = fn(req.get("args") or {}, req.get("results") or "")
    except Exception as e:  # report it to the caller; the service stays up
        return {"ok": False, "error": str(e)}
    out["ok"] = True
    return out


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except ValueError:
            continue
        reply = handle(req)
        reply["id"] = req.get("id")
        sys.stdout.write(json.dumps(reply) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
