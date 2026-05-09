package executor

import (
	"regexp"
	"sort"
	"strings"
)

// ValidateImports parses src for top-level Python `import X` and
// `from X[.Y] import Z` statements, extracts each top-level module name,
// and returns those that are neither in pythonStdlib nor in allowed.
// allowed values are matched against module names with package-name
// equivalence: e.g., the import "PIL" is satisfied by allowed entry
// "pillow" (the wheel name), the import "yaml" by "pyyaml", etc.
//
// Returns the disallowed module names sorted alphabetically, or empty
// if the source is acceptable. The bool error return is reserved for
// future syntax-level rejection — currently always nil.
func ValidateImports(src string, allowed []string) ([]string, error) {
	allowSet := make(map[string]bool, len(allowed)+len(packageAliases))
	for _, a := range allowed {
		allowSet[strings.ToLower(a)] = true
		// Map wheel name back to import name(s) via packageAliases.
		for impName, wheel := range packageAliases {
			if strings.EqualFold(wheel, a) {
				allowSet[strings.ToLower(impName)] = true
			}
		}
	}
	mods := extractTopLevelModules(src)
	bad := []string{}
	for m := range mods {
		lm := strings.ToLower(m)
		if pythonStdlib[lm] {
			continue
		}
		if allowSet[lm] {
			continue
		}
		bad = append(bad, m)
	}
	sort.Strings(bad)
	return bad, nil
}

var importRe = regexp.MustCompile(`(?m)^\s*(?:import\s+([A-Za-z_][\w.]*)|from\s+([A-Za-z_][\w.]*)\s+import\s)`)

func extractTopLevelModules(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range importRe.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		// Top-level package only.
		if i := strings.Index(name, "."); i > 0 {
			name = name[:i]
		}
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// packageAliases maps Python import names to their pip/wheel package names
// where the two differ. Used so that allowed_packages in a build_mcp spec
// can use the wheel name (more familiar) while we still allow the
// corresponding import.
var packageAliases = map[string]string{
	"PIL":           "pillow",
	"yaml":          "pyyaml",
	"cv2":           "opencv-python",
	"sklearn":       "scikit-learn",
	"bs4":           "beautifulsoup4",
	"dateutil":      "python-dateutil",
	"google":        "google-api-python-client",
	"OpenSSL":       "pyopenssl",
}

// pythonStdlib lists Python 3.11 standard-library top-level module names
// (lowercased). Generated from https://docs.python.org/3.11/py-modindex.html.
// Kept in source so the validator has no runtime dependency.
var pythonStdlib = func() map[string]bool {
	names := []string{
		"__future__", "_thread", "abc", "aifc", "argparse", "array", "ast",
		"asynchat", "asyncio", "asyncore", "atexit", "audioop", "base64",
		"bdb", "binascii", "bisect", "builtins", "bz2", "calendar", "cgi",
		"cgitb", "chunk", "cmath", "cmd", "code", "codecs", "codeop",
		"collections", "colorsys", "compileall", "concurrent", "configparser",
		"contextlib", "contextvars", "copy", "copyreg", "crypt", "csv",
		"ctypes", "curses", "dataclasses", "datetime", "dbm", "decimal",
		"difflib", "dis", "distutils", "doctest", "email", "encodings",
		"ensurepip", "enum", "errno", "faulthandler", "fcntl", "filecmp",
		"fileinput", "fnmatch", "fractions", "ftplib", "functools", "gc",
		"genericpath", "getopt", "getpass", "gettext", "glob", "graphlib",
		"grp", "gzip", "hashlib", "heapq", "hmac", "html", "http", "idlelib",
		"imaplib", "imghdr", "imp", "importlib", "inspect", "io",
		"ipaddress", "itertools", "json", "keyword", "lib2to3", "linecache",
		"locale", "logging", "lzma", "mailbox", "mailcap", "marshal", "math",
		"mimetypes", "mmap", "modulefinder", "msilib", "msvcrt", "multiprocessing",
		"netrc", "nis", "nntplib", "numbers", "operator", "optparse", "os",
		"ossaudiodev", "pathlib", "pdb", "pickle", "pickletools", "pipes",
		"pkgutil", "platform", "plistlib", "poplib", "posix", "posixpath",
		"pprint", "profile", "pstats", "pty", "pwd", "py_compile", "pyclbr",
		"pydoc", "pydoc_data", "queue", "quopri", "random", "re", "readline",
		"reprlib", "resource", "rlcompleter", "runpy", "sched", "secrets",
		"select", "selectors", "shelve", "shlex", "shutil", "signal", "site",
		"smtpd", "smtplib", "sndhdr", "socket", "socketserver", "spwd",
		"sqlite3", "sre_compile", "sre_constants", "sre_parse", "ssl", "stat",
		"statistics", "string", "stringprep", "struct", "subprocess", "sunau",
		"symtable", "sys", "sysconfig", "syslog", "tabnanny", "tarfile",
		"telnetlib", "tempfile", "termios", "test", "textwrap", "threading",
		"time", "timeit", "tkinter", "token", "tokenize", "tomllib", "trace",
		"traceback", "tracemalloc", "tty", "turtle", "turtledemo", "types",
		"typing", "unicodedata", "unittest", "urllib", "uu", "uuid", "venv",
		"warnings", "wave", "weakref", "webbrowser", "winreg", "winsound",
		"wsgiref", "xdrlib", "xml", "xmlrpc", "zipapp", "zipfile", "zipimport",
		"zlib", "zoneinfo",
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[strings.ToLower(n)] = true
	}
	return m
}()
