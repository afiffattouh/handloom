"""The web-fetch add-on: fetches one web page and saves its text (Python 3.8 or newer, standard library only).

It speaks the add-on protocol of docs/direction/addons.md: one JSON object per line on standard input, one
per line on standard output. The tool `fetch` takes a url, follows at most MAX_REDIRECTS redirects, reads at
most MAX_BODY bytes, and saves the page's text as a markdown file in the results folder it is given.
"""
import hashlib
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from html.parser import HTMLParser

MAX_REDIRECTS = 5
MAX_BODY = 512 * 1024  # bytes read from the network
MAX_FILE = 1024 * 1024  # the tool's own limit (max_output in addon.yaml) on the saved file
TIMEOUT = 20  # seconds for each network step; the link's per-call timeout is the real limit


class FetchError(Exception):
    """A problem to tell the caller in plain words."""


def check_address(url):
    """Only http and https addresses with a host are fetched. A local path, file: or ftp: is a clear error."""
    if not isinstance(url, str) or not url.strip():
        raise FetchError("give the address of a web page, starting with http:// or https://")
    url = url.strip()
    parts = urllib.parse.urlsplit(url)
    if parts.scheme.lower() not in ("http", "https"):
        raise FetchError("only http:// and https:// addresses can be fetched, not %r; local files are not available" % url)
    if not parts.hostname:
        raise FetchError("the address %r has no host name" % url)
    return url


class _Redirects(urllib.request.HTTPRedirectHandler):
    max_redirections = MAX_REDIRECTS  # more than this raises an HTTPError with the redirect's code
    max_repeats = MAX_REDIRECTS

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Each hop is checked like the first address: a redirect to file: or another scheme is refused.
        check_address(urllib.parse.urljoin(req.full_url, newurl))
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class _Text(HTMLParser):
    """Turns a web page into plain text with blank lines between blocks."""

    BLOCKS = {"p", "div", "section", "article", "header", "footer", "main", "li", "ul", "ol", "br", "tr",
              "table", "blockquote", "pre", "h1", "h2", "h3", "h4", "h5", "h6"}
    SKIP = {"script", "style", "noscript", "template", "svg", "head"}

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.parts = []
        self.title = ""
        self._skip = 0
        self._in_title = False
        self._heading = 0

    def handle_starttag(self, tag, attrs):
        if tag == "title":
            self._in_title = True
        if tag in self.SKIP:
            self._skip += 1
        if tag in self.BLOCKS:
            self.parts.append("\n\n")
        if len(tag) == 2 and tag[0] == "h" and tag[1] in "123456":
            self.parts.append("#" * int(tag[1]) + " ")

    def handle_endtag(self, tag):
        if tag == "title":
            self._in_title = False
        if tag in self.SKIP and self._skip:
            self._skip -= 1
        if tag in self.BLOCKS:
            self.parts.append("\n\n")

    def handle_data(self, data):
        if self._in_title:
            self.title += data
        elif not self._skip:
            self.parts.append(data)

    def text(self):
        text = "".join(self.parts)
        text = re.sub(r"[ \t\r\f\v]+", " ", text)
        text = re.sub(r" *\n *", "\n", text)
        text = re.sub(r"\n{3,}", "\n\n", text)
        return text.strip()


def download(url, max_body=MAX_BODY):
    """Returns (body bytes, content type, charset). Reads at most max_body bytes and refuses more."""
    opener = urllib.request.build_opener(_Redirects)
    req = urllib.request.Request(url, headers={"User-Agent": "handloom-web-fetch/0.1", "Accept": "text/html,text/plain,*/*;q=0.5"})
    try:
        with opener.open(req, timeout=TIMEOUT) as resp:
            ctype = resp.headers.get_content_type()
            charset = resp.headers.get_content_charset() or "utf-8"
            length = resp.headers.get("Content-Length")
            if length and length.isdigit() and int(length) > max_body:
                raise FetchError("the page is %s bytes, more than the %d bytes this add-on reads" % (length, max_body))
            body = resp.read(max_body + 1)
    except FetchError:
        raise
    except urllib.error.HTTPError as e:
        if e.code in (301, 302, 303, 307, 308):
            if "not allowed" in str(e.msg):  # urllib itself refuses a redirect to a scheme that is not http(s)
                raise FetchError("the page redirected to an address that is not http:// or https://, which is not followed")
            raise FetchError("the page redirected more than %d times" % MAX_REDIRECTS)
        raise FetchError("the server answered %d %s" % (e.code, e.reason))
    except urllib.error.URLError as e:
        raise FetchError("the address did not answer: %s" % (e.reason,))
    except (OSError, ValueError) as e:
        raise FetchError("the address did not answer: %s" % e)
    if len(body) > max_body:
        raise FetchError("the page is larger than the %d bytes this add-on reads" % max_body)
    return body, ctype, charset


def file_name(url, digest):
    parts = urllib.parse.urlsplit(url)
    slug = re.sub(r"[^a-z0-9]+", "-", (parts.hostname + parts.path).lower()).strip("-")[:40].strip("-")
    return "%s-%s.md" % (slug or "page", digest[:8])


def fetch(args, results, max_body=MAX_BODY, max_file=MAX_FILE):
    url = check_address(args.get("url"))
    if not results:
        raise FetchError("there is no results folder to save into")
    body, ctype, charset = download(url, max_body)
    try:
        decoded = body.decode(charset, errors="replace")
    except LookupError:
        decoded = body.decode("utf-8", errors="replace")
    if ctype in ("text/html", "application/xhtml+xml"):
        parser = _Text()
        parser.feed(decoded)
        parser.close()
        text = parser.text()
        title = " ".join(parser.title.split())
        if title:
            text = "# %s\n\n%s" % (title, text)
    elif ctype.startswith("text/") or ctype in ("application/json", "application/xml"):
        text = decoded.strip()
    else:
        raise FetchError("the page is %s, which is not text, so nothing was saved" % ctype)
    digest = hashlib.sha256(body).hexdigest()
    fetched = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    header = "---\naddress: %s\nfetched: %s\nsha256: %s\n---\n\n" % (url, fetched, digest)
    name = file_name(url, digest)
    # Bytes that are not valid text become a 3-byte replacement mark, so the saved file can be larger than the
    # download: check the size it will have, not the size that was read.
    content = (header + text + "\n").encode("utf-8")
    if len(content) > max_file:
        raise FetchError("the text of the page would be %d bytes once saved, more than the %d bytes allowed, so nothing was saved" % (len(content), max_file))
    os.makedirs(results, exist_ok=True)
    with open(os.path.join(results, name), "wb") as f:
        f.write(content)
    return {"text": "Saved the text of the page as %s (%d characters)." % (name, len(text)), "files": [name]}


def handle(req):
    tool = req.get("tool")
    if tool == "_health":
        return {"ok": True}
    if tool != "fetch":
        return {"ok": False, "error": "this add-on has no tool %r" % tool}
    try:
        out = fetch(req.get("args") or {}, req.get("results") or "")
    except FetchError as e:
        return {"ok": False, "error": str(e)}
    except Exception as e:  # report it to the caller; the service stays up
        return {"ok": False, "error": "the page could not be fetched: %s" % e}
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
