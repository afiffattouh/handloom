"""TOML reading for factory-kit, so the checks run on Python 3.8 and newer.

Python 3.11+ has tomllib and this module just uses it. Older Pythons (a stock Mac has 3.9)
get a small reader for the part of TOML that factory.toml uses: [tables], key = value with
strings, whole numbers, true/false and arrays of those (also over several lines), and # comments.
Anything else raises TOMLError, which is a ValueError like tomllib's own error.
"""
import re

try:
    import tomllib as _tomllib
except ImportError:  # Python < 3.11
    _tomllib = None


class TOMLError(ValueError):
    pass


_KEY = re.compile(r'^([A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*")\s*=\s*(.*)$')
_TABLE = re.compile(r'^\[\s*([A-Za-z0-9_.-]+)\s*\]\s*(#.*)?$')
_ESC = {'"': '"', "\\": "\\", "n": "\n", "t": "\t", "r": "\r", "b": "\b", "f": "\f"}


def _string(s, i):
    """The basic or literal string starting at s[i]. Returns (value, next index)."""
    quote = s[i]
    out, i = [], i + 1
    while i < len(s):
        c = s[i]
        if c == quote:
            return "".join(out), i + 1
        if quote == '"' and c == "\\":
            i += 1
            if i >= len(s):
                break
            e = s[i]
            if e == "u" and i + 4 < len(s) + 0 and re.fullmatch(r"[0-9a-fA-F]{4}", s[i + 1:i + 5]):
                out.append(chr(int(s[i + 1:i + 5], 16)))
                i += 5
                continue
            if e not in _ESC:
                raise TOMLError("bad escape \\%s" % e)
            out.append(_ESC[e])
        else:
            out.append(c)
        i += 1
    raise TOMLError("unterminated string")


def _skip(s, i):
    while i < len(s) and s[i] in " \t\r\n":
        i += 1
    if i < len(s) and s[i] == "#":
        while i < len(s) and s[i] != "\n":
            i += 1
        return _skip(s, i)
    return i


def _value(s, i):
    i = _skip(s, i)
    if i >= len(s):
        raise TOMLError("missing value")
    c = s[i]
    if c in "\"'":
        if s.startswith(c * 3, i):
            raise TOMLError("multi-line strings are not supported here")
        return _string(s, i)
    if c == "[":
        items, i = [], i + 1
        while True:
            i = _skip(s, i)
            if i >= len(s):
                raise TOMLError("unterminated array")
            if s[i] == "]":
                return items, i + 1
            v, i = _value(s, i)
            items.append(v)
            i = _skip(s, i)
            if i < len(s) and s[i] == ",":
                i += 1
    m = re.compile(r"[A-Za-z0-9_+.-]+").match(s, i)
    if not m:
        raise TOMLError("cannot read the value at %r" % s[i:i + 12])
    word = m.group(0)
    if word in ("true", "false"):
        return word == "true", m.end()
    if re.fullmatch(r"[+-]?\d[\d_]*", word):
        return int(word.replace("_", "")), m.end()
    raise TOMLError("unsupported value %r" % word)


def _loads(text):
    root, table, seen = {}, None, set()
    table = root
    i, n = 0, len(text)
    while True:
        i = _skip(text, i)
        if i >= n:
            return root
        end = text.find("\n", i)
        end = n if end < 0 else end
        line = text[i:end].strip()
        if line.startswith("["):
            m = _TABLE.match(line)
            if not m:
                raise TOMLError("cannot read the table header %r" % line)
            name = m.group(1)
            if name in seen:
                raise TOMLError("table [%s] is defined twice" % name)
            seen.add(name)
            table = root
            for part in name.split("."):
                table = table.setdefault(part, {})
                if not isinstance(table, dict):
                    raise TOMLError("%r is a value, not a table" % part)
            i = end
            continue
        m = _KEY.match(text[i:end])
        if not m:
            raise TOMLError("cannot read the line %r" % line)
        key = m.group(1)
        if key.startswith('"'):
            key = _string(key, 0)[0]
        value_at = i + m.start(2)
        value, j = _value(text, value_at)
        if key in table:
            raise TOMLError("key %r is defined twice" % key)
        table[key] = value
        rest_end = text.find("\n", j)
        rest = text[j:(n if rest_end < 0 else rest_end)].strip()
        if rest and not rest.startswith("#"):
            raise TOMLError("unexpected text after the value: %r" % rest)
        i = n if rest_end < 0 else rest_end


def loads(text):
    if _tomllib is not None:
        try:
            return _tomllib.loads(text)
        except _tomllib.TOMLDecodeError as e:
            raise TOMLError(str(e))
    return _loads(text)
